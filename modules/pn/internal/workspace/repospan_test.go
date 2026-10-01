package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/codes"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

func TestInRepoSpan_EndsWithFnError(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)

	if err := inRepoSpan(ctx, "ok-repo", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := inRepoSpan(ctx, "bad-repo", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	spans := rec.ByName("pn.repo")
	if len(spans) != 2 {
		t.Fatalf("pn.repo spans = %d, want 2", len(spans))
	}
	if spans[0].Status().Code == codes.Error {
		t.Error("ok repo span marked Error")
	}
	if spans[1].Status().Code != codes.Error {
		t.Error("failing repo span not marked Error")
	}
}

// ctxRecordingRunner records the context values it is handed.
type ctxRecordingRunner struct {
	mu   sync.Mutex
	seen []context.Context
}

func (r *ctxRecordingRunner) Run(ctx context.Context, _ string, _ []string, _ exec.RunOptions) (exec.Result, error) {
	r.mu.Lock()
	r.seen = append(r.seen, ctx)
	r.mu.Unlock()
	return exec.Result{}, errors.New("not scripted")
}

// The WorkerPool fan-out MUST run on the caller's (cmd) context so cancellation
// and the active trace reach every job.
func TestDiscover_PoolJobsRunOnCallerContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.a]\nurl = \"github:o/a\"\n\n[repos.b]\nurl = \"github:o/b\"\n")
	for _, n := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rr := &ctxRecordingRunner{}
	w, err := Open(root, rr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)

	tel, rec := telemetrytest.New()
	type marker struct{}
	ctx := context.WithValue(telemetry.WithTelemetry(context.Background(), tel), marker{}, "cmd-ctx")
	ctx, verb := tel.StartVerb(ctx, "workspace discover")
	_, _ = w.Discover(ctx, DiscoverOptions{})
	verb.End(0, nil)

	if len(rr.seen) == 0 {
		t.Fatal("runner never called")
	}
	for _, c := range rr.seen {
		if c.Value(marker{}) != "cmd-ctx" {
			t.Error("a pool job ran on a context that is not derived from the caller's")
		}
	}
	if got := len(rec.ByName("pn.repo")); got != 2 {
		t.Errorf("pn.repo spans = %d, want 2 (one per repo in the pool fan-out)", got)
	}
}
