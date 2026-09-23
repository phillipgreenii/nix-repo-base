// internal/workspace/doctor_checks_nixcachetrust_test.go
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// nixCacheTrustWorkspace builds a one-repo workspace whose repo is a real git
// repo (so isGitRepo/resolveFlakePath's on-disk search both work), with the
// given flake.nix content written at its root. It also overrides
// nixTrustedSettingsPathFn to a fresh, never-existing path under t.TempDir()
// by default -- callers that want a real trust file call
// writeNixTrustedSettings to populate it.
func nixCacheTrustWorkspace(t *testing.T, flakeNix string) (*Workspace, string, *exec.FakeRunner) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "apps")
	initRealRepo(t, dir)
	writeFile(t, filepath.Join(dir, "flake.nix"), flakeNix)

	f := exec.NewFakeRunner()
	ws := &Workspace{
		root: root, runner: f,
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"apps": {URL: "git@github.com:o/apps.git", Branch: "main"},
		}},
	}

	trustPath := filepath.Join(t.TempDir(), "trusted-settings.json") // deliberately never created
	orig := nixTrustedSettingsPathFn
	nixTrustedSettingsPathFn = func() string { return trustPath }
	t.Cleanup(func() { nixTrustedSettingsPathFn = orig })

	return ws, dir, f
}

// writeNixTrustedSettings points nixTrustedSettingsPathFn at a freshly
// written trusted-settings.json with the given content, for the duration of
// t.
func writeNixTrustedSettings(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trusted-settings.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write trusted-settings.json: %v", err)
	}
	orig := nixTrustedSettingsPathFn
	nixTrustedSettingsPathFn = func() string { return path }
	t.Cleanup(func() { nixTrustedSettingsPathFn = orig })
}

const declaringFlakeNix = `{
  description = "test";
  nixConfig = {
    extra-substituters = [ "https://cache.numtide.com" "https://cache.flox.dev" ];
    extra-trusted-public-keys = [ "niks3.numtide.com-1:AAA" "flox-cache-public-1:BBB" ];
  };
  outputs = { self }: { };
}
`

const noNixConfigFlakeNix = `{
  description = "test";
  outputs = { self }: { };
}
`

// scriptNixConfigEval scripts the FakeRunner's response for the exact `nix
// eval ... nixConfig --apply ...` call checkNixCacheTrusted issues against
// dir/flake.nix, returning stdout (the nix eval --json payload) with a nil
// error. Use scriptNixConfigEvalMissing instead when the flake declares no
// nixConfig at all (nix eval fails with an ordinary attribute-not-found
// error).
func scriptNixConfigEval(f *exec.FakeRunner, dir, stdout string) {
	f.AddResponse("nix", []string{
		"eval", "--json", "--file", filepath.Join(dir, "flake.nix"), "nixConfig", "--apply", nixConfigEvalExpr,
	}, exec.Result{Stdout: []byte(stdout)}, nil)
}

func scriptNixConfigEvalMissing(f *exec.FakeRunner, dir string) {
	f.AddResponse("nix", []string{
		"eval", "--json", "--file", filepath.Join(dir, "flake.nix"), "nixConfig", "--apply", nixConfigEvalExpr,
	}, exec.Result{}, errors.New("error: attribute 'nixConfig' in selection path 'nixConfig' not found"))
}

// TestCheckNixCacheTrusted_MatchingTrustIsClean covers the steady state this
// check must NOT flag: the repo's declared nixConfig lists, space-joined,
// are present as accepted (true) entries in trusted-settings.json.
func TestCheckNixCacheTrusted_MatchingTrustIsClean(t *testing.T) {
	ws, dir, f := nixCacheTrustWorkspace(t, declaringFlakeNix)
	scriptNixConfigEval(f, dir, `{"s":["https://cache.numtide.com","https://cache.flox.dev"],"k":["niks3.numtide.com-1:AAA","flox-cache-public-1:BBB"]}`)
	writeNixTrustedSettings(t, `{"extra-substituters":{"https://cache.numtide.com https://cache.flox.dev":true},"extra-trusted-public-keys":{"niks3.numtide.com-1:AAA flox-cache-public-1:BBB":true}}`)

	fs := ws.checkNixCacheTrusted(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("matching trust must produce no finding; got %+v", fs)
	}
}

// TestCheckNixCacheTrusted_MissingTrustFileIsSkippedFinding covers the
// fresh-machine case named explicitly in the bead: trusted-settings.json
// does not exist yet, before any `nix build` has prompted for acceptance.
func TestCheckNixCacheTrusted_MissingTrustFileIsSkippedFinding(t *testing.T) {
	ws, dir, f := nixCacheTrustWorkspace(t, declaringFlakeNix)
	scriptNixConfigEval(f, dir, `{"s":["https://cache.numtide.com","https://cache.flox.dev"],"k":["niks3.numtide.com-1:AAA","flox-cache-public-1:BBB"]}`)
	// nixTrustedSettingsPathFn (set by nixCacheTrustWorkspace) points at a path
	// that was never written -- the fresh-machine case.

	fs := ws.checkNixCacheTrusted(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "nix-cache-trusted", "apps") {
		t.Fatalf("expected a Skipped nix-cache-trusted finding when trusted-settings.json is missing; got %+v", fs)
	}
	f2 := findingByID(t, fs, "nix-cache-trusted")
	if !contains([]byte(f2.Message), "does not exist yet") {
		t.Errorf("message should say the trust file does not exist yet: %q", f2.Message)
	}
}

// TestCheckNixCacheTrusted_TrustFilePresentButNotMatchingIsSkippedFinding
// covers the case where trusted-settings.json exists but does not contain
// this repo's exact declared combination (e.g. the repo's nixConfig changed
// since it was last accepted).
func TestCheckNixCacheTrusted_TrustFilePresentButNotMatchingIsSkippedFinding(t *testing.T) {
	ws, dir, f := nixCacheTrustWorkspace(t, declaringFlakeNix)
	scriptNixConfigEval(f, dir, `{"s":["https://cache.numtide.com","https://cache.flox.dev"],"k":["niks3.numtide.com-1:AAA","flox-cache-public-1:BBB"]}`)
	// A trust file exists, but for a DIFFERENT (unrelated) combination.
	writeNixTrustedSettings(t, `{"extra-substituters":{"https://some.other.cache":true},"extra-trusted-public-keys":{}}`)

	fs := ws.checkNixCacheTrusted(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "nix-cache-trusted", "apps") {
		t.Fatalf("expected a Skipped nix-cache-trusted finding for an untrusted combination; got %+v", fs)
	}
	f2 := findingByID(t, fs, "nix-cache-trusted")
	if !contains([]byte(f2.Message), "extra-substituters") {
		t.Errorf("message should name the mismatched setting: %q", f2.Message)
	}
}

// TestCheckNixCacheTrusted_NoNixConfigDeclaredDoesNotApply is the negative
// space: a repo whose flake.nix declares no nixConfig at all has nothing for
// this check to assert about, regardless of trusted-settings.json's state.
func TestCheckNixCacheTrusted_NoNixConfigDeclaredDoesNotApply(t *testing.T) {
	ws, dir, f := nixCacheTrustWorkspace(t, noNixConfigFlakeNix)
	scriptNixConfigEvalMissing(f, dir)
	writeNixTrustedSettings(t, `{}`) // present but irrelevant -- nothing declared to check

	fs := ws.checkNixCacheTrusted(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("a repo declaring no nixConfig must produce no finding; got %+v", fs)
	}
}

// TestCheckNixCacheTrusted_PartialDeclarationOnlyChecksWhatsDeclared covers a
// repo declaring only extra-substituters (no extra-trusted-public-keys): only
// the declared list is compared, and a mismatch there names only that
// setting.
func TestCheckNixCacheTrusted_PartialDeclarationOnlyChecksWhatsDeclared(t *testing.T) {
	partial := `{
  description = "test";
  nixConfig = {
    extra-substituters = [ "https://cache.numtide.com" ];
  };
  outputs = { self }: { };
}
`
	ws, dir, f := nixCacheTrustWorkspace(t, partial)
	scriptNixConfigEval(f, dir, `{"s":["https://cache.numtide.com"],"k":[]}`)
	writeNixTrustedSettings(t, `{"extra-substituters":{"https://cache.numtide.com":true}}`)

	fs := ws.checkNixCacheTrusted(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("a matching partial declaration must produce no finding; got %+v", fs)
	}
}
