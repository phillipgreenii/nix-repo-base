package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
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
	ws.AddCommand(workspaceNixCmd())
	parent.AddCommand(ws)
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
			ctx := context.Background()
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
			ctx := context.Background()
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
			ctx := context.Background()
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
			ctx := context.Background()
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
			ctx := context.Background()
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
			ctx := context.Background()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "pre-commit-check", func() error {
				return w.PreCommitCheck(ctx, out, errOut, workspace.PreCommitCheckOptions{Terminal: *terminal})
			})
		},
	}
}

func workspacePushCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Git push each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "push", func() error {
				return w.Push(ctx, out, errOut, workspace.PushOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceRebaseCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "rebase",
		Short: "Git rebase each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "rebase", func() error {
				return w.Rebase(ctx, out, errOut, workspace.RebaseOptions{Terminal: *terminal})
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
			ctx := context.Background()
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
			ctx := context.Background()
			return runWithHooks(ctx, w, "tree", func() error {
				return w.Tree(ctx, cmd.OutOrStdout(), workspace.TreeOptions{Terminal: *terminal})
			})
		},
	}
}

func workspaceUpdateCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update each workspace repo (pull + update locks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()

			// Best-effort: never fail the run because the event log can't open.
			// On error lw is the nil *Writer, whose Emit/Close are safe no-ops.
			lw, err := eventlog.New(eventlog.DefaultPath())
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "pn: event log unavailable: %v\n", err)
			} else {
				defer func() { _ = lw.Close() }()
			}

			return runWithHooks(ctx, w, "update", func() error {
				return w.Update(ctx, out, workspace.UpdateOptions{Terminal: *terminal, Log: lw})
			})
		},
	}
}

func workspaceUpgradeCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Update + apply each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "upgrade", func() error {
				return w.Upgrade(ctx, out, workspace.UpgradeOptions{Terminal: *terminal})
			})
		},
	}
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
			return w.NixCommand(context.Background(), args)
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
			ctx := context.Background()
			out := cmd.OutOrStdout()
			return w.Clone(ctx, out, workspace.CloneOptions{
				Terminal: *terminal,
			})
		},
	}
	return cmd
}

func workspaceLockCmd(terminal *string) *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Derive and write pn-workspace.lock.json",
		Long:  "Evaluate flake inputs, build edges, resolve terminal, and write pn-workspace.lock.json atomically. Exits non-zero and preserves any existing lock file if validation errors are found.",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			return runWithHooks(ctx, w, "lock", func() error {
				if err := w.WriteDerivedLockTo(ctx, w.Root(), out, *terminal); err != nil {
					fmt.Fprintln(errOut, err)
					return err
				}
				fmt.Fprintln(out, "pn-workspace.lock.json written")
				return nil
			})
		},
	}
}
