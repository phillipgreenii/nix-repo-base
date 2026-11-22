// internal/workspace/hookbundle_test.go
package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hbRepo creates a real git repo (under a symlink-resolved temp dir) with a
// tracked flake.lock and flake.nix, and returns its path.
func hbRepo(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "repo")
	newTemplateRepo(t, dir, true)
	return dir
}

// hbPgDir returns the pg-hooks directory of the checkout at dir: the shared one
// (<common-dir>/pg-hooks) or, with private, the checkout's own
// (<git-dir>/pg-hooks).
func hbPgDir(t *testing.T, dir string, private bool) string {
	t.Helper()
	flag := "--git-common-dir"
	if private {
		flag = "--absolute-git-dir"
	}
	return filepath.Join(runGitT(t, dir, "rev-parse", "--path-format=absolute", flag), "pg-hooks")
}

// hbBundleOpts shapes writeFakeBundle.
type hbBundleOpts struct {
	Private    bool
	Gen        string // default gen-1
	Overrides  []HookOverride
	StampPaths []string
	// Stamp overrides the recorded stamp (default: the current one).
	Stamp string
	// ClonePath overrides the recorded clone_path (default: the common dir).
	ClonePath string
}

// writeFakeBundle lays out <pg-hooks>/<gen>/{bundle,source.json} and the
// `current` pointer exactly as the installer does (a real directory stands in
// for the nix GC root), recording the checkout's current stamp.
func writeFakeBundle(t *testing.T, dir string, o hbBundleOpts) string {
	t.Helper()
	if o.Gen == "" {
		o.Gen = "gen-1"
	}
	pg := hbPgDir(t, dir, o.Private)
	bundle := filepath.Join(pg, o.Gen, "bundle")
	mustMkdir(t, filepath.Join(bundle, "bin"))
	for _, f := range []string{"prek", "pg-hooks-run"} {
		if err := os.WriteFile(filepath.Join(bundle, "bin", f), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[string]any{"repo": "", "stampPaths": o.StampPaths, "stages": []string{"pre-commit"}}
	b, _ := json.Marshal(meta)
	writeFile(t, filepath.Join(bundle, "meta.json"), string(b))

	top := runGitT(t, dir, "rev-parse", "--path-format=absolute", "--show-toplevel")
	stamp := o.Stamp
	if stamp == "" {
		var err error
		stamp, err = hookStamp(t.Context(), top, append([]string{"flake.lock", "flake.nix"}, o.StampPaths...))
		if err != nil {
			t.Fatal(err)
		}
	}
	clone := o.ClonePath
	if clone == "" {
		clone = runGitT(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	}
	ovs := o.Overrides
	if ovs == nil {
		ovs = []HookOverride{}
	}
	src, _ := json.Marshal(map[string]any{
		"stamp": stamp, "overrides": ovs, "clone_path": clone, "built_at": "2026-10-01T00:00:00Z",
	})
	writeFile(t, filepath.Join(pg, o.Gen, "source.json"), string(src))
	writeFile(t, filepath.Join(pg, "current"), o.Gen+"\n")
	return pg
}

func wantState(t *testing.T, dir string, want HookBundleState) HookBundleInfo {
	t.Helper()
	got, info, err := ReadHookBundleState(t.Context(), dir)
	if err != nil {
		t.Fatalf("ReadHookBundleState(%s): %v", dir, err)
	}
	if got != want {
		t.Fatalf("state = %q, want %q (info %+v)", got, want, info)
	}
	return info
}

func TestPrivateInstallArgs(t *testing.T) {
	got := privateInstallArgs([]string{
		"--override-input", "base", "git+file:///w/set/repo-base",
		"--override-input", "lib", "git+file:///w/set/lib",
	})
	want := []string{"--private", "--override", "base=/w/set/repo-base", "--override", "lib=/w/set/lib"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("privateInstallArgs = %v, want %v", got, want)
	}
	if got := privateInstallArgs(nil); !reflect.DeepEqual(got, []string{"--private"}) {
		t.Errorf("no pins: %v", got)
	}
}

func TestExpandNixRunTokens_InstallArgsOnlyForTheInstallAttr(t *testing.T) {
	v := nixHookVars{
		NixExe:       "nix",
		OverrideArgs: []string{"--override-input", "base", "git+file:///w/s/base"},
		FlakeDir:     "/w/s/consumer",
		InstallArgs:  []string{"--private", "--override", "base=/w/s/base dir"},
	}
	got, _, err := expandNixRunTokens("{nix_run install-pre-commit-hooks}", v)
	if err != nil {
		t.Fatal(err)
	}
	want := "nix run --override-input base 'git+file:///w/s/base' '/w/s/consumer#install-pre-commit-hooks' -- --private --override 'base=/w/s/base dir'"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	other, _, err := expandNixRunTokens("{nix_run something-else}", v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, " -- ") {
		t.Errorf("a different attr must not receive installer args: %q", other)
	}
}
