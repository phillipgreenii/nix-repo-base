package nixlog

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/testfix"
)

// run feeds lines through a Processor, finishes it and returns the result.
func run(lines []string) (*Processor, *collect) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	feedLines(p, lines...)
	p.Finish(t0.Add(time.Hour))
	return p, c
}

func TestHashMismatchGolden(t *testing.T) {
	p, c := run(testfix.HashMismatchRun(testfix.PlaceholderHash))
	s := p.Summary()
	if k := s.FailureKind(1); k != ErrHashMismatch {
		t.Errorf("kind = %q, want hash_mismatch (it outranks the dependent's dependency_failed)", k)
	}
	// FirstError: the msg (not raw_msg, which this event lacks), ANSI-free,
	// whitespace collapsed, no store hashes, at most 200 characters.
	if !strings.HasPrefix(s.FirstError, "error: hash mismatch in fixed-output derivation '/nix/store/demo-fixture-1-deps.drv': specified: sha256-AAAA") {
		t.Errorf("FirstError = %q", s.FirstError)
	}
	if len([]rune(s.FirstError)) != MaxErrorLen {
		t.Errorf("FirstError length = %d, want the %d-character cap", len([]rune(s.FirstError)), MaxErrorLen)
	}
	if strings.ContainsAny(s.FirstError, "\x1b\n") || reStoreHash.MatchString(s.FirstError) {
		t.Errorf("FirstError not clean: %q", s.FirstError)
	}
	if got := strings.Join(s.FailedDrvs, ","); got != "demo-fixture-1-deps,demo-fixture-1" {
		t.Errorf("FailedDrvs = %q", got)
	}
	deps, ok := c.named("demo-fixture-1-deps")
	if !ok || !deps.Failed || deps.ErrorType != ErrHashMismatch || !deps.Placeholder || deps.Instant {
		t.Errorf("fixed-output build = %+v (found %v)", deps, ok)
	}
	top, ok := c.named("demo-fixture-1")
	if !ok || top.ErrorType != ErrDependencyFailed || !top.Instant || top.Placeholder {
		t.Errorf("dependent = %+v (found %v)", top, ok)
	}
}

func TestHashMismatchWithARealSpecifiedHashIsNotAPlaceholder(t *testing.T) {
	_, c := run(testfix.HashMismatchRun(testfix.RealHash))
	a, ok := c.named("demo-fixture-1-deps")
	if !ok || a.ErrorType != ErrHashMismatch || a.Placeholder {
		t.Errorf("fixed-output build = %+v (found %v)", a, ok)
	}
}

func TestIsPlaceholderHash(t *testing.T) {
	for h, want := range map[string]bool{
		testfix.PlaceholderHash:   true,
		"sha256-AAAA=":            true,
		"sha512-AAAAAAAAAAAAAAAA": true,
		testfix.RealHash:          false,
		"sha256-AAAB=":            false,
		"sha256-=":                false,
		"":                        false,
		"AAAA":                    true,
	} {
		if got := isPlaceholderHash(h); got != want {
			t.Errorf("isPlaceholderHash(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestMismatchAfterStopAndBeforeStopBothFailTheBuild(t *testing.T) {
	deps := testfix.Drv(10, "demo-2-deps")
	msg := testfix.HashMismatchMsg(deps, testfix.PlaceholderHash, testfix.RealHash)
	_, early := run([]string{testfix.StartBuild(1, deps), msg, testfix.Stop(1)})
	_, late := run([]string{testfix.StartBuild(1, deps), testfix.Stop(1), msg})
	for name, c := range map[string]*collect{"message before stop": early, "message after stop": late} {
		if len(c.acts) != 1 || !c.acts[0].Failed || c.acts[0].ErrorType != ErrHashMismatch || c.acts[0].Instant {
			t.Errorf("%s: %+v", name, c.acts)
		}
	}
}

func TestEvalCacheBusyAndDBBusyAreCountedSeparately(t *testing.T) {
	p, c := run(testfix.EvalCacheBusyRun())
	s := p.Summary()
	if s.EvalCacheBusy != 3 || s.DBBusy != 2 {
		t.Errorf("EvalCacheBusy=%d DBBusy=%d, want 3 and 2", s.EvalCacheBusy, s.DBBusy)
	}
	if s.FirstError != "" || len(s.FailedDrvs) != 0 || s.FailureKind(1) != "" {
		t.Errorf("an ignored error is not a failure: %+v", s)
	}
	if len(c.acts) != 0 {
		t.Errorf("activities = %+v", c.acts)
	}
}

func TestEvalErrorWithNoDerivation(t *testing.T) {
	p, _ := run(testfix.EvalErrorRun())
	s := p.Summary()
	if k := s.FailureKind(1); k != ErrEval {
		t.Errorf("kind = %q, want eval_error", k)
	}
	if !strings.HasPrefix(s.FirstError, "error: … while calling the 'import' builtin at «string»:5:5:") {
		t.Errorf("FirstError = %q", s.FirstError)
	}
	if strings.Contains(s.FirstError, "/nix/store/0") || !strings.Contains(s.FirstError, "/nix/store/") {
		t.Errorf("store hash not removed from %q", s.FirstError)
	}
	if len(s.FailedDrvs) != 0 {
		t.Errorf("FailedDrvs = %v", s.FailedDrvs)
	}
}

func TestInterruptedByTheUserIsSignalAndBeatsEvalError(t *testing.T) {
	p, c := run(testfix.InterruptedRun())
	s := p.Summary()
	if k := s.FailureKind(1); k != ErrSignal {
		t.Errorf("kind = %q, want signal for exit 1 + interrupted message", k)
	}
	if !strings.HasPrefix(s.FirstError, "error: attribute 'missing' missing") {
		t.Errorf("FirstError is the FIRST error, got %q", s.FirstError)
	}
	// The open build is still closed as aborted.
	if len(c.acts) != 1 || c.acts[0].ErrorType != ErrAborted {
		t.Errorf("activities = %+v", c.acts)
	}
}

func TestSignalExitWithNoMessageIsSignal(t *testing.T) {
	p, _ := run(testfix.KilledRun())
	s := p.Summary()
	for _, code := range []int{130, 143, 129, 137} {
		if k := s.FailureKind(code); k != ErrSignal {
			t.Errorf("exit %d: kind = %q, want signal", code, k)
		}
	}
	for _, code := range []int{1, 2, 3, 127, 128, 255} {
		if k := s.FailureKind(code); k != "" {
			t.Errorf("exit %d: kind = %q, want nothing known", code, k)
		}
	}
	if s.FirstError != "" {
		t.Errorf("FirstError = %q", s.FirstError)
	}
}

func TestFailureKindPrecedenceAndOtherForUnclassifiedDrvErrors(t *testing.T) {
	d := testfix.Drv(60, "thing")
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"builder failure", []string{testfix.CannotBuild(d, testfix.Out(60, "thing"), "builder failed with exit code 1")}, ErrBuildFailed},
		{"dependency only", []string{testfix.CannotBuild(d, testfix.Out(60, "thing"), "1 dependency failed")}, ErrDependencyFailed},
		{"builder beats dependency", []string{
			testfix.CannotBuild(testfix.Drv(61, "dep"), testfix.Out(61, "dep"), "2 dependencies failed"),
			testfix.CannotBuild(d, testfix.Out(60, "thing"), "builder failed with exit code 2"),
		}, ErrBuildFailed},
		{"mismatch beats builder", []string{
			testfix.CannotBuild(d, testfix.Out(60, "thing"), "builder failed with exit code 2"),
			testfix.HashMismatchMsg(testfix.Drv(62, "fod"), testfix.RealHash, testfix.RealHash),
		}, ErrHashMismatch},
		{"error naming a drv we cannot classify", []string{testfix.Msg(0, "error: something odd about '"+d+"'")}, ErrOther},
		{"build beats eval", []string{
			testfix.Msg(0, "error: attribute 'x' missing"),
			testfix.CannotBuild(d, testfix.Out(60, "thing"), "builder failed with exit code 1"),
		}, ErrBuildFailed},
		{"a warning is not an error", []string{testfix.Msg(0, "warning: Git tree is dirty")}, ""},
		{"error text at a non-zero level is not an error", []string{testfix.Msg(3, "error: not at level 0")}, ""},
	}
	for _, c := range cases {
		p, _ := run(c.lines)
		if got := p.Summary().FailureKind(1); got != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFailedDrvsAreCappedAtFiveInMessageOrder(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, testfix.CannotBuild(testfix.Drv(100+i, fmt.Sprintf("d%d", i)),
			testfix.Out(100+i, "x"), "builder failed with exit code 1"))
	}
	s := func() Summary { p, _ := run(lines); return p.Summary() }()
	if got := strings.Join(s.FailedDrvs, ","); got != "d0,d1,d2,d3,d4" {
		t.Errorf("FailedDrvs = %q", got)
	}
	// A repeated message names the drv once.
	s = func() Summary { p, _ := run(append(lines[:1:1], lines[0])); return p.Summary() }()
	if len(s.FailedDrvs) != 1 {
		t.Errorf("FailedDrvs = %v", s.FailedDrvs)
	}
}

func TestSummaryFailedDrvsIsACopy(t *testing.T) {
	p, _ := run(testfix.HashMismatchRun(testfix.RealHash))
	a := p.Summary()
	a.FailedDrvs[0] = "mutated"
	if b := p.Summary(); b.FailedDrvs[0] == "mutated" {
		t.Error("Summary leaked its internal slice")
	}
}

func TestFirstErrorMatchesMsgNotRawMsg(t *testing.T) {
	// raw_msg alone never carries the "error:" prefix: a message with only
	// raw_msg text (and an empty msg) records no first error.
	p, _ := run([]string{`{"action":"msg","level":0,"msg":"","raw_msg":"error: looks like one"}`})
	if s := p.Summary(); s.FirstError != "" || s.FailureKind(1) != "" {
		t.Errorf("summary = %+v", s)
	}
	// With both, msg wins for the error text even though raw_msg has the drv.
	d := testfix.Drv(70, "x")
	p, _ = run([]string{testfix.CannotBuild(d, testfix.Out(70, "x"), "builder failed with exit code 1")})
	if s := p.Summary(); !strings.HasPrefix(s.FirstError, "error: Cannot build '/nix/store/x.drv'. Reason: builder failed with exit code 1.") {
		t.Errorf("FirstError = %q", s.FirstError)
	}
}

func TestCleanErrorRedactsCredentialsAndTruncates(t *testing.T) {
	cases := map[string]string{
		"error: unable to download 'https://user:s3cr3t@example.com/a/b.tar.gz': HTTP error 401": "error: unable to download 'https://<redacted>@example.com/a/b.tar.gz': HTTP error 401",
		"error: git+https://tok3n@host.example/org/repo failed":                                  "error: git+https://<redacted>@host.example/org/repo failed",
		"error: user:pw@ without a scheme stays; see http://a.example/x@y":                       "error: user:pw@ without a scheme stays; see http://a.example/x@y",
		"error:   spaces \n\t and\nnewlines":                                                     "error: spaces and newlines",
		"error: in /nix/store/" + strings.Repeat("a", 32) + "-pkg-1.0/bin":                       "error: in /nix/store/pkg-1.0/bin",
	}
	for in, want := range cases {
		if got := CleanError(in); got != want {
			t.Errorf("CleanError(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	long := "error: " + strings.Repeat("é", 500)
	if got := []rune(CleanError(long)); len(got) != MaxErrorLen {
		t.Errorf("length = %d, want %d characters", len(got), MaxErrorLen)
	}
	// Credentials are redacted BEFORE the cut, so a cut never exposes half of one.
	pad := strings.Repeat("x", MaxErrorLen-len("error: https://us"))
	if got := CleanError("error: " + pad + "https://user:token@h/"); strings.Contains(got, "token") {
		t.Errorf("credential leaked through truncation: %q", got)
	}
}

func TestFirstErrorRecordsCredentialsRedacted(t *testing.T) {
	p, _ := run([]string{testfix.Msg(0, "\x1b[31;1merror:\x1b[0m cannot fetch 'https://user:token@host.example/repo.git'")})
	s := p.Summary()
	if s.FirstError != "error: cannot fetch 'https://<redacted>@host.example/repo.git'" {
		t.Errorf("FirstError = %q", s.FirstError)
	}
}

func TestWaitCountCountsEmittedWaitActivities(t *testing.T) {
	out := testfix.Out(80, "locked")
	p, _ := run([]string{
		testfix.StartWait(1, out), testfix.Stop(1),
		testfix.StartWait(2, out), // still open at the end: aborted, still a wait span
	})
	if s := p.Summary(); s.Waits != 2 {
		t.Errorf("Waits = %d, want 2", s.Waits)
	}
}

func TestWaitForLockGoldenCountsOneWait(t *testing.T) {
	p, _, _ := feedFile(t, "wait-for-lock.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	if s := p.Summary(); s.Waits != 1 {
		t.Errorf("Waits = %d", s.Waits)
	}
}

// Every kind FailureKind can return is in the closed set, whatever the input.
func TestFailureKindNeverEscapesTheClosedSet(t *testing.T) {
	closed := map[string]bool{"": true}
	for _, k := range FailureKinds {
		closed[k] = true
	}
	for _, lines := range [][]string{
		testfix.HashMismatchRun(testfix.PlaceholderHash), testfix.EvalCacheBusyRun(), testfix.EvalErrorRun(),
		testfix.InterruptedRun(), testfix.KilledRun(),
		{testfix.Msg(0, "error: odd '"+testfix.Drv(1, "x")+"'")},
	} {
		p, _ := run(lines)
		for _, code := range []int{0, 1, 2, 3, 127, 130, 143, 255} {
			if k := p.Summary().FailureKind(code); !closed[k] {
				t.Errorf("kind %q for exit %d is outside the closed set", k, code)
			}
		}
	}
}
