package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

func addWorkspaceCmd(parent *cobra.Command) {
	ws := &cobra.Command{
		Use:   "workspace",
		Short: "Operate on the pn workspace",
	}
	ws.AddCommand(workspaceStatusCmd())
	ws.AddCommand(workspaceInitCmd())
	ws.AddCommand(workspaceBuildCmd())
	ws.AddCommand(workspaceApplyCmd())
	ws.AddCommand(workspaceFlakeCheckCmd())
	ws.AddCommand(workspacePreCommitCheckCmd())
	ws.AddCommand(workspacePushCmd())
	ws.AddCommand(workspaceRebaseCmd())
	ws.AddCommand(workspaceTreeCmd())
	ws.AddCommand(workspaceUpdateCmd())
	ws.AddCommand(workspaceUpgradeCmd())
	ws.AddCommand(workspaceDiscoverCmd())
	ws.AddCommand(workspaceNixCmd())
	parent.AddCommand(ws)
}

// openWorkspace opens the workspace by walking up from cwd (or PN_WORKSPACE_ROOT).
func openWorkspace() (*workspace.Workspace, error) { return openWorkspaceRoot("") }

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

func workspaceStatusCmd() *cobra.Command {
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
				return w.Status(ctx, cmd.OutOrStdout())
			})
		},
	}
}

func workspaceInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Clone repos from pn-workspace.toml; reconcile existing; write lock",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "init", func() error {
				return w.Init(ctx, out, workspace.InitOptions{})
			})
		},
	}
}

func workspaceBuildCmd() *cobra.Command {
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
				return w.Build(ctx, out, workspace.BuildOptions{})
			})
		},
	}
}

func workspaceApplyCmd() *cobra.Command {
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
				return w.Apply(ctx, out, workspace.ApplyOptions{})
			})
		},
	}
}

func workspaceFlakeCheckCmd() *cobra.Command {
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
			return runWithHooks(ctx, w, "flake-check", func() error {
				return w.FlakeCheck(ctx, out, workspace.FlakeCheckOptions{})
			})
		},
	}
}

func workspacePreCommitCheckCmd() *cobra.Command {
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
			return runWithHooks(ctx, w, "pre-commit-check", func() error {
				return w.PreCommitCheck(ctx, out, workspace.PreCommitCheckOptions{})
			})
		},
	}
}

func workspacePushCmd() *cobra.Command {
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
			return runWithHooks(ctx, w, "push", func() error {
				return w.Push(ctx, out, workspace.PushOptions{})
			})
		},
	}
}

func workspaceRebaseCmd() *cobra.Command {
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
			return runWithHooks(ctx, w, "rebase", func() error {
				return w.Rebase(ctx, out, workspace.RebaseOptions{})
			})
		},
	}
}

func workspaceTreeCmd() *cobra.Command {
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
				return w.Tree(ctx, cmd.OutOrStdout(), workspace.TreeOptions{})
			})
		},
	}
}

func workspaceUpdateCmd() *cobra.Command {
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
			return runWithHooks(ctx, w, "update", func() error {
				return w.Update(ctx, out, workspace.UpdateOptions{})
			})
		},
	}
}

func workspaceUpgradeCmd() *cobra.Command {
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
				return w.Upgrade(ctx, out, workspace.UpgradeOptions{})
			})
		},
	}
}

func workspaceDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			repos, err := w.Discover()
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
