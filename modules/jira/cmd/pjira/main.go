// Command pjira is a generic, tenant-agnostic Atlassian Jira access tool.
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

	"github.com/phillipgreenii/nix-repo-base/modules/jira/pkg/pjira"
	"github.com/spf13/cobra"
)

// osRunner backs the command secret source in production (exec'd directly, no shell).
type osRunner struct{}

func (osRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}

func defaultConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pjira", "config.toml")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "pjira", "config.toml")
	}
	return ""
}

// resolveConfig composes precedence defaults -> file -> env.
func resolveConfig(cmd *cobra.Command) (pjira.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = defaultConfigPath()
	}
	fileCfg, err := pjira.LoadFile(path)
	if err != nil {
		return pjira.Config{}, err
	}
	envCfg := pjira.Config{
		BaseURL: os.Getenv("JIRA_BASE_URL"),
		Email:   os.Getenv("JIRA_EMAIL"),
	}
	cfg := pjira.DefaultConfig().Merge(fileCfg).Merge(envCfg)
	if cfg.BaseURL == "" {
		return pjira.Config{}, fmt.Errorf("pjira: base_url not configured (set JIRA_BASE_URL, --config, or config file)")
	}
	if cfg.Email == "" {
		return pjira.Config{}, fmt.Errorf("pjira: email not configured (set JIRA_EMAIL, --config, or config file)")
	}
	return cfg, nil
}

func newClient(cmd *cobra.Command) (*pjira.Client, pjira.Config, error) {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return nil, cfg, err
	}
	src, err := pjira.NewSecretSource(cfg.Secret, osRunner{})
	if err != nil {
		return nil, cfg, err
	}
	token, err := src.Token(cmd.Context())
	if err != nil {
		return nil, cfg, err
	}
	return pjira.NewClient(cfg.BaseURL, cfg.Email, token), cfg, nil
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
	var jql, expand, cursor string
	var limit, maxPages int
	var all bool
	c := &cobra.Command{
		Use:   "search",
		Short: "JQL search; writes {items,truncated,next_page_token?} JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(jql) == "" {
				return fmt.Errorf("pjira search: --jql is required")
			}
			if all && strings.TrimSpace(cursor) != "" {
				return fmt.Errorf("pjira search: --all and --cursor are mutually exclusive")
			}
			cl, cfg, err := newClient(cmd)
			if err != nil {
				return err
			}
			if limit == 0 {
				limit = cfg.DefaultLimit
			}
			var exp pjira.ExpandOpts
			for _, e := range strings.Split(expand, ",") {
				switch strings.TrimSpace(e) {
				case "changelog":
					exp.Changelog = true
				case "comments":
					exp.Comments = true
				}
			}
			if all {
				res, err := cl.SearchAll(cmd.Context(), jql, limit, exp, maxPages)
				if err != nil {
					return err
				}
				if res.Truncated {
					fmt.Fprintf(cmd.ErrOrStderr(), "pjira search: result truncated at max-pages=%d (%d items returned; more remain)\n", maxPages, len(res.Items))
				}
				return writeJSON(cmd, res)
			}
			res, err := cl.SearchPage(cmd.Context(), jql, limit, exp, strings.TrimSpace(cursor))
			if err != nil {
				return err
			}
			return writeJSON(cmd, res)
		},
	}
	c.Flags().StringVar(&jql, "jql", "", "JQL query (required)")
	c.Flags().IntVar(&limit, "limit", 0, "max results per page (0 = config default)")
	c.Flags().StringVar(&expand, "expand", "", "comma-separated: changelog,comments")
	c.Flags().StringVar(&cursor, "cursor", "", "fetch the single page at this nextPageToken")
	c.Flags().BoolVar(&all, "all", false, "fetch all pages (loops nextPageToken to completeness)")
	c.Flags().IntVar(&maxPages, "max-pages", pjira.DefaultMaxSearchPages, "safety cap on pages fetched by --all")
	_ = c.Flags().MarkHidden("max-pages")
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
			src, err := pjira.NewSecretSource(cfg.Secret, osRunner{})
			if err != nil {
				return err
			}
			token, terr := src.Token(cmd.Context())
			if terr != nil || token == "" {
				fmt.Fprintln(cmd.OutOrStdout(), pjira.AuthMissing)
				os.Exit(3)
			}
			state, _ := pjira.NewClient(cfg.BaseURL, cfg.Email, token).AuthStatus(cmd.Context())
			fmt.Fprintln(cmd.OutOrStdout(), state)
			switch state {
			case pjira.AuthOK:
				return nil
			case pjira.AuthForbidden:
				os.Exit(4)
			case pjira.AuthUnauthenticated:
				os.Exit(5)
			default:
				os.Exit(1)
			}
			return nil
		},
	}
}

// NewRootCmd builds the pjira CLI root with all subcommands attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pjira",
		Short:         "Generic Atlassian Jira access tool (issue / search / auth-status)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config TOML (default: $XDG_CONFIG_HOME/pjira/config.toml)")
	root.AddCommand(newIssueCmd(), newSearchCmd(), newAuthStatusCmd())
	return root
}

func main() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
