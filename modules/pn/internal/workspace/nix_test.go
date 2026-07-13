package workspace

import (
	"bytes"
	"context"
	"io"
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
	err := w.NixCommand(context.Background(), io.Discard, []string{"flake", "update"})
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
	if err := w.NixCommand(context.Background(), io.Discard, []string{"flake", "lock"}); err == nil {
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
	if err := w.NixCommand(context.Background(), io.Discard, []string{"flake", "update", "--commit-lock-file"}); err == nil {
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
	if err := w.NixCommand(context.Background(), io.Discard, []string{"flake", "show"}); err != nil {
		t.Fatalf("NixCommand should allow `nix flake show`: %v", err)
	}
}

// TestNixCommand_StreamsOutput guards the regression fixed for the pre-commit
// test hooks (bead pg2-cys8): the underlying nix invocation MUST receive live
// stdout/stderr sinks so a failing build's full output reaches the terminal,
// rather than being buffered and truncated into the returned CommandError.
func TestNixCommand_StreamsOutput(t *testing.T) {
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
	runner.AddResponse("nix", []string{"build"}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.NixCommand(context.Background(), &out, []string{"build"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}

	var nixCall *exec.Call
	calls := runner.Calls()
	for i := range calls {
		if calls[i].Name == "nix" {
			nixCall = &calls[i]
			break
		}
	}
	if nixCall == nil {
		t.Fatal("expected a `nix` invocation")
	}
	if nixCall.Opts.Stdout != &out || nixCall.Opts.Stderr != &out {
		t.Errorf("nix must be run with live stdout/stderr sinks; got Stdout=%v Stderr=%v", nixCall.Opts.Stdout, nixCall.Opts.Stderr)
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
	err := w.NixCommand(context.Background(), io.Discard, []string{"--", "flake", "update"})
	if err == nil {
		t.Fatal("expected refusal of `nix -- flake update`")
	}
	if !strings.Contains(err.Error(), "flake update") {
		t.Errorf("error should name the denied subcommand: %v", err)
	}
}

// TestMatchesDeniedSubcommand exercises the de-flagged subsequence matcher over
// the bypass vectors (leading boolean/value-taking flags, leading --, trailing
// args, interspersed flags) and the allow cases (bead pg2-odu4p).
func TestMatchesDeniedSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"bare flake update", []string{"flake", "update"}, true},
		{"bare flake lock", []string{"flake", "lock"}, true},
		{"leading boolean flag", []string{"--verbose", "flake", "update"}, true},
		{"leading short flag", []string{"-L", "flake", "lock"}, true},
		{"leading value-taking flag", []string{"--option", "build-cores", "4", "flake", "update"}, true},
		{"value-taking flag BETWEEN deny words", []string{"flake", "--option", "build-cores", "4", "update"}, true},
		{"short value-taking flag between deny words", []string{"flake", "-j", "4", "lock"}, true},
		{"leading double dash", []string{"--", "flake", "update"}, true},
		{"trailing args", []string{"flake", "update", "--commit-lock-file"}, true},
		{"interspersed flag", []string{"flake", "--verbose", "update"}, true},
		// A deny word appearing only as a value-taking flag's VALUE (consumed,
		// not a positional) must NOT match — no false positive (pg2-9p527).
		{"deny word only as an option value", []string{"build", "--argstr", "x", "flake", "--argstr", "y", "update"}, false},
		{"allow flake show", []string{"flake", "show"}, false},
		{"allow build", []string{"build"}, false},
		{"allow flagged build", []string{"--verbose", "build"}, false},
		{"allow develop", []string{"develop"}, false},
		{"allow empty", []string{}, false},
		{"allow flake check", []string{"flake", "check"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := matchesDeniedSubcommand(tc.args)
			if got != tc.want {
				t.Errorf("matchesDeniedSubcommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestNixCommand_RefusesFlakeUpdateWithLeadingFlag proves the cited bypass
// (`pn workspace nix --verbose flake update`) is refused through the public
// entry point (bead pg2-odu4p).
func TestNixCommand_RefusesFlakeUpdateWithLeadingFlag(t *testing.T) {
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
	err := w.NixCommand(context.Background(), io.Discard, []string{"--verbose", "flake", "update"})
	if err == nil {
		t.Fatal("expected refusal of `nix --verbose flake update`")
	}
	if !strings.Contains(err.Error(), "flake update") {
		t.Errorf("error should name the denied subcommand: %v", err)
	}
}

// TestNixCommand_RefusesFlakeUpdateWithInterspersedValueFlag proves the bypass
// where a value-taking global flag sits BETWEEN the deny words
// (`pn workspace nix flake --option build-cores 4 update`) is refused through
// the public entry point — nix parses that argv as `flake update` (bead
// pg2-9p527).
func TestNixCommand_RefusesFlakeUpdateWithInterspersedValueFlag(t *testing.T) {
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
	err := w.NixCommand(context.Background(), io.Discard, []string{"flake", "--option", "build-cores", "4", "update"})
	if err == nil {
		t.Fatal("expected refusal of `nix flake --option build-cores 4 update`")
	}
	if !strings.Contains(err.Error(), "flake update") {
		t.Errorf("error should name the denied subcommand: %v", err)
	}
}

// TestNixCommand_AllowsFlaggedBuild proves a legit flagged build is NOT a false
// positive, and that only the original args (not the de-flagged form) are
// forwarded to nix (bead pg2-odu4p).
func TestNixCommand_AllowsFlaggedBuild(t *testing.T) {
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
	// foo has no override edges, so the forwarded argv is exactly the original.
	runner := w.Runner().(*exec.FakeRunner)
	runner.AddResponse("nix", []string{"--verbose", "build"}, exec.Result{}, nil)
	if err := w.NixCommand(context.Background(), io.Discard, []string{"--verbose", "build"}); err != nil {
		t.Fatalf("NixCommand should allow `nix --verbose build`: %v", err)
	}
}
