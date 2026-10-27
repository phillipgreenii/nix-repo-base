package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

// Tests of apply's build-as-user flow (bead pg2-sdbhh4.9, ADR 0038). Everything
// here runs against fake runners: nothing real runs, in particular no sudo, no
// nix-env and no darwin-rebuild activation. The real-tool tests (the drift guard
// and the probe) are in apply_asuser_realtool_test.go.

const (
	fakeHash   = "0123456789abcdfghijklmnpqrsvwxyz" // 32 characters of the nix base32 alphabet
	fakeHash2  = "zyxwvsrqpnmlkjihgfdcba9876543210"
	asUserHost = "asuserhost"
)

// asUserTOML is applyTOML plus the v1 activate_command and a build_command that
// leaves ./result.
const asUserTOML = `
[workspace]
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"
activate_command = "sudo {out}/sw/bin/darwin-rebuild activate"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`

// fakeSystem is a store directory with a system "built" in it.
type fakeSystem struct {
	store string
	out   string
}

// newFakeSystem lays out <store>/<hash>-darwin-system-test with an executable
// activate and sw/bin/darwin-rebuild as a symlink into ANOTHER store package, the
// shape of a real system (so the invoked-path versus realpath semantics hold).
func newFakeSystem(t *testing.T, store, hash string) fakeSystem {
	t.Helper()
	out := filepath.Join(store, hash+"-darwin-system-test")
	pkg := filepath.Join(store, hash+"-darwin-rebuild", "bin")
	for _, d := range []string{out + "/sw/bin", pkg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{out + "/activate", pkg + "/darwin-rebuild"} {
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(pkg+"/darwin-rebuild", out+"/sw/bin/darwin-rebuild"); err != nil {
		t.Fatal(err)
	}
	return fakeSystem{store: store, out: out}
}

const realUsage = "darwin-rebuild [--help] {edit | switch | activate | build | check | changelog}\n               [--list-generations] [{--profile-name | -p} name] [--rollback]\n"

// asUserFake is the Runner for these tests. Each behaviour is a field a test can
// replace; the defaults are a successful run. Every call is recorded.
type asUserFake struct {
	t   *testing.T
	sys fakeSystem

	mu    sync.Mutex
	calls []exec.Call

	build      func(ctx context.Context, opts exec.RunOptions) (exec.Result, error)
	probe      func(ctx context.Context, opts exec.RunOptions) (exec.Result, error)
	setProfile func(ctx context.Context, args []string) (exec.Result, error)
	activate   func(ctx context.Context, args []string) (exec.Result, error)
	fallback   func(ctx context.Context, args []string) (exec.Result, error)
}

func cmdErr(name string, args []string, code int) (exec.Result, error) {
	res := exec.Result{ExitCode: code, Stderr: []byte(name + " failed\n")}
	if code < 0 {
		res.Signal = "SIGTERM"
	}
	return res, &exec.CommandError{Name: name, Args: args, Result: res}
}

func (f *asUserFake) Run(ctx context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, exec.Call{Name: name, Args: append([]string{}, args...), Opts: opts})
	f.mu.Unlock()
	switch {
	case name == "nix" && len(args) > 0 && args[0] == "eval":
		return exec.Result{}, nil
	case name == "git":
		for _, a := range args {
			switch a {
			case "status":
				return exec.Result{}, nil
			case "rev-parse":
				return exec.Result{Stdout: []byte("h\n")}, nil
			case "--exec-path":
				return exec.Result{Stdout: []byte("/g\n")}, nil
			}
		}
		return exec.Result{}, nil
	case name == "darwin-rebuild" && len(args) > 0 && args[0] == "build":
		if f.build != nil {
			return f.build(ctx, opts)
		}
		return exec.Result{}, os.Symlink(f.sys.out, filepath.Join(opts.Dir, "result"))
	case strings.HasSuffix(name, "/sw/bin/darwin-rebuild") && len(args) == 1 && args[0] == "--help":
		if f.probe != nil {
			return f.probe(ctx, opts)
		}
		return cmdErrResult(name, args, exec.Result{ExitCode: 1, Stderr: []byte(realUsage)})
	case name == "sudo" && len(args) > 0 && args[0] == "env":
		if f.setProfile != nil {
			return f.setProfile(ctx, args)
		}
		return exec.Result{}, nil
	case name == "sudo" && len(args) == 2 && strings.HasSuffix(args[0], "/sw/bin/darwin-rebuild") && args[1] == "activate":
		if f.activate != nil {
			return f.activate(ctx, args)
		}
		return exec.Result{}, nil
	case name == "sudo" && len(args) > 0 && args[0] == "darwin-rebuild" && args[1] == "switch":
		if f.fallback != nil {
			return f.fallback(ctx, args)
		}
		return exec.Result{}, nil
	}
	f.t.Errorf("asUserFake: unexpected call %s %v", name, args)
	return exec.Result{}, fmt.Errorf("unexpected call %s %v", name, args)
}

// cmdErrResult mimics the real runner: a non-zero exit is returned both as the
// Result and as a *CommandError.
func cmdErrResult(name string, args []string, res exec.Result) (exec.Result, error) {
	if res.ExitCode == 0 {
		return res, nil
	}
	return res, &exec.CommandError{Name: name, Args: args, Result: res}
}

// calls returns the recorded calls that are not git or the daemon probe.
func (f *asUserFake) significant() []exec.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []exec.Call
	for _, c := range f.calls {
		if c.Name == "git" || (c.Name == "nix" && len(c.Args) > 0 && c.Args[0] == "eval") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (f *asUserFake) rootCalls() []exec.Call {
	var out []exec.Call
	for _, c := range f.significant() {
		if c.Name == "sudo" {
			out = append(out, c)
		}
	}
	return out
}

func argvOf(c exec.Call) string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

type asUserEnv struct {
	w       *Workspace
	f       *asUserFake
	root    string
	leafDir string
	depDir  string
	sys     fakeSystem
	state   string // XDG_STATE_HOME
}

// newAsUserEnv builds a trusted two-repo workspace over an asUserFake. toml is
// the pn-workspace.toml text; wrap, when non-nil, decorates the runner.
func newAsUserEnv(t *testing.T, toml string, wrap func(exec.Runner) exec.Runner) *asUserEnv {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml)
	writeFile(t, filepath.Join(root, LockFileName), applyLock)
	trustWS(t, root) // sets XDG_STATE_HOME to a fresh temp dir
	store := t.TempDir()
	f := &asUserFake{t: t, sys: newFakeSystem(t, store, fakeHash)}
	var r exec.Runner = f
	if wrap != nil {
		r = wrap(f)
	}
	w, err := Open(root, r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.hostnameFn = func() string { return asUserHost }
	w.asUserSeams = &asUserSeams{nixEnvUsable: func() bool { return true }, storeDir: store}
	return &asUserEnv{
		w: w, f: f, root: root,
		leafDir: filepath.Join(root, "leaf"), depDir: filepath.Join(root, "dep"),
		sys: f.sys, state: os.Getenv("XDG_STATE_HOME"),
	}
}

func (e *asUserEnv) apply(ctx context.Context, out io.Writer) error {
	return e.w.Apply(ctx, out, ApplyOptions{Force: true, Builder: "darwin-rebuild"})
}

// runDirs lists the per-run directories currently under apply-build.
func (e *asUserEnv) runDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.state, "pn", "apply-build"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, ent := range entries {
		out = append(out, ent.Name())
	}
	return out
}

// applyLogText is the most recent apply log.
func (e *asUserEnv) applyLogText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.state, "pn", "apply-logs", "latest"))
	if err != nil {
		t.Fatalf("no apply log: %v", err)
	}
	return string(b)
}

// trailerValue returns the value of a `key:` line of the apply log's trailer.
func trailerValue(t *testing.T, log, key string) string {
	t.Helper()
	_, trailer, ok := strings.Cut(log, applyLogTrailer)
	if !ok {
		t.Fatalf("no trailer in apply log:\n%s", log)
	}
	for _, l := range strings.Split(trailer, "\n") {
		if v, ok := strings.CutPrefix(l, key+": "); ok {
			return v
		}
	}
	return "<absent>"
}

func (e *asUserEnv) applied(t *testing.T) bool {
	t.Helper()
	_, ok, err := readAppliedState(e.w.appliedStateKeyPath("leaf"))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// --- The command sequence, the exact root argv, Dir, WrapNix ----------------

func TestAsUser_CommandSequenceExactRootArgvAndOptions(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	var buildDir string
	e.f.build = func(_ context.Context, opts exec.RunOptions) (exec.Result, error) {
		buildDir = opts.Dir
		fi, err := os.Stat(opts.Dir)
		if err != nil {
			t.Errorf("the build's Dir does not exist: %v", err)
		} else {
			if fi.Mode().Perm() != 0o700 {
				t.Errorf("run dir mode = %o, want 0700", fi.Mode().Perm())
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
				t.Errorf("run dir owner = %d, want the invoking user %d", st.Uid, os.Getuid())
			}
		}
		return exec.Result{}, os.Symlink(e.sys.out, filepath.Join(opts.Dir, "result"))
	}
	var out bytes.Buffer
	if err := e.apply(context.Background(), &out); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out.String())
	}

	out0 := e.sys.out
	want := []string{
		"darwin-rebuild build --flake " + e.leafDir + "#" + asUserHost + " --override-input dep-input git+file://" + e.depDir,
		out0 + "/sw/bin/darwin-rebuild --help",
		"sudo env -i HOME=/var/root NIX_REMOTE=daemon PATH=/run/current-system/sw/bin:/usr/bin:/bin /run/current-system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set " + out0,
		"sudo " + out0 + "/sw/bin/darwin-rebuild activate",
	}
	calls := e.f.significant()
	if len(calls) != len(want) {
		t.Fatalf("calls = %d, want %d:\n%s", len(calls), len(want), dumpCalls(calls))
	}
	for i, c := range calls {
		if got := argvOf(c); got != want[i] {
			t.Errorf("call %d:\n got  %s\n want %s", i, got, want[i])
		}
	}

	// Exactly one build, no root nix build or eval.
	builds := 0
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "build" {
			builds++
		}
		if c.Name == "sudo" && (hasString(c.Args, "build") || hasString(c.Args, "switch") || c.Args[0] == "nix") {
			t.Errorf("a root step evaluates or builds: %s", argvOf(c))
		}
	}
	if builds != 1 {
		t.Errorf("nix builds = %d, want exactly 1", builds)
	}

	// build: user, wrapped, private dir (not the repo dir), own process group.
	b := calls[0]
	if !b.Opts.WrapNix || !b.Opts.OwnProcessGroup {
		t.Errorf("build options = %+v, want WrapNix and OwnProcessGroup", b.Opts)
	}
	if buildDir == "" || b.Opts.Dir != buildDir || strings.HasPrefix(buildDir, e.leafDir) || buildDir == e.leafDir {
		t.Errorf("build Dir = %q, want the private run dir (not the repo dir %s)", b.Opts.Dir, e.leafDir)
	}
	if filepath.Dir(buildDir) != filepath.Join(e.state, "pn", "apply-build") || !strings.HasPrefix(filepath.Base(buildDir), "run-") {
		t.Errorf("build Dir = %q, want <state>/pn/apply-build/run-*", buildDir)
	}
	// probe: user, unwrapped, in the run dir.
	if p := calls[1]; p.Opts.WrapNix || p.Opts.Dir != buildDir || p.Opts.OwnProcessGroup {
		t.Errorf("probe options = %+v", p.Opts)
	}
	// root steps: never wrapped, never their own process group (the sudo prompt
	// needs the terminal's foreground group).
	for _, c := range calls[2:] {
		if c.Opts.WrapNix || c.Opts.OwnProcessGroup {
			t.Errorf("root step %s options = %+v, want neither WrapNix nor OwnProcessGroup", argvOf(c), c.Opts)
		}
		if c.Opts.Env != nil {
			t.Errorf("root step %s passes an Env overlay %v (the out path travels as argv only)", argvOf(c), c.Opts.Env)
		}
	}
	// The directory is gone and the apply was recorded.
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("run dirs left after a successful apply: %v", dirs)
	}
	if !e.applied(t) {
		t.Error("a successful apply did not record the applied state")
	}
	if !strings.Contains(out.String(), "waiting for sudo") {
		t.Errorf("no 'waiting for sudo' line:\n%s", out.String())
	}
}

func hasString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func dumpCalls(cs []exec.Call) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString("  " + argvOf(c) + "\n")
	}
	return b.String()
}

// The apply log header records every step's command and the trailer names no
// failed step.
func TestAsUser_ApplyLogRecordsAllStepsAndNoFailure(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	log := e.applyLogText(t)
	header, _, _ := strings.Cut(log, applyLogOutput)
	for _, want := range []string{
		"command: darwin-rebuild build --flake " + e.leafDir + "#" + asUserHost,
		"set-profile command: sudo env -i HOME=/var/root NIX_REMOTE=daemon PATH=/run/current-system/sw/bin:/usr/bin:/bin /run/current-system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set <out>",
		"activate command: sudo <out>/sw/bin/darwin-rebuild activate",
		"fallback command (if the built system cannot be activated this way): sudo darwin-rebuild switch --flake " + e.leafDir + "#" + asUserHost,
	} {
		if !strings.Contains(header, want) {
			t.Errorf("apply log header lacks %q:\n%s", want, header)
		}
	}
	if got := trailerValue(t, log, "failed_step"); got != "none" {
		t.Errorf("failed_step = %q, want none", got)
	}
	if got := trailerValue(t, log, "exit_status"); got != "0" {
		t.Errorf("exit_status = %q, want 0", got)
	}
	if got := trailerValue(t, log, "result"); got != "success" {
		t.Errorf("result = %q", got)
	}
}

// With activate_command unset the legacy trailer has no failed_step line.
func TestAsUser_LegacyApplyLogHasNoFailedStep(t *testing.T) {
	e := newAsUserEnv(t, strings.Replace(asUserTOML, `activate_command = "sudo {out}/sw/bin/darwin-rebuild activate"`, "", 1), nil)
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != "<absent>" {
		t.Errorf("legacy trailer carries failed_step = %q", got)
	}
	for _, c := range e.f.significant() {
		if c.Name == "darwin-rebuild" {
			t.Errorf("the legacy flow ran a user build: %s", argvOf(c))
		}
	}
}

// --- Dry run ----------------------------------------------------------------

func TestAsUser_ShowNixCommandsPrintsTheBuildAndTheLaterSteps(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	var out bytes.Buffer
	if err := e.w.Apply(context.Background(), &out, ApplyOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"darwin-rebuild build --flake " + e.leafDir + "#" + asUserHost + " --override-input dep-input git+file://" + e.depDir,
		"# set-profile command: sudo env -i HOME=/var/root",
		"# activate command: sudo <out>/sw/bin/darwin-rebuild activate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dry run lacks %q:\n%s", want, got)
		}
	}
	if n := len(e.f.significant()); n != 0 {
		t.Errorf("a dry run ran %d commands", n)
	}
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("a dry run created run dirs %v", dirs)
	}
}

// --- WrapNix ------------------------------------------------------------------

// WrapNix is true for the user build only: false for the probe and both root steps.
func TestWrapNix_AsUserApplyWrapsTheBuildOnly(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	calls := e.f.significant()
	if len(calls) != 4 {
		t.Fatalf("calls:\n%s", dumpCalls(calls))
	}
	for i, wantWrap := range []bool{true, false, false, false} {
		if calls[i].Opts.WrapNix != wantWrap {
			t.Errorf("WrapNix of %q = %v, want %v", argvOf(calls[i]), calls[i].Opts.WrapNix, wantWrap)
		}
	}
	for _, c := range e.f.calls {
		if c.Name == "nix" && c.Opts.WrapNix {
			t.Errorf("the nix daemon probe must not set WrapNix")
		}
	}
}

// --- Path validation ----------------------------------------------------------

func TestValidateBuildOut(t *testing.T) {
	store := t.TempDir()
	good := newFakeSystem(t, store, fakeHash)

	// Positive: sw/bin/darwin-rebuild is a symlink into another store path (the
	// invoked path is validated, not its realpath, which is NOT under out).
	if err := validateBuildOut(good.out, store); err != nil {
		t.Errorf("a real-shaped system was rejected: %v", err)
	}
	real, _ := filepath.EvalSymlinks(filepath.Join(good.out, "sw", "bin", "darwin-rebuild"))
	if strings.HasPrefix(real, good.out) {
		t.Fatalf("fixture invalid: the realpath %s is under out", real)
	}
	// Positive: a two-level symlink chain.
	chain := newFakeSystem(t, store, fakeHash2)
	link := filepath.Join(store, "chain-link")
	if err := os.Symlink(filepath.Join(store, fakeHash2+"-darwin-rebuild", "bin", "darwin-rebuild"), link); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(chain.out, "sw", "bin", "darwin-rebuild"))
	if err := os.Symlink(link, filepath.Join(chain.out, "sw", "bin", "darwin-rebuild")); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildOut(chain.out, store); err != nil {
		t.Errorf("a two-level symlink chain was rejected: %v", err)
	}

	tests := []struct {
		name string
		out  func(t *testing.T) (out, storeDir string)
		want string
	}{
		{"outside the store", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			return s.out, "/nix/store"
		}, "not a single store path"},
		{"extra path element", func(t *testing.T) (string, string) { return good.out + "/sw", store }, "not a single store path"},
		{"trailing slash", func(t *testing.T) (string, string) { return good.out + "/", store }, "not in clean form"},
		{"dot dot", func(t *testing.T) (string, string) { return store + "/../" + filepath.Base(good.out), store }, "not in clean form"},
		{"relative", func(t *testing.T) (string, string) { return "result", store }, "not absolute"},
		{"empty", func(t *testing.T) (string, string) { return "", store }, "not absolute"},
		{"bad hash alphabet (contains e)", func(t *testing.T) (string, string) {
			return filepath.Join(store, "0123456789abcdfghijklmnpqrsvwxye-x"), store
		}, "not a single store path"},
		{"short hash", func(t *testing.T) (string, string) { return filepath.Join(store, "0123-x"), store }, "not a single store path"},
		{"not a directory (a file)", func(t *testing.T) (string, string) {
			p := filepath.Join(store, fakeHash2[:31]+"a-file")
			_ = os.WriteFile(p, nil, 0o755)
			return p, store
		}, "not a directory"},
		{"a symlink to a directory", func(t *testing.T) (string, string) {
			p := filepath.Join(store, "0123456789abcdfghijklmnpqrsvwxyy-symlink")
			if err := os.Symlink(good.out, p); err != nil {
				t.Fatal(err)
			}
			return p, store
		}, "not a directory"},
		{"missing", func(t *testing.T) (string, string) {
			return filepath.Join(store, "0123456789abcdfghijklmnpqrsvwxyx-missing"), store
		}, "no such file"},
		{"no activate", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			_ = os.Remove(s.out + "/activate")
			return s.out, filepath.Dir(s.out)
		}, "no activate script"},
		{"activate not executable", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			_ = os.Chmod(s.out+"/activate", 0o644)
			return s.out, filepath.Dir(s.out)
		}, "not an executable regular file"},
		{"activate is a symlink", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			_ = os.Remove(s.out + "/activate")
			_ = os.Symlink(s.out+"/sw/bin/darwin-rebuild", s.out+"/activate")
			return s.out, filepath.Dir(s.out)
		}, "not an executable regular file"},
		{"no sw/bin/darwin-rebuild", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			_ = os.Remove(s.out + "/sw/bin/darwin-rebuild")
			return s.out, filepath.Dir(s.out)
		}, "no sw/bin/darwin-rebuild"},
		{"darwin-rebuild dangling symlink", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			_ = os.Remove(s.out + "/sw/bin/darwin-rebuild")
			_ = os.Symlink(s.out+"/nowhere", s.out+"/sw/bin/darwin-rebuild")
			return s.out, filepath.Dir(s.out)
		}, "no sw/bin/darwin-rebuild"},
		{"darwin-rebuild not executable", func(t *testing.T) (string, string) {
			s := newFakeSystem(t, t.TempDir(), fakeHash)
			_ = os.Chmod(filepath.Join(filepath.Dir(s.out), fakeHash+"-darwin-rebuild", "bin", "darwin-rebuild"), 0o644)
			return s.out, filepath.Dir(s.out)
		}, "does not resolve to an executable regular file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, sd := tc.out(t)
			err := validateBuildOut(out, sd)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("validateBuildOut(%q) = %v, want an error containing %q", out, err, tc.want)
			}
		})
	}
}

// A result link that is missing, not a symlink, relative, or invalid fails the
// apply as read-result BEFORE any root process starts, and never builds twice.
func TestAsUser_ReadResultFailuresRunNoRootStep(t *testing.T) {
	cases := []struct {
		name  string
		build func(e *asUserEnv) func(context.Context, exec.RunOptions) (exec.Result, error)
		want  string
	}{
		{"no result link", func(e *asUserEnv) func(context.Context, exec.RunOptions) (exec.Result, error) {
			return func(context.Context, exec.RunOptions) (exec.Result, error) { return exec.Result{}, nil }
		}, "left no ./result"},
		{"result is a regular file", func(e *asUserEnv) func(context.Context, exec.RunOptions) (exec.Result, error) {
			return func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
				return exec.Result{}, os.WriteFile(filepath.Join(o.Dir, "result"), []byte("x"), 0o644)
			}
		}, "is not a symlink"},
		{"result is a relative link", func(e *asUserEnv) func(context.Context, exec.RunOptions) (exec.Result, error) {
			return func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
				return exec.Result{}, os.Symlink("../elsewhere", filepath.Join(o.Dir, "result"))
			}
		}, "not absolute"},
		{"result points outside the store", func(e *asUserEnv) func(context.Context, exec.RunOptions) (exec.Result, error) {
			return func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
				return exec.Result{}, os.Symlink("/usr", filepath.Join(o.Dir, "result"))
			}
		}, "not a single store path"},
		{"result points at an invalid system", func(e *asUserEnv) func(context.Context, exec.RunOptions) (exec.Result, error) {
			return func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
				bad := newFakeSystem(t, e.sys.store, fakeHash2)
				_ = os.Remove(bad.out + "/activate")
				return exec.Result{}, os.Symlink(bad.out, filepath.Join(o.Dir, "result"))
			}
		}, "no activate script"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAsUserEnv(t, asUserTOML, nil)
			e.f.build = tc.build(e)
			err := e.apply(context.Background(), io.Discard)
			if err == nil || !strings.Contains(err.Error(), "read build result") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a read-build-result error containing %q", err, tc.want)
			}
			if n := len(e.f.rootCalls()); n != 0 {
				t.Errorf("a root step ran after a failed read: %d", n)
			}
			builds := 0
			for _, c := range e.f.significant() {
				if c.Name == "darwin-rebuild" {
					builds++
				}
				if c.Name == "sudo" || strings.HasSuffix(c.Name, "/sw/bin/darwin-rebuild") {
					t.Errorf("unexpected call after a failed read: %s", argvOf(c))
				}
			}
			if builds != 1 {
				t.Errorf("builds = %d, want 1 (no second build)", builds)
			}
			log := e.applyLogText(t)
			if got := trailerValue(t, log, "failed_step"); got != "read-result" {
				t.Errorf("failed_step = %q, want read-result", got)
			}
			if got := trailerValue(t, log, "exit_status"); got != "none" {
				t.Errorf("exit_status = %q, want none", got)
			}
			if e.applied(t) {
				t.Error("markApplied written on a failure")
			}
			if dirs := e.runDirs(t); len(dirs) != 0 {
				t.Errorf("run dirs left: %v", dirs)
			}
		})
	}
}

// --- Read once, and the root's build key ---------------------------------------

// The out path is read ONCE; a link rewritten afterwards changes nothing, and the
// root steps get the validated string.
func TestAsUser_OutPathIsReadOnceAndNeverReread(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	other := newFakeSystem(t, e.sys.store, fakeHash2)
	e.f.probe = func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
		// Rewrite R/result to a different, perfectly valid system after the read.
		link := filepath.Join(o.Dir, "result")
		_ = os.Remove(link)
		if err := os.Symlink(other.out, link); err != nil {
			t.Error(err)
		}
		return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte(realUsage)})
	}
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.f.rootCalls() {
		if strings.Contains(argvOf(c), other.out) || !strings.Contains(argvOf(c), e.sys.out) {
			t.Errorf("root step uses the rewritten link, not the validated string: %s", argvOf(c))
		}
	}
}

// The root's build key is held from before the build until after the read (and
// through every later step), so a competing build in the same root is refused.
func TestAsUser_HoldsTheBuildKeyThroughTheReadAndLater(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	other, err := Open(e.root, exec.NewFakeRunner())
	if err != nil {
		t.Fatal(err)
	}
	other.hostnameFn = func() string { return asUserHost }
	probed := map[string]error{}
	probe := func(label string) {
		probed[label] = other.Build(context.Background(), io.Discard, BuildOptions{})
	}
	e.f.build = func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
		probe("during the build")
		return exec.Result{}, os.Symlink(e.sys.out, filepath.Join(o.Dir, "result"))
	}
	e.w.stepRecorder = func(_ string, shared bool, step string) {
		switch step {
		case stepReadResult, stepProbe, stepSetProfile, stepActivate, stepPostSteps, stepMarkApplied:
			probe("at " + step)
		}
	}
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"during the build", "at " + stepReadResult, "at " + stepProbe, "at " + stepSetProfile, "at " + stepActivate, "at " + stepMarkApplied} {
		var lb *LockBusyError
		if err, ok := probed[label]; !ok || !errors.As(err, &lb) || lb.Key != "build" {
			t.Errorf("%s: a competing build was not refused on the build key: %v", label, err)
		}
	}
}

// --- Fallback --------------------------------------------------------------------

func TestAsUser_ProbeFailureFallsBackToTheLegacyApply(t *testing.T) {
	cases := []struct {
		name  string
		probe func(context.Context, exec.RunOptions) (exec.Result, error)
		why   string
	}{
		{"exit 0", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return exec.Result{ExitCode: 0, Stderr: []byte(realUsage)}, nil
		}, "exited 0, want 1"},
		{"usage on stdout", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stdout: []byte(realUsage)})
		}, "wrote to stdout"},
		{"no activate action", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte("darwin-rebuild [--help] {edit | switch | build | check}\n")})
		}, `lists no "activate" action`},
		{"not darwin-rebuild usage", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte("usage: something else\n")})
		}, "does not start with darwin-rebuild"},
		{"cannot run", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return exec.Result{}, errors.New("fork/exec: permission denied")
		}, "the probe could not run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAsUserEnv(t, asUserTOML, nil)
			e.f.probe = tc.probe
			var out bytes.Buffer
			if err := e.apply(context.Background(), &out); err != nil {
				t.Fatalf("Apply: %v\n%s", err, out.String())
			}
			announce := "falling back to: sudo darwin-rebuild switch --flake " + e.leafDir + "#" + asUserHost
			if !strings.Contains(out.String(), "has no usable 'darwin-rebuild activate'") ||
				!strings.Contains(out.String(), tc.why) || !strings.Contains(out.String(), announce) {
				t.Errorf("the fallback was not announced with its reason (%q):\n%s", tc.why, out.String())
			}
			roots := e.f.rootCalls()
			if len(roots) != 1 || argvOf(roots[0]) != "sudo darwin-rebuild switch --flake "+e.leafDir+"#"+asUserHost+" --override-input dep-input git+file://"+e.depDir {
				t.Fatalf("root calls:\n%s", dumpCalls(roots))
			}
			if !roots[0].Opts.WrapNix || roots[0].Opts.Dir != e.leafDir || roots[0].Opts.OwnProcessGroup {
				t.Errorf("the fallback must run as the legacy command does: %+v", roots[0].Opts)
			}
			if !e.applied(t) {
				t.Error("a successful fallback must record the applied state")
			}
			if dirs := e.runDirs(t); len(dirs) != 0 {
				t.Errorf("run dirs left: %v", dirs)
			}
			if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != "none" {
				t.Errorf("failed_step = %q", got)
			}
		})
	}
}

func TestAsUser_ProbeFailureWithoutApplyCommandFailsClearly(t *testing.T) {
	toml := strings.Replace(asUserTOML, `apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"`, "", 1)
	e := newAsUserEnv(t, toml, nil)
	e.f.probe = func(context.Context, exec.RunOptions) (exec.Result, error) {
		return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte("darwin-rebuild {switch | build}\n")})
	}
	err := e.apply(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "probe") || !strings.Contains(err.Error(), `lists no "activate" action`) || !strings.Contains(err.Error(), "no apply_command") {
		t.Fatalf("err = %v, want a clear probe failure naming the reason and the missing apply_command", err)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("root steps ran: %d", n)
	}
	log := e.applyLogText(t)
	if got := trailerValue(t, log, "failed_step"); got != "probe" {
		t.Errorf("failed_step = %q, want probe", got)
	}
	if got := trailerValue(t, log, "exit_status"); got != "1" {
		t.Errorf("exit_status = %q, want the probe's 1", got)
	}
	if e.applied(t) {
		t.Error("markApplied written on a failure")
	}
}

// A failed preflight runs the legacy flow BEFORE building anything, with one line.
func TestAsUser_PreflightFailureRunsTheLegacyApplyBeforeAnyBuild(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	e.w.asUserSeams.nixEnvUsable = func() bool { return false }
	var out bytes.Buffer
	if err := e.apply(context.Background(), &out); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "build-as-user flow unavailable") || !strings.Contains(out.String(), nixEnvPath) ||
		!strings.Contains(out.String(), "running apply_command instead: sudo darwin-rebuild switch") {
		t.Errorf("preflight failure not announced:\n%s", out.String())
	}
	calls := e.f.significant()
	if len(calls) != 1 || calls[0].Name != "sudo" || calls[0].Args[0] != "darwin-rebuild" || calls[0].Args[1] != "switch" {
		t.Errorf("calls:\n%s", dumpCalls(calls))
	}
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("run dirs: %v", dirs)
	}
	if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != "<absent>" {
		t.Errorf("the legacy trailer must not carry failed_step: %q", got)
	}
}

// If the per-run directory cannot be created the legacy flow runs, announced,
// and nothing has been built.
func TestAsUser_RunDirFailureRunsTheLegacyApplyBeforeAnyBuild(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	// A FILE where the apply-build directory must go.
	if err := os.MkdirAll(filepath.Join(e.state, "pn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, "pn", "apply-build"), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.apply(context.Background(), &out); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "cannot create the per-run build directory") || !strings.Contains(out.String(), "falling back to: sudo darwin-rebuild switch") {
		t.Errorf("not announced:\n%s", out.String())
	}
	calls := e.f.significant()
	if len(calls) != 1 || calls[0].Name != "sudo" || calls[0].Args[1] != "switch" {
		t.Errorf("calls:\n%s", dumpCalls(calls))
	}
}

func TestAsUser_PreflightFailureWithoutApplyCommandFailsBeforeAnyBuild(t *testing.T) {
	toml := strings.Replace(asUserTOML, `apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"`, "", 1)
	e := newAsUserEnv(t, toml, nil)
	e.w.asUserSeams.nixEnvUsable = func() bool { return false }
	err := e.apply(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unavailable") || !strings.Contains(err.Error(), "no apply_command") {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.f.significant()); n != 0 {
		t.Errorf("commands ran before the preflight failure was reported: %d", n)
	}
}

// An explicit per-invocation apply command (upgrade's ApplyCmd) keeps the legacy
// flow even when activate_command is configured.
func TestAsUser_ExplicitApplyCommandKeepsTheLegacyFlow(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	err := e.w.Apply(context.Background(), io.Discard, ApplyOptions{
		Force: true, Builder: "darwin-rebuild",
		ApplyCmd: "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}",
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := e.f.significant()
	if len(calls) != 1 || calls[0].Name != "sudo" {
		t.Errorf("calls:\n%s", dumpCalls(calls))
	}
}

// --- Failure mapping ----------------------------------------------------------------

func TestAsUser_FailureMapping(t *testing.T) {
	cases := []struct {
		name        string
		set         func(f *asUserFake)
		wantText    string
		wantStep    string
		wantExit    string
		wantCode    int
		wantRootRan int // root calls
	}{
		{"build", func(f *asUserFake) {
			f.build = func(context.Context, exec.RunOptions) (exec.Result, error) { return cmdErr("darwin-rebuild", nil, 3) }
		}, "apply failed: build:", "build", "3", 3, 0},
		{"build killed by a signal", func(f *asUserFake) {
			f.build = func(context.Context, exec.RunOptions) (exec.Result, error) { return cmdErr("darwin-rebuild", nil, -1) }
		}, "apply failed: build:", "build", "-1", -1, 0},
		{"set-profile", func(f *asUserFake) {
			f.setProfile = func(context.Context, []string) (exec.Result, error) { return cmdErr("sudo", nil, 4) }
		}, "apply failed: set system profile:", "set-profile", "4", 4, 1},
		{"activate", func(f *asUserFake) {
			f.activate = func(context.Context, []string) (exec.Result, error) { return cmdErr("sudo", nil, 5) }
		}, "apply failed: activate:", "activate", "5", 5, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAsUserEnv(t, asUserTOML, nil)
			tc.set(e.f)
			err := e.apply(context.Background(), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err = %v, want text %q", err, tc.wantText)
			}
			var ce *exec.CommandError
			if !errors.As(err, &ce) || ce.Result.ExitCode != tc.wantCode {
				t.Errorf("the error does not carry the failed step's own exit code %d: %v", tc.wantCode, err)
			}
			log := e.applyLogText(t)
			if got := trailerValue(t, log, "failed_step"); got != tc.wantStep {
				t.Errorf("failed_step = %q, want %q", got, tc.wantStep)
			}
			if got := trailerValue(t, log, "exit_status"); got != tc.wantExit {
				t.Errorf("exit_status = %q, want %q", got, tc.wantExit)
			}
			if got := trailerValue(t, log, "result"); got != "failed" {
				t.Errorf("result = %q", got)
			}
			if n := len(e.f.rootCalls()); n != tc.wantRootRan {
				t.Errorf("root calls = %d, want %d:\n%s", n, tc.wantRootRan, dumpCalls(e.f.rootCalls()))
			}
			if e.applied(t) {
				t.Error("markApplied written on a failure")
			}
			if dirs := e.runDirs(t); len(dirs) != 0 {
				t.Errorf("run dirs left after a failure: %v", dirs)
			}
		})
	}
}

// The step errors are told apart by text, not only by exit code.
func TestAsUser_StepErrorTextsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, step := range []string{"build", "set-profile", "activate"} {
		e := newAsUserEnv(t, asUserTOML, nil)
		switch step {
		case "build":
			e.f.build = func(context.Context, exec.RunOptions) (exec.Result, error) { return cmdErr("x", nil, 1) }
		case "set-profile":
			e.f.setProfile = func(context.Context, []string) (exec.Result, error) { return cmdErr("x", nil, 1) }
		case "activate":
			e.f.activate = func(context.Context, []string) (exec.Result, error) { return cmdErr("x", nil, 1) }
		}
		err := e.apply(context.Background(), io.Discard)
		prefix, _, _ := strings.Cut(err.Error(), ":")
		_, rest, _ := strings.Cut(err.Error(), ": ")
		label, _, _ := strings.Cut(rest, ":")
		key := prefix + "/" + label
		if prev, dup := seen[key]; dup {
			t.Errorf("steps %s and %s produce the same error prefix %q", prev, step, key)
		}
		seen[key] = step
	}
}

// --- Cancel ------------------------------------------------------------------------

// A cancel during the build leaves no root step and removes the run dir.
func TestAsUser_CancelDuringTheBuildRunsNoRootStep(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.f.build = func(c context.Context, o exec.RunOptions) (exec.Result, error) {
		cancel()
		<-c.Done()
		// What the real runner reports for a child stopped by the cancel.
		return cmdErr("darwin-rebuild", nil, -1)
	}
	err := e.apply(ctx, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "apply failed: build:") {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("root steps ran after a cancelled build: %d", n)
	}
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("run dirs left after a cancel: %v", dirs)
	}
	if e.applied(t) {
		t.Error("markApplied written after a cancel")
	}
}

// A cancel after the build and before set-profile starts no root step.
func TestAsUser_CancelBeforeSetProfileStartsNoRootStep(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.f.probe = func(context.Context, exec.RunOptions) (exec.Result, error) {
		cancel() // the probe itself succeeds; the cancel lands right after it
		return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte(realUsage)})
	}
	err := e.apply(ctx, io.Discard)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "before any root step started") {
		t.Fatalf("err = %v, want a cancel before any root step", err)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("root steps ran after a cancel: %d", n)
	}
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("run dirs left: %v", dirs)
	}
	if e.applied(t) {
		t.Error("markApplied written after a cancel")
	}
}

// A cancel while the probe runs must not turn into a fallback to the legacy root
// switch.
func TestAsUser_CancelDuringTheProbeDoesNotFallBack(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.f.probe = func(c context.Context, _ exec.RunOptions) (exec.Result, error) {
		cancel()
		<-c.Done()
		return exec.Result{}, c.Err()
	}
	err := e.apply(ctx, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("a cancel during the probe started root steps: %s", dumpCalls(e.f.rootCalls()))
	}
}

// Once set-profile has started a cancel does not reach it or activate; both run
// to completion and the cancel is reported afterwards.
func TestAsUser_CancelAfterSetProfileStartedLetsBothRootStepsFinish(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var spCtxErr, actCtxErr error
	activated := false
	e.f.setProfile = func(c context.Context, _ []string) (exec.Result, error) {
		cancel()
		spCtxErr = c.Err()
		return exec.Result{}, nil
	}
	e.f.activate = func(c context.Context, _ []string) (exec.Result, error) {
		actCtxErr = c.Err()
		activated = true
		return exec.Result{}, nil
	}
	err := e.apply(ctx, io.Discard)
	if !activated {
		t.Fatal("activate did not run after a cancel during set-profile")
	}
	if spCtxErr != nil || actCtxErr != nil {
		t.Errorf("the root steps' contexts were cancelled: set-profile %v, activate %v", spCtxErr, actCtxErr)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "ran to completion") {
		t.Fatalf("err = %v, want the cancel reported after completion", err)
	}
	if e.applied(t) {
		t.Error("markApplied written on a cancelled apply")
	}
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("run dirs left: %v", dirs)
	}
}

// If set-profile fails (for instance killed by the terminal's Ctrl-C), activate
// MUST NOT run.
func TestAsUser_ActivateNeverRunsAfterAFailedSetProfile(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.f.setProfile = func(context.Context, []string) (exec.Result, error) {
		cancel()
		return cmdErr("sudo", nil, -1)
	}
	err := e.apply(ctx, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "set system profile") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range e.f.rootCalls() {
		if strings.HasSuffix(c.Args[0], "/sw/bin/darwin-rebuild") {
			t.Errorf("activate ran after a failed set-profile: %s", argvOf(c))
		}
	}
}

// --- Telemetry ----------------------------------------------------------------------

// pn.exec.subcommand is nix-env for the set-profile step and `darwin-rebuild
// activate` for the activate step (the normalizer looks through sudo, env -i and
// NAME=VALUE).
func TestAsUser_ExecSpansCarryTheSubcommands(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, func(r exec.Runner) exec.Runner { return exec.WithTracing(r) })
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	if err := e.apply(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	var subs []string
	for _, s := range rec.ByName(telemetry.SpanExec) {
		a := attrs(s)
		if v, ok := a[telemetry.AttrExecSubcommand]; ok {
			subs = append(subs, v.AsString())
		}
	}
	for _, want := range []string{"darwin-rebuild build", "nix-env", "darwin-rebuild activate"} {
		if !hasString(subs, want) {
			t.Errorf("no pn.exec span with subcommand %q; have %v", want, subs)
		}
	}
	// The set-profile span is named for the process pn started (sudo), as before.
	var sudoSpans int
	for _, s := range rec.ByName(telemetry.SpanExec) {
		if attrs(s)[telemetry.AttrExecutable].AsString() == "sudo" {
			sudoSpans++
		}
	}
	if sudoSpans != 2 {
		t.Errorf("sudo pn.exec spans = %d, want 2 (set-profile, activate)", sudoSpans)
	}
}

// --- Run directory housekeeping -----------------------------------------------------

func TestPruneStaleRunDirs(t *testing.T) {
	parent := t.TempDir()
	now := time.Now()
	mk := func(name string, age time.Duration) {
		p := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(p, "inner"), 0o700); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	mk("run-old", 25*time.Hour)
	mk("run-fresh", time.Hour)
	mk("other-old", 48*time.Hour) // not a run dir: never touched
	if err := os.WriteFile(filepath.Join(parent, "run-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pruneStaleRunDirs(parent, 24*time.Hour, now)
	for name, want := range map[string]bool{"run-old": false, "run-fresh": true, "other-old": true, "run-file": true} {
		_, err := os.Stat(filepath.Join(parent, name))
		if (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
}

// A previous apply's abandoned run dir is pruned when the next apply starts.
func TestAsUser_NextApplyPrunesAnAbandonedRunDir(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	stale := filepath.Join(e.state, "pn", "apply-build", "run-stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Hour)
	_ = os.Chtimes(stale, old, old)
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the abandoned run dir was not pruned")
	}
}

// The probe parser, on the captured usage of the real script and variants.
func TestParseDarwinRebuildUsage(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		out, err string
		wantErr  string
	}{
		{"captured real usage", 1, "", realUsage, ""},
		{"extra whitespace in the group", 1, "", "darwin-rebuild [--help] {  edit|switch |activate| build }\n", ""},
		{"exit 2", 2, "", realUsage, "exited 2"},
		{"exit 0", 0, "", realUsage, "exited 0"},
		{"stdout used", 1, "x", realUsage, "stdout"},
		{"empty stderr", 1, "", "", "does not start with darwin-rebuild"},
		{"no group", 1, "", "darwin-rebuild --help\n", "no {action"},
		{"missing activate", 1, "", "darwin-rebuild {edit | switch | build}\n", `"activate"`},
		{"missing switch", 1, "", "darwin-rebuild {activate | build}\n", `"switch"`},
		{"missing build", 1, "", "darwin-rebuild {switch | activate}\n", `"build"`},
		{"activate only on a later line", 1, "", "darwin-rebuild {switch | build}\n  activate\n", `"activate"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := parseDarwinRebuildUsage(tc.code, []byte(tc.out), []byte(tc.err))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("rejected: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// A probe that outlives its timeout is a probe failure, not a hang.
func TestAsUser_ProbeTimeoutFallsBack(t *testing.T) {
	old := probeTimeout
	probeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })
	e := newAsUserEnv(t, asUserTOML, nil)
	e.f.probe = func(c context.Context, _ exec.RunOptions) (exec.Result, error) {
		<-c.Done()
		return cmdErr("p", nil, -1)
	}
	var out bytes.Buffer
	if err := e.apply(context.Background(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "did not finish within") || len(e.f.rootCalls()) != 1 {
		t.Errorf("timeout not handled as a probe failure:\n%s\n%s", out.String(), dumpCalls(e.f.rootCalls()))
	}
}
