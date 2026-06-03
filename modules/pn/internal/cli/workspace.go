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
	ws.AddCommand(workspaceLockCmd())
	ws.AddCommand(workspaceUpdateCmd())
	ws.AddCommand(workspaceUpgradeCmd())
	ws.AddCommand(workspaceDiscoverCmd())
	ws.AddCommand(workspaceNixCmd())
	parent.AddCommand(ws)
}

// openWorkspace opens the workspace by walking up from cwd (or PN_WORKSPACE_ROOT).
func openWorkspace() (*workspace.Workspace, error) { return openWorkspaceRoot("") }

// openWorkspaceRoot opens the workspace rooted via resolveWorkspaceRoot(rootFlag).
func openWorkspaceRoot(rootFlag string) (*workspace.Workspace, error) {
	root, err := resolveWorkspaceRoot(rootFlag)
	if err != nil {
		return nil, err
	}
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
	var root, buildCmd string
	var overridePaths []string
	var showOnly bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the terminal flake with local workspace overrides",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspaceRoot(root)
			if err != nil {
				return err
			}
			ovr, err := workspace.ParseOverridePaths(overridePaths)
			if err != nil {
				return err
			}
			return w.Build(context.Background(), cmd.OutOrStdout(), workspace.BuildOptions{
				BuildCmd:            buildCmd,
				OverridePaths:       ovr,
				ShowNixCommandsOnly: showOnly,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "workspace root (default: PN_WORKSPACE_ROOT or walk up from cwd)")
	cmd.Flags().StringVar(&buildCmd, "build-cmd", "", "override build_command template")
	cmd.Flags().StringArrayVar(&overridePaths, "override-path", nil, "override a repo path: name=path (repeatable)")
	cmd.Flags().BoolVar(&showOnly, "show-nix-commands-only", false, "print commands without running")
	return cmd
}

func workspaceApplyCmd() *cobra.Command {
	var root, applyCmd string
	var overridePaths []string
	var showOnly, force bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply (activate) the terminal flake with local workspace overrides",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspaceRoot(root)
			if err != nil {
				return err
			}
			ovr, err := workspace.ParseOverridePaths(overridePaths)
			if err != nil {
				return err
			}
			return w.Apply(context.Background(), cmd.OutOrStdout(), workspace.ApplyOptions{
				ApplyCmd:            applyCmd,
				OverridePaths:       ovr,
				ShowNixCommandsOnly: showOnly,
				Force:               force,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "workspace root (default: PN_WORKSPACE_ROOT or walk up from cwd)")
	cmd.Flags().StringVar(&applyCmd, "apply-cmd", "", "override apply_command template")
	cmd.Flags().StringArrayVar(&overridePaths, "override-path", nil, "override a repo path: name=path (repeatable)")
	cmd.Flags().BoolVar(&showOnly, "show-nix-commands-only", false, "print commands without running")
	cmd.Flags().BoolVar(&force, "force", false, "always rebuild (bypass the unchanged-skip gate)")
	return cmd
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

func workspaceLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Regenerate pn-workspace.lock from each repo's declared flake inputs",
		Long: "Re-derive the workspace dependency DAG from the inputs declared in " +
			"each repo's flake.nix and write it to pn-workspace.lock. Performs no " +
			"clone or reconcile (unlike init), so it is safe to run any time.",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			if err := w.RefreshLock(context.Background()); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Wrote %s — dependency order:\n", workspace.LockFileName)
			for _, name := range w.Lock().Order {
				fmt.Fprintf(out, "  %s\n", name)
			}
			return nil
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
			return w.Upgrade(context.Background(), cmd.OutOrStdout(), workspace.UpgradeOptions{})
		},
	}
}

func workspaceDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List workspace repos in dependency order with their input names",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			repos, err := w.Discover(context.Background())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, r := range repos {
				inputName := r.InputName
				if inputName == "" {
					inputName = "(terminal)"
				}
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", r.Name, inputName, r.URL, r.Path)
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
