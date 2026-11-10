package app

// The shim half of --check: does `nix` / `darwin-rebuild` on the caller's PATH
// resolve to a shim, what would the shim wrap, and would a root run pass
// through for lack of its configuration. Every line starts with "shim:" so a
// caller (pn workspace doctor) can tell them from the telemetry lines.
//
// A silent fail-open is NOT made visible by this: a passthrough execs the real
// binary before any telemetry exists. --check is how a human or the doctor
// finds a shim that is not installed, shadowed, or missing its root config.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/resolve"
)

// StatusPrefix starts the one summary line --check prints last, so a parser
// can separate telemetry health from shim health without scraping prose.
const StatusPrefix = "status: "

// pathLookup is the first executable PATH entry named name, and whether it is
// a shim: its fully resolved final component is the wrapper's name.
func pathLookup(name, pathEnv string) (path string, shim, found bool) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, name)
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		if abs, err := filepath.Abs(cand); err == nil {
			cand = abs
		}
		real, err := filepath.EvalSymlinks(cand)
		return cand, err == nil && filepath.Base(real) == resolve.WrapperName, true
	}
	return "", false, false
}

// checkShim prints the shim lines and reports whether both names resolve to
// a shim (the only condition that makes it unhealthy).
func (r *runner) checkShim() (ok bool) {
	ok = true
	for _, tool := range []string{ToolNix, ToolDarwinRebuild} {
		p, shim, found := pathLookup(tool, r.getenv("PATH"))
		switch {
		case !found:
			fmt.Fprintf(r.Stdout, "shim: %s: NOT a shim: not found on PATH\n", tool)
			ok = false
		case !shim:
			fmt.Fprintf(r.Stdout, "shim: %s: NOT a shim: %s is a real binary shadowing it\n", tool, p)
			ok = false
		default:
			fmt.Fprintf(r.Stdout, "shim: %s: shim at %s\n", tool, p)
		}
	}

	allow := DefaultAllowlist()
	rc, why := r.loadRootConfig()
	rootPath := r.RootConfigPath
	if rootPath == "" {
		rootPath = DefaultRootConfigPath
	}
	var extra []string
	if r.Euid == 0 {
		if why == "" {
			extra = rc.Verbs
		}
	} else {
		extra = r.userVerbs()
	}
	ignored := allow.Add(extra)
	fmt.Fprintf(r.Stdout, "shim: allowlist: %s\n", strings.Join(allow.Verbs(), ", "))
	if len(ignored) > 0 {
		fmt.Fprintf(r.Stdout, "shim: allowlist-ignored: %s (never wrapped or unknown)\n", strings.Join(ignored, ", "))
	}

	switch why {
	case "":
		resolved, _ := filepath.EvalSymlinks(rootPath)
		fmt.Fprintf(r.Stdout, "shim: root-config: %s -> %s (store regular file)\n", rootPath, resolved)
	case "no root configuration":
		fmt.Fprintf(r.Stdout, "shim: root-config: absent (%s)\n", rootPath)
	default:
		fmt.Fprintf(r.Stdout, "shim: root-config: %s present but not usable: %s (not a store symlink)\n", rootPath, why)
	}
	switch {
	case why != "":
		fmt.Fprintf(r.Stdout, "shim: root-run: would pass through (%s)\n", why)
	case rc.RootEndpoint == "":
		fmt.Fprintln(r.Stdout, "shim: root-run: would pass through (root configuration has no rootEndpoint)")
	default:
		fmt.Fprintf(r.Stdout, "shim: root-run: would wrap (rootEndpoint %s)\n", rc.RootEndpoint)
	}
	return ok
}

func okWord(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}
