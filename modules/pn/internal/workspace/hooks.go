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
//   - HookPhasePre: any non-zero exit aborts. Returns that error.
//   - HookPhasePost: non-zero exits print stderr to os.Stderr but do not
//     abort or propagate as an error.
func RunHooks(ctx context.Context, runner exec.Runner, entries []string, workspaceRoot string, phase HookPhase) error {
	for _, raw := range entries {
		resolved, err := rewriteFirstToken(raw, workspaceRoot)
		if err != nil {
			return fmt.Errorf("hook %q: resolve: %w", raw, err)
		}
		res, err := runner.Run(ctx, "sh", []string{"-c", resolved}, exec.RunOptions{Dir: workspaceRoot})
		if err == nil {
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
