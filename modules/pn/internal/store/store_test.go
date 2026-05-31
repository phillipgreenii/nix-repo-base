package store

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNew_ExposesRunner(t *testing.T) {
	f := exec.NewFakeRunner()
	s := New(f)
	if s.Runner() != f {
		t.Fatalf("Runner() did not return the runner passed to New")
	}
}

func TestAudit_RunsStoreSizeOnly(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("du", []string{"-sh", "/nix/store"}, exec.Result{Stdout: []byte("12G\t/nix/store\n")}, nil)

	if err := New(f).Audit(context.Background(), AuditOptions{}); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "du" {
		t.Errorf("expected du call, got %q", calls[0].Name)
	}
}

func TestAudit_FullAddsDeadPathsEstimate(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("du", []string{"-sh", "/nix/store"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"store", "gc", "--dry-run"}, exec.Result{}, nil)

	if err := New(f).Audit(context.Background(), AuditOptions{Full: true}); err != nil {
		t.Fatalf("Audit(Full): %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[1].Name != "nix" || calls[1].Args[0] != "store" || calls[1].Args[1] != "gc" {
		t.Errorf("expected nix store gc call, got %v %v", calls[1].Name, calls[1].Args)
	}
}

func TestAudit_PropagatesStoreSizeError(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("du", []string{"-sh", "/nix/store"}, exec.Result{ExitCode: 1}, errors.New("boom"))

	err := New(f).Audit(context.Background(), AuditOptions{})
	if err == nil {
		t.Fatal("expected error from du failure; got nil")
	}
}

func TestDeepClean_DryRunUsesEstimateAndSkipsGC(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"store", "gc", "--dry-run"}, exec.Result{}, nil)

	if err := New(f).DeepClean(context.Background(), DeepCleanOptions{DryRun: true}); err != nil {
		t.Fatalf("DeepClean(DryRun): %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call in dry-run, got %d", len(calls))
	}
	if calls[0].Name != "nix" {
		t.Errorf("dry-run should call nix store gc; got %q", calls[0].Name)
	}
	for _, c := range calls {
		if c.Name == "sudo" {
			t.Errorf("dry-run must not invoke sudo; got %v", c)
		}
	}
}

func TestDeepClean_RunsSudoGC(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sudo", []string{"nix-store", "--gc"}, exec.Result{}, nil)

	if err := New(f).DeepClean(context.Background(), DeepCleanOptions{}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "sudo" || calls[0].Args[0] != "nix-store" || calls[0].Args[1] != "--gc" {
		t.Errorf("expected sudo nix-store --gc, got %v %v", calls[0].Name, calls[0].Args)
	}
}

func TestDeepClean_PropagatesGCError(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sudo", []string{"nix-store", "--gc"}, exec.Result{ExitCode: 1}, errors.New("boom"))

	err := New(f).DeepClean(context.Background(), DeepCleanOptions{})
	if err == nil {
		t.Fatal("expected error from GC failure; got nil")
	}
}
