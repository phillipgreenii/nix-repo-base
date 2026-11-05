package app

// The shim's allowlist and the metric label tables come from ONE table,
// verbTable, so a verb that is wrapped always has a bounded operation label.
//
// Classes: default verbs are always wrapped in shim mode; opt-in verbs are
// wrapped only when named by an opt-in source (shim.toml, PG_NIX_SHIM_VERBS
// or the root configuration file); never verbs (interactive or long-lived)
// and unclassified label-only verbs are never wrapped, whatever a source says.

import (
	"slices"
	"strings"
)

// WrapClass says how the shim treats a verb.
type WrapClass int

const (
	// WrapNone is a labelled verb the shim never wraps and no source can add.
	WrapNone WrapClass = iota
	// WrapDefault is wrapped in shim mode with no configuration.
	WrapDefault
	// WrapOptIn is wrapped only when an opt-in source names it.
	WrapOptIn
	// WrapNever is interactive or long-lived; it is never wrapped and a source
	// naming it is ignored.
	WrapNever
)

// Tool names the shim serves.
const (
	ToolNix            = "nix"
	ToolDarwinRebuild  = "darwin-rebuild"
	wrapperDisableNote = "never"
)

type verbRow struct {
	Tool string
	// Label is the Subcommand label ("build", "flake check").
	Label string
	// Labelled is false for a verb that must keep the label "other" (repl).
	Labelled bool
	Class    WrapClass
}

var verbTable = []verbRow{
	{ToolNix, "build", true, WrapDefault},
	{ToolNix, "flake check", true, WrapDefault},
	{ToolNix, "eval", true, WrapOptIn},
	{ToolNix, "fmt", true, WrapOptIn},
	{ToolNix, "run", true, WrapOptIn},
	{ToolNix, "flake lock", true, WrapOptIn},
	{ToolNix, "flake update", true, WrapOptIn},
	{ToolNix, "develop", true, WrapNever},
	{ToolNix, "shell", true, WrapNever},
	{ToolNix, "repl", false, WrapNever},
	{ToolNix, "path-info", true, WrapNone},
	{ToolNix, "store", true, WrapNone},
	{ToolNix, "copy", true, WrapNone},
	{ToolNix, "flake show", true, WrapNone},
	{ToolNix, "flake metadata", true, WrapNone},
	{ToolDarwinRebuild, "build", true, WrapDefault},
	{ToolDarwinRebuild, "switch", true, WrapDefault},
	{ToolDarwinRebuild, "check", true, WrapDefault},
	{ToolDarwinRebuild, "activate", true, WrapNever},
	{ToolDarwinRebuild, "edit", true, WrapNone},
	{ToolDarwinRebuild, "changelog", true, WrapNone},
}

// deriveLabelTables builds the Subcommand scan tables from verbTable.
func deriveLabelTables() (nix, nixFlake, darwin map[string]bool) {
	nix, nixFlake, darwin = map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, row := range verbTable {
		if !row.Labelled {
			continue
		}
		switch row.Tool {
		case ToolNix:
			if first, second, ok := strings.Cut(row.Label, " "); ok {
				nix[first] = true
				nixFlake[second] = true
			} else {
				nix[row.Label] = true
			}
		case ToolDarwinRebuild:
			darwin[row.Label] = true
		}
	}
	return nix, nixFlake, darwin
}

// Allowlist is the set of (tool, label) pairs the shim wraps.
type Allowlist struct{ wrapped map[string]bool }

func key(tool, label string) string { return tool + " " + label }

// DefaultAllowlist holds exactly the WrapDefault rows.
func DefaultAllowlist() Allowlist {
	a := Allowlist{wrapped: map[string]bool{}}
	for _, row := range verbTable {
		if row.Class == WrapDefault {
			a.wrapped[key(row.Tool, row.Label)] = true
		}
	}
	return a
}

// Wraps reports whether the shim wraps label for tool.
func (a Allowlist) Wraps(tool, label string) bool { return a.wrapped[key(tool, label)] }

// Verbs lists the wrapped verbs as "tool label", sorted.
func (a Allowlist) Verbs() []string {
	out := make([]string, 0, len(a.wrapped))
	for k := range a.wrapped {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Add merges opt-in entries and never removes anything. An entry is a label
// ("run", "flake lock") or "tool label" ("nix run"); a bare label applies to
// every tool that has it. Only WrapOptIn rows can be added (an entry naming a
// default verb is a no-op). Entries that match nothing addable, including
// every WrapNever and WrapNone verb and unknown words, are returned as
// ignored.
func (a Allowlist) Add(entries []string) (ignored []string) {
	for _, e := range entries {
		e = strings.Join(strings.Fields(e), " ")
		if e == "" {
			continue
		}
		matched, added := false, false
		for _, row := range verbTable {
			if e != row.Label && e != key(row.Tool, row.Label) {
				continue
			}
			switch row.Class {
			case WrapOptIn:
				a.wrapped[key(row.Tool, row.Label)] = true
				added = true
			case WrapDefault:
				matched = true
			}
		}
		if !added && !matched {
			ignored = append(ignored, e)
		}
	}
	return ignored
}
