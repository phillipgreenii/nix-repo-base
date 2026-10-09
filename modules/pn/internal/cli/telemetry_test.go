package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	pnexec "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// withTracedWorkspace stubs openWorkspace with a two-repo workspace (real git
// repos on disk) driven by a FakeRunner wrapped in the pn.exec decorator, so
// `workspace status` exercises pn.verb > pn.repo > pn.exec deterministically.
func withTracedWorkspace(t *testing.T) {
	t.Helper()
	// Hermetic sibling repos alpha and beta (x/gittest) under one workspace root.
	alpha := gittest.New(t, gitfixture.RepoOptions{Suite: "pn-telemetry", Name: "alpha"})
	beta := gittest.NewSibling(t, alpha, "beta", gitfixture.RepoOptions{})
	root := filepath.Dir(alpha.Dir)
	if filepath.Dir(beta.Dir) != root {
		t.Fatalf("alpha (%s) and beta (%s) do not share a workspace root", alpha.Dir, beta.Dir)
	}
	toml := "[repos.alpha]\nurl = \"github:o/alpha\"\n\n[repos.beta]\nurl = \"github:o/beta\"\n"
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	fr := pnexec.NewFakeRunner()
	for _, n := range []string{"alpha", "beta"} {
		fr.AddResponse("git", []string{"-C", filepath.Join(root, n), "worktree", "list", "--porcelain"},
			pnexec.Result{Stdout: []byte("worktree " + filepath.Join(root, n) + "\n\n")}, nil)
	}
	w, err := workspace.Open(root, pnexec.WithTracing(fr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return w, nil }
	t.Cleanup(func() { openWorkspace = orig })
}

func TestTelemetry_StatusProducesVerbRepoExecTrace(t *testing.T) {
	withTracedWorkspace(t)
	tel, rec := telemetrytest.New()

	var out, errBuf bytes.Buffer
	if err := executeWith("test", []string{"workspace", "status"}, &out, &errBuf, telemetrycfg.Resolution{}, tel); err != nil {
		t.Fatalf("status: %v (%s)", err, errBuf.String())
	}

	verbs := rec.ByName("pn.verb")
	if len(verbs) != 1 {
		t.Fatalf("pn.verb spans = %d, want 1", len(verbs))
	}
	if v := verbs[0].Attributes(); len(v) == 0 {
		t.Error("pn.verb has no attributes")
	}
	repos := rec.ByName("pn.repo")
	if len(repos) != 2 {
		t.Fatalf("pn.repo spans = %d, want 2", len(repos))
	}
	repoIDs := map[trace.SpanID]bool{}
	for _, r := range repos {
		if r.Parent().SpanID() != verbs[0].SpanContext().SpanID() {
			t.Errorf("pn.repo not a child of pn.verb")
		}
		repoIDs[r.SpanContext().SpanID()] = true
	}
	var execUnderRepo int
	for _, e := range rec.ByName("pn.exec") {
		if e.SpanContext().TraceID() != verbs[0].SpanContext().TraceID() {
			t.Error("pn.exec in a different trace")
		}
		if repoIDs[e.Parent().SpanID()] {
			execUnderRepo++
		}
	}
	if execUnderRepo < 2 {
		t.Errorf("pn.exec spans under a pn.repo = %d, want >= 2 (git worktree list per repo)", execUnderRepo)
	}
	// The root span ended exactly once and recorded the duration metric once.
	rm := rec.Metrics(t)
	h := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])
	if len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
		t.Errorf("duration datapoints = %+v, want one point with count 1", h.DataPoints)
	}
}

func TestTelemetry_FailingVerbEndsRootSpanWithError(t *testing.T) {
	tel, rec := telemetrytest.New()
	var out, errBuf bytes.Buffer
	// The removed-command stub fails in RunE; cobra then skips PostRunE, which
	// is exactly why the root span is ended by a defer in Execute instead.
	err := executeWith("test", []string{"workspace", "install-hooks"}, &out, &errBuf, telemetrycfg.Resolution{}, tel)
	if err == nil {
		t.Fatal("expected error")
	}
	verbs := rec.ByName("pn.verb")
	if len(verbs) != 1 {
		t.Fatalf("pn.verb spans = %d, want 1", len(verbs))
	}
	if verbs[0].Status().Code != codes.Error {
		t.Error("failed verb span is not Error")
	}
	var exit int64 = -1
	for _, kv := range verbs[0].Attributes() {
		if string(kv.Key) == "process.exit.code" {
			exit = kv.Value.AsInt64()
		}
	}
	if exit != int64(ExitCode(err)) {
		t.Errorf("process.exit.code = %d, want %d", exit, ExitCode(err))
	}
	h := rec.Metrics(t).ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])
	if v, _ := h.DataPoints[0].Attributes.Value("status"); v.AsString() != "error" {
		t.Errorf("status dim = %q, want error", v.AsString())
	}
}

// Commands that never reach PersistentPreRunE (--version) emit nothing.
func TestTelemetry_VersionEmitsNoSpans(t *testing.T) {
	tel, rec := telemetrytest.New()
	var out, errBuf bytes.Buffer
	if err := executeWith("test", []string{"--version"}, &out, &errBuf, telemetrycfg.Resolution{}, tel); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("spans = %d, want 0", n)
	}
}

// Telemetry on or off, the command's output is byte-identical.
func TestTelemetry_OutputByteIdenticalOnAndOff(t *testing.T) {
	run := func(tel *telemetry.Telemetry) (string, string) {
		withTracedWorkspace(t)
		var out, errBuf bytes.Buffer
		if err := executeWith("test", []string{"workspace", "status"}, &out, &errBuf, telemetrycfg.Resolution{}, tel); err != nil {
			t.Fatal(err)
		}
		return out.String(), errBuf.String()
	}
	offOut, offErr := run(nil)
	tel, _ := telemetrytest.New()
	onOut, onErr := run(tel)
	if offOut != onOut || offErr != onErr {
		t.Errorf("output differs with telemetry on:\noff=%q/%q\non=%q/%q", offOut, offErr, onOut, onErr)
	}
}

// With EnableTraverseRunHooks a child-level persistent hook MUST NOT replace
// the root hook that starts pn.verb.
func TestTelemetry_ChildPersistentHookDoesNotReplaceRoot(t *testing.T) {
	if !cobra.EnableTraverseRunHooks {
		t.Fatal("cobra.EnableTraverseRunHooks is not set")
	}
	tel, rec := telemetrytest.New()
	root := newRootCmd("test")
	var childSawSpan, runSawSpan bool
	child := &cobra.Command{
		Use: "probe",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			childSawSpan = trace.SpanFromContext(cmd.Context()).SpanContext().IsValid()
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			runSawSpan = trace.SpanFromContext(cmd.Context()).SpanContext().IsValid()
			return nil
		},
	}
	root.AddCommand(child)
	// Drive through executeWith's context plumbing by hand.
	state := &verbState{}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx = context.WithValue(ctx, verbStateKey{}, state)
	root.SetArgs([]string{"probe"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	state.span.End(0, nil)
	if !childSawSpan || !runSawSpan {
		t.Errorf("pn.verb not visible to child hook / RunE: child=%v run=%v", childSawSpan, runSawSpan)
	}
	if got := rec.ByName("pn.verb"); len(got) != 1 {
		t.Errorf("pn.verb spans = %d, want 1", len(got))
	}
}

// The root span's trace id reaches telemetrycfg.RunState, which feeds the
// stderr trace hint (pg2-kqrrs.6) -- the seam between the two phases.
func TestTelemetry_RootSpanPublishesTraceIDToRunStateAndHint(t *testing.T) {
	tel, rec := telemetrytest.New()
	var out, errBuf bytes.Buffer
	res := telemetrycfg.Resolution{Verbose: true}
	if err := executeWith("test", []string{"workspace", "install-hooks"}, &out, &errBuf, res, tel); err == nil {
		t.Fatal("expected error")
	}
	verbs := rec.ByName("pn.verb")
	if len(verbs) != 1 {
		t.Fatalf("pn.verb spans = %d, want 1", len(verbs))
	}
	want := "trace: " + verbs[0].SpanContext().TraceID().String() + "\n"
	if errBuf.String() != want {
		t.Errorf("stderr = %q, want %q", errBuf.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty (hint is stderr-only)", out.String())
	}
}

// With the no-op Telemetry there is no trace id, so no hint is printed even
// under -v.
func TestTelemetry_NoopTelemetryPrintsNoHint(t *testing.T) {
	var out, errBuf bytes.Buffer
	res := telemetrycfg.Resolution{Verbose: true}
	_ = executeWith("test", []string{"workspace", "install-hooks"}, &out, &errBuf, res, nil)
	if strings.Contains(errBuf.String(), "trace:") {
		t.Errorf("stderr = %q, want no trace hint", errBuf.String())
	}
}

func TestExecuteAndShutdown_ShutsDownOnceOnEveryPath(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"success", []string{"--version"}, false},
		{"failing verb", []string{"workspace", "install-hooks"}, true},
		{"parse error", []string{"--no-such-flag"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tel, rec := telemetrytest.New()
			var out, errBuf bytes.Buffer
			err := executeAndShutdown("test", c.args, &out, &errBuf, telemetrycfg.Resolution{}, tel)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if rec.ShutdownCount() != 1 {
				t.Errorf("shutdown count = %d, want 1", rec.ShutdownCount())
			}
		})
	}
}

// A Shutdown failure MUST NOT change the returned error (hence the exit code)
// nor leak onto stderr.
func TestExecuteAndShutdown_ShutdownFailureDoesNotChangeOutcome(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"workspace", "install-hooks"}} {
		okTel, _ := telemetrytest.New()
		var o1, e1 bytes.Buffer
		wantErr := executeAndShutdown("test", args, &o1, &e1, telemetrycfg.Resolution{}, okTel)

		tel, rec := telemetrytest.New()
		rec.ShutdownErr = errors.New("collector exploded")
		var o2, e2 bytes.Buffer
		gotErr := executeAndShutdown("test", args, &o2, &e2, telemetrycfg.Resolution{}, tel)
		if (gotErr == nil) != (wantErr == nil) || ExitCode(gotErr) != ExitCode(wantErr) {
			t.Errorf("args=%v: err = %v (exit %d), want %v (exit %d)", args, gotErr, ExitCode(gotErr), wantErr, ExitCode(wantErr))
		}
		if o1.String() != o2.String() || e1.String() != e2.String() {
			t.Errorf("args=%v: output changed by shutdown failure", args)
		}
		if strings.Contains(e2.String(), "collector exploded") {
			t.Errorf("shutdown failure leaked to stderr: %q", e2.String())
		}
	}
}
