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

func TestDetectNixSubcommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"flake", "check"}, "flake check"},
		{[]string{"flake", "update"}, "flake update"},
		{[]string{"flake", "lock"}, "flake lock"},
		{[]string{"build", ".#pkg"}, "build"},
		{[]string{"-v", "flake", "update"}, "flake update"},
		{[]string{"flake", "--recreate-lock-file", "update"}, "flake update"},
		{[]string{"store", "gc"}, "store"},
		{[]string{"flake"}, "flake"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := detectNixSubcommand(c.args); got != c.want {
			t.Errorf("detectNixSubcommand(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestParseNonOverrideAction(t *testing.T) {
	t.Run("default is warn", func(t *testing.T) {
		t.Setenv("PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION", "")
		action, rest, err := parseNonOverrideAction([]string{"flake", "update"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if action != nonOverrideWarn {
			t.Errorf("action = %v, want warn", action)
		}
		if !reflect.DeepEqual(rest, []string{"flake", "update"}) {
			t.Errorf("rest = %v", rest)
		}
	})
	t.Run("space-separated flag is stripped and honored", func(t *testing.T) {
		action, rest, err := parseNonOverrideAction([]string{"--non-override-subcommand-action", "error", "flake", "update"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if action != nonOverrideError {
			t.Errorf("action = %v, want error", action)
		}
		if !reflect.DeepEqual(rest, []string{"flake", "update"}) {
			t.Errorf("rest = %v", rest)
		}
	})
	t.Run("equals form is stripped and honored", func(t *testing.T) {
		action, rest, err := parseNonOverrideAction([]string{"--non-override-subcommand-action=ignore", "build", "."})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if action != nonOverrideIgnore {
			t.Errorf("action = %v, want ignore", action)
		}
		if !reflect.DeepEqual(rest, []string{"build", "."}) {
			t.Errorf("rest = %v", rest)
		}
	})
	t.Run("invalid value errors", func(t *testing.T) {
		if _, _, err := parseNonOverrideAction([]string{"--non-override-subcommand-action=bogus", "flake", "check"}); err == nil {
			t.Error("expected error for invalid action value")
		}
	})
	t.Run("env supplies value when flag absent", func(t *testing.T) {
		t.Setenv("PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION", "ignore")
		action, _, err := parseNonOverrideAction([]string{"flake", "update"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if action != nonOverrideIgnore {
			t.Errorf("action = %v, want ignore (from env)", action)
		}
	})
	t.Run("flag takes priority over env", func(t *testing.T) {
		t.Setenv("PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION", "ignore")
		action, _, err := parseNonOverrideAction([]string{"--non-override-subcommand-action", "error", "flake", "update"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if action != nonOverrideError {
			t.Errorf("action = %v, want error (flag beats env)", action)
		}
	})
}

func TestDecideNix(t *testing.T) {
	t.Run("override-applicable subcommand injects overrides", func(t *testing.T) {
		d := decideNix([]string{"flake", "check"}, nonOverrideWarn)
		if !d.injectOverrides || d.warn != "" || d.abort != "" {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("deny-listed + warn runs without overrides and warns", func(t *testing.T) {
		d := decideNix([]string{"flake", "update"}, nonOverrideWarn)
		if d.injectOverrides || d.warn == "" || d.abort != "" {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("deny-listed + error aborts", func(t *testing.T) {
		d := decideNix([]string{"flake", "lock"}, nonOverrideError)
		if d.abort == "" {
			t.Errorf("expected abort, got %+v", d)
		}
	})
	t.Run("deny-listed + ignore runs silently without overrides", func(t *testing.T) {
		d := decideNix([]string{"flake", "update"}, nonOverrideIgnore)
		if d.injectOverrides || d.warn != "" || d.abort != "" {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("double-dash on override-applicable subcommand aborts", func(t *testing.T) {
		d := decideNix([]string{"run", ".#tool", "--", "arg"}, nonOverrideWarn)
		if d.abort == "" {
			t.Errorf("expected abort for '--', got %+v", d)
		}
	})
}

func TestNixCommand_AppendsGitFileOverrideForEachRepo(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "foo", "git+file://" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.NixCommand(context.Background(), &out, []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
	// The nix invocation streams its output live (Opts.Stdout set).
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Opts.Stdout == nil {
		t.Errorf("nix should stream output (Opts.Stdout set); calls=%+v", calls)
	}
}

func TestNixCommand_NoRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "x"
`)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "show"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), &bytes.Buffer{}, []string{"flake", "show"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_MultipleOverridesAlphabetical(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	mkRepoDir(t, root, "bar")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"build", ".",
		"--override-input", "bar", "git+file://" + filepath.Join(root, "bar"),
		"--override-input", "foo", "git+file://" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), &bytes.Buffer{}, []string{"build", "."}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_DenyListedRunsWithoutOverrides(t *testing.T) {
	t.Setenv("PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION", "warn")
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	// flake update is deny-listed: must run with NO override flags.
	f.AddResponse("nix", []string{"flake", "update"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), &bytes.Buffer{}, []string{"flake", "update"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	for _, a := range calls[0].Args {
		if a == "--override-input" {
			t.Errorf("deny-listed subcommand must not receive overrides; got %v", calls[0].Args)
		}
	}
}

func TestNixCommand_DenyListedErrorAborts(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner() // no responses: nix must NOT be invoked.

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.NixCommand(context.Background(), &bytes.Buffer{}, []string{"--non-override-subcommand-action", "error", "flake", "update"})
	if err == nil {
		t.Fatal("expected error for deny-listed subcommand under action=error")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("nix must not run under action=error; got %d calls", len(f.Calls()))
	}
}

func TestNixCommand_RefusesDoubleDash(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner() // no responses: nix must NOT be invoked.

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.NixCommand(context.Background(), &bytes.Buffer{}, []string{"run", ".#tool", "--", "arg"})
	if err == nil {
		t.Fatal("expected error refusing to inject overrides past '--'")
	}
	if !strings.Contains(err.Error(), "--") {
		t.Errorf("error should mention '--'; got %q", err.Error())
	}
	if len(f.Calls()) != 0 {
		t.Errorf("nix must not run when '--' present; got %d calls", len(f.Calls()))
	}
}

func TestNixCommand_UsesConfiguredInputName(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "phillipg-nix-repo-base")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.phillipg-nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"
input-name = "phillipgreenii-nix-base"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "phillipgreenii-nix-base", "git+file://" + filepath.Join(root, "phillipg-nix-repo-base"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), &bytes.Buffer{}, []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}
