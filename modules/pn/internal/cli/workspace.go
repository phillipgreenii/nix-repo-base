package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

func addWorkspaceCmd(parent *cobra.Command) {
	// terminalFlag holds the --terminal value shared across all subcommands.
	var terminalFlag string

	ws := &cobra.Command{
		Use:   "workspace",
		Short: "Operate on the pn workspace",
		Long: `Operate on the pn workspace.

All subcommands resolve the workspace root using this order:
  1. PN_WORKSPACE_ROOT environment variable
  2. Walk upward from cwd until pn-workspace.toml is found

Once resolved, PN_WORKSPACE_ROOT and WORKSPACE_ROOT are exported into
every subprocess pn spawns (hooks, update-locks.sh, etc.).

A workspace that declares [[hooks]] must be trusted once with
'pn workspace allow' before those hooks will execute; editing
pn-workspace.toml re-blocks until re-allowed. See ADR-0019.

Environment variables:
  PN_WORKSPACE_ROOT          Override workspace root (path to dir with pn-workspace.toml)
  PN_WORKSPACE_OVERRIDE_PATHS  Comma-separated name=path pairs to pin repo locations
  XDG_STATE_HOME             Override the apply-cache state parent dir (default ~/.local/state)
  NO_COLOR                   Disable ANSI colour codes in tree output`,
	}
	// --terminal is a persistent flag inherited by all subcommands.
	ws.PersistentFlags().StringVar(&terminalFlag, "terminal", "", "override the terminal repo (the flake build/apply targets)")

	ws.AddCommand(workspaceStatusCmd(&terminalFlag))
	ws.AddCommand(workspaceInitCmd(&terminalFlag))
	ws.AddCommand(workspaceCloneCmd(&terminalFlag))
	ws.AddCommand(workspaceLockCmd(&terminalFlag))
	ws.AddCommand(workspaceBuildCmd(&terminalFlag))
	ws.AddCommand(workspaceApplyCmd(&terminalFlag))
	ws.AddCommand(workspaceFlakeCheckCmd(&terminalFlag))
	ws.AddCommand(workspacePreCommitCheckCmd(&terminalFlag))
	ws.AddCommand(workspacePushCmd(&terminalFlag))
	ws.AddCommand(workspaceRebaseCmd(&terminalFlag))
	ws.AddCommand(workspaceFormatCmd(&terminalFlag))
	ws.AddCommand(workspaceTreeCmd(&terminalFlag))
	ws.AddCommand(workspaceUpdateCmd(&terminalFlag))
	ws.AddCommand(workspaceUpgradeCmd(&terminalFlag))
	ws.AddCommand(workspaceDiscoverCmd(&terminalFlag))
	ws.AddCommand(workspaceInfoCmd(&terminalFlag))
	ws.AddCommand(workspaceDoctorCmd(&terminalFlag))
	ws.AddCommand(workspaceNixCmd())
	ws.AddCommand(workspaceWorkforestCmd())
	ws.AddCommand(workspaceAllowCmd())
	ws.AddCommand(workspaceDenyCmd())
	ws.AddCommand(workspaceRemovedInstallHooksCmd())
	parent.AddCommand(ws)
}

// workspaceRemovedInstallHooksCmd is a hidden stub for the removed
// `install-hooks` / `sync-hooks` subcommands (superseded by event hooks in
// ADR-0019). Without it, cobra treats these muscle-memory / CI invocations as an
// unknown subcommand, prints the group help, and exits 0 — a silent no-op that
// would re-open the stale-gate bug. The stub fails loudly and points at the
// replacement (bd pg2-lbsi).
func workspaceRemovedInstallHooksCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "install-hooks",
		Aliases: []string{"sync-hooks"},
		Short:   "(removed) superseded by per-repo event hooks — see ADR-0019",
		Hidden:  true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf(
				"`pn workspace install-hooks` (and `sync-hooks`) was removed: hook resync is now a per-repo event hook. " +
					"Add to pn-workspace.toml, e.g. [[repos.<key>.hooks]] when=['post-clone','post-rebase','post-update'] " +
					"run=['{nix_run install-pre-commit-hooks}']; it fires automatically on those commands. See ADR-0019",
			)
		},
	}
}

// openWorkspace opens the workspace by walking up from cwd (or PN_WORKSPACE_ROOT).
// It is a variable so that tests can replace it with a stub that returns a
// controlled *workspace.Workspace without touching the file system or spawning
// real subprocesses. Production code must never reassign it.
var openWorkspace = func() (*workspace.Workspace, error) { return openWorkspaceRoot("") }

// openWorkspaceRoot opens the workspace rooted via resolveWorkspaceRoot(rootFlag).
// It also exports the resolved root as PN_WORKSPACE_ROOT and WORKSPACE_ROOT so
// every subprocess pn spawns (update-locks.sh, hooks, determine-ul-lib-dir, …)
// can locate the workspace without recomputing or guessing.
func openWorkspaceRoot(rootFlag string) (*workspace.Workspace, error) {
	root, err := resolveWorkspaceRoot(rootFlag)
	if err != nil {
		return nil, err
	}
	_ = os.Setenv("PN_WORKSPACE_ROOT", root)
	_ = os.Setenv("WORKSPACE_ROOT", root)
	return workspace.Open(root, exec.NewRealRunner())
}

// resolveWorkspaceRoot resolves the workspace root: --root flag, then
// PN_WORKSPACE_ROOT, then the nearest ancestor of cwd containing pn-workspace.toml.
func resolveWorkspaceRoot(rootFlag string) (string, error) {
	check := func(dir string) (string, error) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if !fileExists(filepath.Join(abs, workspace.ConfigFileName)) {
			return "", fmt.Errorf("no %s in %s", workspace.ConfigFileName, abs)
		}
		return abs, nil
	}
	if rootFlag != "" {
		return check(rootFlag)
	}
	if env := os.Getenv("PN_WORKSPACE_ROOT"); env != "" {
		return check(env)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, workspace.ConfigFileName)) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in cwd or any ancestor", workspace.ConfigFileName)
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func workspaceStatusCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print git status across all workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			return runWithHooks(ctx, w, "status", func() error {
				return w.Status(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), workspace.StatusOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceInitCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scan workspace root for git repos; reconcile into pn-workspace.toml (config-only; no clone, no lock write)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "init", func() error {
				return w.Init(ctx, out, workspace.InitOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceBuildCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Build all workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "build", func() error {
				return w.Build(ctx, out, workspace.BuildOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceApplyCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Apply nix configurations across workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "apply", func() error {
				return w.Apply(ctx, out, workspace.ApplyOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceFlakeCheckCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "flake-check",
		Short: "Run nix flake check on each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "flake-check", func() error {
				return w.FlakeCheck(ctx, out, errOut, workspace.FlakeCheckOptions{Terminal: *terminal})
			})
		},
	}
}

func workspacePreCommitCheckCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "pre-commit-check",
		Short: "Run pre-commit checks on each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "pre-commit-check", func() error {
				return w.PreCommitCheck(ctx, out, errOut, workspace.PreCommitCheckOptions{Terminal: *terminal})
			})
		},
	}
}

func workspacePushCmd(terminal *string) *cobra.Command {
	var setUpstream bool
	var remoteFlag string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Git push each workspace repo",
		Long: `Git push each workspace repo.

For repos that already have a configured upstream, runs plain 'git push'.
For repos with no upstream, the --set-upstream/-u flag is required; pn then
resolves the push remote via this convention chain (highest priority first):

  1. --remote <name>  Explicit override (applies to every repo).
  2. Single-remote    If the repo has exactly one remote, use it.
  3. branch.<branch>.pushRemote  Per-branch git config.
  4. remote.pushDefault (local)  Repo-local git config.
  5. remote.pushDefault (global) User-global git config.
  6. "origin"         If present among the repo's remotes.
  7. Per-repo error   Skips the repo; continues the push loop.

To configure a default push remote for a multi-remote repo:
  git -C <repo> config remote.pushDefault <name>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "push", func() error {
				return w.Push(ctx, out, errOut, workspace.PushOptions{
					Terminal:    *terminal,
					SetUpstream: setUpstream,
					Remote:      remoteFlag,
				})
			})
		},
	}
	cmd.Flags().BoolVarP(&setUpstream, "set-upstream", "u", false, "push with -u <remote> <branch> for repos that have no upstream yet; remote is resolved via convention chain")
	cmd.Flags().StringVar(&remoteFlag, "remote", "", "override remote name for all repos when --set-upstream is set (skip repo if remote absent)")
	return cmd
}

func workspaceRebaseCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "rebase [branch]",
		Short: "Git rebase each workspace repo",
		Long: `Git rebase each workspace repo.

Without [branch]: fetches and runs 'git pull --rebase --autostash' in each
repo that has a configured upstream. Repos without an upstream are skipped.

With [branch]: runs 'git rebase --autostash <branch>' in each repo using the
given local ref (branch name, remote-tracking ref, etc.). No fetch is
performed. Repos where the ref does not resolve are skipped with a notice.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			opts := workspace.RebaseOptions{Terminal: *terminal}
			if len(args) == 1 {
				opts.Onto = args[0]
			}
			return runWithHooks(ctx, w, "rebase", func() error {
				return w.Rebase(ctx, out, errOut, opts)
			})
		},
	}
}

func workspaceFormatCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "format",
		Short: "Run `nix fmt` in each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "format", func() error {
				return w.Format(ctx, out, errOut, workspace.FormatOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceTreeCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "tree",
		Short: "Print the workspace repo tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			return runWithHooks(ctx, w, "tree", func() error {
				return w.Tree(ctx, cmd.OutOrStdout(), workspace.TreeOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceUpdateCmd(terminal *string) *cobra.Command {
	var inPlace bool
	var siblingsOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update each workspace repo (worktree-isolated; --in-place for direct-on-main, --siblings-only to relock just sibling inputs)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			lw, err := eventlog.New(eventlog.DefaultPath())
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "pn: event log unavailable: %v\n", err)
			} else {
				defer func() { _ = lw.Close() }()
			}

			return runWithHooks(ctx, w, "update", func() error {
				return w.Update(ctx, out, workspace.UpdateOptions{Terminal: *terminal, Log: lw, InPlace: inPlace, SiblingsOnly: siblingsOnly})
			})
		},
	}
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "update each repo directly on its primary main instead of in an isolated worktree")
	cmd.Flags().BoolVar(&siblingsOnly, "siblings-only", false, "relock only the workspace-sibling flake inputs (skip update-locks.sh; leaves nixpkgs and other third-party inputs untouched)")
	return cmd
}

func workspaceUpgradeCmd(terminal *string) *cobra.Command {
	var inPlace bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update + apply each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "upgrade", func() error {
				return w.Upgrade(ctx, out, workspace.UpgradeOptions{Terminal: *terminal, InPlace: inPlace})
			})
		},
	}
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "update phase runs directly on primary main instead of in an isolated worktree")
	return cmd
}

func workspaceDiscoverCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			repos, err := w.Discover(workspace.DiscoverOptions{Terminal: *terminal})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, r := range repos {
				fmt.Fprintf(out, "%s\t%s\t%s\n", r.Name, r.URL, r.Path)
			}
			return nil
		},
	}
}

func workspaceInfoCmd(_ *string) *cobra.Command {
	var infoJSON bool
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show the workspace identity and per-repo applied state",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			info, err := w.Info(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if infoJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Fprintf(out, "wsid:     %s\n", info.Wsid)
			fmt.Fprintf(out, "root:     %s\n", info.Root)
			fmt.Fprintf(out, "terminal: %s\n", info.Terminal)
			for _, r := range info.Repos {
				applied := r.AppliedRef
				if applied == "" {
					applied = "(none)"
				}
				dirty := ""
				if r.Dirty {
					dirty = " (dirty)"
				}
				fmt.Fprintf(out, "  %s\t%s\t%s%s\n", r.Name, r.Path, applied, dirty)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&infoJSON, "json", false, "JSON output")
	return cmd
}

func workspaceDoctorCmd(terminal *string) *cobra.Command {
	var fix, dryRun, offline, jsonOut, strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Audit (and optionally repair) workspace drift",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && !fix {
				return fmt.Errorf("--dry-run requires --fix")
			}
			root, err := resolveWorkspaceRoot("")
			if err != nil {
				return err
			}
			_ = os.Setenv("PN_WORKSPACE_ROOT", root)
			_ = os.Setenv("WORKSPACE_ROOT", root)

			opts := workspace.DoctorOptions{
				Fix: fix, DryRun: dryRun, Offline: offline, JSON: jsonOut,
				Strict: strict, Terminal: *terminal,
			}
			report, derr := workspace.Doctor(cmd.Context(), root, exec.NewRealRunner(), opts)
			if derr != nil {
				// doctor itself failed -> exit 2
				return ExitCodeError{Code: 2, Msg: derr.Error()}
			}
			if rerr := workspace.RenderDoctor(cmd.OutOrStdout(), report, opts); rerr != nil {
				return rerr
			}
			if code := report.ExitCode(strict); code != 0 {
				return ExitCodeError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe fixes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --fix: print the fix plan, change nothing")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip remote-dependent checks")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit findings as JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors for the exit code")
	return cmd
}

func workspaceNixCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "nix [-- <nix args>]",
		Short:              "Run nix with --override-input pinned to local workspace clones",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close() // not a hookable verb, so not covered by runWithHooks (bead pg2-oewgp)
			return w.NixCommand(cmd.Context(), cmd.OutOrStdout(), args)
		},
	}
}

func workspaceCloneCmd(terminal *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone",
		Short: "Clone repos listed in pn-workspace.toml that are missing on disk",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "clone", func() error {
				return w.Clone(ctx, out, workspace.CloneOptions{Terminal: *terminal})
			})
		},
	}
	return cmd
}

// workspaceWorkforestCmd returns the `pn workspace workforest` parent command with
// add/list/remove/prune subcommands. These are scaffolding-only commands and
// are NOT wired through runWithHooks (they are not hookable event commands).
func workspaceWorkforestCmd() *cobra.Command {
	wt := &cobra.Command{
		Use:   "workforest",
		Short: "Manage coordinated workforest sets",
	}
	wt.AddCommand(workspaceWorkforestAddCmd())
	wt.AddCommand(workspaceWorkforestAddRepoCmd())
	wt.AddCommand(workspaceWorkforestRemoveRepoCmd())
	wt.AddCommand(workspaceWorkforestListCmd())
	wt.AddCommand(workspaceWorkforestRemoveCmd())
	wt.AddCommand(workspaceWorkforestPruneCmd())
	return wt
}

func workspaceWorkforestAddCmd() *cobra.Command {
	var repos []string
	cmd := &cobra.Command{
		Use:   "add <branch> [<commit-ish>]",
		Short: "Create a coordinated workforest set on <branch> (all repos, or a subset via --repos)",
		Long: `Create a coordinated workforest set on <branch>.

Without --repos the set contains every repo in pn-workspace.toml. With --repos
the set contains only the named subset; the set's own pn-workspace.toml records
that membership (the canonical config is untouched). A workspace dependency that
is excluded from the subset resolves against its locked flake input rather than a
set-internal override, and a notice names each such consumer->dependency edge.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			opts := workspace.WorkforestAddOptions{
				Branch: args[0],
				Repos:  repos,
			}
			if len(args) == 2 {
				opts.CommitIsh = args[1]
			}
			return w.WorkforestAdd(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "subset of repo keys to include (comma-separated or repeated); default: all repos")
	return cmd
}

func workspaceWorkforestAddRepoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-repo <branch> <repo>",
		Short: "Add a single repo to an existing coordinated workforest set",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			return w.WorkforestAddRepo(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), workspace.WorkforestAddRepoOptions{
				Branch: args[0],
				Repo:   args[1],
			})
		},
	}
}

func workspaceWorkforestRemoveRepoCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "remove-repo <branch> <repo>",
		Aliases: []string{"rm-repo"},
		Short:   "Remove a single repo from an existing coordinated workforest set (does NOT delete the branch)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			return w.WorkforestRemoveRepo(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), workspace.WorkforestRemoveRepoOptions{
				Branch: args[0],
				Repo:   args[1],
				Force:  force,
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "force removal even if the worktree is dirty or locked")
	return cmd
}

func workspaceWorkforestListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List coordinated workforest sets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			return w.WorkforestList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), workspace.WorkforestListOptions{})
		},
	}
}

func workspaceWorkforestRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "remove <branch>",
		Aliases: []string{"rm"},
		Short:   "Remove a coordinated workforest set (mirrors git worktree remove; does NOT delete the branch)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			return w.WorkforestRemove(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), workspace.WorkforestRemoveOptions{
				Branch: args[0],
				Force:  force,
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "force removal even if worktrees are dirty or locked")
	return cmd
}

func workspaceWorkforestPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Run git worktree prune in each canonical repo (clear stale admin entries)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			return w.WorkforestPrune(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), workspace.WorkforestPruneOptions{})
		},
	}
}

func workspaceLockCmd(terminal *string) *cobra.Command {
	var allowMissingEdges bool
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Derive and write pn-workspace.lock.json",
		Long:  "Evaluate flake inputs, build edges, resolve terminal, and write pn-workspace.lock.json atomically. Exits non-zero and preserves any existing lock file if validation errors are found.",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "lock", func() error {
				if err := w.WriteDerivedLockTo(ctx, w.Root(), out, *terminal, allowMissingEdges); err != nil {
					fmt.Fprintln(errOut, err)
					return err
				}
				fmt.Fprintln(out, "pn-workspace.lock.json written")
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&allowMissingEdges, "allow-missing-edges", false,
		"write the lock even if a repo's flake inputs fail to evaluate (edges for that repo are omitted; risks a build resolving that sibling from its published locked input)")
	return cmd
}

// printDeclaredHooks echoes every [[hooks]] and [[repos.*.hooks]] run line so the
// operator reviews exactly what `pn workspace allow` will trust (bead pg2-oymai).
func printDeclaredHooks(out io.Writer, cfg *workspace.WorkspaceConfig) {
	any := false
	for _, h := range cfg.Hooks {
		for _, r := range h.Run {
			fmt.Fprintf(out, "  [workspace] when=%v run: %s\n", h.When, r)
			any = true
		}
	}
	keys := make([]string, 0, len(cfg.Repos))
	for k := range cfg.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, h := range cfg.Repos[k].Hooks {
			for _, r := range h.Run {
				fmt.Fprintf(out, "  [repos.%s] when=%v run: %s\n", k, h.When, r)
				any = true
			}
		}
	}
	if !any {
		fmt.Fprintln(out, "  (no hooks declared)")
	}
}

// workspaceAllowCmd trusts the resolved workspace root (TOFU) so its hooks may
// execute. It is intentionally NOT routed through runWithHooks — `allow` is not
// a hookable command, so no hook can fire for it. (bead pg2-oymai)
func workspaceAllowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "allow",
		Short: "Trust this workspace's pn-workspace.toml hooks (trust-on-first-use)",
		Long: "Record trust for the resolved workspace root so its [[hooks]] and\n" +
			"[[repos.*.hooks]] may execute. The declared hook commands are echoed for\n" +
			"review. Editing pn-workspace.toml re-blocks until you run this again. See ADR-0019.",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveWorkspaceRoot("")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if w, oerr := workspace.Open(root, exec.NewRealRunner()); oerr == nil {
				defer w.Close()
				fmt.Fprintf(out, "hooks declared in %s:\n", root)
				printDeclaredHooks(out, w.Config())
			}
			if err := trust.Allow(root); err != nil {
				return err
			}
			fmt.Fprintf(out, "trusted workspace hooks in %s\n", root)
			return nil
		},
	}
}

// workspaceDenyCmd revokes trust for the resolved workspace root. (bead pg2-oymai)
func workspaceDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny",
		Short: "Revoke trust for this workspace's hooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveWorkspaceRoot("")
			if err != nil {
				return err
			}
			if err := trust.Deny(root); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked hook trust for %s\n", root)
			return nil
		},
	}
}
