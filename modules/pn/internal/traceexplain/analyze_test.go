package traceexplain_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/traceexplain"
)

const testTraceID = "0123456789abcdef0123456789abcdef"

func loadSpans(t *testing.T, name string) []traceexplain.Span {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	spans, err := traceexplain.ParseTempoV2(data)
	if err != nil {
		t.Fatal(err)
	}
	return spans
}

func analyzeFixture(t *testing.T, name string, top int) traceexplain.Report {
	t.Helper()
	rep, err := traceexplain.Analyze(testTraceID, loadSpans(t, name), top, false)
	if err != nil {
		t.Fatalf("Analyze(%s): %v", name, err)
	}
	return rep
}

func repoByName(t *testing.T, rep traceexplain.Report, name string) traceexplain.RepoReport {
	t.Helper()
	for _, r := range rep.Repos {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no repo %q in %+v", name, rep.Repos)
	return traceexplain.RepoReport{}
}

func TestAnalyze_RepoBreakdown(t *testing.T) {
	a := repoByName(t, analyzeFixture(t, "update-two-repos.json", 5), "a")
	if a.WallSeconds != 600 || a.GitSeconds != 10 || a.RelockSeconds != 10 || a.UnattributedSeconds != 0 {
		t.Errorf("a wall/git/relock/unattributed = %v/%v/%v/%v, want 600/10/10/0",
			a.WallSeconds, a.GitSeconds, a.RelockSeconds, a.UnattributedSeconds)
	}
	ul := a.UpdateLocks
	if ul == nil {
		t.Fatal("a has no update_locks")
	}
	wantSteps := []traceexplain.Step{{Name: "nix-flake-update", Outcome: "ok", Seconds: 100}, {Name: "update-deps-x", Outcome: "ok", Seconds: 400}}
	if ul.Seconds != 580 || ul.SetupSeconds != 10 || ul.BetweenStepsSeconds != 10 || ul.FinalizeSeconds != 60 || ul.StepsMissing {
		t.Errorf("update_locks = %+v, want 580/setup 10/between 10/finalize 60", *ul)
	}
	if !reflect.DeepEqual(ul.Steps, wantSteps) {
		t.Errorf("steps = %+v, want %+v", ul.Steps, wantSteps)
	}
}

func TestAnalyze_RepoWithoutSteps(t *testing.T) {
	b := repoByName(t, analyzeFixture(t, "update-two-repos.json", 5), "b")
	if b.UpdateLocks == nil || !b.UpdateLocks.StepsMissing || len(b.UpdateLocks.Steps) != 0 || b.UpdateLocks.Seconds != 295 {
		t.Errorf("b update_locks = %+v, want steps_missing, no steps, 295 s", b.UpdateLocks)
	}
	if b.UnattributedSeconds != 100 {
		t.Errorf("b unattributed = %v, want 100 (400 - 5 git - 295 script)", b.UnattributedSeconds)
	}
}

func TestAnalyze_OutsideReposAndHooks(t *testing.T) {
	rep := analyzeFixture(t, "update-two-repos.json", 5)
	if rep.OutsideReposSeconds != 50 {
		t.Errorf("outside_repos_seconds = %v, want 50", rep.OutsideReposSeconds)
	}
	if want := []traceexplain.Hook{{When: "post", Name: "update", Seconds: 40}}; !reflect.DeepEqual(rep.Hooks, want) {
		t.Errorf("hooks = %+v, want %+v", rep.Hooks, want)
	}
	if rep.Verb != "workspace update" || rep.Scope != "full" || rep.WallSeconds != 1050 || rep.TraceID != testTraceID || rep.Incomplete {
		t.Errorf("header fields = %q %q %v %q %v", rep.Verb, rep.Scope, rep.WallSeconds, rep.TraceID, rep.Incomplete)
	}
	if want := time.Unix(1763467200, 0).UTC(); !rep.Start.Equal(want) {
		t.Errorf("start = %v, want %v", rep.Start, want)
	}
}

func TestAnalyze_TopBuildsOrderedAndStepTagged(t *testing.T) {
	rep := analyzeFixture(t, "update-two-repos.json", 2)
	want := []traceexplain.Build{
		{Drv: "slow-drv", Repo: "a", Step: "update-deps-x", Seconds: 300},
		{Drv: "fast-drv", Repo: "a", Step: "nix-flake-update", Seconds: 30},
	}
	if !reflect.DeepEqual(rep.TopBuilds, want) {
		t.Errorf("top_builds = %+v, want %+v", rep.TopBuilds, want)
	}
	if got := analyzeFixture(t, "update-two-repos.json", 1).TopBuilds; len(got) != 1 {
		t.Errorf("top=1 gave %d builds", len(got))
	}
}

const (
	anomalyB       = "nix.invocation develop in b ends 50s after its parent pn.exec (b's figures clamp it to its parent's end)"
	anomalyOrphans = "1 span(s) reference a parent missing from the trace (Tempo may have truncated it; those spans are left out of every figure)"
)

func TestAnalyze_Anomalies(t *testing.T) {
	rep := analyzeFixture(t, "update-two-repos.json", 5)
	if want := []string{anomalyB, anomalyOrphans}; !reflect.DeepEqual(rep.Anomalies, want) {
		t.Errorf("anomalies = %q, want %q", rep.Anomalies, want)
	}
}

func TestAnalyze_LockWaitSummed(t *testing.T) {
	if got := analyzeFixture(t, "update-two-repos.json", 5).LockWaitSecondsSummed; got != 40 {
		t.Errorf("lock_wait_seconds_summed = %v, want 40", got)
	}
}

func TestAnalyze_NixCallsAndSubstitutes(t *testing.T) {
	rep := analyzeFixture(t, "update-two-repos.json", 5)
	if got, want := repoByName(t, rep, "a").Nix, []traceexplain.NixCall{{Subcommand: "develop", Seconds: 576, PlanBuilds: 2, PlanFetches: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("a nix = %+v, want %+v", got, want)
	}
	if got, want := repoByName(t, rep, "b").Nix, []traceexplain.NixCall{{Subcommand: "develop", Seconds: 293}}; !reflect.DeepEqual(got, want) {
		t.Errorf("b nix = %+v, want %+v (clamped to 900 - 607)", got, want)
	}
	if rep.SubstituteSecondsSummed != 20 {
		t.Errorf("substitute_seconds_summed = %v, want 20", rep.SubstituteSecondsSummed)
	}
}

func TestAnalyze_IncompleteTrace(t *testing.T) {
	spans := loadSpans(t, "incomplete.json")
	if _, err := traceexplain.Analyze(testTraceID, spans, 5, false); !errors.Is(err, traceexplain.ErrIncompleteTrace) {
		t.Fatalf("err = %v, want ErrIncompleteTrace", err)
	}
	rep, err := traceexplain.Analyze(testTraceID, spans, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Incomplete || len(rep.Repos) != 1 || rep.Repos[0].Name != "a" || rep.Repos[0].WallSeconds != 600 {
		t.Errorf("partial report = incomplete %v repos %+v", rep.Incomplete, rep.Repos)
	}
}

func TestAnalyze_FlakeScopeRepo(t *testing.T) {
	rep := analyzeFixture(t, "flake-scope.json", 5)
	c := repoByName(t, rep, "c")
	if c.UpdateLocks != nil {
		t.Errorf("c update_locks = %+v, want nil", c.UpdateLocks)
	}
	if c.RelockSeconds != 90 || c.GitSeconds != 10 || c.UnattributedSeconds != 5 {
		t.Errorf("c relock/git/unattributed = %v/%v/%v, want 90/10/5", c.RelockSeconds, c.GitSeconds, c.UnattributedSeconds)
	}
	if want := []traceexplain.NixCall{{Subcommand: "flake update", Seconds: 82}}; !reflect.DeepEqual(c.Nix, want) {
		t.Errorf("c nix = %+v, want %+v", c.Nix, want)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["update_locks"]; ok {
		t.Error("update_locks key present for a repo without the script")
	}
	if len(rep.Anomalies) != 0 {
		t.Errorf("anomalies = %q, want none", rep.Anomalies)
	}
}

func TestAnalyze_SumsAndRecursiveClamp(t *testing.T) {
	rep := analyzeFixture(t, "sums-and-clamps.json", 5)
	if rep.SubstituteSecondsSummed != 25 || rep.LockWaitSecondsSummed != 15 {
		t.Errorf("substitute/lock-wait = %v/%v, want 25/15", rep.SubstituteSecondsSummed, rep.LockWaitSecondsSummed)
	}
	want := []traceexplain.Build{{Drv: "e-drv", Repo: "e", Seconds: 80}, {Drv: "late-drv", Repo: "d", Seconds: 19}}
	if !reflect.DeepEqual(rep.TopBuilds, want) {
		t.Errorf("top_builds = %+v, want %+v", rep.TopBuilds, want)
	}
	wantAnoms := []string{
		"nix.build late-drv in d ends 31s after its parent nix.invocation (d's figures clamp it to its parent's end)",
		"nix.invocation develop in e ends 9s after its parent pn.exec (e's figures clamp it to its parent's end)",
	}
	if !reflect.DeepEqual(rep.Anomalies, wantAnoms) {
		t.Errorf("anomalies = %q, want %q", rep.Anomalies, wantAnoms)
	}
	if got := repoByName(t, rep, "e").UnattributedSeconds; got != 10 {
		t.Errorf("e unattributed = %v, want 10", got)
	}
}

func span(id, parent, name string, start, end int, attrs map[string]string) traceexplain.Span {
	base := time.Unix(1763467200, 0).UTC()
	return traceexplain.Span{
		SpanID: id, ParentID: parent, Name: name,
		Start: base.Add(time.Duration(start) * time.Second), End: base.Add(time.Duration(end) * time.Second), Attrs: attrs,
	}
}

func TestAnalyze_NoRepoSpans(t *testing.T) {
	rep, err := traceexplain.Analyze(testTraceID, []traceexplain.Span{
		span("v", "", "pn.verb", 0, 10, map[string]string{"pn.verb": "workspace update"}),
	}, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Repos) != 0 || rep.OutsideReposSeconds != 10 {
		t.Errorf("repos %d outside %v, want 0 / 10", len(rep.Repos), rep.OutsideReposSeconds)
	}
}

func TestAnalyze_NestedRepo(t *testing.T) {
	rep, err := traceexplain.Analyze(testTraceID, []traceexplain.Span{
		span("v", "", "pn.verb", 0, 100, nil),
		span("o", "v", "pn.repo", 0, 100, map[string]string{"pn.repo": "outer"}),
		span("i", "o", "pn.repo", 10, 60, map[string]string{"pn.repo": "inner"}),
		span("x", "i", "pn.exec", 10, 20, map[string]string{"process.executable.name": "git"}),
	}, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	inner := repoByName(t, rep, "inner")
	if inner.WallSeconds != 50 || len(rep.Repos) != 2 {
		t.Errorf("inner = %+v, repos %d", inner, len(rep.Repos))
	}
	if outer := repoByName(t, rep, "outer"); outer.GitSeconds != 0 {
		t.Errorf("outer git = %v, want 0 (the git call belongs to inner)", outer.GitSeconds)
	}
	if want := []string{"pn.repo inner is nested inside pn.repo outer"}; !reflect.DeepEqual(rep.Anomalies, want) {
		t.Errorf("anomalies = %q, want %q", rep.Anomalies, want)
	}
}

func TestReportJSONKeys(t *testing.T) {
	rep := analyzeFixture(t, "update-two-repos.json", 5)
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	wantTop := []string{
		"anomalies", "hooks", "incomplete", "lock_wait_seconds_summed", "outside_repos_seconds", "repos",
		"scope", "start", "substitute_seconds_summed", "top_builds", "trace_id", "verb", "wall_seconds",
	}
	if got := keys(m); !reflect.DeepEqual(got, wantTop) {
		t.Errorf("top-level keys = %v, want %v", got, wantTop)
	}
	if m["start"] != "2025-11-18T12:00:00Z" {
		t.Errorf("start = %v, want RFC3339 UTC", m["start"])
	}
	repos := m["repos"].([]any)
	a := repos[0].(map[string]any)
	wantRepo := []string{"git_seconds", "name", "nix", "relock_seconds", "unattributed_seconds", "update_locks", "wall_seconds"}
	if got := keys(a); !reflect.DeepEqual(got, wantRepo) {
		t.Errorf("repo keys = %v, want %v", got, wantRepo)
	}
	ul := a["update_locks"].(map[string]any)
	wantUL := []string{"between_steps_seconds", "finalize_seconds", "seconds", "setup_seconds", "steps", "steps_missing"}
	if got := keys(ul); !reflect.DeepEqual(got, wantUL) {
		t.Errorf("update_locks keys = %v, want %v", got, wantUL)
	}
	if got := keys(ul["steps"].([]any)[0].(map[string]any)); !reflect.DeepEqual(got, []string{"name", "outcome", "seconds"}) {
		t.Errorf("step keys = %v", got)
	}
	if got := keys(a["nix"].([]any)[0].(map[string]any)); !reflect.DeepEqual(got, []string{"plan_builds", "plan_fetches", "seconds", "subcommand"}) {
		t.Errorf("nix keys = %v", got)
	}
	if got := keys(m["top_builds"].([]any)[0].(map[string]any)); !reflect.DeepEqual(got, []string{"drv", "repo", "seconds", "step"}) {
		t.Errorf("build keys = %v", got)
	}
	if got := keys(m["hooks"].([]any)[0].(map[string]any)); !reflect.DeepEqual(got, []string{"name", "seconds", "when"}) {
		t.Errorf("hook keys = %v", got)
	}
}

func TestReportJSONRoundsToTenthsOfASecond(t *testing.T) {
	rep := traceexplain.Report{
		WallSeconds: 12.34, LockWaitSecondsSummed: 0.06, SubstituteSecondsSummed: 1.04,
		Repos:     []traceexplain.RepoReport{{Name: "a", GitSeconds: 0.26, Nix: []traceexplain.NixCall{{Subcommand: "develop", Seconds: 2.949}}}},
		TopBuilds: []traceexplain.Build{{Drv: "d", Seconds: 7.25}},
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["wall_seconds"] != 12.3 || m["lock_wait_seconds_summed"] != 0.1 || m["substitute_seconds_summed"] != 1.0 {
		t.Errorf("top-level rounding = %v %v %v", m["wall_seconds"], m["lock_wait_seconds_summed"], m["substitute_seconds_summed"])
	}
	a := m["repos"].([]any)[0].(map[string]any)
	if a["git_seconds"] != 0.3 || a["nix"].([]any)[0].(map[string]any)["seconds"] != 2.9 {
		t.Errorf("repo rounding = %v", a)
	}
	if rep.WallSeconds != 12.34 {
		t.Errorf("Marshal mutated the report: %v", rep.WallSeconds)
	}
}

// spansFromRecorder is a test-only adapter from the SDK's recorded spans to
// the Span type Analyze reads, in the shape ParseTempoV2 would produce.
func spansFromRecorder(ended []sdktrace.ReadOnlySpan) []traceexplain.Span {
	out := make([]traceexplain.Span, 0, len(ended))
	for _, s := range ended {
		sp := traceexplain.Span{
			SpanID: s.SpanContext().SpanID().String(),
			Name:   s.Name(),
			Start:  s.StartTime(),
			End:    s.EndTime(),
			Attrs:  map[string]string{},
			Error:  s.Status().Code == codes.Error,
		}
		if s.Parent().SpanID().IsValid() {
			sp.ParentID = s.Parent().SpanID().String()
		}
		for _, kv := range s.Attributes() {
			sp.Attrs[string(kv.Key)] = attrText(kv.Value)
		}
		out = append(out, sp)
	}
	return out
}

func attrText(v attribute.Value) string { return v.String() }

func TestAnalyze_SpansFromTelemetryStarters(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)

	nowMS := time.Now().UnixMilli()
	ctx, verb := tel.StartVerb(ctx, "workspace update")
	telemetry.RecordUpdateScope(ctx, "flake")
	rctx, endRepo := telemetry.StartRepo(ctx, "repo-a")
	_, endScript := telemetry.StartExec(rctx, "./update-locks.sh")
	endScript(0, nil)
	telemetry.RecordULSteps(rctx, []telemetry.ULStep{
		{Name: "first-step", Outcome: "ok", StartMS: nowMS, MS: 1},
		{Name: "second-step", Outcome: "skipped", StartMS: nowMS + 1, MS: 1},
	})
	_, endRelock := telemetry.StartRelock(rctx, "flake")
	time.Sleep(2 * time.Millisecond)
	endRelock(0, nil)
	endRepo(nil)
	verb.End(0, nil)

	rep, err := traceexplain.Analyze(verb.TraceID(), spansFromRecorder(rec.Ended()), 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scope != "flake" || rep.Verb != "workspace update" {
		t.Errorf("verb/scope = %q/%q", rep.Verb, rep.Scope)
	}
	if len(rep.Repos) != 1 || rep.Repos[0].Name != "repo-a" {
		t.Fatalf("repos = %+v", rep.Repos)
	}
	r := rep.Repos[0]
	if r.RelockSeconds <= 0 {
		t.Errorf("relock_seconds = %v, want > 0", r.RelockSeconds)
	}
	if r.UpdateLocks == nil {
		t.Fatal("update_locks missing: the pn.exec executable attribute no longer matches ./update-locks.sh")
	}
	var names, outcomes []string
	for _, s := range r.UpdateLocks.Steps {
		names, outcomes = append(names, s.Name), append(outcomes, s.Outcome)
	}
	if !reflect.DeepEqual(names, []string{"first-step", "second-step"}) || !reflect.DeepEqual(outcomes, []string{"ok", "skipped"}) {
		t.Errorf("steps = %v / %v", names, outcomes)
	}
}
