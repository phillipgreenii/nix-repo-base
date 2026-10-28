package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

// toolTestWS opens a hermetic workspace whose build/apply commands start with a
// bare darwin-rebuild (apply behind sudo). trusted controls the trust gate.
func toolTestWS(t *testing.T, trusted bool) (*Workspace, *exec.FakeRunner, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "tooltest"
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"
`)
	if trusted {
		trustWS(t, root)
	}
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.hostnameFn = func() string { return "toolhost" }
	return w, f, root
}

func runVerb(ctx context.Context, w *Workspace, verb string) error {
	var out bytes.Buffer
	if verb == "build" {
		return w.Build(ctx, &out, BuildOptions{})
	}
	return w.Apply(ctx, &out, ApplyOptions{Force: true})
}

// TestRebuild_MissingToolFailsFirst (acceptance criterion): when the tool is on
// neither PATH nor the system profile, BOTH verbs fail before any subprocess
// (the fake runner sees no call at all, not even the nix daemon probe), before
// the trust gate (the workspace here is deliberately untrusted, and the error
// is still the tool error) and before the workspace-id check, with the distinct
// exit status and a countable pn.exec span.
func TestRebuild_MissingToolFailsFirst(t *testing.T) {
	for _, verb := range []string{"build", "apply"} {
		t.Run(verb, func(t *testing.T) {
			w, f, _ := toolTestWS(t, false)
			var looked []string
			w.resolveTool = func(name string) (map[string]string, error) {
				looked = append(looked, name)
				return nil, &exec.ToolNotFoundError{Tool: name, Tried: exec.SystemProfileBinDirs}
			}
			var steps []string
			w.stepRecorder = func(_ string, shared bool, step string) {
				if shared {
					steps = append(steps, step)
				}
			}
			tel, rec := telemetrytest.New()
			ctx := telemetry.WithTelemetry(context.Background(), tel)

			err := runVerb(ctx, w, verb)
			if !exec.IsToolNotFound(err) {
				t.Fatalf("err = %v, want a ToolNotFoundError", err)
			}
			if got := f.Calls(); len(got) != 0 {
				t.Errorf("subprocess calls before the failure: %+v", got)
			}
			if !reflect.DeepEqual(looked, []string{"darwin-rebuild"}) {
				t.Errorf("resolved %v, want [darwin-rebuild] (the name after sudo for apply)", looked)
			}
			if last := steps[len(steps)-1]; last != stepResolveTool {
				t.Errorf("last prelude step = %q, want %q (no trust gate, wsid or daemon step may run)", last, stepResolveTool)
			}
			spans := rec.ByName("pn.exec")
			if len(spans) != 1 {
				t.Fatalf("pn.exec spans = %d, want 1", len(spans))
			}
			attrs := map[string]string{}
			for _, kv := range spans[0].Attributes() {
				attrs[string(kv.Key)] = kv.Value.String()
			}
			if attrs["pn.exec.tool_not_found"] != "true" || attrs["process.executable.name"] != "darwin-rebuild" ||
				attrs["process.exit.code"] != "69" {
				t.Errorf("span attributes = %v", attrs)
			}
		})
	}
}

// TestRebuild_FallbackToolRunsBareNameWithPATHOverlay (acceptance criterion):
// when the resolver finds the tool via the system profile, both verbs run the
// SAME bare argv (so the wrapper rewrite still applies) with WrapNix and the
// PATH overlay.
func TestRebuild_FallbackToolRunsBareNameWithPATHOverlay(t *testing.T) {
	overlay := map[string]string{"PATH": "/usr/bin:/run/current-system/sw/bin"}
	for _, verb := range []string{"build", "apply"} {
		t.Run(verb, func(t *testing.T) {
			w, f, root := toolTestWS(t, true)
			leaf := filepath.Join(root, "leaf")
			w.resolveTool = func(string) (map[string]string, error) { return overlay, nil }
			f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
			f.AddResponse("darwin-rebuild", []string{"build", "--flake", leaf + "#toolhost"}, exec.Result{}, nil)
			f.AddResponse("sudo", []string{"darwin-rebuild", "switch", "--flake", leaf + "#toolhost"}, exec.Result{}, nil)
			for i := 0; i < 3; i++ {
				f.AddResponse("git", []string{"-C", leaf, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
				f.AddResponse("git", []string{"-C", leaf, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("h\n")}, nil)
			}
			f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
			f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
			if err := runVerb(context.Background(), w, verb); err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			wantName := "darwin-rebuild"
			if verb == "apply" {
				wantName = "sudo"
			}
			var found bool
			for _, c := range f.Calls() {
				if c.Name != wantName || !c.Opts.WrapNix {
					continue
				}
				found = true
				// pg2-sdbhh4.20: the user-run build leads its own process group so a
				// stop reaches its descendants; a sudo child must not (SIGTTIN).
				if wantGroup := verb == "build"; c.Opts.OwnProcessGroup != wantGroup {
					t.Errorf("OwnProcessGroup = %v, want %v for %s", c.Opts.OwnProcessGroup, wantGroup, wantName)
				}
				if !reflect.DeepEqual(c.Opts.Env, overlay) {
					t.Errorf("Env = %v, want %v", c.Opts.Env, overlay)
				}
			}
			if !found {
				t.Fatalf("no WrapNix %s call recorded: %+v", wantName, f.Calls())
			}
		})
	}
}

// TestRebuild_ResolutionIsOffUntilEnabled: a Workspace opened by a test (fake
// runner) never consults the real filesystem, and EnableToolResolution is what
// production uses to turn it on.
func TestRebuild_ResolutionIsOffUntilEnabled(t *testing.T) {
	w, _, _ := toolTestWS(t, true)
	if w.resolveTool != nil {
		t.Fatal("resolveTool must be nil by default")
	}
	w.EnableToolResolution()
	if w.resolveTool == nil {
		t.Fatal("EnableToolResolution did not set the resolver")
	}
}

// TestRebuild_SudoWithOptionIsNotResolved: only `sudo <tool>` is resolved; an
// option after sudo is not guessed at.
func TestRebuild_SudoWithOptionIsNotResolved(t *testing.T) {
	w, _, _ := toolTestWS(t, true)
	called := false
	w.resolveTool = func(string) (map[string]string, error) { called = true; return nil, nil }
	if env, err := w.resolveCommandTool(context.Background(), []string{"sudo", "-E", "darwin-rebuild"}); env != nil || err != nil || called {
		t.Fatalf("resolveCommandTool = %v, %v (resolver called: %v)", env, err, called)
	}
}
