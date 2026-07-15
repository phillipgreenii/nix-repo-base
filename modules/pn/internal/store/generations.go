package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

type generation struct {
	Num     int
	Date    string
	Current bool
}

// sudoArgs prefixes name+args with "sudo" when sudo is true. When
// nonInteractive is also true it inserts sudo's -n flag: sudo(8) then never
// prompts for a password on /dev/tty and instead fails fast with a non-zero
// exit if no credentials are cached. Read-only callers (the `audit` command)
// pass nonInteractive=true so they can never block on a password prompt in a
// non-interactive context (CI, scripted audit, an agent capturing output);
// interactive/mutating callers (`deepclean`) pass false so sudo may still
// prompt as expected. When sudo is false, nonInteractive has no effect.
func sudoArgs(sudo, nonInteractive bool, name string, args ...string) (string, []string) {
	if !sudo {
		return name, args
	}
	prefix := []string{}
	if nonInteractive {
		prefix = append(prefix, "-n")
	}
	prefix = append(prefix, name)
	return "sudo", append(prefix, args...)
}

func listGenerations(ctx context.Context, r exec.Runner, profile string, sudo, nonInteractive bool) ([]generation, error) {
	name, args := sudoArgs(sudo, nonInteractive, "nix-env", "--profile", profile, "--list-generations")
	res, err := r.Run(ctx, name, args, exec.RunOptions{})
	if err != nil {
		return nil, err
	}
	var out []generation
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		num, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		current := strings.Contains(line, "(current)")
		out = append(out, generation{Num: num, Date: fields[1], Current: current})
	}
	return out, nil
}

func generationsToPrune(gens []generation, keepDays, keepCount int, now time.Time) []int {
	total := len(gens)
	if total == 0 {
		return nil
	}
	countProtected := map[int]bool{}
	if keepCount > 0 {
		start := max(0, total-keepCount)
		for i := start; i < total; i++ {
			countProtected[gens[i].Num] = true
		}
	}
	var cutoff time.Time
	if keepDays > 0 {
		cutoff = now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	}
	var del []int
	for _, g := range gens {
		if g.Current || countProtected[g.Num] {
			continue
		}
		if keepDays > 0 {
			// ParseInLocation(..., time.Local) → LOCAL midnight, matching the
			// bash `date -d "$gdate"` (local). Plain time.Parse yields UTC
			// midnight and would skew the boundary by the TZ offset. On parse
			// error, do NOT time-protect (matches bash falling through).
			if t, err := time.ParseInLocation("2006-01-02", g.Date, time.Local); err == nil {
				if !t.Before(cutoff) { // t >= cutoff → time-protected
					continue
				}
			}
		}
		del = append(del, g.Num)
	}
	return del
}

func pruneGenerations(ctx context.Context, r exec.Runner, profile string, nums []int, sudo, nonInteractive bool) error {
	if len(nums) == 0 {
		return nil
	}
	strs := make([]string, len(nums))
	for i, n := range nums {
		strs[i] = strconv.Itoa(n)
	}
	name, args := sudoArgs(sudo, nonInteractive, "nix-env", "--profile", profile, "--delete-generations")
	args = append(args, strs...)
	_, err := r.Run(ctx, name, args, exec.RunOptions{})
	return err
}
