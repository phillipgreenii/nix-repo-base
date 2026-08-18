package pjira

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	out []byte
	err error
}

func (f fakeRunner) Run(context.Context, []string) ([]byte, error) { return f.out, f.err }

func tok(t *testing.T, cfg SecretConfig, r Runner) (string, error) {
	t.Helper()
	src, err := NewSecretSource(cfg, r)
	if err != nil {
		return "", err
	}
	return src.Token(context.Background())
}

func TestSecret_Env(t *testing.T) {
	t.Setenv("MY_TOK", "abc")
	got, err := tok(t, SecretConfig{Source: "env", EnvVar: "MY_TOK"}, nil)
	if err != nil || got != "abc" {
		t.Fatalf("env: got %q err %v", got, err)
	}
}

func TestSecret_File_TrimsNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(p, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tok(t, SecretConfig{Source: "file", Path: p}, nil)
	if err != nil || got != "secret" {
		t.Fatalf("file: got %q err %v", got, err)
	}
}

func TestSecret_Command_TrimsAndErrors(t *testing.T) {
	got, err := tok(t, SecretConfig{Source: "command", Command: []string{"x"}}, fakeRunner{out: []byte("tk\n")})
	if err != nil || got != "tk" {
		t.Fatalf("command ok: got %q err %v", got, err)
	}
	if _, err := tok(t, SecretConfig{Source: "command", Command: []string{"x"}}, fakeRunner{err: errors.New("exit 1")}); err == nil {
		t.Fatal("command non-zero exit must error")
	}
}

func TestSecret_UnknownSource(t *testing.T) {
	if _, err := NewSecretSource(SecretConfig{Source: "smtp"}, nil); err == nil {
		t.Fatal("unknown source must error")
	}
}

// TestSecret_EnvVarDefaultsToJIRAAPIToken pins the EnvVar DEFAULT fallback inside
// NewSecretSource. Both halves are needed: an EMPTY EnvVar must fall back to
// JIRA_API_TOKEN, and a CONFIGURED one must be honoured instead — otherwise a
// fallback that always fires would look correct.
func TestSecret_EnvVarDefaultsToJIRAAPIToken(t *testing.T) {
	t.Setenv("JIRA_API_TOKEN", "dummy-from-default-var")
	t.Setenv("PJIRA_TEST_EXPLICIT_VAR", "dummy-from-explicit-var")

	// An empty EnvVar under an explicit source=env.
	if got, err := tok(t, SecretConfig{Source: "env"}, nil); err != nil || got != "dummy-from-default-var" {
		t.Errorf("empty EnvVar must fall back to JIRA_API_TOKEN: got %q err %v", got, err)
	}
	// An entirely zero SecretConfig: source "" also routes to env.
	if got, err := tok(t, SecretConfig{}, nil); err != nil || got != "dummy-from-default-var" {
		t.Errorf("zero SecretConfig must resolve JIRA_API_TOKEN: got %q err %v", got, err)
	}
	// A configured EnvVar must win over the default.
	got, err := tok(t, SecretConfig{Source: "env", EnvVar: "PJIRA_TEST_EXPLICIT_VAR"}, nil)
	if err != nil || got != "dummy-from-explicit-var" {
		t.Errorf("configured EnvVar must be honoured, not defaulted: got %q err %v", got, err)
	}
}

// TestSecret_EmptyAndMissingGuards pins the guards that are this module's only
// defence against handing an EMPTY token to the Jira client. Without them pjira
// sends an empty credential and the failure surfaces as a confusing 401 far from
// the cause, so each row asserts the MESSAGE rather than merely a non-nil error —
// a guard that starts rejecting for the wrong reason is still a regression.
func TestSecret_EmptyAndMissingGuards(t *testing.T) {
	const unsetVar = "PJIRA_TEST_UNSET_VAR"
	const emptyVar = "PJIRA_TEST_EMPTY_VAR"
	cases := []struct {
		name    string
		setup   func(*testing.T)
		cfg     SecretConfig
		runner  Runner
		wantMsg string
	}{
		{
			name: "env var not set at all",
			setup: func(t *testing.T) {
				// t.Setenv registers the restore; Unsetenv then guarantees the
				// variable is genuinely absent for this test only.
				t.Setenv(unsetVar, "placeholder")
				if err := os.Unsetenv(unsetVar); err != nil {
					t.Fatal(err)
				}
			},
			cfg:     SecretConfig{Source: "env", EnvVar: unsetVar},
			wantMsg: "env " + unsetVar + " is empty",
		},
		{
			name:    "env var set to the empty string",
			setup:   func(t *testing.T) { t.Setenv(emptyVar, "") },
			cfg:     SecretConfig{Source: "env", EnvVar: emptyVar},
			wantMsg: "env " + emptyVar + " is empty",
		},
		{
			name:    "secret command emits only whitespace",
			cfg:     SecretConfig{Source: "command", Command: []string{"x"}},
			runner:  fakeRunner{out: []byte(" \t\r\n ")},
			wantMsg: "secret command produced an empty token",
		},
		{
			name:    "source=file with no path",
			cfg:     SecretConfig{Source: "file"},
			wantMsg: "secret source=file requires path",
		},
		{
			name:    "source=command with empty argv",
			cfg:     SecretConfig{Source: "command"},
			runner:  fakeRunner{out: []byte("dummy-token")},
			wantMsg: "secret source=command requires a non-empty command argv",
		},
		{
			name:    "source=command with a nil Runner",
			cfg:     SecretConfig{Source: "command", Command: []string{"x"}},
			runner:  nil,
			wantMsg: "secret source=command requires a Runner",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			got, err := tok(t, c.cfg, c.runner)
			if err == nil {
				t.Fatalf("want an error mentioning %q, got token %q and nil error", c.wantMsg, got)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error = %v, want it to mention %q", err, c.wantMsg)
			}
			if got != "" {
				t.Errorf("a rejected secret must yield an empty token, got %q", got)
			}
		})
	}
}

// TestSecret_File_MissingFileErrors pins the read-failure path of source=file:
// the os.ReadFile error is wrapped and returned rather than degrading to an
// empty token.
func TestSecret_File_MissingFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "absent")
	got, err := tok(t, SecretConfig{Source: "file", Path: p}, nil)
	if err == nil {
		t.Fatalf("a missing token file must error, got token %q", got)
	}
	if !strings.Contains(err.Error(), "read token file") {
		t.Errorf("error = %v, want it to mention %q", err, "read token file")
	}
}
