package store

import (
	"context"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// The store verbs run `sudo nix-store --gc`, `sudo nix-env` and `nix store
// optimise` etc. None of these is one of the long-running call sites that
// exec.RunOptions.WrapNix is reserved for (pg2-kqrrs.8): the wrapper decorator
// must never rewrite them, so none may set the flag.
func TestStoreCalls_NeverSetWrapNix(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sudo", []string{"nix-store", "--gc", "--print-dead"}, exec.Result{}, nil)
	f.AddResponse("sudo", []string{"nix-env", "--profile", "/p", "--list-generations"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", "/p"}, exec.Result{}, nil)

	ctx := context.Background()
	deadPathsSize(ctx, f, false)
	_, _ = listGenerations(ctx, f, "/p", true, false)
	profileClosureSize(ctx, f, "/p")

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	for _, c := range calls {
		if c.Opts.WrapNix {
			t.Errorf("%s %v must not set WrapNix", c.Name, c.Args)
		}
	}
}
