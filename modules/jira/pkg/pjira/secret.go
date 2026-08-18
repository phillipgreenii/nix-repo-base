package pjira

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Runner executes a command argv and returns its stdout. It is injectable so
// the command secret source is testable without spawning processes.
type Runner interface {
	Run(ctx context.Context, argv []string) (stdout []byte, err error)
}

// SecretConfig selects how the API token is resolved at runtime. The token
// value itself never lives in config.
type SecretConfig struct {
	Source  string   `toml:"source"`  // env | file | command
	EnvVar  string   `toml:"env_var"` // for source=env (default JIRA_API_TOKEN)
	Path    string   `toml:"path"`    // for source=file
	Command []string `toml:"command"` // for source=command (argv, exec'd directly — no shell)
}

// SecretSource resolves the API token at runtime.
type SecretSource interface {
	Token(ctx context.Context) (string, error)
}

// All three sources below share ONE empty-token contract: trim whitespace,
// then reject an empty result. Keeping the trim-then-reject order identical
// across env/file/command means a whitespace-only token is rejected the same
// way regardless of source, rather than only file and command catching it.

type envSecret struct{ varName string }

func (e envSecret) Token(context.Context) (string, error) {
	v := strings.TrimSpace(os.Getenv(e.varName))
	if v == "" {
		return "", fmt.Errorf("pjira: env %s is empty", e.varName)
	}
	return v, nil
}

type fileSecret struct{ path string }

func (f fileSecret) Token(context.Context) (string, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return "", fmt.Errorf("pjira: read token file: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("pjira: token file %s is empty", f.path)
	}
	return t, nil
}

type commandSecret struct {
	argv   []string
	runner Runner
}

func (c commandSecret) Token(ctx context.Context) (string, error) {
	out, err := c.runner.Run(ctx, c.argv)
	if err != nil {
		return "", fmt.Errorf("pjira: secret command failed: %w", err)
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", fmt.Errorf("pjira: secret command produced an empty token")
	}
	return t, nil
}

// NewSecretSource builds the configured source. runner is required only for
// source=command (the env/file sources ignore it).
func NewSecretSource(cfg SecretConfig, runner Runner) (SecretSource, error) {
	switch cfg.Source {
	case "", "env":
		v := cfg.EnvVar
		if v == "" {
			v = "JIRA_API_TOKEN"
		}
		return envSecret{varName: v}, nil
	case "file":
		if cfg.Path == "" {
			return nil, fmt.Errorf("pjira: secret source=file requires path")
		}
		return fileSecret{path: cfg.Path}, nil
	case "command":
		if len(cfg.Command) == 0 {
			return nil, fmt.Errorf("pjira: secret source=command requires a non-empty command argv")
		}
		if runner == nil {
			return nil, fmt.Errorf("pjira: secret source=command requires a Runner")
		}
		return commandSecret{argv: cfg.Command, runner: runner}, nil
	default:
		return nil, fmt.Errorf("pjira: unknown secret source %q", cfg.Source)
	}
}
