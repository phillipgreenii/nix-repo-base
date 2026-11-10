package workspace

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Tests of the sudo session (see sudo_session.go). Every call goes to sudoFake:
// no real sudo ever runs. Every test that starts a session registers
// t.Cleanup(sess.Stop) so no refresh goroutine outlives its test.

// sudoFake answers `sudo -n -v` (probe, verify and refresh), `sudo -v` (the
// prompt) and `sudo -k`. By default it behaves like sudo: `-n -v` fails until a
// `-v` has run, and `-k` drops the credentials.
type sudoFake struct {
	mu    sync.Mutex
	calls []exec.Call
	held  bool
	nv    int // count of `-n -v` calls so far

	// onNV, when set, answers the n-th (1-based) `-n -v` call instead of the default.
	onNV func(ctx context.Context, n int) error
	// onV, when set, runs on the prompting `sudo -v`; a non-nil error fails it.
	onV func(ctx context.Context) error

	killCtxErr []error // ctx.Err() seen by each `sudo -k`
	// nvSig receives one non-blocking signal per `-n -v` call (buffered).
	nvSig chan struct{}
}

func newSudoFake() *sudoFake { return &sudoFake{nvSig: make(chan struct{}, 4096)} }

func sudoExit1(args []string) error {
	return &exec.CommandError{Name: "sudo", Args: args, Result: exec.Result{ExitCode: 1}}
}

// heldNV is the default `-n -v` answer.
func (f *sudoFake) heldNV(args []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.held {
		return sudoExit1(args)
	}
	return nil
}

func (f *sudoFake) Run(ctx context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, exec.Call{Name: name, Args: append([]string{}, args...), Opts: opts})
	f.mu.Unlock()
	if name != "sudo" {
		return exec.Result{}, errors.New("sudoFake: not sudo: " + name)
	}
	switch strings.Join(args, " ") {
	case "-n -v":
		f.mu.Lock()
		f.nv++
		n := f.nv
		f.mu.Unlock()
		select {
		case f.nvSig <- struct{}{}:
		default:
		}
		if f.onNV != nil {
			return exec.Result{}, f.onNV(ctx, n)
		}
		return exec.Result{}, f.heldNV(args)
	case "-v":
		if f.onV != nil {
			if err := f.onV(ctx); err != nil {
				return exec.Result{}, err
			}
		}
		f.mu.Lock()
		f.held = true
		f.mu.Unlock()
		return exec.Result{}, nil
	case "-k":
		f.mu.Lock()
		f.held = false
		f.killCtxErr = append(f.killCtxErr, ctx.Err())
		f.mu.Unlock()
		return exec.Result{}, nil
	}
	return exec.Result{}, errors.New("sudoFake: unexpected argv " + strings.Join(args, " "))
}

func (f *sudoFake) argvs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

func (f *sudoFake) count(argv string) int {
	n := 0
	for _, a := range f.argvs() {
		if a == argv {
			n++
		}
	}
	return n
}

func (f *sudoFake) snapshot() []exec.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exec.Call{}, f.calls...)
}

// fastSudo shortens the refresh interval for one test.
func fastSudo(t *testing.T) {
	t.Helper()
	oi, ot := sudoRefreshInterval, sudoRefreshTimeout
	sudoRefreshInterval = 5 * time.Millisecond
	t.Cleanup(func() { sudoRefreshInterval, sudoRefreshTimeout = oi, ot })
}

// startFast starts a session over f with the interval shortened and registers Stop.
func startFast(t *testing.T, ctx context.Context, f *sudoFake, out *bytes.Buffer) *sudoSession {
	t.Helper()
	fastSudo(t)
	sess, err := startSudoSession(ctx, f, out, nil)
	if err != nil {
		t.Fatalf("startSudoSession: %v", err)
	}
	t.Cleanup(sess.Stop)
	return sess
}

func waitDone(t *testing.T, s *sudoSession) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh loop did not end within 5s")
	}
}

func waitSignals(t *testing.T, ch chan struct{}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d signals arrived", i, n)
		}
	}
}

// runningRefreshLoops counts goroutines currently inside (*sudoSession).refreshLoop.
func runningRefreshLoops() int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return strings.Count(string(buf), "(*sudoSession).refreshLoop")
}

// expectNoLoops polls until no refresh loop is running (a goroutine is still on
// the stack for a moment after its deferred close(done) ran).
func expectNoLoops(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := runningRefreshLoops()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d refresh goroutine(s) still running after 2s", n)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

func TestSudoSession_PrimesWithVThenVerifiesWithNV(t *testing.T) {
	f := newSudoFake()
	var out bytes.Buffer
	startFast(t, context.Background(), f, &out)
	calls := f.snapshot()
	if len(calls) < 3 {
		t.Fatalf("calls = %v", f.argvs())
	}
	want := []string{"-n -v", "-v", "-n -v"}
	for i, w := range want {
		if got := strings.Join(calls[i].Args, " "); got != w {
			t.Fatalf("call %d = %q, want %q (all: %v)", i, got, w, f.argvs())
		}
	}
	prompt := calls[1].Opts
	if prompt.Stdout != &out || prompt.Stderr != &out {
		t.Errorf("the prompting sudo -v must write to out")
	}
	if prompt.Dir != "/" || prompt.OwnProcessGroup {
		t.Errorf("prompt opts Dir=%q OwnProcessGroup=%v, want / and false", prompt.Dir, prompt.OwnProcessGroup)
	}
	if !strings.Contains(out.String(), "authenticate sudo now") {
		t.Errorf("no prompt notice in out: %q", out.String())
	}
}

func TestSudoSession_ExistingCredentialsAreNeverInvalidated(t *testing.T) {
	f := newSudoFake()
	f.held = true
	var out bytes.Buffer
	sess := startFast(t, context.Background(), f, &out)
	sess.Stop()
	if f.count("-k") != 0 || f.count("-v") != 0 {
		t.Errorf("calls = %v, want no -k and no prompting -v", f.argvs())
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want nothing", out.String())
	}
}

func TestSudoSession_StopInvalidatesCredentialsPnCreated(t *testing.T) {
	f := newSudoFake()
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	sess.Stop()
	a := f.argvs()
	if a[len(a)-1] != "-k" {
		t.Errorf("last call = %q, want -k (all: %v)", a[len(a)-1], a)
	}
}

func TestSudoSession_RefreshesWhileRunningAndStopsOnStop(t *testing.T) {
	f := newSudoFake()
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	waitSignals(t, f.nvSig, 5) // probe, verify, then three refreshes
	sess.Stop()
	before := f.argvs()
	sess.Stop()
	after := f.argvs()
	if len(before) != len(after) {
		t.Errorf("calls changed after the second Stop: %v -> %v", before, after)
	}
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times", f.count("-k"))
	}
	expectNoLoops(t)
}

func TestSudoSession_RefreshFailureSetsLostAndEndsTheLoop(t *testing.T) {
	f := newSudoFake()
	f.onNV = func(_ context.Context, n int) error {
		if n >= 4 { // probe, verify, one good refresh, then failure
			return sudoExit1([]string{"-n", "-v"})
		}
		return f.heldNV([]string{"-n", "-v"})
	}
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	waitDone(t, sess)
	if !sess.Lost() {
		t.Error("Lost() = false after a failed refresh")
	}
	if n := f.count("-n -v"); n != 4 {
		t.Errorf("-n -v ran %d times, want 4 (no refresh after the failing one)", n)
	}
}

func TestSudoSession_AuthenticationFailureReturnsErrorAndStartsNoLoop(t *testing.T) {
	f := newSudoFake()
	boom := sudoExit1([]string{"-v"})
	f.onV = func(context.Context) error { return boom }
	fastSudo(t)
	sess, err := startSudoSession(context.Background(), f, &bytes.Buffer{}, nil)
	if sess != nil || err == nil || !errors.Is(err, boom) {
		t.Fatalf("sess=%v err=%v, want nil session and an error wrapping the failure", sess, err)
	}
	if !strings.Contains(err.Error(), "sudo authentication") {
		t.Errorf("err = %v", err)
	}
	if n := f.count("-n -v"); n != 1 {
		t.Errorf("-n -v ran %d times, want only the probe", n)
	}
}

func TestSudoSession_NoCredentialCacheWarnsOnceAndKeepsNoLoop(t *testing.T) {
	f := newSudoFake()
	f.onNV = func(_ context.Context, _ int) error { return sudoExit1([]string{"-n", "-v"}) }
	var out bytes.Buffer
	sess := startFast(t, context.Background(), f, &out)
	if !sess.Cacheless() || sess.Lost() {
		t.Errorf("Cacheless=%v Lost=%v, want true false", sess.Cacheless(), sess.Lost())
	}
	if c := strings.Count(out.String(), "does not cache credentials"); c != 1 {
		t.Errorf("warning count = %d in %q", c, out.String())
	}
	waitDone(t, sess)
	expectNoLoops(t)
	if n := f.count("-n -v"); n != 2 {
		t.Errorf("-n -v ran %d times, want probe and verify only", n)
	}
	sess.Stop()
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
}

func TestSudoSession_StopSurvivesACancelledContext(t *testing.T) {
	f := newSudoFake()
	ctx, cancel := context.WithCancel(context.Background())
	sess := startFast(t, ctx, f, &bytes.Buffer{})
	cancel()
	sess.Stop()
	if len(f.killCtxErr) != 1 {
		t.Fatalf("-k ran %d times", len(f.killCtxErr))
	}
	if f.killCtxErr[0] != nil {
		t.Errorf("the -k context was cancelled: %v", f.killCtxErr[0])
	}
}

func TestSudoSession_CancelDuringPromptReturnsContextError(t *testing.T) {
	f := newSudoFake()
	f.onV = func(ctx context.Context) error {
		<-ctx.Done()
		return &exec.CommandError{Name: "sudo", Args: []string{"-v"}, Result: exec.Result{ExitCode: -1, Signal: "SIGTERM"}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	fastSudo(t)
	sess, err := startSudoSession(ctx, f, &bytes.Buffer{}, nil)
	if sess != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("sess=%v err=%v, want nil and context.Canceled", sess, err)
	}
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1 (the ticket may exist)", f.count("-k"))
	}
	expectNoLoops(t)
}

func TestSudoSession_ProbeRefreshAndVerifyNeverTouchOut(t *testing.T) {
	f := newSudoFake()
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	waitSignals(t, f.nvSig, 4)
	sess.Stop()
	for _, c := range f.snapshot() {
		if strings.Join(c.Args, " ") == "-n -v" && (c.Opts.Stdout != nil || c.Opts.Stderr != nil) {
			t.Errorf("a -n -v call has Stdout/Stderr set")
		}
	}
}

func TestSudoSession_StopIsIdempotentAndNilSafe(t *testing.T) {
	f := newSudoFake()
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	sess.Stop()
	sess.Stop()
	var nilSess *sudoSession
	nilSess.Stop()
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
	if nilSess.Lost() || nilSess.Cacheless() {
		t.Error("a nil session must report neither lost nor cacheless")
	}
}

func TestSudoSession_CancelAfterPromptBeforeVerifyInvalidatesAndReturnsCancel(t *testing.T) {
	f := newSudoFake()
	ctx, cancel := context.WithCancel(context.Background())
	f.onV = func(context.Context) error { cancel(); return nil }
	var out bytes.Buffer
	fastSudo(t)
	sess, err := startSudoSession(ctx, f, &out, nil)
	if sess != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("sess=%v err=%v, want nil and context.Canceled", sess, err)
	}
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
	if strings.Contains(out.String(), "does not cache credentials") {
		t.Errorf("a cancel was misclassified as no credential cache: %q", out.String())
	}
	if f.count("-n -v") != 1 {
		t.Errorf("the verify call ran after the cancel: %v", f.argvs())
	}
	expectNoLoops(t)
}

func TestSudoSession_StopOnACachelessSessionDoesNotPanic(t *testing.T) {
	f := newSudoFake()
	f.onNV = func(_ context.Context, _ int) error { return sudoExit1([]string{"-n", "-v"}) }
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	sess.Stop()
	sess.Stop()
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
}

func TestSudoSession_ParentCancelEndsTheLoopWithoutStop(t *testing.T) {
	f := newSudoFake()
	ctx, cancel := context.WithCancel(context.Background())
	sess := startFast(t, ctx, f, &bytes.Buffer{})
	cancel()
	waitDone(t, sess)
	expectNoLoops(t)
	if sess.Lost() {
		t.Error("a cancelled refresh must not mark the session lost")
	}
	sess.Stop()
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
}

func TestSudoSession_StopInterruptsAHungRefresh(t *testing.T) {
	f := newSudoFake()
	started := make(chan struct{}, 1)
	var refreshCtx context.Context
	var mu sync.Mutex
	f.onNV = func(ctx context.Context, n int) error {
		if n < 3 {
			return f.heldNV([]string{"-n", "-v"})
		}
		mu.Lock()
		refreshCtx = ctx
		mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return &exec.CommandError{Name: "sudo", Args: []string{"-n", "-v"}, Result: exec.Result{ExitCode: -1, Signal: "SIGTERM"}}
	}
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	waitSignals(t, started, 1)
	returned := make(chan struct{})
	go func() { sess.Stop(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}
	mu.Lock()
	err := refreshCtx.Err()
	mu.Unlock()
	if err == nil {
		t.Error("the hung refresh's context was not cancelled")
	}
	if sess.Lost() {
		t.Error("an interrupted refresh must not mark the session lost")
	}
}

func TestSudoSession_RefreshTimeoutMarksLost(t *testing.T) {
	f := newSudoFake()
	hadDeadline := make(chan bool, 1)
	f.onNV = func(ctx context.Context, n int) error {
		if n < 3 {
			return f.heldNV([]string{"-n", "-v"})
		}
		_, ok := ctx.Deadline()
		select {
		case hadDeadline <- ok:
		default:
		}
		<-ctx.Done()
		return &exec.CommandError{Name: "sudo", Args: []string{"-n", "-v"}, Result: exec.Result{ExitCode: -1, Signal: "SIGKILL"}}
	}
	fastSudo(t)
	sudoRefreshTimeout = 10 * time.Millisecond
	sess, err := startSudoSession(context.Background(), f, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Stop)
	waitDone(t, sess)
	if !sess.Lost() {
		t.Error("a refresh that timed out must mark the session lost")
	}
	select {
	case ok := <-hadDeadline:
		if !ok {
			t.Error("the refresh call had no deadline")
		}
	default:
		t.Error("the refresh never ran")
	}
	sess.Stop()
}

func TestSudoSession_NoGoroutineAfterStop(t *testing.T) {
	for i := 0; i < 20; i++ {
		f := newSudoFake()
		fastSudo(t)
		sess, err := startSudoSession(context.Background(), f, &bytes.Buffer{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		waitSignals(t, f.nvSig, 3) // probe, verify, one refresh
		sess.Stop()
	}
	expectNoLoops(t)
}

func TestSudoSession_NoGoroutineWhenAuthenticationFails(t *testing.T) {
	f := newSudoFake()
	f.onV = func(context.Context) error { return sudoExit1([]string{"-v"}) }
	fastSudo(t)
	if _, err := startSudoSession(context.Background(), f, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("expected an error")
	}
	if n := runningRefreshLoops(); n != 0 {
		t.Errorf("%d refresh goroutine(s) after a failed authentication", n)
	}
}

func TestSudoSession_ConcurrentStopRunsOneInvalidation(t *testing.T) {
	f := newSudoFake()
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; sess.Stop() }()
	}
	close(start)
	wg.Wait()
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
}

func TestSudoSession_StopAfterTheLoopAlreadyEnded(t *testing.T) {
	f := newSudoFake()
	f.onNV = func(_ context.Context, n int) error {
		if n >= 3 {
			return sudoExit1([]string{"-n", "-v"})
		}
		return f.heldNV([]string{"-n", "-v"})
	}
	sess := startFast(t, context.Background(), f, &bytes.Buffer{})
	waitDone(t, sess)
	returned := make(chan struct{})
	go func() { sess.Stop(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked after the loop ended")
	}
	if f.count("-k") != 1 {
		t.Errorf("-k ran %d times, want 1", f.count("-k"))
	}
}

func TestSudoSession_StdinIsPassedThrough(t *testing.T) {
	f := newSudoFake()
	in := strings.NewReader("")
	fastSudo(t)
	sess, err := startSudoSession(context.Background(), f, &bytes.Buffer{}, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Stop)
	waitSignals(t, f.nvSig, 3)
	sess.Stop()
	for _, c := range f.snapshot() {
		if c.Opts.Stdin != in {
			t.Errorf("call %v did not receive the stdin reader", c.Args)
		}
	}
}
