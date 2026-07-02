package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// templateVars holds the values substituted into build_command / apply_command
// templates. See ADR 0017.
type templateVars struct {
	TerminalRepoDir    string // {terminal_repo_dir}
	TerminalNixDir     string // {terminal_nix_dir}
	TerminalNixRelPath string // {terminal_nix_relative_path}
	Hostname           string // {hostname}
	Builder            string // {builder}
}

// knownPlaceholders lists the placeholder names recognized in command templates,
// in a stable order for error messages.
var knownPlaceholders = []string{
	"terminal_repo_dir",
	"terminal_nix_dir",
	"terminal_nix_relative_path",
	"hostname",
	"builder",
}

var knownPlaceholderSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(knownPlaceholders))
	for _, k := range knownPlaceholders {
		m[k] = struct{}{}
	}
	return m
}()

// placeholderTokenRe matches a lowercase {token}. The preceding byte is checked
// separately (scanPlaceholders) because Go's regexp has no lookbehind: a token
// preceded by '$' is a shell variable (e.g. ${home}), not one of our placeholders.
var placeholderTokenRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// scanPlaceholders returns the names of all lowercase {token}s in tmpl that are
// NOT preceded by '$'. This lets legitimate shell variables like ${home} pass
// while still catching real placeholders (and typos / removed placeholders).
func scanPlaceholders(tmpl string) []string {
	var names []string
	for _, loc := range placeholderTokenRe.FindAllStringSubmatchIndex(tmpl, -1) {
		start := loc[0]
		if start > 0 && tmpl[start-1] == '$' {
			continue // ${shellvar}, not our placeholder
		}
		names = append(names, tmpl[loc[2]:loc[3]])
	}
	return names
}

// unknownPlaceholders returns the sorted, de-duplicated set of $-unescaped
// {token}s in tmpl that are not known placeholders.
func unknownPlaceholders(tmpl string) []string {
	var unknown []string
	seen := map[string]struct{}{}
	for _, n := range scanPlaceholders(tmpl) {
		if _, ok := knownPlaceholderSet[n]; ok {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		unknown = append(unknown, n)
	}
	sort.Strings(unknown)
	return unknown
}

// validateCommandPlaceholders reports an error if tmpl references any unknown
// placeholder. It is a static NAME check only (used at config parse time); the
// {builder}-emptiness guard is host-dependent and stays a run-time check in
// substituteCommand.
func validateCommandPlaceholders(field, tmpl string) error {
	if unknown := unknownPlaceholders(tmpl); len(unknown) > 0 {
		return fmt.Errorf(
			"%s references unknown placeholder(s) %q; known placeholders: %q",
			field, unknown, knownPlaceholders)
	}
	return nil
}

// substituteCommand expands the known placeholders in a command template and
// splits the result into argv on whitespace. It fails loudly when the template
// references {builder} on an OS with no built-in builder, when it references an
// unknown placeholder, or when it expands to an empty command.
func substituteCommand(tmpl string, v templateVars) ([]string, error) {
	names := scanPlaceholders(tmpl)

	// Guard 1: {builder} referenced but no builder for this OS.
	for _, n := range names {
		if n == "builder" && v.Builder == "" {
			return nil, fmt.Errorf(
				"no built-in builder for this OS (GOOS=%s); set build_command/apply_command explicitly in pn-workspace.toml",
				runtime.GOOS)
		}
	}

	// Guard 2: unknown placeholders (typos, or the removed {terminal_flake}).
	if unknown := unknownPlaceholders(tmpl); len(unknown) > 0 {
		return nil, fmt.Errorf(
			"unknown placeholder(s) %q in command template; known placeholders: %q",
			unknown, knownPlaceholders)
	}

	r := strings.NewReplacer(
		"{terminal_repo_dir}", v.TerminalRepoDir,
		"{terminal_nix_dir}", v.TerminalNixDir,
		"{terminal_nix_relative_path}", v.TerminalNixRelPath,
		"{hostname}", v.Hostname,
		"{builder}", v.Builder,
	)
	args := strings.Fields(r.Replace(tmpl))
	if len(args) == 0 {
		return nil, fmt.Errorf("command template %q expands to an empty command", tmpl)
	}
	return args, nil
}

// detectBuilder maps an OS (and, on Linux, whether it is NixOS) to the built-in
// activation tool. It is defined only for the symmetric nixos-rebuild /
// darwin-rebuild pair; any other host yields "" (no built-in builder). A pure
// function so it can be unit-tested directly.
func detectBuilder(goos string, isNixOS bool) string {
	switch {
	case goos == "darwin":
		return "darwin-rebuild"
	case goos == "linux" && isNixOS:
		return "nixos-rebuild"
	default:
		return ""
	}
}

// isNixOSHost reports whether etcDir belongs to a NixOS host: either
// <etcDir>/NIXOS exists, or <etcDir>/os-release declares ID=nixos (the value MAY
// be quoted per the os-release spec, so surrounding quotes are stripped). No
// PATH probing — a stray nixos-rebuild binary on a non-NixOS host must not
// trigger detection. etcDir is injectable for tests.
func isNixOSHost(etcDir string) bool {
	if fileExists(filepath.Join(etcDir, "NIXOS")) {
		return true
	}
	data, err := os.ReadFile(filepath.Join(etcDir, "os-release"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "ID" {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if val == "nixos" {
			return true
		}
	}
	return false
}

// defaultBuilder returns the built-in builder for the current host, or "" when
// this OS has none (foreign Linux, etc.).
func defaultBuilder() string {
	return detectBuilder(runtime.GOOS, isNixOSHost("/etc"))
}

// shortenHostname truncates a hostname at the first dot (mimics `hostname -s`).
func shortenHostname(h string) string {
	if i := strings.IndexByte(h, '.'); i >= 0 {
		return h[:i]
	}
	return h
}

// shortHostname returns the current host's short name.
func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return shortenHostname(h)
}
