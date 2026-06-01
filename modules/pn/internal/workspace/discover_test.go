package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// helper: build a workspace whose runner is pre-scripted with the per-repo
// nix eval + git remote responses.
func newTestWorkspace(t *testing.T, configToml string, perRepo map[string]struct {
	flakeInputs string // raw JSON; empty -> nix eval not scripted (FakeRunner returns err)
	gitRemotes  string // raw `git remote -v` output; empty -> no remotes
	createFlake bool   // whether to create flake.nix on disk (gates the inputs lookup)
}) *Workspace {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), configToml)
	runner := exec.NewFakeRunner()
	for repoName, fixture := range perRepo {
		repoDir := filepath.Join(root, repoName)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if fixture.createFlake {
			writeFile(t, filepath.Join(repoDir, "flake.nix"), "{}")
			runner.AddResponse("nix",
				[]string{"eval", "--json", "--file", filepath.Join(repoDir, "flake.nix"), "inputs"},
				exec.Result{Stdout: []byte(fixture.flakeInputs)},
				nil)
		}
		runner.AddResponse("git",
			[]string{"-C", repoDir, "remote", "-v"},
			exec.Result{Stdout: []byte(fixture.gitRemotes)},
			nil)
	}
	w, err := Open(root, runner)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestDiscover_SimpleDep_OrderAndInputName(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "personal"

[repos.base]
url = "github:o/base"

[repos.personal]
url = "github:o/personal"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"base": {
			flakeInputs: `{}`,
			gitRemotes:  "origin\tgithub:o/base (fetch)\norigin\tgithub:o/base (push)\n",
			createFlake: true,
		},
		"personal": {
			flakeInputs: `{"upstream-base": {"url": "github:o/base"}}`,
			gitRemotes:  "origin\tgithub:o/personal (fetch)\norigin\tgithub:o/personal (push)\n",
			createFlake: true,
		},
	})
	repos, err := w.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(repos))
	}
	if repos[0].Name != "base" {
		t.Errorf("first repo = %q, want base", repos[0].Name)
	}
	if !repos[1].IsTerminal {
		t.Errorf("last repo (personal) should be terminal")
	}
	if repos[0].InputName != "upstream-base" {
		t.Errorf("base inputName = %q, want upstream-base", repos[0].InputName)
	}
	if repos[1].InputName != "" {
		t.Errorf("personal (terminal) InputName should be empty; got %q", repos[1].InputName)
	}
}

func TestDiscover_MultiRemoteIdentity(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "personal"

[repos.lib]
remotes = [
  { name = "origin", url = "github:o/lib" },
  { name = "mirror", url = "https://github.com/o/lib-mirror" },
]

[repos.personal]
url = "github:o/personal"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"lib": {
			flakeInputs: `{}`,
			gitRemotes:  "mirror\thttps://github.com/o/lib-mirror (fetch)\nmirror\thttps://github.com/o/lib-mirror (push)\norigin\tgithub:o/lib (fetch)\norigin\tgithub:o/lib (push)\n",
			createFlake: true,
		},
		"personal": {
			// personal uses the MIRROR url, not origin
			flakeInputs: `{"my-lib": {"url": "https://github.com/o/lib-mirror"}}`,
			gitRemotes:  "origin\tgithub:o/personal (fetch)\norigin\tgithub:o/personal (push)\n",
			createFlake: true,
		},
	})
	repos, err := w.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// "personal" should be terminal; "lib" should be first with inputName "my-lib".
	if repos[0].Name != "lib" || repos[0].InputName != "my-lib" {
		t.Errorf("lib: %+v", repos[0])
	}
	if !repos[1].IsTerminal {
		t.Errorf("personal should be terminal: %+v", repos[1])
	}
}

func TestDiscover_RemoteAgreementFailure(t *testing.T) {
	cfg := `
[workspace]
name = "test"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {
			gitRemotes:  "origin\tgithub:o/SOMETHING-ELSE (fetch)\norigin\tgithub:o/SOMETHING-ELSE (push)\n",
			createFlake: false,
		},
	})
	_, err := w.Discover()
	if err == nil {
		t.Fatal("expected remote-agreement error")
	}
}

func TestDiscover_EmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "empty"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	repos, err := w.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected empty repo list; got %v", repos)
	}
}
