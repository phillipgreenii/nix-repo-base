package pjira

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
