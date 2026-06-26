// Command jira is a generic, tenant-agnostic Atlassian Jira access tool.
// It hard-codes no tenant, credential location, or OS-specific behavior;
// all of those are supplied as configuration (see modules/jira/README.md).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/jira/pkg/jira"
	"github.com/spf13/cobra"
)

// osRunner backs the command secret source in production (exec'd directly, no shell).
type osRunner struct{}

func (osRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}

func defaultConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "jira", "config.toml")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "jira", "config.toml")
	}
	return ""
}

// resolveConfig composes precedence defaults -> file -> env -> flags.
func resolveConfig(cmd *cobra.Command) (jira.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = defaultConfigPath()
	}
	fileCfg, err := jira.LoadFile(path)
	if err != nil {
		return jira.Config{}, err
	}
	envCfg := jira.Config{
		BaseURL: os.Getenv("JIRA_BASE_URL"),
		Email:   os.Getenv("JIRA_EMAIL"),
	}
	cfg := jira.DefaultConfig().Merge(fileCfg).Merge(envCfg)
	if cfg.BaseURL == "" {
		return jira.Config{}, fmt.Errorf("jira: base_url not configured (set JIRA_BASE_URL, --config, or config file)")
	}
	if cfg.Email == "" {
		return jira.Config{}, fmt.Errorf("jira: email not configured (set JIRA_EMAIL, --config, or config file)")
	}
	return cfg, nil
}

func newClient(cmd *cobra.Command) (*jira.Client, jira.Config, error) {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return nil, cfg, err
	}
	src, err := jira.NewSecretSource(cfg.Secret, osRunner{})
	if err != nil {
		return nil, cfg, err
	}
	token, err := src.Token(cmd.Context())
	if err != nil {
		return nil, cfg, err
	}
	return jira.NewClient(cfg.BaseURL, cfg.Email, token), cfg, nil
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newIssueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "issue <KEY>",
		Short: "Fetch one issue as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := newClient(cmd)
			if err != nil {
				return err
			}
			iss, err := c.GetIssue(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd, iss)
		},
	}
}

func newSearchCmd() *cobra.Command {
	var jql, expand string
	var limit int
	c := &cobra.Command{
		Use:   "search",
		Short: "JQL search; writes {items,truncated} JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(jql) == "" {
				return fmt.Errorf("jira search: --jql is required")
			}
			cl, cfg, err := newClient(cmd)
			if err != nil {
				return err
			}
			if limit == 0 {
				limit = cfg.DefaultLimit
			}
			var exp jira.ExpandOpts
			for _, e := range strings.Split(expand, ",") {
				switch strings.TrimSpace(e) {
				case "changelog":
					exp.Changelog = true
				case "comments":
					exp.Comments = true
				}
			}
			res, err := cl.Search(cmd.Context(), jql, limit, exp)
			if err != nil {
				return err
			}
			return writeJSON(cmd, res)
		},
	}
	c.Flags().StringVar(&jql, "jql", "", "JQL query (required)")
	c.Flags().IntVar(&limit, "limit", 0, "max results (0 = config default)")
	c.Flags().StringVar(&expand, "expand", "", "comma-separated: changelog,comments")
	return c
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "auth-status",
		Short: "Check credential validity",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			src, err := jira.NewSecretSource(cfg.Secret, osRunner{})
			if err != nil {
				return err
			}
			token, terr := src.Token(cmd.Context())
			if terr != nil || token == "" {
				fmt.Fprintln(cmd.OutOrStdout(), jira.AuthMissing)
				os.Exit(3)
			}
			state, _ := jira.NewClient(cfg.BaseURL, cfg.Email, token).AuthStatus(cmd.Context())
			fmt.Fprintln(cmd.OutOrStdout(), state)
			switch state {
			case jira.AuthOK:
				return nil
			case jira.AuthForbidden:
				os.Exit(4)
			case jira.AuthUnauthenticated:
				os.Exit(5)
			default:
				os.Exit(1)
			}
			return nil
		},
	}
}

// NewRootCmd builds the jira CLI root with all subcommands attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "jira",
		Short:         "Generic Atlassian Jira access tool (issue / search / auth-status)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config TOML (default: $XDG_CONFIG_HOME/jira/config.toml)")
	root.AddCommand(newIssueCmd(), newSearchCmd(), newAuthStatusCmd())
	return root
}

func main() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
