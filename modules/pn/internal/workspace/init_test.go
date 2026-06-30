package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// mkGitRepo creates a minimal git repo dir (directory with .git subdir) at
// root/name and returns its path. Used by init tests to simulate cloned repos.
func mkGitRepo(t *testing.T, root, name string) string {
	t.Helper()
	repoDir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkGitRepo %s: %v", name, err)
	}
	return repoDir
}

// TestInit_CreatesConfigFromEmptyDir: three git repos on disk, no
// pn-workspace.toml. Init should create config with all three repos.
func TestInit_CreatesConfigFromEmptyDir(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		mkGitRepo(t, root, name)
	}
	// Write a minimal TOML (Open requires it).
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "")

	f := exec.NewFakeRunner()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		url := "https://github.com/o/" + name + ".git"
		f.AddResponse("git",
			[]string{"-C", filepath.Join(root, name), "remote", "-v"},
			exec.Result{Stdout: []byte(
				"origin\t" + url + " (fetch)\norigin\t" + url + " (push)\n",
			)}, nil)
	}

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Init(context.Background(), &out, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(string(tomlData), name) {
			t.Errorf("TOML should mention %q; got:\n%s", name, string(tomlData))
		}
	}
	// Init must NOT write a lock file.
	if _, err := os.Stat(filepath.Join(root, LockFileName)); err == nil {
		t.Error("Init should not write a lock file")
	}
	// Summary should mention added repos.
	if !strings.Contains(out.String(), "added repo") && !strings.Contains(out.String(), "alpha") {
		t.Errorf("Init output should mention added repos; got:\n%s", out.String())
	}
}

// TestInit_AddsNewRepoToExistingConfig: config has 2 repos; a 3rd is on disk.
// Init adds the 3rd to config without touching the first two.
func TestInit_AddsNewRepoToExistingConfig(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "existing")
	mkGitRepo(t, root, "new-repo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.existing]
url = "github:o/existing"
`)

	f := exec.NewFakeRunner()
	// Only the new repo needs a remote -v call.
	const newURL = "https://github.com/o/new-repo.git"
	f.AddResponse("git",
		[]string{"-C", filepath.Join(root, "new-repo"), "remote", "-v"},
		exec.Result{Stdout: []byte(
			"origin\t" + newURL + " (fetch)\norigin\t" + newURL + " (push)\n",
		)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Init(context.Background(), &out, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	if !strings.Contains(string(tomlData), "new-repo") {
		t.Errorf("TOML should mention new-repo; got:\n%s", string(tomlData))
	}
	if !strings.Contains(string(tomlData), "existing") {
		t.Errorf("TOML should still mention existing; got:\n%s", string(tomlData))
	}
}

// TestInit_DiscoversMultiRemote: a fresh repo on disk has both an origin
// (github) and a bitbucket remote. Init must record both in the multi-remote
// `[[repos.NAME.remotes]]` form so a subsequent `pn workspace clone` re-creates
// all of them (regression for tc-bufhe — homepage's bitbucket mirror was lost
// because Init only read `git remote get-url origin`).
func TestInit_DiscoversMultiRemote(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "homepage")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "")

	f := exec.NewFakeRunner()
	f.AddResponse("git",
		[]string{"-C", filepath.Join(root, "homepage"), "remote", "-v"},
		exec.Result{Stdout: []byte(
			"bitbucket\tgit@bitbucket.org:phillipgreenii/homepage.git (fetch)\n" +
				"bitbucket\tgit@bitbucket.org:phillipgreenii/homepage.git (push)\n" +
				"origin\tssh://git@github.com/phillipgreenii/homepage.git (fetch)\n" +
				"origin\tssh://git@github.com/phillipgreenii/homepage.git (push)\n",
		)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Init(context.Background(), &out, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	toml := string(tomlData)
	// Must record both remotes. go-toml v2 serializes strings with single quotes.
	for _, want := range []string{
		"[[repos.homepage.remotes]]",
		"name = 'bitbucket'",
		"url = 'git@bitbucket.org:phillipgreenii/homepage.git'",
		"name = 'origin'",
		"url = 'ssh://git@github.com/phillipgreenii/homepage.git'",
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("TOML should contain %q; got:\n%s", want, toml)
		}
	}
	if !strings.Contains(out.String(), "remotes: bitbucket, origin") {
		t.Errorf("Init summary should list discovered remotes; got:\n%s", out.String())
	}
}

// TestInit_SingleNonOriginRemoteUsesMultiRemoteForm: when the sole remote is
// not named "origin", Init records it as `[[repos.NAME.remotes]]` so the name
// survives a fresh clone (single-url form implies origin).
func TestInit_SingleNonOriginRemoteUsesMultiRemoteForm(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "")

	f := exec.NewFakeRunner()
	f.AddResponse("git",
		[]string{"-C", filepath.Join(root, "foo"), "remote", "-v"},
		exec.Result{Stdout: []byte(
			"upstream\thttps://github.com/o/foo.git (fetch)\nupstream\thttps://github.com/o/foo.git (push)\n",
		)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	toml := string(tomlData)
	if !strings.Contains(toml, "name = 'upstream'") {
		t.Errorf("TOML should preserve the non-origin remote name; got:\n%s", toml)
	}
}

// TestInit_DoesNotOverwritePreexistingURL: a repo already in config has a URL
// that differs from git origin. Init must NOT overwrite it.
func TestInit_DoesNotOverwritePreexistingURL(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "pinned")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.pinned]
url = "github:o/pinned-canonical"
`)

	// No remote get-url call expected since repo is already in config.
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Init(context.Background(), &out, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	if !strings.Contains(string(tomlData), "pinned-canonical") {
		t.Errorf("Init must not overwrite existing URL; got:\n%s", string(tomlData))
	}
}

// TestInit_DefaultFlakePathNotWritten: repo has flake.nix at the default
// location (flake.nix). Init should NOT write flake_path to config.
func TestInit_DefaultFlakePathNotWritten(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "repo")
	writeFile(t, filepath.Join(root, "repo", "flake.nix"), "{ inputs = {}; }")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.repo]
url = "github:o/repo"
`)

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	if strings.Contains(string(tomlData), "flake_path") {
		t.Errorf("Init should not write flake_path for default location; got:\n%s", string(tomlData))
	}
}

// TestInit_NixDirFlakePathNotWritten: repo has flake.nix at nix/flake.nix
// (also a default). Init should NOT write flake_path to config.
func TestInit_NixDirFlakePathNotWritten(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "repo")
	if err := os.MkdirAll(filepath.Join(root, "repo", "nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "repo", "nix", "flake.nix"), "{ inputs = {}; }")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.repo]
url = "github:o/repo"
`)

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	if strings.Contains(string(tomlData), "flake_path") {
		t.Errorf("Init should not write flake_path for nix/ default location; got:\n%s", string(tomlData))
	}
}

// TestInit_PreservesExistingFlakePath: config already has a non-default
// flake_path. Init must preserve it and not overwrite.
func TestInit_PreservesExistingFlakePath(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "repo")
	if err := os.MkdirAll(filepath.Join(root, "repo", "custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "repo", "custom", "flake.nix"), "{ inputs = {}; }")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.repo]
url = "github:o/repo"
flake_path = "custom/flake.nix"
`)

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML: %v", err)
	}
	if !strings.Contains(string(tomlData), "custom/flake.nix") {
		t.Errorf("Init must preserve existing flake_path; got:\n%s", string(tomlData))
	}
}

// TestInit_Idempotent: running Init twice produces "no changes" on the second
// run, and the TOML is identical.
func TestInit_Idempotent(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "repo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.repo]
url = "github:o/repo"
`)

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out1 bytes.Buffer
	if err := w.Init(context.Background(), &out1, InitOptions{}); err != nil {
		t.Fatalf("Init (first): %v", err)
	}
	toml1, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML after first init: %v", err)
	}

	// Re-open and init again.
	w2, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open (second): %v", err)
	}
	var out2 bytes.Buffer
	if err := w2.Init(context.Background(), &out2, InitOptions{}); err != nil {
		t.Fatalf("Init (second): %v", err)
	}
	toml2, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read TOML after second init: %v", err)
	}

	if string(toml1) != string(toml2) {
		t.Errorf("Init is not idempotent; TOML changed:\nbefore:\n%s\nafter:\n%s", toml1, toml2)
	}
	if !strings.Contains(out2.String(), "no changes") {
		t.Errorf("second Init should report 'no changes'; got:\n%s", out2.String())
	}
}

// TestInit_NeverErrors_IncompleteConfig: even with an incomplete config (no
// terminal, missing repos), Init should succeed (never errors on indeterminacy).
func TestInit_NeverErrors_IncompleteConfig(t *testing.T) {
	root := t.TempDir()
	// No repos on disk, no terminal in config.
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "")

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init should not error on incomplete config; got: %v", err)
	}
}

// TestInit_DoesNotClone: init must not invoke git clone.
func TestInit_DoesNotClone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "clone" {
			t.Errorf("Init must not clone; got git clone call: %v", c.Args)
		}
	}
}

// TestInit_DoesNotWriteLock: init must not write a lock file.
func TestInit_DoesNotWriteLock(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, LockFileName)); err == nil {
		t.Error("Init must not write a lock file")
	}
}

// TestInit_SkipsConfiguredWorkforestsDirNonDot: when workforests_dir is a non-dot
// relative name (e.g. "sets"), Init must not add that directory as a repo even
// if it looks like a git repo.
func TestInit_SkipsConfiguredWorkforestsDirNonDot(t *testing.T) {
	root := t.TempDir()
	// "sets" is a non-dot directory that looks like a git repo.
	mkGitRepo(t, root, "sets")
	// "real-repo" is a normal repo that SHOULD be discovered.
	mkGitRepo(t, root, "real-repo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
workforests_dir = "sets"

[repos.existing]
url = "github:o/existing"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git",
		[]string{"-C", filepath.Join(root, "real-repo"), "remote", "get-url", "origin"},
		exec.Result{Stdout: []byte("https://github.com/o/real-repo.git\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Init(context.Background(), &out, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, exists := w.config.Repos["sets"]; exists {
		t.Error("Init must not add the configured workforests_dir 'sets' as a repo")
	}
	if _, exists := w.config.Repos["real-repo"]; !exists {
		t.Error("Init should have discovered real-repo")
	}
}

// TestInit_SkipsDotWorkforestsByDefault: the default ".workforests" is already
// skipped by the dot-prefix rule; confirm it continues to be skipped.
func TestInit_SkipsDotWorkforestsByDefault(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, ".workforests")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "")

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), &bytes.Buffer{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, exists := w.config.Repos[".workforests"]; exists {
		t.Error("Init must not add .workforests as a repo")
	}
}
