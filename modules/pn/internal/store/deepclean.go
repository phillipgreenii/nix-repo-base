package store

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// DeepCleanOptions configures DeepClean.
type DeepCleanOptions struct {
	// DryRun shows what would be cleaned without deleting. Maps to `--dry-run`.
	DryRun bool
	// KeepSince overrides the time-based retention period (e.g. "14d", "2w").
	// Empty falls back to the config default (it is NOT parsed as 0d).
	KeepSince string
	// Keep overrides the count-based retention. Negative means "use config
	// default". 0 disables count protection (only the current generation is
	// always kept).
	Keep int
}

// keepSinceWeeksRE / keepSinceDaysRE recognize the two accepted suffixes.
var (
	keepSinceWeeksRE = regexp.MustCompile(`^(\d+)w$`)
	keepSinceDaysRE  = regexp.MustCompile(`^(\d+)d$`)
)

// parseKeepSince parses a retention string into a number of days.
//
//	^(\d+)w$ → N×7   (0w → (0, true))
//	^(\d+)d$ → N     (0d → (0, true))
//	anything else (including "") → (0, false)
func parseKeepSince(s string) (days int, ok bool) {
	if m := keepSinceWeeksRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n * 7, true
	}
	if m := keepSinceDaysRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	return 0, false
}

// prunedCategories is the fixed order the summary reports prune counts in.
// It is also the order the sections execute in.
var prunedCategories = []string{
	"system",
	"home-manager",
	"user-profiles",
	"devbox-global",
	"devbox-util",
	"devbox-projects",
	"result-symlinks",
	"stale-nix-profiles",
	"nh-temp-roots",
}

// DeepClean prunes old profile generations, orphaned standalone home-manager
// profiles, stale ~/.nix-profiles entries, result symlinks, NH temp roots, and
// (in non-dry-run mode) runs `sudo nix-store --gc` followed by
// `nix store optimise`, then reports a summary.
//
// Output is sectioned (one `=== <Title> ===` header per category, each followed
// by a blank line). Warnings from devboxProjects (missing search dirs) go to
// errOut. The GC step streams live to out; all other subprocesses run with
// empty RunOptions.
func (s *Store) DeepClean(ctx context.Context, out, errOut io.Writer, opts DeepCleanOptions) error {
	cfg := LoadConfig(s.env)

	// ─── NB6: keep-since / keep resolution ─────────────────────────────────────
	keepDays := cfg.KeepDays
	if opts.KeepSince != "" { // empty ⇒ config default; do NOT parseKeepSince("")
		d, ok := parseKeepSince(opts.KeepSince)
		if !ok {
			return fmt.Errorf("--keep-since must be <N>d or <N>w (e.g. 14d, 2w)")
		}
		keepDays = d // 0d ⇒ 0 ⇒ time protection off
	}
	keepCount := cfg.KeepCount
	if opts.Keep >= 0 { // <0 ⇒ config; 0 ⇒ count off
		keepCount = opts.Keep
	}

	now := time.Now()
	prunedCounts := map[string]int{}

	// ─── 1. System Profiles ────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== System Profiles ===")
	s.processProfile(ctx, out, prunedCounts, "system", s.systemProfile(), "system", true, keepDays, keepCount, now, opts.DryRun)
	fmt.Fprintln(out)

	// ─── 2. Home Manager ───────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Home Manager ===")
	s.processHomeManager(ctx, out, prunedCounts, keepDays, keepCount, now, opts.DryRun)
	fmt.Fprintln(out)

	// ─── 3. User Profiles ──────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== User Profiles ===")
	for _, p := range s.userProfiles() {
		s.processProfile(ctx, out, prunedCounts, s.formatProfileLabel(p, "user-profiles"), p, "user-profiles", false, keepDays, keepCount, now, opts.DryRun)
	}
	fmt.Fprintln(out)

	// ─── 4. Devbox Global ──────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Devbox Global ===")
	if dbg := s.devboxGlobalProfile(); dbg != "" {
		s.processProfile(ctx, out, prunedCounts, "devbox-global", dbg, "devbox-global", false, keepDays, keepCount, now, opts.DryRun)
	} else {
		fmt.Fprintln(out, "  (not installed)")
	}
	fmt.Fprintln(out)

	// ─── 5. Devbox Util ────────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Devbox Util ===")
	if dbu := s.devboxUtilProfile(); dbu != "" {
		s.processProfile(ctx, out, prunedCounts, "devbox-util", dbu, "devbox-util", false, keepDays, keepCount, now, opts.DryRun)
	} else {
		fmt.Fprintln(out, "  (not installed)")
	}
	fmt.Fprintln(out)

	// ─── 6. Devbox Projects ────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Devbox Projects ===")
	if len(cfg.SearchDirs) == 0 {
		fmt.Fprintln(out, "  (no search dirs configured)")
	} else {
		for _, p := range s.devboxProjects(ctx, errOut, cfg.SearchDirs) {
			s.processProfile(ctx, out, prunedCounts, s.formatProfileLabel(p, "devbox-projects"), p, "devbox-projects", false, keepDays, keepCount, now, opts.DryRun)
		}
	}
	fmt.Fprintln(out)

	// ─── 7. Result Symlinks ────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Result Symlinks ===")
	s.processResultSymlinks(out, prunedCounts, cfg.SearchDirs, opts.DryRun)
	fmt.Fprintln(out)

	// ─── 8. Stale Nix Profiles ─────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Stale Nix Profiles ===")
	s.processStaleNixProfiles(out, prunedCounts, keepDays, now, opts.DryRun)
	fmt.Fprintln(out)

	// ─── 9. NH Temp Roots ──────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== NH Temp Roots ===")
	s.processNHTempRoots(out, prunedCounts, opts.DryRun)
	fmt.Fprintln(out)

	// ─── Summary ───────────────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Summary ===")
	if opts.DryRun {
		fmt.Fprintln(out, "DRY RUN — no changes made")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Would prune:")
		s.printPrunedCounts(out, prunedCounts)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Reclaimable estimate (dead paths):")
		fmt.Fprintf(out, "%s\n", deadPathsSize(ctx, s.runner))
		return nil
	}

	// Live run.
	fmt.Fprintf(out, "Store before: %s\n", storeSize(ctx, s.runner))
	if _, err := s.runner.Run(ctx, "sudo", []string{"nix-store", "--gc"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("nix-store --gc: %w", err)
	}
	// Hard-link duplicate files in the surviving store paths. Runs AFTER the GC
	// so it never optimises paths that are about to be deleted. This is the
	// batched replacement for auto-optimise-store (disabled in nix-settings.nix):
	// optimising per-fetch stalled `pn workspace update` on the "copying to the
	// store" phase, so it is deferred to this deliberate cleanup instead. No
	// sudo: in a multi-user (daemon) install the nix-daemon performs the
	// privileged hard-linking on the caller's behalf.
	fmt.Fprintln(out, "Optimising store (hard-linking duplicate files)...")
	if _, err := s.runner.Run(ctx, "nix", []string{"store", "optimise"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("nix store optimise: %w", err)
	}
	fmt.Fprintf(out, "Store after:  %s\n", storeSize(ctx, s.runner))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Pruned generations:")
	s.printPrunedCounts(out, prunedCounts)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "=== Runtime Roots ===")
	if summary := runtimeRootsSummary(ctx, s.runner); summary != "" {
		fmt.Fprintln(out, summary)
	}
	return nil
}

// printPrunedCounts emits the 9-category prune counts in fixed order.
func (s *Store) printPrunedCounts(out io.Writer, counts map[string]int) {
	for _, cat := range prunedCategories {
		fmt.Fprintf(out, "  %s: %d generation(s)\n", cat, counts[cat])
	}
}

// processProfile prunes one profile's generations. If the profile symlink is
// missing (os.Lstat fails), it returns without printing. Otherwise it prints
// either "  <label>: nothing to prune" or
// "  <label>: N generation(s) to prune (n1 n2 ...)" and, on a live run, deletes
// the selected generations. It accumulates len(nums) into counts[category].
//
// The section's trailing blank line is printed by the caller, not here.
func (s *Store) processProfile(ctx context.Context, out io.Writer, counts map[string]int, label, profile, category string, sudo bool, keepDays, keepCount int, now time.Time, dryRun bool) {
	if _, err := os.Lstat(profile); err != nil {
		return
	}
	gens, err := listGenerations(ctx, s.runner, profile, sudo)
	if err != nil {
		// Intentional bash-parity: a profile whose nix-env listing fails prunes
		// nothing and the run continues.
		gens = nil
	}
	nums := generationsToPrune(gens, keepDays, keepCount, now)
	counts[category] += len(nums)
	if len(nums) == 0 {
		fmt.Fprintf(out, "  %s: nothing to prune\n", label)
		return
	}
	strs := make([]string, len(nums))
	for i, n := range nums {
		strs[i] = strconv.Itoa(n)
	}
	fmt.Fprintf(out, "  %s: %d generation(s) to prune (%s)\n", label, len(nums), strings.Join(strs, " "))
	if !dryRun {
		_ = pruneGenerations(ctx, s.runner, profile, nums, sudo)
	}
}

// processHomeManager handles the Home Manager section. When the HM profile is an
// orphaned standalone profile (darwin-integrated HM is active), it reports the
// orphan removal of the profile symlink plus its generation links. Otherwise it
// processes the HM profile like any normal profile.
func (s *Store) processHomeManager(ctx context.Context, out io.Writer, counts map[string]int, keepDays, keepCount int, now time.Time, dryRun bool) {
	hm := s.homeManagerProfile()
	if _, err := os.Lstat(hm); err != nil {
		return
	}
	if s.isOrphanedStandaloneHMProfile(hm) {
		genLinks := s.homeManagerGenLinks()
		fmt.Fprintln(out, "  home-manager: orphaned standalone profile (darwin-integrated HM is active)")
		fmt.Fprintf(out, "    Profile: %s\n", hm)
		fmt.Fprintf(out, "    Removing: profile symlink + %d generation link(s)\n", len(genLinks))
		counts["home-manager"] += len(genLinks)
		if !dryRun {
			_ = os.Remove(hm)
			for _, link := range genLinks {
				_ = os.Remove(link)
			}
		}
		return
	}
	s.processProfile(ctx, out, counts, "home-manager", hm, "home-manager", false, keepDays, keepCount, now, dryRun)
}

// processResultSymlinks reports and (on a live run) removes result symlinks under
// the configured search dirs.
func (s *Store) processResultSymlinks(out io.Writer, counts map[string]int, searchDirs []string, dryRun bool) {
	links := discoverResultSymlinks(searchDirs)
	counts["result-symlinks"] = len(links)
	if len(links) == 0 {
		fmt.Fprintln(out, "  nothing to clean")
		return
	}
	fmt.Fprintf(out, "  %d result symlink(s) to remove:\n", len(links))
	for _, link := range links {
		fmt.Fprintf(out, "    %s\n", link)
	}
	if !dryRun {
		for _, link := range links {
			_ = os.Remove(link)
		}
	}
}

// processStaleNixProfiles reports and (on a live run) removes stale ~/.nix-profiles
// entries. The listing prints basenames; removal targets the full paths.
func (s *Store) processStaleNixProfiles(out io.Writer, counts map[string]int, keepDays int, now time.Time, dryRun bool) {
	entries := s.staleNixProfiles(keepDays, now)
	counts["stale-nix-profiles"] = len(entries)
	if len(entries) == 0 {
		fmt.Fprintln(out, "  nothing to clean")
		return
	}
	fmt.Fprintf(out, "  %d stale profile(s) to remove:\n", len(entries))
	for _, p := range entries {
		fmt.Fprintf(out, "    %s\n", filepath.Base(p))
	}
	if !dryRun {
		for _, p := range entries {
			_ = os.Remove(p)
		}
	}
}

// processNHTempRoots reports and (on a live run) removes NH temp roots in TMPDIR.
func (s *Store) processNHTempRoots(out io.Writer, counts map[string]int, dryRun bool) {
	roots := s.nhTempRoots()
	counts["nh-temp-roots"] = len(roots)
	if len(roots) == 0 {
		fmt.Fprintln(out, "  nothing to clean")
		return
	}
	fmt.Fprintf(out, "  %d temp root(s) to remove:\n", len(roots))
	for _, r := range roots {
		fmt.Fprintf(out, "    %s\n", r)
	}
	if !dryRun {
		for _, r := range roots {
			_ = os.Remove(r)
		}
	}
}
