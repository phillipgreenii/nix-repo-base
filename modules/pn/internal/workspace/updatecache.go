package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// stateDir returns the update-cache state root, honoring XDG_STATE_HOME.
func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "zn-self-upgrade")
}

func appliedHashDir() string { return filepath.Join(stateDir(), "apply", "applied-hash") }
func appliedHashFile(repoDir string) string {
	return filepath.Join(appliedHashDir(), filepath.Base(repoDir))
}

// needsRebuild reports whether apply must rebuild. Returns true if force is set,
// any repo's working tree is dirty, any repo's HEAD differs from the recorded
// applied hash, or any repo has no recorded hash. Returns false (with a notice)
// only when every repo is clean and unchanged.
func (ws *Workspace) needsRebuild(ctx context.Context, repoDirs []string, force bool, out io.Writer) (bool, error) {
	if force {
		return true, nil
	}
	for _, dir := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "status", "--porcelain"}, exec.RunOptions{})
		if err != nil {
			return false, fmt.Errorf("git status in %s: %w", dir, err)
		}
		if strings.TrimSpace(string(res.Stdout)) != "" {
			return true, nil
		}
		res, err = ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return false, fmt.Errorf("git rev-parse in %s: %w", dir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		stored, err := os.ReadFile(appliedHashFile(dir))
		if err != nil {
			return true, nil
		}
		if head != strings.TrimSpace(string(stored)) {
			return true, nil
		}
	}
	fmt.Fprintln(out, "Skipping rebuild: all workspace repos clean and unchanged since last apply")
	return false, nil
}

// markApplied records each repo's current HEAD as the last applied hash.
func (ws *Workspace) markApplied(ctx context.Context, repoDirs []string) error {
	if err := os.MkdirAll(appliedHashDir(), 0o755); err != nil {
		return err
	}
	for _, dir := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("git rev-parse in %s: %w", dir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		if err := os.WriteFile(appliedHashFile(dir), []byte(head+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// checkNixDaemon probes daemon responsiveness with a 10s-bounded `nix eval`. On
// failure it returns an actionable error (the interactive restart prompt from
// the bash version is intentionally omitted).
func (ws *Workspace) checkNixDaemon(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := ws.runner.Run(tctx, "nix", []string{"eval", "--expr", "true"}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("nix daemon health check failed: %w\n  Try: sudo launchctl kickstart -k system/org.nixos.nix-daemon", err)
	}
	return nil
}
