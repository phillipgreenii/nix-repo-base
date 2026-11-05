package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"
)

// installHookProbeTimeout bounds one `nix eval` probe for the
// install-pre-commit-hooks flake output. An eval that overruns counts as absent.
const installHookProbeTimeout = 60 * time.Second

// installHookWhen is the event list of the per-repo hook init proposes: the
// four events at which a repo's hook bundle is (re)installed (ADR 0019
// idempotency-gate amendment; ADR 0032).
var installHookWhen = []string{"post-clone", "post-rebase", "post-update", "post-upgrade"}

// installHookRun is the run entry of the proposed hook.
const installHookRun = "{nix_run " + installPreCommitHooksAttr + "}"

// InitHooksOptions configures DetectInstallHooks.
type InitHooksOptions struct {
	// Yes accepts every candidate without prompting.
	Yes bool
	// Confirm, when non-nil, is asked once per candidate (default No) and makes
	// the run interactive. When nil and Yes is false the run is non-interactive:
	// nothing is written and one advisory is printed. It MUST NOT read stdin.
	Confirm func(repo string) bool
}

// InstallHookCandidates returns, sorted, each configured repo that is cloned on
// disk, does not already declare {nix_run install-pre-commit-hooks} in any
// per-repo hook, and whose flake exposes install-pre-commit-hooks. Probes fan out
// over the workspace pool, each bounded by installHookProbeTimeout; an eval
// error counts as absent. Repos that already declare the token are skipped
// before any eval.
func (ws *Workspace) InstallHookCandidates(ctx context.Context) []string {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		hits = map[string]bool{}
	)
	for _, name := range orderedRepoNames(ws.config.Repos) {
		if ws.installHookDeclared(name) || !isGitRepo(filepath.Join(ws.root, name)) {
			continue
		}
		wg.Add(1)
		ws.pool.Submit(func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, installHookProbeTimeout)
			defer cancel()
			if ws.flakeHasOutput(pctx, name, installPreCommitHooksAttr) {
				mu.Lock()
				hits[name] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	var out []string
	for _, name := range orderedRepoNames(ws.config.Repos) {
		if hits[name] {
			out = append(out, name)
		}
	}
	return out
}

// DetectInstallHooks is the bootstrap aid run by `pn workspace init` after Init:
// it finds repos whose flake exposes install-pre-commit-hooks but whose entry does
// not declare the per-repo event hook, and (per opts) appends a dedicated
// EventHook for each accepted one, written once through the canonical writer.
// Existing hooks are never modified. Returns the repos it added the hook to.
func (ws *Workspace) DetectInstallHooks(ctx context.Context, out io.Writer, opts InitHooksOptions) ([]string, error) {
	cands := ws.InstallHookCandidates(ctx)
	if len(cands) == 0 {
		return nil, nil
	}
	if !opts.Yes && opts.Confirm == nil {
		fmt.Fprintf(out, "advisory: %d repo(s) expose %s but do not declare the per-repo hook %s: %v; "+
			"re-run `pn workspace init --yes` to add it (--no-detect-hooks silences this)\n",
			len(cands), installPreCommitHooksAttr, installHookRun, cands)
		return nil, nil
	}
	const rewriteNote = "note: writing pn-workspace.toml rewrites it canonically; hand-written comments are dropped"
	fmt.Fprintln(out, rewriteNote)
	var accepted []string
	for _, name := range cands {
		if !opts.Yes {
			fmt.Fprintf(out, "add [[repos.%s.hooks]] when=%v run=[%q]? [y/N]: ", name, installHookWhen, installHookRun)
			if !opts.Confirm(name) {
				continue
			}
		}
		accepted = append(accepted, name)
	}
	if len(accepted) == 0 {
		return nil, nil
	}
	for _, name := range accepted {
		r := ws.config.Repos[name]
		r.Hooks = append(append([]EventHook(nil), r.Hooks...), EventHook{
			When: append([]string(nil), installHookWhen...),
			Run:  []string{installHookRun},
		})
		ws.config.Repos[name] = r
		fmt.Fprintf(out, "added %s hook for %s\n", installPreCommitHooksAttr, name)
	}
	if err := ws.writeConfigTOMLAtomic(); err != nil {
		return nil, fmt.Errorf("init: write hooks: %w", err)
	}
	return accepted, nil
}
