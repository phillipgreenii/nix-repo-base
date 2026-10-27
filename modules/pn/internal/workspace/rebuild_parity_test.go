package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// wantPrelude is the golden prelude, in order. Adding a step to the shared
// prelude means updating this list deliberately; adding a step to ONE verb only
// (outside the shared prelude) makes parityRun's per-verb lists diverge and
// TestRebuildParity_PreludeIdenticalForBuildAndApply fail.
var wantPrelude = []string{
	stepRequireTerminal,
	stepRepoSpan,
	stepResolveOverrides,
	stepCheckFollows,
	stepSubstituteTemplate,
	stepDryRunGate,
	stepResolveTool,
	stepTrustGate,
	stepCheckWsidUnique,
	stepCheckNixDaemon,
}

type recordedSteps struct{ shared, own []string }

// parityRun runs verb ("build" or "apply") end to end against a hermetic fake
// runner and returns the steps it recorded. Nothing real runs: the runner is a
// FakeRunner, and XDG state/data point at temp dirs.
func parityRun(t *testing.T, verb string) recordedSteps {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "paritytest"
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"
`)
	trustWS(t, root)
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	f.AddResponse("darwin-rebuild", []string{"build", "--flake", leafDir + "#parityhost"}, exec.Result{}, nil)
	f.AddResponse("sudo", []string{"darwin-rebuild", "switch", "--flake", leafDir + "#parityhost"}, exec.Result{}, nil)
	for i := 0; i < 3; i++ { // gate probes, markApplied's probe
		f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("h\n")}, nil)
	}
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.hostnameFn = func() string { return "parityhost" }
	var rec recordedSteps
	w.stepRecorder = func(v string, shared bool, step string) {
		if v != verb {
			t.Errorf("recorder saw verb %q while running %q", v, verb)
		}
		if shared {
			rec.shared = append(rec.shared, step)
		} else {
			rec.own = append(rec.own, step)
		}
	}
	var out bytes.Buffer
	switch verb {
	case "build":
		err = w.Build(context.Background(), &out, BuildOptions{})
	case "apply":
		err = w.Apply(context.Background(), &out, ApplyOptions{Force: true})
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", verb, err, out.String())
	}
	return rec
}

// TestRebuildParity_PreludeIdenticalForBuildAndApply is the parity contract
// (bead pg2-sdbhh4.8): the ordered prelude steps of build and apply MUST be
// identical, so a check added to one verb and not the other fails here. Agents
// cannot run apply, so build is only a faithful stand-in while this holds.
func TestRebuildParity_PreludeIdenticalForBuildAndApply(t *testing.T) {
	b := parityRun(t, "build")
	a := parityRun(t, "apply")
	if !reflect.DeepEqual(b.shared, a.shared) {
		t.Fatalf("build and apply preludes diverged:\n build: %v\n apply: %v", b.shared, a.shared)
	}
	if !reflect.DeepEqual(b.shared, wantPrelude) {
		t.Errorf("prelude changed; update wantPrelude deliberately:\n got:  %v\n want: %v", b.shared, wantPrelude)
	}
}

// TestRebuildParity_VerbSpecificSteps pins what each verb does AFTER the shared
// prelude, so a step added or dropped after the prelude is a visible, reviewed
// change (the delta between build and apply is meant to stay small and known).
func TestRebuildParity_VerbSpecificSteps(t *testing.T) {
	if got, want := parityRun(t, "build").own, []string{stepReportApplyDecision, stepRunCommand}; !reflect.DeepEqual(got, want) {
		t.Errorf("build steps = %v, want %v", got, want)
	}
	want := []string{stepSkipGate, stepApplyLog, stepCaptureHeads, stepRunCommand, stepPostSteps, stepMarkApplied}
	if got := parityRun(t, "apply").own; !reflect.DeepEqual(got, want) {
		t.Errorf("apply steps = %v, want %v", got, want)
	}
}

// TestRebuild_BuildAndApplyShareTheHostnameResolver: both verbs substitute
// {hostname} through Workspace.hostname, so a stubbed resolver reaches both
// commands (parityRun scripts both with "#parityhost" and would fail otherwise).
func TestRebuild_BuildAndApplyShareTheHostnameResolver(t *testing.T) {
	parityRun(t, "build")
	parityRun(t, "apply")
}

// TestRebuild_EmptyHostnameIsAnErrorWhenReferenced: an unresolvable hostname
// must not silently address `<dir>#`.
func TestRebuild_EmptyHostnameIsAnErrorWhenReferenced(t *testing.T) {
	_, err := substituteCommand("darwin-rebuild build --flake {terminal_nix_dir}#{hostname}",
		templateVars{TerminalNixDir: "/x", Hostname: "", Builder: "darwin-rebuild"})
	if err == nil || !strings.Contains(err.Error(), "{hostname}") {
		t.Fatalf("expected a hostname error, got %v", err)
	}
	if _, err := substituteCommand("darwin-rebuild build --flake {terminal_nix_dir}", templateVars{TerminalNixDir: "/x"}); err != nil {
		t.Fatalf("a template without {hostname} must not need one: %v", err)
	}
}
