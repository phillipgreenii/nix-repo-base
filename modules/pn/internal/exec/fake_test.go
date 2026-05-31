package exec

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunner_RecordsCalls(t *testing.T) {
	f := NewFakeRunner()
	f.AddResponse("git", []string{"status"}, Result{ExitCode: 0, Stdout: []byte("clean\n")}, nil)
	res, err := f.Run(context.Background(), "git", []string{"status"}, RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Stdout) != "clean\n" {
		t.Errorf("expected scripted stdout, got %q", string(res.Stdout))
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call recorded, got %d", len(calls))
	}
	if calls[0].Name != "git" {
		t.Errorf("expected call name git, got %q", calls[0].Name)
	}
}

func TestFakeRunner_ReturnsScriptedError(t *testing.T) {
	f := NewFakeRunner()
	want := errors.New("scripted failure")
	f.AddResponse("nix", []string{"build"}, Result{ExitCode: 1}, want)
	_, err := f.Run(context.Background(), "nix", []string{"build"}, RunOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected scripted error, got %v", err)
	}
}

func TestFakeRunner_UnmatchedCallFails(t *testing.T) {
	f := NewFakeRunner()
	_, err := f.Run(context.Background(), "unscripted", nil, RunOptions{})
	if err == nil {
		t.Error("expected error for unscripted call; got nil")
	}
}
