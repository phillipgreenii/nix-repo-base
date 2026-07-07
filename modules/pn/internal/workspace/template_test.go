package workspace

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// baseVars returns a templateVars with all fields populated for the root-flake
// case (nix dir == repo dir, relative path ".").
func baseVars() templateVars {
	return templateVars{
		TerminalRepoDir:    "/ws/leaf",
		TerminalNixDir:     "/ws/leaf",
		TerminalNixRelPath: ".",
		Hostname:           "host01",
		Builder:            "darwin-rebuild",
	}
}

func TestSubstituteCommand_AllVars(t *testing.T) {
	// Subdir-flake case: nix dir under repo dir, relative path "nix".
	v := templateVars{
		TerminalRepoDir:    "/ws/homelab",
		TerminalNixDir:     "/ws/homelab/nix",
		TerminalNixRelPath: "nix",
		Hostname:           "monorepod",
		Builder:            "nixos-rebuild",
	}
	got, err := substituteCommand(
		"{builder} switch --flake {terminal_nix_dir}#{hostname} {terminal_repo_dir} {terminal_nix_relative_path}", v,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"nixos-rebuild", "switch", "--flake", "/ws/homelab/nix#monorepod", "/ws/homelab", "nix"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_RootFlake(t *testing.T) {
	got, err := substituteCommand("{builder} build --flake {terminal_nix_dir}", baseVars())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"darwin-rebuild", "build", "--flake", "/ws/leaf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_NoPlaceholders(t *testing.T) {
	got, err := substituteCommand("echo hello", baseVars())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"echo", "hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_BuilderEmptyReferenced_Errors(t *testing.T) {
	v := baseVars()
	v.Builder = ""
	_, err := substituteCommand("{builder} build --flake {terminal_nix_dir}", v)
	if err == nil {
		t.Fatal("expected error when {builder} referenced with empty Builder")
	}
	if !containsAll(err.Error(), "GOOS=", "pn-workspace.toml") {
		t.Errorf("error should name GOOS and mention pn-workspace.toml; got: %v", err)
	}
}

func TestSubstituteCommand_BuilderEmptyNotReferenced_OK(t *testing.T) {
	v := baseVars()
	v.Builder = ""
	got, err := substituteCommand("darwin-rebuild build --flake {terminal_nix_dir}", v)
	if err != nil {
		t.Fatalf("unexpected error when {builder} not referenced: %v", err)
	}
	want := []string{"darwin-rebuild", "build", "--flake", "/ws/leaf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_UnknownPlaceholder_Errors(t *testing.T) {
	if _, err := substituteCommand("do {foo}", baseVars()); err == nil {
		t.Error("expected error for unknown placeholder {foo}")
	}
}

func TestSubstituteCommand_RemovedTerminalFlake_Errors(t *testing.T) {
	_, err := substituteCommand("darwin-rebuild build --flake {terminal_flake}", baseVars())
	if err == nil {
		t.Fatal("expected error for removed placeholder {terminal_flake}")
	}
	if !containsAll(err.Error(), "terminal_flake") {
		t.Errorf("error should name the offending placeholder; got: %v", err)
	}
}

func TestSubstituteCommand_ShellVarNotRejected(t *testing.T) {
	// A lowercase ${home} shell variable must pass through untouched, not be
	// treated as an unknown placeholder.
	got, err := substituteCommand("echo ${home}/x", baseVars())
	if err != nil {
		t.Fatalf("lowercase ${home} shell var must not be rejected; got: %v", err)
	}
	want := []string{"echo", "${home}/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_EmptyResult_Errors(t *testing.T) {
	if _, err := substituteCommand("   ", baseVars()); err == nil {
		t.Error("expected error for empty command template")
	}
}

func TestDetectBuilder(t *testing.T) {
	cases := []struct {
		goos    string
		isNixOS bool
		want    string
	}{
		{"darwin", false, "darwin-rebuild"},
		{"darwin", true, "darwin-rebuild"},
		{"linux", true, "nixos-rebuild"},
		{"linux", false, ""},
		{"windows", false, ""},
		{"freebsd", true, ""},
	}
	for _, c := range cases {
		if got := detectBuilder(c.goos, c.isNixOS); got != c.want {
			t.Errorf("detectBuilder(%q, %v) = %q; want %q", c.goos, c.isNixOS, got, c.want)
		}
	}
}

func TestIsNixOSHost(t *testing.T) {
	t.Run("NIXOS marker file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "NIXOS"), "")
		if !isNixOSHost(dir) {
			t.Error("expected true when <etc>/NIXOS exists")
		}
	})
	t.Run("os-release ID=nixos unquoted", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "os-release"), "NAME=NixOS\nID=nixos\n")
		if !isNixOSHost(dir) {
			t.Error("expected true for ID=nixos")
		}
	})
	t.Run("os-release ID=\"nixos\" quoted", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "os-release"), "NAME=\"NixOS\"\nID=\"nixos\"\n")
		if !isNixOSHost(dir) {
			t.Error("expected true for quoted ID=\"nixos\"")
		}
	})
	t.Run("os-release ID=ubuntu", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "os-release"), "NAME=Ubuntu\nID=ubuntu\n")
		if isNixOSHost(dir) {
			t.Error("expected false for ID=ubuntu")
		}
	})
	t.Run("neither file", func(t *testing.T) {
		if isNixOSHost(t.TempDir()) {
			t.Error("expected false when neither NIXOS nor os-release present")
		}
	})
}

func TestShortenHostname(t *testing.T) {
	if got := shortenHostname("phillipg-mbp-02.local"); got != "phillipg-mbp-02" {
		t.Errorf("got %q", got)
	}
	if got := shortenHostname("plainhost"); got != "plainhost" {
		t.Errorf("got %q", got)
	}
}

// containsAll reports whether s contains every substring in subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestExpandNixRunTokens_ExpandsWithOverridesAndQuoting(t *testing.T) {
	v := nixHookVars{NixExe: "nix", OverrideArgs: []string{"--override-input", "base", "git+file:///w/repo-base"}, FlakeDir: "/w/consumer"}
	got, attrs, err := expandNixRunTokens("{nix_run install-pre-commit-hooks}", v)
	if err != nil {
		t.Fatal(err)
	}
	want := "nix run --override-input base 'git+file:///w/repo-base' '/w/consumer#install-pre-commit-hooks'"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if len(attrs) != 1 || attrs[0] != "install-pre-commit-hooks" {
		t.Fatalf("attrs %v", attrs)
	}
}

func TestExpandNixRunTokens_PreservesSurroundingText(t *testing.T) {
	got, _, err := expandNixRunTokens("echo x && {nix_run y} && echo ${HOME}", nixHookVars{NixExe: "nix", FlakeDir: "/w/c"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo x && nix run '/w/c#y' && echo ${HOME}" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandNixRunTokens_NoTokenVerbatim(t *testing.T) {
	got, attrs, err := expandNixRunTokens("ls -la", nixHookVars{})
	if err != nil || attrs != nil || got != "ls -la" {
		t.Fatalf("got %q attrs %v err %v", got, attrs, err)
	}
}

func TestExpandNixRunTokens_MultipleTokensError(t *testing.T) {
	if _, _, err := expandNixRunTokens("{nix_run a} {nix_run b}", nixHookVars{NixExe: "nix", FlakeDir: "/w/c"}); err == nil {
		t.Fatal("want error for >1 token")
	}
}
