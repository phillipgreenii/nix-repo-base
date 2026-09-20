package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// HookPhase distinguishes pre- from post-hooks.
type HookPhase int

const (
	HookPhasePre HookPhase = iota
	HookPhasePost
)

// resolveHookPath converts a TOML-declared hook command string into the path
// to invoke. Rules:
//   - absolute paths (start with /) returned unchanged
//   - file-relative (./foo, ../foo) joined with workspaceRoot
//   - everything else returned unchanged (PATH lookup at execution time)
func resolveHookPath(cmd, workspaceRoot string) (string, error) {
	if strings.HasPrefix(cmd, "/") {
		return cmd, nil
	}
	if strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, "../") {
		return filepath.Join(workspaceRoot, cmd), nil
	}
	return cmd, nil
}

// RunHooks executes each command in entries in declaration order via `sh -c`.
// Path resolution per resolveHookPath is applied to the FIRST word of each
// command string; the rest of the string is preserved verbatim for shell
// argument handling.
//
// Phase semantics:
//   - HookPhasePre: any non-zero exit aborts. Returns that error. Stdout is
//     discarded on success, deliberately: a pre-hook is a gate, and a gate
//     that passes silently is conventional (bd tc-5cfwb) — there is no
//     RAN-vs-never-ran ambiguity to resolve here the way there is for
//     post-hooks, because a pre-hook that never ran would instead have
//     skipped the command it gates entirely, which is already observable.
//   - HookPhasePost: non-zero exits print stderr to os.Stderr but do not
//     abort or propagate as an error. On success, a one-line "ran ..."
//     acknowledgement (not the hook's stdout body) is written to os.Stderr —
//     see the decision below.
//
// Decision (bd tc-5cfwb): a successful post-hook's stdout was previously
// discarded entirely, making a hook that ran-and-succeeded indistinguishable
// from one that never fired at all (e.g. because its `when` never matched).
// Of the three options considered — always relay full stdout, gate it behind
// a new verbosity flag, or print a one-line acknowledgement — this picks the
// one-line acknowledgement:
//   - "always" would dump arbitrary hook stdout into `pn workspace push`
//     output that is already long across many repos, and a hook's stdout is
//     not formatted for that context.
//   - a verbosity flag would need new CLI plumbing (no such flag exists on
//     pn today) for a problem that a one-liner already solves.
//   - a one-line acknowledgement is the minimal change that satisfies the
//     actual requirement: RAN and DID-NOT-RUN must stop being
//     indistinguishable. It doesn't relay the hook's payload, so it doesn't
//     add to the verbosity problem the previous silence was presumably
//     trying to avoid.
func RunHooks(ctx context.Context, runner exec.Runner, entries []string, workspaceRoot string, phase HookPhase) error {
	for _, raw := range entries {
		resolved, err := rewriteFirstToken(raw, workspaceRoot)
		if err != nil {
			return fmt.Errorf("hook %q: resolve: %w", raw, err)
		}
		res, err := runner.Run(ctx, "sh", []string{"-c", resolved}, exec.RunOptions{Dir: workspaceRoot})
		if err == nil {
			if phase == HookPhasePost {
				_, _ = fmt.Fprintf(os.Stderr, "ran post-hook: %s\n", raw)
			}
			continue
		}
		if phase == HookPhasePre {
			_, _ = io.Copy(os.Stderr, strings.NewReader(string(res.Stderr)))
			return fmt.Errorf("pre-hook failed: %s: %w", raw, err)
		}
		// post-hook failure — warn, continue
		_, _ = fmt.Fprintf(os.Stderr, "warning: post-hook failed: %s: %v\n", raw, err)
		_, _ = os.Stderr.Write(res.Stderr)
	}
	return nil
}

// rewriteFirstToken takes a raw shell command string like "./foo --arg" and
// rewrites the leading executable token per resolveHookPath, preserving the
// rest of the string verbatim. This keeps shell features (pipes, &&, $vars)
// available to the user.
func rewriteFirstToken(raw, workspaceRoot string) (string, error) {
	trimmed := strings.TrimLeft(raw, " \t")
	if trimmed == "" {
		return raw, nil
	}
	// Find the end of the first token (first whitespace).
	end := strings.IndexAny(trimmed, " \t")
	var first, rest string
	if end < 0 {
		first = trimmed
		rest = ""
	} else {
		first = trimmed[:end]
		rest = trimmed[end:]
	}
	resolved, err := resolveHookPath(first, workspaceRoot)
	if err != nil {
		return "", err
	}
	return resolved + rest, nil
}
