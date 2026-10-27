package app

import (
	"math/rand"
	"strings"
	"testing"
)

func TestSubcommand(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  []string
		want string
	}{
		{"build", []string{"nix", "build", ".#x"}, "build"},
		{"absolute path", []string{"/run/current-system/sw/bin/nix", "build", ".#x"}, "build"},
		{"flag with value before the verb", []string{"nix", "--extra-experimental-features", "flakes", "build"}, "build"},
		{"flag=value form", []string{"nix", "--extra-experimental-features=flakes", "build"}, "build"},
		{"flake check", []string{"nix", "flake", "check", "--no-build"}, "flake check"},
		{"flake update", []string{"nix", "flake", "update"}, "flake update"},
		{"flake with an interposed value flag", []string{"nix", "flake", "--option", "build-cores", "4", "check"}, "flake check"},
		{"flake without an allowlisted second word", []string{"nix", "flake", "init"}, "other"},
		{"bare flake", []string{"nix", "flake"}, "other"},
		{"fmt stops at --", []string{"nix", "fmt", "--", "f"}, "fmt"},
		{"program args after -- are not verbs", []string{"nix", "run", ".#x", "--", "build"}, "run"},
		{"only program args after --", []string{"nix", "--", "build"}, "other"},
		{"eval", []string{"nix", "eval", "--raw", ".#x"}, "eval"},
		{"darwin-rebuild switch", []string{"darwin-rebuild", "switch", "--flake", "d#h"}, "switch"},
		{"darwin-rebuild build", []string{"darwin-rebuild", "build"}, "build"},
		{"darwin-rebuild flag before the verb", []string{"darwin-rebuild", "--flake", "d#h", "check"}, "check"},
		{"nix does not know darwin verbs", []string{"nix", "switch"}, "other"},
		{"unknown verb", []string{"nix", "frobnicate"}, "other"},
		{"no verb", []string{"nix"}, "other"},
		{"flags only", []string{"nix", "--version"}, "other"},
		{"empty command", nil, "other"},
		{"another program", []string{"nixos-rebuild", "switch"}, "other"},
		{"fake of another name", []string{"fake-nix", "build"}, "other"},
		// Value-flags whose values look like verbs must not be taken as the verb.
		{"--option value is not a verb", []string{"nix", "--option", "foo", "build", "eval", ".#x"}, "eval"},
		{"--override-input values are not verbs", []string{"nix", "--override-input", "build", "path", "flake", "check"}, "flake check"},
		// ACCEPTED IMPRECISION: a value-taking flag missing from
		// subcommandValueFlagArity leaves its value in the scan. --unlisted
		// (not a real nix flag) stands for such a flag; its value "build" is
		// reported as the verb although the operation is eval. The table
		// covers the flags the wrapped commands take; a handful of
		// mislabelled runs is acceptable, and the label stays bounded.
		{"unknown value-flag misleads the scan", []string{"nix", "--unlisted", "build", "eval", ".#x"}, "build"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Subcommand(c.cmd); got != c.want {
				t.Errorf("Subcommand(%q) = %q, want %q", c.cmd, got, c.want)
			}
		})
	}
}

// The label MUST stay closed: whatever the argv, only allowlisted values or
// "other" come out, never a raw user string.
func TestSubcommandLabelSetIsClosed(t *testing.T) {
	allowed := map[string]bool{SubcommandOther: true}
	for v := range nixVerbs {
		if v != "flake" {
			allowed[v] = true
		}
	}
	for v := range nixFlakeVerbs {
		allowed["flake "+v] = true
	}
	for v := range darwinRebuildVerbs {
		allowed[v] = true
	}
	pool := []string{"build", "flake", "check", "switch", "--option", "--flake", "-j", "--", "x", "secret-user-string", "-", "", "frob", "--a=b"}
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 5000; n++ {
		cmd := []string{[]string{"nix", "darwin-rebuild", "other-prog"}[rng.Intn(3)]}
		for i, k := 0, rng.Intn(7); i < k; i++ {
			cmd = append(cmd, pool[rng.Intn(len(pool))])
		}
		if got := Subcommand(cmd); !allowed[got] {
			t.Fatalf("Subcommand(%q) = %q is outside the closed label set", cmd, got)
		}
	}
	if strings.Contains(Subcommand([]string{"nix", "secret-user-string"}), "secret") {
		t.Error("raw user string leaked into the label")
	}
}
