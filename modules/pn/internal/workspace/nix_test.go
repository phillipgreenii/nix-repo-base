package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNixCommand_RefusesFlakeUpdate(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {flakeInputs: `{}`, gitRemotes: "origin\tgithub:o/foo (fetch)\norigin\tgithub:o/foo (push)\n", createFlake: true},
	})
	err := w.NixCommand(context.Background(), []string{"flake", "update"})
	if err == nil {
		t.Fatal("expected refusal of `nix flake update`")
	}
	if !strings.Contains(err.Error(), "flake update") {
		t.Errorf("error should name the denied subcommand: %v", err)
	}
}

func TestNixCommand_RefusesFlakeLock(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {flakeInputs: `{}`, gitRemotes: "origin\tgithub:o/foo (fetch)\norigin\tgithub:o/foo (push)\n", createFlake: true},
	})
	if err := w.NixCommand(context.Background(), []string{"flake", "lock"}); err == nil {
		t.Fatal("expected refusal of `nix flake lock`")
	}
}

func TestNixCommand_RefusesFlakeUpdateWithExtraArgs(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {flakeInputs: `{}`, gitRemotes: "origin\tgithub:o/foo (fetch)\norigin\tgithub:o/foo (push)\n", createFlake: true},
	})
	// Extra flags after the matched prefix should still refuse.
	if err := w.NixCommand(context.Background(), []string{"flake", "update", "--commit-lock-file"}); err == nil {
		t.Fatal("expected refusal of `nix flake update --commit-lock-file`")
	}
}

func TestNixCommand_AllowsFlakeShow(t *testing.T) {
	// `nix flake show` is NOT in the deny-list. It should run, with overrides.
	cfg := `
[workspace]
name = "test"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {flakeInputs: `{}`, gitRemotes: "origin\tgithub:o/foo (fetch)\norigin\tgithub:o/foo (push)\n", createFlake: true},
	})
	runner := w.Runner().(*exec.FakeRunner)
	runner.AddResponse("nix", []string{"flake", "show"}, exec.Result{}, nil)
	if err := w.NixCommand(context.Background(), []string{"flake", "show"}); err != nil {
		t.Fatalf("NixCommand should allow `nix flake show`: %v", err)
	}
}

func TestNixCommand_StripsLeadingDoubleDash(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {flakeInputs: `{}`, gitRemotes: "origin\tgithub:o/foo (fetch)\norigin\tgithub:o/foo (push)\n", createFlake: true},
	})
	// `-- flake update` should be treated as `flake update` and refused.
	err := w.NixCommand(context.Background(), []string{"--", "flake", "update"})
	if err == nil {
		t.Fatal("expected refusal of `nix -- flake update`")
	}
	if !strings.Contains(err.Error(), "flake update") {
		t.Errorf("error should name the denied subcommand: %v", err)
	}
}
