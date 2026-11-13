package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// OptimiseSkippedExitCode is the process exit status when `deepclean
// --optimise` skipped or refused the optimise step (host busy, on battery, or
// /nix too full). It is deliberately neither 0 (the step did not run) nor 1
// (nothing failed), so a scheduler can tell "try again next week" from a real
// failure. 75 is sysexits.h EX_TEMPFAIL.
const OptimiseSkippedExitCode = 75

// MaxIdleLoad is the 1-minute load average at or above which the optimise step
// is skipped (unless forced).
const MaxIdleLoad = 4.0

// DefaultOptimiseMinFreeGB is the default free space /nix must have before the
// optimise step starts (store.toml key optimise_min_free_gb overrides it).
const DefaultOptimiseMinFreeGB = 10

// nixVolume is the mount point whose free space the prerequisite check reads.
const nixVolume = "/nix"

// OptimiseSkippedError reports that the optimise step did not run and why.
type OptimiseSkippedError struct{ Reason string }

func (e *OptimiseSkippedError) Error() string {
	return "nix store optimise skipped: " + e.Reason
}

// IsOptimiseSkipped reports whether err carries an OptimiseSkippedError.
func IsOptimiseSkipped(err error) bool {
	var s *OptimiseSkippedError
	return errors.As(err, &s)
}

// optimiseArgv builds the command line for the optimise step: niced, and on
// darwin additionally in the background QoS class (a long batch nobody waits
// on). extra is appended after `nix store optimise` (tests pass --store).
func optimiseArgv(goos string, extra ...string) (name string, args []string) {
	args = []string{"-n", "10"}
	if goos == "darwin" {
		args = append(args, "taskpolicy", "-b")
	}
	args = append(args, "nix", "store", "optimise")
	args = append(args, extra...)
	return "nice", args
}

var loadAvgRE = regexp.MustCompile(`^\{?\s*([0-9]+(?:\.[0-9]+)?)`)

// parseLoadAvg extracts the 1-minute load from `sysctl -n vm.loadavg`
// ("{ 1.23 4.56 7.89 }").
func parseLoadAvg(s string) (float64, bool) {
	m := loadAvgRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}

// idleGate returns "" when the host looks idle (on AC power and 1-minute load
// below MaxIdleLoad), else the reason it does not. A signal that cannot be
// read counts as not idle: a batch job should not start on a guess.
func (s *Store) idleGate(ctx context.Context) string {
	res, err := s.runner.Run(ctx, "pmset", []string{"-g", "batt"}, exec.RunOptions{})
	switch {
	case err != nil:
		return "cannot determine power source (pmset -g batt failed)"
	case !strings.Contains(string(res.Stdout), "AC Power"):
		return "not on AC power"
	}
	res, err = s.runner.Run(ctx, "sysctl", []string{"-n", "vm.loadavg"}, exec.RunOptions{})
	if err != nil {
		return "cannot determine load average (sysctl -n vm.loadavg failed)"
	}
	load, ok := parseLoadAvg(string(res.Stdout))
	if !ok {
		return fmt.Sprintf("cannot parse load average %q", strings.TrimSpace(string(res.Stdout)))
	}
	if load >= MaxIdleLoad {
		return fmt.Sprintf("1-minute load average %.2f is not below %.0f", load, MaxIdleLoad)
	}
	return ""
}

// freeSpaceGate returns "" when /nix has at least minFreeGB GiB free, else why not.
func (s *Store) freeSpaceGate(ctx context.Context, minFreeGB int) string {
	res, err := s.runner.Run(ctx, "df", []string{"-k", nixVolume}, exec.RunOptions{})
	if err != nil {
		return "cannot determine free space on " + nixVolume + " (df failed)"
	}
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	if len(lines) < 2 {
		return "cannot parse df output for " + nixVolume
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return "cannot parse df output for " + nixVolume
	}
	availKB, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return "cannot parse df output for " + nixVolume
	}
	if free := availKB * 1024; free < int64(minFreeGB)<<30 {
		return fmt.Sprintf("%s has %s free, less than the required %d GiB (a full volume makes GC and optimise slow and risky)",
			nixVolume, formatSize(free), minFreeGB)
	}
	return ""
}

// countLinks counts the entries of <storeDir>/.links (the dedup hash index).
// -1 when the directory cannot be read.
func countLinks(storeDir string) int {
	d, err := os.Open(filepath.Join(storeDir, ".links"))
	if err != nil {
		return -1
	}
	defer d.Close()
	n := 0
	for {
		names, err := d.Readdirnames(8192)
		n += len(names)
		if err != nil {
			if err == io.EOF {
				return n
			}
			return -1
		}
	}
}

func formatLinks(n int) string {
	if n < 0 {
		return "unknown"
	}
	return strconv.Itoa(n)
}

// runOptimise runs the optimise step (gates already passed), reporting start
// and end time, elapsed time and .links entry count so a later run can
// estimate cost. It claims no percentage: nix store optimise cannot report one.
func (s *Store) runOptimise(ctx context.Context, out io.Writer, extra ...string) error {
	storeDir := s.env.NixStoreDir
	if storeDir == "" {
		storeDir = "/nix/store"
	}
	name, args := optimiseArgv(runtime.GOOS, extra...)
	start := time.Now()
	fmt.Fprintf(out, "Optimising store (hard-linking duplicate files): %s %s\n", name, strings.Join(args, " "))
	fmt.Fprintf(out, "  started %s; .links entries: %s\n", start.Format(time.RFC3339), formatLinks(countLinks(storeDir)))
	_, err := s.runner.Run(ctx, name, args, exec.RunOptions{Stdout: out, Stderr: out})
	fmt.Fprintf(out, "  finished after %s; .links entries: %s\n", time.Since(start).Round(time.Second), formatLinks(countLinks(storeDir)))
	return err
}

// optimiseStep applies the gates and runs the optimise step. A skipped step
// returns an *OptimiseSkippedError; a failed one a plain error.
func (s *Store) optimiseStep(ctx context.Context, out io.Writer, cfg Config, force bool) error {
	if !force {
		if why := s.idleGate(ctx); why != "" {
			fmt.Fprintf(out, "Skipping optimise: %s (use --force to override)\n", why)
			return &OptimiseSkippedError{Reason: why}
		}
	}
	if why := s.freeSpaceGate(ctx, cfg.OptimiseMinFreeGB); why != "" {
		fmt.Fprintf(out, "Refusing to optimise: %s\n", why)
		return &OptimiseSkippedError{Reason: why}
	}
	if err := s.runOptimise(ctx, out); err != nil {
		return fmt.Errorf("nix store optimise: %w", err)
	}
	return nil
}
