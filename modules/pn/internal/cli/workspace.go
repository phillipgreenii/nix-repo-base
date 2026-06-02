package cli

import (
	"context"
	"fmt"
	"os"

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

// openWorkspace finds the workspace root and opens it.
func openWorkspace() (*workspace.Workspace, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// TODO: walk up from cwd to find pn-workspace.toml. For now, require cwd to be the workspace root.
	return workspace.Open(root, exec.NewRealRunner())
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
			return w.Status(context.Background(), cmd.OutOrStdout())
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
			return w.Init(context.Background(), workspace.InitOptions{})
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
			return w.Build(context.Background(), cmd.OutOrStdout(), workspace.BuildOptions{})
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
			return w.Apply(context.Background(), workspace.ApplyOptions{})
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
			return w.FlakeCheck(context.Background(), workspace.FlakeCheckOptions{})
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
			return w.PreCommitCheck(context.Background(), workspace.PreCommitCheckOptions{})
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
			return w.Push(context.Background(), workspace.PushOptions{})
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
			return w.Rebase(context.Background(), workspace.RebaseOptions{})
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
			return w.Tree(context.Background(), cmd.OutOrStdout(), workspace.TreeOptions{})
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
			return w.Update(context.Background(), workspace.UpdateOptions{})
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
			return w.Upgrade(context.Background(), workspace.UpgradeOptions{})
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
			repos := w.Discover()
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
