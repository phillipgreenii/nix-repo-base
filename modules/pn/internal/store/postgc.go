package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Post-GC step (bead pg2-rvw5m).
//
// Some caches (compiled-binary caches, wrapper dirs, ...) hold executables
// that link to /nix/store paths. `nix-store --gc` can delete those paths and
// leave the cached binaries dangling, so a cache like that MUST be cleared
// after every GC. pn stays generic: the directories and commands come from
// store.toml (`post_gc_clear_dirs`, `post_gc_commands`), supplied by whichever
// repo's home config knows about the cache. pn contains no tool-specific paths.

// resolveClearDir expands a leading "~" and validates a post_gc_clear_dirs
// entry. It refuses anything that is not an absolute path after expansion, the
// filesystem root, and $HOME itself, because the step runs `rm -rf` on it.
func (e Env) resolveClearDir(raw string) (string, error) {
	p := raw
	switch {
	case p == "~":
		p = e.Home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(e.Home, p[2:])
	}
	if p == "" || !filepath.IsAbs(p) {
		return "", fmt.Errorf("post_gc_clear_dirs entry %q: must be an absolute path or start with ~/", raw)
	}
	p = filepath.Clean(p)
	if p == string(filepath.Separator) || (e.Home != "" && p == filepath.Clean(e.Home)) {
		return "", fmt.Errorf("post_gc_clear_dirs entry %q: refusing to remove %s", raw, p)
	}
	return p, nil
}

// hasPostGC reports whether the config defines any post-GC work.
func (c Config) hasPostGC() bool {
	return len(c.PostGCClearDirs) > 0 || len(c.PostGCCommands) > 0
}

// runPostGC removes the configured cache dirs and runs the configured commands.
// It never aborts early: every entry is attempted, failures are reported on
// errOut, and the returned error joins them all. A directory that does not
// exist is not a failure (caches are routinely absent). It prints nothing when
// the config defines no post-GC work.
func (s *Store) runPostGC(ctx context.Context, out, errOut io.Writer, cfg Config) error {
	if !cfg.hasPostGC() {
		return nil
	}
	fmt.Fprintln(out, "Post-GC cleanup:")
	var errs []error
	for _, raw := range cfg.PostGCClearDirs {
		dir, err := s.env.resolveClearDir(raw)
		if err != nil {
			fmt.Fprintf(errOut, "  %v\n", err)
			errs = append(errs, err)
			continue
		}
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			fmt.Fprintf(out, "  %s: not present\n", dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			err = fmt.Errorf("remove %s: %w", dir, err)
			fmt.Fprintf(errOut, "  %v\n", err)
			errs = append(errs, err)
			continue
		}
		fmt.Fprintf(out, "  %s: removed\n", dir)
	}
	for _, argv := range cfg.PostGCCommands {
		if len(argv) == 0 || argv[0] == "" {
			err := errors.New("post_gc_commands entry is empty")
			fmt.Fprintf(errOut, "  %v\n", err)
			errs = append(errs, err)
			continue
		}
		fmt.Fprintf(out, "  run: %s\n", strings.Join(argv, " "))
		if _, err := s.runner.Run(ctx, argv[0], argv[1:], exec.RunOptions{Stdout: out, Stderr: errOut}); err != nil {
			err = fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
			fmt.Fprintf(errOut, "  post-GC command failed: %v\n", err)
			errs = append(errs, err)
		}
	}
	fmt.Fprintln(out)
	return errors.Join(errs...)
}

// printPostGCPlan lists what a live run would do after the GC (dry-run only).
// It prints nothing when the config defines no post-GC work.
func (s *Store) printPostGCPlan(out io.Writer, cfg Config) {
	if !cfg.hasPostGC() {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Would run after GC:")
	for _, raw := range cfg.PostGCClearDirs {
		if dir, err := s.env.resolveClearDir(raw); err == nil {
			fmt.Fprintf(out, "  remove %s\n", dir)
		} else {
			fmt.Fprintf(out, "  (invalid) %v\n", err)
		}
	}
	for _, argv := range cfg.PostGCCommands {
		fmt.Fprintf(out, "  run: %s\n", strings.Join(argv, " "))
	}
}
