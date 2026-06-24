package workspace

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// primaryState classifies a primary checkout for smart integration (step 7).
type primaryState int

const (
	primaryOnCleanMain   primaryState = iota // on main, clean → merge --ff-only
	primaryOnOtherBranch                     // main not checked out → ff the ref
	primaryOnDirtyMain                       // on main but dirty → defer
)

// updateWorktreesSubdir is the dot-prefixed dir under WorktreesDir() holding the
// ephemeral per-repo update worktrees. Dot-prefixed so WorktreeList and the
// filesystem scanners skip it.
const updateWorktreesSubdir = ".pn-update"

// updateRunStampFn produces the per-run suffix used for the shared branch name
// and per-repo worktree dir names. Time + PID so concurrent runs don't collide
// on a coarse timestamp. A package var so tests can pin it deterministically.
var updateRunStampFn = func() string {
	return fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102-150405"), os.Getpid())
}

// primaryMainState probes the primary checkout's branch + cleanliness to decide
// how step 7 advances main. A non-"main" current branch (or a probe error) is
// treated as primaryOnOtherBranch: main is not checked out, so its ref can be
// fast-forwarded without touching the working tree.
func (ws *Workspace) primaryMainState(ctx context.Context, primary string) primaryState {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", primary, "rev-parse", "--abbrev-ref", "HEAD"}, exec.RunOptions{})
	cur := ""
	if err == nil {
		cur = strings.TrimSpace(string(res.Stdout))
	}
	if cur != "main" {
		return primaryOnOtherBranch
	}
	if ws.isDirty(ctx, primary) {
		return primaryOnDirtyMain
	}
	return primaryOnCleanMain
}
