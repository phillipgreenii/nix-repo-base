package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
)

// Tests for the build/apply single-flight lock (rebuildlock.go, bead
// pg2-sdbhh4.11). Every test points XDG_STATE_HOME at a private temp dir (the
// package TestMain does it too), so none touches the real lock directory. The
// locks are REAL flock(2) locks; a Workspace per "process" holds its own open
// file description, so goroutines in one test process contend exactly as two pn
// processes would. A gateRunner stands in for the long-running darwin-rebuild.

const lockTestHost = "lockhost"

// lockTestEnv isolates state/data dirs and speeds up the wait poll.
func lockTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	old := lockPollInterval
	lockPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { lockPollInterval = old })
}

// newLockRoot makes a trusted workspace root whose terminal is "leaf".
func newLockRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "`+filepath.Base(root)+`"
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"
`)
	if err := trust.Allow(root); err != nil {
		t.Fatalf("trust.Allow: %v", err)
	}
	return root
}

// gateRunner wraps a FakeRunner and, when gated, blocks the verb's build/switch
// command (a long-running darwin-rebuild) until release is closed.
type gateRunner struct {
	exec.Runner
	started chan struct{} // closed when the gated command is reached; nil: ungated
	release chan struct{} // the test closes it to let the command finish
	once    sync.Once
}

func newGate() *gateRunner {
	return &gateRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateRunner) Run(ctx context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	if g.started != nil && (name == "darwin-rebuild" || name == "sudo") {
		g.once.Do(func() { close(g.started) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return exec.Result{}, ctx.Err()
		}
	}
	return g.Runner.Run(ctx, name, args, opts)
}

// newLockWS opens the workspace at root over a scripted fake runner (several
// runs' worth of responses), gated when gate is non-nil.
func newLockWS(t *testing.T, root string, gate *gateRunner) *Workspace {
	t.Helper()
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	for i := 0; i < 4; i++ {
		f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
		f.AddResponse("darwin-rebuild", []string{"build", "--flake", leafDir + "#" + lockTestHost}, exec.Result{}, nil)
		f.AddResponse("sudo", []string{"darwin-rebuild", "switch", "--flake", leafDir + "#" + lockTestHost}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("h\n")}, nil)
		f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
		f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	}
	var r exec.Runner = f
	if gate != nil {
		gate.Runner = f
		r = gate
	}
	w, err := Open(root, r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.hostnameFn = func() string { return lockTestHost }
	return w
}

// runLockVerb runs verb ("build" or "apply") with the given wait.
func runLockVerb(ctx context.Context, w *Workspace, verb string, wait time.Duration, out io.Writer) error {
	switch verb {
	case "build":
		return w.Build(ctx, out, BuildOptions{Wait: wait})
	default:
		return w.Apply(ctx, out, ApplyOptions{Force: true, Wait: wait})
	}
}

// startGated starts verb in a goroutine against a gated runner and returns once
// it is inside its (blocked) command, holding its locks. finish releases the
// gate and returns the run's error.
func startGated(t *testing.T, root, verb string) (finish func() error) {
	t.Helper()
	gate := newGate()
	w := newLockWS(t, root, gate)
	done := make(chan error, 1)
	go func() { done <- runLockVerb(context.Background(), w, verb, 0, io.Discard) }()
	select {
	case <-gate.started:
	case err := <-done:
		t.Fatalf("%s returned before reaching its command: %v", verb, err)
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never reached its command", verb)
	}
	var once sync.Once
	var result error
	finish = func() error {
		once.Do(func() {
			close(gate.release)
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				result = errors.New("timed out waiting for the gated run to finish")
			}
		})
		return result
	}
	t.Cleanup(func() { _ = finish() }) // never leave the goroutine or the lock behind
	return finish
}

// wantBusy asserts err is a LockBusyError for key, naming the holder.
func wantBusy(t *testing.T, err error, key, holderVerb, holderRoot string) *LockBusyError {
	t.Helper()
	var lb *LockBusyError
	if !errors.As(err, &lb) {
		t.Fatalf("want a *LockBusyError, got %v", err)
	}
	if lb.Key != key {
		t.Errorf("busy key = %q, want %q", lb.Key, key)
	}
	if lb.Holder == nil {
		t.Fatalf("no holder recorded: %v", err)
	}
	if lb.Holder.PID != os.Getpid() || lb.Holder.Verb != holderVerb || lb.Holder.Root != holderRoot {
		t.Errorf("holder = %+v, want pid %d verb %s root %s", *lb.Holder, os.Getpid(), holderVerb, holderRoot)
	}
	msg := err.Error()
	for _, want := range []string{"pid " + strconv.Itoa(os.Getpid()), "pn workspace " + holderVerb, holderRoot, "started", "--wait=<duration>", "needs '='"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	return lb
}

// blockedFor reports whether done stays unresolved for d.
func blockedFor(done <-chan error, d time.Duration) bool {
	select {
	case <-done:
		return false
	case <-time.After(d):
		return true
	}
}

func awaitErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the waiting run to finish")
		return nil
	}
}

// A second apply, from the same OR a different workspace root, fails fast naming
// the holder; with --wait it blocks until the first finishes or the wait expires.
func TestRebuildLock_SecondApply(t *testing.T) {
	for _, sameRoot := range []bool{true, false} {
		name := "different_root"
		if sameRoot {
			name = "same_root"
		}
		t.Run(name, func(t *testing.T) {
			lockTestEnv(t)
			root1 := newLockRoot(t)
			root2 := root1
			if !sameRoot {
				root2 = newLockRoot(t)
			}
			finish := startGated(t, root1, "apply")

			var out bytes.Buffer
			err := runLockVerb(context.Background(), newLockWS(t, root2, nil), "apply", 0, &out)
			wantBusy(t, err, "apply", "apply", root1)

			// --wait=<short> gives up and says how long it waited.
			err = runLockVerb(context.Background(), newLockWS(t, root2, nil), "apply", 150*time.Millisecond, io.Discard)
			lb := wantBusy(t, err, "apply", "apply", root1)
			if lb.Waited < 150*time.Millisecond {
				t.Errorf("Waited = %v, want >= 150ms", lb.Waited)
			}

			// --wait=<long> blocks until the first apply finishes, then runs.
			done := make(chan error, 1)
			var waitOut bytes.Buffer
			go func() {
				done <- runLockVerb(context.Background(), newLockWS(t, root2, nil), "apply", 30*time.Second, &waitOut)
			}()
			if !blockedFor(done, 150*time.Millisecond) {
				t.Fatal("the waiting apply finished while the first still held the lock")
			}
			if err := finish(); err != nil {
				t.Fatalf("first apply: %v", err)
			}
			if err := awaitErr(t, done); err != nil {
				t.Fatalf("waiting apply: %v", err)
			}
			if !strings.Contains(waitOut.String(), "waiting up to 30s") {
				t.Errorf("no wait notice in output:\n%s", waitOut.String())
			}

			// Everything released: a plain apply works again.
			if err := runLockVerb(context.Background(), newLockWS(t, root2, nil), "apply", 0, io.Discard); err != nil {
				t.Fatalf("apply after release: %v", err)
			}
		})
	}
}

// A build fails fast while an apply holds the apply key, even from a different
// workspace root (workforest versus canonical); --wait blocks instead.
func TestRebuildLock_BuildFailsWhileApplyHoldsApplyKey(t *testing.T) {
	lockTestEnv(t)
	canonical := newLockRoot(t)
	workforest := newLockRoot(t)
	finish := startGated(t, canonical, "apply")

	err := runLockVerb(context.Background(), newLockWS(t, workforest, nil), "build", 0, io.Discard)
	wantBusy(t, err, "apply", "apply", canonical)

	done := make(chan error, 1)
	go func() {
		done <- runLockVerb(context.Background(), newLockWS(t, workforest, nil), "build", 30*time.Second, io.Discard)
	}()
	if !blockedFor(done, 150*time.Millisecond) {
		t.Fatal("the waiting build finished while the apply still held the apply key")
	}
	if err := finish(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := awaitErr(t, done); err != nil {
		t.Fatalf("waiting build: %v", err)
	}
}

// Two builds in one root conflict; builds in different roots do not.
func TestRebuildLock_Builds(t *testing.T) {
	t.Run("same_root_conflict", func(t *testing.T) {
		lockTestEnv(t)
		root := newLockRoot(t)
		finish := startGated(t, root, "build")
		err := runLockVerb(context.Background(), newLockWS(t, root, nil), "build", 0, io.Discard)
		wantBusy(t, err, "build", "build", root)

		done := make(chan error, 1)
		go func() {
			done <- runLockVerb(context.Background(), newLockWS(t, root, nil), "build", 30*time.Second, io.Discard)
		}()
		if !blockedFor(done, 150*time.Millisecond) {
			t.Fatal("the waiting build finished while the first still held the build key")
		}
		if err := finish(); err != nil {
			t.Fatal(err)
		}
		if err := awaitErr(t, done); err != nil {
			t.Fatalf("waiting build: %v", err)
		}
	})
	t.Run("different_roots_do_not_conflict", func(t *testing.T) {
		lockTestEnv(t)
		root1, root2 := newLockRoot(t), newLockRoot(t)
		finish1 := startGated(t, root1, "build")
		finish2 := startGated(t, root2, "build") // reaches its command while build 1 holds its own key
		if err := finish1(); err != nil {
			t.Errorf("build 1: %v", err)
		}
		if err := finish2(); err != nil {
			t.Errorf("build 2: %v", err)
		}
	})
}

// While a build holds a root's build key, an apply in the same root fails fast
// (or waits), but an apply in another root does not wait for it, and the failed
// apply leaves no apply lock behind.
func TestRebuildLock_ApplyVersusBuildInSameRoot(t *testing.T) {
	lockTestEnv(t)
	root1, root2 := newLockRoot(t), newLockRoot(t)
	finish := startGated(t, root1, "build")

	err := runLockVerb(context.Background(), newLockWS(t, root1, nil), "apply", 0, io.Discard)
	wantBusy(t, err, "build", "build", root1)

	// The failed apply released the apply key it took first: an apply from
	// another root is not blocked by the build and not by a leaked key.
	if err := runLockVerb(context.Background(), newLockWS(t, root2, nil), "apply", 0, io.Discard); err != nil {
		t.Fatalf("apply from another root while a build runs: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- runLockVerb(context.Background(), newLockWS(t, root1, nil), "apply", 30*time.Second, io.Discard)
	}()
	if !blockedFor(done, 150*time.Millisecond) {
		t.Fatal("the waiting apply finished while the build still held the build key")
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if err := awaitErr(t, done); err != nil {
		t.Fatalf("waiting apply: %v", err)
	}
}

// An apply holds its root's build key until after the build command and every
// step that follows it (the later read of ./result belongs to those steps).
func TestRebuildLock_ApplyHoldsBuildKeyThroughItsRun(t *testing.T) {
	lockTestEnv(t)
	root := newLockRoot(t)
	w := newLockWS(t, root, nil)
	other := newLockWS(t, root, nil)
	probed := map[string]error{}
	w.stepRecorder = func(verb string, shared bool, step string) {
		switch step {
		case stepRunCommand, stepPostSteps, stepMarkApplied:
			probed[step] = runLockVerb(context.Background(), other, "build", 0, io.Discard)
		}
	}
	if err := runLockVerb(context.Background(), w, "apply", 0, io.Discard); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, step := range []string{stepRunCommand, stepPostSteps, stepMarkApplied} {
		err, ok := probed[step]
		if !ok {
			t.Fatalf("step %s never ran", step)
		}
		// A competing build reports the BUILD key first only if the apply holds its
		// root's build key (a build takes its build key before probing the apply key).
		var lb *LockBusyError
		if !errors.As(err, &lb) || lb.Key != "build" {
			t.Errorf("a build at step %s was not refused on the build key: %v", step, err)
		}
	}
	// Released at the end.
	if err := runLockVerb(context.Background(), other, "build", 0, io.Discard); err != nil {
		t.Fatalf("build after the apply finished: %v", err)
	}
}

// A dry run (--show-nix-commands style) takes no lock, so it works while an
// apply holds everything.
func TestRebuildLock_DryRunTakesNoLock(t *testing.T) {
	lockTestEnv(t)
	root := newLockRoot(t)
	finish := startGated(t, root, "apply")
	defer func() { _ = finish() }()
	var out bytes.Buffer
	err := newLockWS(t, root, nil).Build(context.Background(), &out, BuildOptions{ShowNixCommandsOnly: true})
	if err != nil {
		t.Fatalf("dry-run build: %v", err)
	}
}

// A cancelled context ends a wait with the context's error.
func TestRebuildLock_WaitHonoursContext(t *testing.T) {
	lockTestEnv(t)
	root := newLockRoot(t)
	finish := startGated(t, root, "apply")
	defer func() { _ = finish() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runLockVerb(ctx, newLockWS(t, root, nil), "apply", WaitForever, io.Discard) }()
	if !blockedFor(done, 100*time.Millisecond) {
		t.Fatal("a wait-forever apply finished while the lock was held")
	}
	cancel()
	if err := awaitErr(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

const (
	lockHelperEnv      = "PN_LOCK_TEST_HELPER_ROOT"
	lockHelperStateEnv = "PN_LOCK_TEST_HELPER_STATE" // the package TestMain overrides XDG_STATE_HOME, so it travels under its own name
)

// TestRebuildLockHelperProcess is not a test: it is the body of the subprocess
// TestRebuildLock_ReleasedWhenHolderIsKilled starts (the test binary re-execs
// itself). It takes the apply lock exactly as pn does, announces it, and then
// holds it until its stdin closes, so it also ends if the parent test dies.
func TestRebuildLockHelperProcess(t *testing.T) {
	root := os.Getenv(lockHelperEnv)
	if root == "" {
		t.Skip("helper process body; only runs when re-exec'd by TestRebuildLock_ReleasedWhenHolderIsKilled")
	}
	t.Setenv("XDG_STATE_HOME", os.Getenv(lockHelperStateEnv))
	w := &Workspace{root: root, hostnameFn: func() string { return lockTestHost }}
	if _, err := w.acquireRebuildLock(context.Background(), io.Discard, "apply", "leaf", 0); err != nil {
		os.Stdout.WriteString("error: " + err.Error() + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("locked\n")
	_, _ = io.Copy(io.Discard, os.Stdin) // hold until the parent goes away
	os.Exit(0)
}

// The kernel releases the flock when the holder is SIGKILLed: a new run then
// acquires with no stale-lock cleanup.
func TestRebuildLock_ReleasedWhenHolderIsKilled(t *testing.T) {
	lockTestEnv(t)
	root := newLockRoot(t)

	cmd := osexec.Command(os.Args[0], "-test.run=^TestRebuildLockHelperProcess$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+root, lockHelperStateEnv+"="+os.Getenv("XDG_STATE_HOME"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	var reaped sync.Once
	reap := func() {
		reaped.Do(func() {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(reap) // never leak the helper, even on a failed assertion

	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(20 * time.Second)
waitLocked:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("helper exited before locking")
			}
			if strings.HasPrefix(l, "error:") {
				t.Fatalf("helper: %s", l)
			}
			if l == "locked" {
				break waitLocked
			}
		case <-deadline:
			t.Fatal("helper never reported the lock")
		}
	}

	// While the helper lives, an apply is refused and names the helper's pid.
	err = runLockVerb(context.Background(), newLockWS(t, root, nil), "apply", 0, io.Discard)
	var lb *LockBusyError
	if !errors.As(err, &lb) || lb.Holder == nil || lb.Holder.PID != cmd.Process.Pid {
		t.Fatalf("want a LockBusyError naming helper pid %d, got %v", cmd.Process.Pid, err)
	}

	if err := cmd.Process.Kill(); err != nil { // SIGKILL: no deferred release can run
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reaped.Do(func() {}) // already reaped

	if err := runLockVerb(context.Background(), newLockWS(t, root, nil), "apply", 0, io.Discard); err != nil {
		t.Fatalf("apply after the holder was SIGKILLed: %v", err)
	}
}

func TestParseWait(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"5m", 5 * time.Minute, false},
		{"90s", 90 * time.Second, false},
		{"1h30m", 90 * time.Minute, false},
		{"0", 0, false},
		{"0s", 0, false},
		{"forever", WaitForever, false},
		{"-1m", 0, true},
		{"5", 0, true},
		{"", 0, true},
		{"soon", 0, true},
	} {
		got, err := ParseWait(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseWait(%q) = %v, %v; want %v, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestRebuildLockDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x/state")
	if got, err := rebuildLockDir(); err != nil || got != "/x/state/pn/locks" {
		t.Errorf("XDG: got %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/h")
	if got, err := rebuildLockDir(); err != nil || got != "/h/.local/state/pn/locks" {
		t.Errorf("HOME fallback: got %q, %v", got, err)
	}
	t.Setenv("HOME", "")
	if _, err := rebuildLockDir(); err == nil {
		t.Error("with neither XDG_STATE_HOME nor HOME the lock dir must be an error")
	}
}

func TestRebuildLockNames(t *testing.T) {
	if got := applyLockName("leaf", "host1"); got != "apply-leaf-host1.lock" {
		t.Errorf("applyLockName = %q", got)
	}
	if applyLockName("a/b", "h o") != "apply-a_b-h_o.lock" {
		t.Errorf("unsafe characters must be mapped: %q", applyLockName("a/b", "h o"))
	}
	if applyLockName("leaf", "") != "apply-leaf-unknown.lock" {
		t.Errorf("empty hostname: %q", applyLockName("leaf", ""))
	}
	r1, r2 := t.TempDir(), t.TempDir()
	if buildLockName(r1, "leaf") != buildLockName(r1, "leaf") {
		t.Error("build key must be stable")
	}
	if buildLockName(r1, "leaf") == buildLockName(r2, "leaf") {
		t.Error("different roots must have different build keys")
	}
	if buildLockName(r1, "leaf") == buildLockName(r1, "other") {
		t.Error("different terminals must have different build keys")
	}
	if n := buildLockName(r1, "leaf"); !strings.HasPrefix(n, "build-") || !strings.HasSuffix(n, ".lock") {
		t.Errorf("build key name = %q", n)
	}
}

// Lock files live under $XDG_STATE_HOME/pn/locks and hold the holder record.
func TestRebuildLock_FilesAndRecord(t *testing.T) {
	lockTestEnv(t)
	root := newLockRoot(t)
	finish := startGated(t, root, "apply")
	defer func() { _ = finish() }()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pn", "locks")
	h := readHolder(filepath.Join(dir, applyLockName("leaf", lockTestHost)))
	if h == nil || h.PID != os.Getpid() || h.Verb != "apply" || h.Root != root || h.Terminal != "leaf" {
		t.Fatalf("apply record = %+v", h)
	}
	if _, err := time.Parse(time.RFC3339Nano, h.Started); err != nil {
		t.Errorf("started %q is not RFC3339: %v", h.Started, err)
	}
	if b := readHolder(filepath.Join(dir, buildLockName(root, "leaf"))); b == nil || b.Verb != "apply" {
		t.Errorf("an apply must also record itself in its root's build key, got %+v", b)
	}
}
