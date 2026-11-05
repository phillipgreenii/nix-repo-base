package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
)

// shimRig is a rig whose program name is nix or darwin-rebuild and whose PATH
// holds a stub "real" tool (a script that records $0 and runs the fake).
type shimRig struct {
	*rig
	stub, rec string
}

func newShimRig(t *testing.T, tool string, spec fakecmd.Spec, extraEnv ...string) *shimRig {
	t.Helper()
	r := newRig(t, spec, extraEnv...)
	bin := filepath.Join(t.TempDir(), "sw", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(r.home, "argv0")
	script := "#!/bin/sh\necho \"$0\" > '" + rec + "'\nexec '" + fakes.Command(t, "fake-nix") + "' \"$@\"\n"
	stub := filepath.Join(bin, tool)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.setEnv("PATH", bin+":/usr/bin:/bin")
	r.d.Argv0 = "/somewhere/" + tool
	return &shimRig{rig: r, stub: stub, rec: rec}
}

func (r *shimRig) quiet(t *testing.T) {
	t.Helper()
	for name, b := range map[string]string{"stdout": r.stdout.String(), "stderr": r.stderr.String(), "child stdout": r.childOut.String(), "child stderr": r.childErr.String()} {
		if b != "" {
			t.Errorf("%s not empty: %q", name, b)
		}
	}
}

func (r *shimRig) dumpEnv(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(r.dumpTo)
	if err != nil {
		t.Fatalf("real command did not run: %v", err)
	}
	var v struct {
		Env []string `json:"env"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Env
}

func count(env []string, kv string) int {
	n := 0
	for _, e := range env {
		if e == kv {
			n++
		}
	}
	return n
}

func TestShimDispatchTable(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		args    []string
		env     []string
		wrapped bool
	}{
		{"nix build", "nix", []string{"build", ".#x"}, nil, true},
		{"nix flake check", "nix", []string{"flake", "check"}, nil, true},
		{"nix flag before verb", "nix", []string{"-L", "build", ".#x"}, nil, true},
		{"darwin-rebuild switch", "darwin-rebuild", []string{"switch", "--flake", "."}, nil, true},
		{"darwin-rebuild build", "darwin-rebuild", []string{"build"}, nil, true},
		{"darwin-rebuild check", "darwin-rebuild", []string{"check"}, nil, true},
		{"darwin-rebuild activate is never wrapped", "darwin-rebuild", []string{"activate"}, nil, false},
		{"nix eval is opt-in", "nix", []string{"eval", ".#x"}, nil, false},
		{"nix develop", "nix", []string{"develop"}, nil, false},
		{"nix --version", "nix", []string{"--version"}, nil, false},
		{"nix --help", "nix", []string{"--help"}, nil, false},
		{"completion call", "nix", []string{"build", "--op"}, []string{"NIX_GET_COMPLETIONS=2"}, false},
		{"no subcommand", "nix", nil, nil, false},
		{"unknown subcommand", "nix", []string{"frobnicate", "build-ish"}, nil, false},
		{"value flag whose value is a verb", "nix", []string{"--option", "foo", "build", "eval", ".#x"}, nil, false},
		{"PG_NIX_LOG_DISABLE", "nix", []string{"build"}, []string{"PG_NIX_LOG_DISABLE=1"}, false},
		{"PG_NIX_LOG_ACTIVE", "nix", []string{"build"}, []string{"PG_NIX_LOG_ACTIVE=1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newShimRig(t, c.tool, fakecmd.Spec{}, c.env...)
			if code := r.run(c.args...); code != 0 {
				t.Fatalf("exit %d, stderr %q", code, r.stderr)
			}
			r.quiet(t)
			if c.wrapped {
				if r.mem.calls != 1 || len(r.execs) != 0 {
					t.Fatalf("telemetry calls=%d execs=%d, want a wrapped run", r.mem.calls, len(r.execs))
				}
				if got := count(r.dumpEnv(t), "PG_NIX_LOG_ACTIVE=1"); got != 1 {
					t.Errorf("wrapped child saw the guard %d times", got)
				}
				return
			}
			if r.mem.calls != 0 || len(r.execs) != 1 {
				t.Fatalf("telemetry calls=%d execs=%d, want one direct exec", r.mem.calls, len(r.execs))
			}
			e := r.execs[0]
			if e.path != r.stub || e.argv[0] != r.stub || !slices.Equal(e.argv[1:], c.args) {
				t.Errorf("exec %q argv %q, want %q %q", e.path, e.argv, r.stub, c.args)
			}
			if !slices.Equal(e.env, r.d.Environ) {
				t.Errorf("passthrough changed the environment: %v", e.env)
			}
		})
	}
}

func TestShimDebugNamesTheBranch(t *testing.T) {
	r := newShimRig(t, "nix", fakecmd.Spec{}, "PG_NIX_LOG_DEBUG=1")
	r.run("--version")
	lines := strings.Split(strings.TrimSpace(r.stderr.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "shim: passthrough") || !strings.Contains(lines[0], "allowlist") {
		t.Errorf("stderr = %q", r.stderr)
	}
	r = newShimRig(t, "nix", fakecmd.Spec{}, "PG_NIX_LOG_DEBUG=1")
	r.run("build")
	if !strings.Contains(r.stderr.String(), "shim: wrapping nix build") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestShimKeepsPathFoundArgv0(t *testing.T) {
	// Direct: the real binary is exec'd with the PATH-found absolute path.
	r := newShimRig(t, "darwin-rebuild", fakecmd.Spec{})
	r.run("activate")
	if !strings.HasSuffix(r.execs[0].argv[0], "/sw/bin/darwin-rebuild") || !filepath.IsAbs(r.execs[0].argv[0]) {
		t.Errorf("direct argv[0] = %q", r.execs[0].argv[0])
	}
	// Wrapped: the stub records its own $0.
	r = newShimRig(t, "darwin-rebuild", fakecmd.Spec{})
	r.run("build")
	b, err := os.ReadFile(r.rec)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(b)), "/sw/bin/darwin-rebuild") {
		t.Errorf("wrapped $0 = %q (%v)", b, err)
	}
}

func TestShimNothingOnPathExits127(t *testing.T) {
	r := newShimRig(t, "nix", fakecmd.Spec{})
	r.setEnv("PATH", t.TempDir())
	if code := r.run("build"); code != 127 || len(r.execs) != 0 {
		t.Errorf("exit %d execs %d", code, len(r.execs))
	}
}

func TestShimNeverCallsParseArgs(t *testing.T) {
	// ParseArgs rejects unknown flags (exit 2); the shim must not.
	for _, args := range [][]string{{"--version"}, {"-L", "build", ".#x"}, {"--help"}} {
		r := newShimRig(t, "nix", fakecmd.Spec{})
		if code := r.run(args...); code == 2 || strings.Contains(r.stderr.String(), "usage") {
			t.Errorf("%v: exit %d stderr %q", args, code, r.stderr)
		}
	}
}

func TestGuardIsExportedOnWrappedRunsOnly(t *testing.T) {
	// Explicit wrapper: sets the guard on the child, and ignores one set by an
	// outer run (it still wraps; the child sees exactly one).
	r := newRig(t, fakecmd.Spec{}, "PG_NIX_LOG_ACTIVE=1")
	r.run("fake-nix", "build")
	if r.mem.calls != 1 {
		t.Fatalf("explicit nested wrapper did not wrap (calls %d)", r.mem.calls)
	}
	sr := &shimRig{rig: r}
	if got := count(sr.dumpEnv(t), "PG_NIX_LOG_ACTIVE=1"); got != 1 {
		t.Errorf("child saw the guard %d times", got)
	}
	// Fail-open exec: environment untouched.
	r = newRig(t, fakecmd.Spec{}, "PG_NIX_LOG_DISABLE=1")
	r.run("fake-nix", "build")
	if len(r.execs) != 1 || !slices.Equal(r.execs[0].env, r.d.Environ) {
		t.Errorf("fail-open environment changed: %v", r.execs)
	}
}

func TestNixStartedBy(t *testing.T) {
	cases := []struct {
		name string
		shim bool
		env  string
		want string
	}{
		{"shim wins over pn", true, "pn", "shim"},
		{"shim", true, "", "shim"},
		{"explicit with pn", false, "pn", "pn"},
		{"explicit", false, "", "explicit"},
		{"unknown value", false, "something-else", "explicit"},
		{"shim with unknown value", true, "something-else", "shim"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var env []string
			if c.env != "" {
				env = []string{"PG_NIX_LOG_STARTED_BY=" + c.env}
			}
			var r *rig
			if c.shim {
				r = newShimRig(t, "nix", fakecmd.Spec{}, env...).rig
				r.run("build")
			} else {
				r = newRig(t, fakecmd.Spec{}, env...)
				r.run("fake-nix", "build")
			}
			v, ok := attrOf(r.mem.span(t, "nix.invocation"), "nix.started_by")
			if !ok || v.AsString() != c.want {
				t.Errorf("nix.started_by = %v (found %v), want %q", v, ok, c.want)
			}
		})
	}
}

func TestShimOverrideGuard(t *testing.T) {
	args := []string{"build", ".#x", "--override-input", "bogus", "path:/x"}
	r := newShimRig(t, "nix", fakecmd.Spec{Lines: unmatchedWarnings})
	if code := r.run(args...); code != ExitUnmatchedOverride {
		t.Errorf("allowlisted verb: exit %d, want %d", code, ExitUnmatchedOverride)
	}
	r = newShimRig(t, "nix", fakecmd.Spec{Lines: unmatchedWarnings}, AllowUnmatchedOverrideEnv+"=1")
	if code := r.run(args...); code != 0 {
		t.Errorf("hatch: exit %d", code)
	}
	r = newShimRig(t, "nix", fakecmd.Spec{Lines: unmatchedWarnings})
	r.run("eval", ".#x", "--override-input", "bogus", "path:/x")
	if len(r.execs) != 1 || r.mem.calls != 0 {
		t.Errorf("off-allowlist verb was observed: execs %d calls %d", len(r.execs), r.mem.calls)
	}
}

// ---- allowlist ----

func TestDefaultAllowlistIsExactly(t *testing.T) {
	want := []string{"darwin-rebuild build", "darwin-rebuild check", "darwin-rebuild switch", "nix build", "nix flake check"}
	if got := DefaultAllowlist().Verbs(); !slices.Equal(got, want) {
		t.Errorf("default = %v, want %v", got, want)
	}
}

func TestEveryWrappableVerbHasAMetricLabel(t *testing.T) {
	optIn := 0
	for _, row := range verbTable {
		if row.Class != WrapDefault && row.Class != WrapOptIn {
			continue
		}
		if row.Class == WrapOptIn {
			optIn++
		}
		cmd := append([]string{row.Tool}, strings.Fields(row.Label)...)
		if got := Subcommand(cmd); got == SubcommandOther || got != row.Label {
			t.Errorf("%s %s: label %q", row.Tool, row.Label, got)
		}
	}
	if optIn != 5 {
		t.Errorf("opt-in verbs = %d, want 5 (run, fmt, flake lock, flake update, eval)", optIn)
	}
}

func TestWrapSetIsASubsetOfTheLabelTables(t *testing.T) {
	// The label tables are wider (develop, activate, path-info ...).
	for _, w := range []string{"develop", "shell", "path-info", "store", "copy", "show", "metadata"} {
		if !nixVerbs[w] && !nixFlakeVerbs[w] {
			t.Errorf("nix label tables lost %q", w)
		}
	}
	for _, w := range []string{"activate", "edit", "changelog"} {
		if !darwinRebuildVerbs[w] {
			t.Errorf("darwin-rebuild label table lost %q", w)
		}
	}
	for _, v := range DefaultAllowlist().Verbs() {
		tool, label, _ := strings.Cut(v, " ")
		if got := Subcommand(append([]string{tool}, strings.Fields(label)...)); got != label {
			t.Errorf("%s is wrapped but labelled %q", v, got)
		}
	}
	if Subcommand([]string{"nix", "repl"}) != SubcommandOther {
		t.Error("repl must keep the other label")
	}
}

func TestNeverWrappedVerbsAreIgnoredInEverySource(t *testing.T) {
	a := DefaultAllowlist()
	ign := a.Add([]string{"develop", "shell", "repl", "activate", "nix develop", "darwin-rebuild activate", "nonsense", "path-info"})
	if len(ign) != 8 {
		t.Errorf("ignored = %v", ign)
	}
	if !slices.Equal(a.Verbs(), DefaultAllowlist().Verbs()) {
		t.Errorf("verbs changed: %v", a.Verbs())
	}
	a.Add([]string{"run", "nix flake lock", "flake update", "fmt", "eval", "build"})
	if got := len(a.Verbs()); got != 10 {
		t.Errorf("verbs = %v", a.Verbs())
	}
	// End to end, through the env source.
	r := newShimRig(t, "nix", fakecmd.Spec{}, "PG_NIX_SHIM_VERBS=develop, shell,repl")
	r.run("develop")
	r.run("shell")
	if len(r.execs) != 2 || r.mem.calls != 0 {
		t.Errorf("execs %d calls %d", len(r.execs), r.mem.calls)
	}
}

func decide(r *rig, tool string, args ...string) (endpoint, why string) {
	rr := &runner{Deps: r.d, getenv: envLookup(r.d.Environ)}
	opts, why := rr.shimDecide(tool, append([]string{tool}, args...))
	return opts.OTLPEndpoint, why
}

func writeUserToml(t *testing.T, r *rig, content string) {
	t.Helper()
	dir := filepath.Join(r.home, ".config", "pn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shim.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAllowlistSourcesAreAdditiveForNonRoot(t *testing.T) {
	r := newRig(t, fakecmd.Spec{}, "PG_NIX_SHIM_VERBS=eval, nix fmt")
	writeUserToml(t, r, `verbs = ["run", "flake lock"]`)
	for _, args := range [][]string{{"run", "."}, {"flake", "lock"}, {"eval", "."}, {"fmt"}, {"build"}, {"flake", "check"}} {
		if _, why := decide(r, "nix", args...); why != "" {
			t.Errorf("nix %v not wrapped: %s", args, why)
		}
	}
	if _, why := decide(r, "nix", "flake", "update"); why == "" {
		t.Error("an unlisted opt-in verb is wrapped")
	}
	// Empty sources never remove a default.
	r = newRig(t, fakecmd.Spec{})
	writeUserToml(t, r, `verbs = []`)
	if _, why := decide(r, "nix", "build"); why != "" {
		t.Errorf("default removed: %s", why)
	}
}

func TestMalformedShimTomlFallsBackToDefaults(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	writeUserToml(t, r, "verbs = [ this is not toml")
	if _, why := decide(r, "nix", "build"); why != "" {
		t.Errorf("default lost: %s", why)
	}
	if _, why := decide(r, "nix", "run"); why == "" {
		t.Error("opt-in verb wrapped from a malformed file")
	}
	writeUserToml(t, r, `verbs = "run"`) // wrong type
	if _, why := decide(r, "nix", "run"); why == "" {
		t.Error("opt-in verb wrapped from a wrongly typed file")
	}
}

// ---- root ----

type rootRig struct {
	*rig
	store, link, cfg string
	reads            []string
}

func newRootRig(t *testing.T, content string) *rootRig {
	t.Helper()
	r := newRig(t, fakecmd.Spec{}, "PG_NIX_SHIM_VERBS=run")
	store, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(store, "pg-nix-shim.json")
	if err := os.WriteFile(cfg, []byte(content), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "etc-pg-nix-shim.json")
	if err := os.Symlink(cfg, link); err != nil {
		t.Fatal(err)
	}
	rr := &rootRig{rig: r, store: store, link: link, cfg: cfg}
	writeUserToml(t, r, `verbs = ["run"]`)
	r.d.Euid = 0
	r.d.RootConfigPath, r.d.StorePrefix = link, store
	r.d.ReadFile = func(p string) ([]byte, error) {
		rr.reads = append(rr.reads, p)
		return os.ReadFile(p)
	}
	return rr
}

func (r *rootRig) assertNoHomeRead(t *testing.T) {
	t.Helper()
	for _, p := range r.reads {
		if strings.HasPrefix(p, r.home) {
			t.Errorf("root read under the user's home: %s", p)
		}
	}
}

func TestRootShimReadsEndpointFromTheRootFile(t *testing.T) {
	r := newRootRig(t, `{"rootEndpoint":"http://127.0.0.1:4318","verbs":["fmt"]}`)
	ep, why := decide(r.rig, "nix", "build")
	if why != "" || ep != "http://127.0.0.1:4318" {
		t.Fatalf("endpoint %q why %q", ep, why)
	}
	if _, why := decide(r.rig, "nix", "fmt"); why != "" {
		t.Errorf("verb from the root file not wrapped: %s", why)
	}
	// Root ignores shim.toml and the variable (both set by newRootRig).
	if _, why := decide(r.rig, "nix", "run"); why == "" {
		t.Error("root honoured the user's opt-in sources")
	}
	if !slices.Equal(r.reads, []string{r.cfg, r.cfg, r.cfg}) {
		t.Errorf("reads = %v, want the resolved path %s only", r.reads, r.cfg)
	}
	r.assertNoHomeRead(t)
}

func TestRootShimRefusals(t *testing.T) {
	t.Run("no file", func(t *testing.T) {
		r := newRootRig(t, `{"rootEndpoint":"http://x"}`)
		r.d.RootConfigPath = filepath.Join(t.TempDir(), "absent")
		if _, why := decide(r.rig, "nix", "build"); why == "" {
			t.Error("passed with no configuration")
		}
		r.assertNoHomeRead(t)
	})
	t.Run("outside the store prefix", func(t *testing.T) {
		r := newRootRig(t, `{"rootEndpoint":"http://x"}`)
		r.d.StorePrefix = t.TempDir()
		if _, why := decide(r.rig, "nix", "build"); why == "" || len(r.reads) != 0 {
			t.Errorf("why %q reads %v", why, r.reads)
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		r := newRootRig(t, `{"rootEndpoint":"http://x"}`)
		dir := filepath.Join(r.store, "adir")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		r.d.RootConfigPath = dir
		if _, why := decide(r.rig, "nix", "build"); why == "" || len(r.reads) != 0 {
			t.Errorf("why %q reads %v", why, r.reads)
		}
	})
	t.Run("malformed or without endpoint", func(t *testing.T) {
		for _, content := range []string{"not json", `{"verbs":["run"]}`, `{"rootEndpoint":""}`} {
			r := newRootRig(t, content)
			if _, why := decide(r.rig, "nix", "build"); why == "" {
				t.Errorf("%q: wrapped", content)
			}
		}
	})
}

func TestRootShimReadsFromTheResolvedPath(t *testing.T) {
	r := newRootRig(t, `{"rootEndpoint":"http://good"}`)
	evil := filepath.Join(t.TempDir(), "evil.json")
	if err := os.WriteFile(evil, []byte(`{"rootEndpoint":"http://evil"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Swap the link between the check and the read.
	r.d.ReadFile = func(p string) ([]byte, error) {
		r.reads = append(r.reads, p)
		_ = os.Remove(r.link)
		_ = os.Symlink(evil, r.link)
		return os.ReadFile(p)
	}
	ep, why := decide(r.rig, "nix", "build")
	if why != "" || ep != "http://good" || !slices.Equal(r.reads, []string{r.cfg}) {
		t.Errorf("endpoint %q why %q reads %v", ep, why, r.reads)
	}
}

func TestNonRootShimPassesNoEndpointSoEnabledFalseWins(t *testing.T) {
	r := newShimRig(t, "nix", fakecmd.Spec{})
	if ep, why := decide(r.rig, "nix", "build"); ep != "" || why != "" {
		t.Errorf("endpoint %q why %q", ep, why)
	}
	if err := os.MkdirAll(filepath.Join(r.home, ".config", "pn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, ".config", "pn", "telemetry.toml"), []byte("enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run("build")
	if r.mem.calls != 0 || len(r.execs) != 1 {
		t.Errorf("enabled = false ignored: calls %d execs %d", r.mem.calls, len(r.execs))
	}
}

func TestProductionRootDefaults(t *testing.T) {
	d := DefaultDeps("v")
	if d.RootConfigPath != "/etc/pg-nix-shim.json" || d.StorePrefix != "/nix/store/" {
		t.Errorf("defaults = %q %q", d.RootConfigPath, d.StorePrefix)
	}
	if DefaultRootConfigPath != "/etc/pg-nix-shim.json" || DefaultStorePrefix != "/nix/store/" {
		t.Error("default constants changed")
	}
	if d.Argv0 == "" {
		t.Error("DefaultDeps lost argv[0]")
	}
}
