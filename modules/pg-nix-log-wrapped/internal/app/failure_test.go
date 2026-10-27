package app

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/nixlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/testfix"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// invocationPoints returns the label sets of nix.invocation.duration.
func (m *mem) invocationPoints(t *testing.T) []map[string]string {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := m.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "nix.invocation.duration" {
				continue
			}
			for _, dp := range mt.Data.(metricdata.Histogram[float64]).DataPoints {
				l := map[string]string{}
				for _, kv := range dp.Attributes.ToSlice() {
					l[string(kv.Key)] = kv.Value.AsString()
				}
				out = append(out, l)
			}
		}
	}
	return out
}

// failureScenarios are the run shapes of the bead's acceptance criteria, each
// run through the real wrapper path with a fake nix that replays the stream.
var failureScenarios = []struct {
	name       string
	lines      []string
	exit, kill int
	wantExit   int
	errorType  string // "" = no error.type (success)
	kind       string // failure_kind label ("" = none: success)
}{
	{"hash mismatch plus dependent", testfix.HashMismatchRun(testfix.PlaceholderHash), 1, 0, 1, "hash_mismatch", "hash_mismatch"},
	{"flake-check eval error", testfix.EvalErrorRun(), 1, 0, 1, "eval_error", "eval_error"},
	{"killed by SIGTERM, exit 143", testfix.KilledRun(), 0, 15, 143, "signal", "signal"},
	{"interrupted by the user, exit 1", testfix.InterruptedRun(), 1, 0, 1, "signal", "signal"},
	{"non-zero exit with nothing known", []string{testfix.Plan(1)}, 3, 0, 3, "nonzero_exit", "other"},
	{"ignored eval-cache busy errors, exit 0", testfix.EvalCacheBusyRun(), 0, 0, 0, "", ""},
}

func TestFailureKindScenariosEndToEnd(t *testing.T) {
	for _, c := range failureScenarios {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, c.lines...), Exit: c.exit, KillSelf: c.kill})
			if code := r.run("fake-nix", "build"); code != c.wantExit {
				t.Fatalf("exit = %d, want %d", code, c.wantExit)
			}
			inv := r.mem.span(t, "nix.invocation")
			got, has := attrOf(inv, "error.type")
			if has != (c.errorType != "") || got.AsString() != c.errorType {
				t.Errorf("error.type = %q (found %v), want %q", got.AsString(), has, c.errorType)
			}
			if (inv.Status().Code == codes.Error) != (c.wantExit != 0) {
				t.Errorf("status = %v for exit %d", inv.Status(), c.wantExit)
			}
			pts := r.mem.invocationPoints(t)
			if len(pts) != 1 {
				t.Fatalf("invocation points = %v", pts)
			}
			k, hasK := pts[0]["failure_kind"]
			if hasK != (c.kind != "") || k != c.kind {
				t.Errorf("failure_kind = %q (found %v), want %q", k, hasK, c.kind)
			}
			if (pts[0]["status"] == "error") != (c.wantExit != 0) {
				t.Errorf("status label = %q", pts[0]["status"])
			}
		})
	}
}

// The produced error.type and failure_kind values are a CLOSED set: this
// fails on a value the docs do not list, so adding one is a deliberate change.
func TestProducedErrorTypesAndFailureKindsAreTheClosedSet(t *testing.T) {
	errTypes := map[string]bool{ErrTypeNonzeroExit: true}
	kinds := map[string]bool{}
	for _, k := range nixlog.FailureKinds {
		errTypes[k], kinds[k] = true, true
	}
	seenErr, seenKind := map[string]bool{}, map[string]bool{}
	for _, c := range failureScenarios {
		r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, c.lines...), Exit: c.exit, KillSelf: c.kill})
		r.run("fake-nix", "build")
		if v, ok := attrOf(r.mem.span(t, "nix.invocation"), "error.type"); ok {
			if !errTypes[v.AsString()] {
				t.Errorf("%s: unknown error.type %q", c.name, v.AsString())
			}
			seenErr[v.AsString()] = true
		}
		for _, p := range r.mem.invocationPoints(t) {
			if k, ok := p["failure_kind"]; ok {
				if !kinds[k] {
					t.Errorf("%s: unknown failure_kind %q", c.name, k)
				}
				seenKind[k] = true
			}
		}
		// Per-derivation spans keep their own (activity-level) error types.
		for _, s := range r.mem.spans.Ended() {
			if s.Name() != "nix.build" {
				continue
			}
			if v, ok := attrOf(s, "error.type"); ok && !map[string]bool{
				"build_failed": true, "dependency_failed": true, "hash_mismatch": true, "aborted": true,
			}[v.AsString()] {
				t.Errorf("%s: unknown nix.build error.type %q", c.name, v.AsString())
			}
		}
	}
	// The scenarios exercise most of the set (build_failed and
	// dependency_failed come from the existing failed-build goldens below).
	for _, want := range []string{"hash_mismatch", "eval_error", "signal", "nonzero_exit"} {
		if !seenErr[want] {
			t.Errorf("no scenario produced error.type %q", want)
		}
	}
	for _, want := range []string{"hash_mismatch", "eval_error", "signal", "other"} {
		if !seenKind[want] {
			t.Errorf("no scenario produced failure_kind %q", want)
		}
	}
}

func TestBuildFailedAndDependencyFailedGoldensSetRunKinds(t *testing.T) {
	for fixture, want := range map[string]string{
		"failed-build-keep-going.jsonl": "build_failed",
		"failed-build-dependency.jsonl": "build_failed", // the primary failure outranks the dependent's
	} {
		r := newRig(t, fakecmd.Spec{Fixture: golden(fixture), Exit: 1})
		if code := r.run("fake-nix", "build"); code != 1 {
			t.Fatalf("%s: exit %d", fixture, code)
		}
		if v, _ := attrOf(r.mem.span(t, "nix.invocation"), "error.type"); v.AsString() != want {
			t.Errorf("%s: error.type = %q, want %q", fixture, v.AsString(), want)
		}
		if p := r.mem.invocationPoints(t); len(p) != 1 || p[0]["failure_kind"] != want {
			t.Errorf("%s: points = %v", fixture, p)
		}
	}
	// Dependency failure alone (its primary failure message was lost).
	d := testfix.Drv(1, "top")
	r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, testfix.CannotBuild(d, testfix.Out(1, "top"), "1 dependency failed")), Exit: 1})
	r.run("fake-nix", "build")
	if v, _ := attrOf(r.mem.span(t, "nix.invocation"), "error.type"); v.AsString() != "dependency_failed" {
		t.Errorf("error.type = %v", v)
	}
}

func TestHashMismatchRunAttributesAndPlaceholderCountsAsFailed(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, testfix.HashMismatchRun(testfix.PlaceholderHash)...), Exit: 1})
	r.run("fake-nix", "build")
	inv := r.mem.span(t, "nix.invocation")
	msg, ok := attrOf(inv, "nix.error.message")
	if !ok || !strings.HasPrefix(msg.AsString(), "error: hash mismatch in fixed-output derivation '/nix/store/demo-fixture-1-deps.drv'") ||
		len([]rune(msg.AsString())) > nixlog.MaxErrorLen || strings.Contains(msg.AsString(), "\x1b") {
		t.Errorf("nix.error.message = %q (found %v)", msg.AsString(), ok)
	}
	drvs, ok := attrOf(inv, "nix.failed.drvs")
	if !ok || strings.Join(drvs.AsStringSlice(), ",") != "demo-fixture-1-deps,demo-fixture-1" {
		t.Errorf("nix.failed.drvs = %v (found %v)", drvs.AsStringSlice(), ok)
	}
	// Decision: a placeholder hash mismatch COUNTS as a failed derivation so
	// the metric stays honest; dashboards filter on the span attribute.
	var placeholder int
	for _, s := range r.mem.spans.Ended() {
		if v, ok := attrOf(s, "nix.hash_mismatch.placeholder"); ok && v.AsBool() {
			placeholder++
			if e, _ := attrOf(s, "error.type"); e.AsString() != "hash_mismatch" || s.Status().Code != codes.Error {
				t.Errorf("placeholder span = %v / %v", e, s.Status())
			}
		}
	}
	if placeholder != 1 {
		t.Errorf("placeholder spans = %d, want 1", placeholder)
	}
	if got := r.mem.counter(t, "failed"); got != 2 {
		t.Errorf("failed derivations = %d, want 2 (the mismatch and its dependent)", got)
	}
	if got := r.mem.counter(t, "built"); got != 0 {
		t.Errorf("built derivations = %d, want 0 (a mismatch is not a success)", got)
	}
}

func TestEvalCacheAndDBBusyAttributes(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, testfix.EvalCacheBusyRun()...)})
	if code := r.run("fake-nix", "flake", "check"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	inv := r.mem.span(t, "nix.invocation")
	for key, want := range map[string]int64{"nix.ignored.eval_cache_busy": 3, "nix.db_busy": 2} {
		if v, ok := attrOf(inv, key); !ok || v.AsInt64() != want {
			t.Errorf("%s = %v (found %v), want %d", key, v, ok, want)
		}
	}
	for _, key := range []string{"nix.error.message", "nix.failed.drvs", "error.type", "nix.build.wait.count"} {
		if _, ok := attrOf(inv, key); ok {
			t.Errorf("%s must be absent on an ignored-errors-only success", key)
		}
	}
}

func TestInterruptedRunKeepsFirstErrorAndSignalKind(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, testfix.InterruptedRun()...), Exit: 1})
	r.run("fake-nix", "build")
	inv := r.mem.span(t, "nix.invocation")
	if v, _ := attrOf(inv, "nix.error.message"); v.AsString() != "error: attribute 'missing' missing" {
		t.Errorf("nix.error.message = %q", v.AsString())
	}
	if _, ok := attrOf(inv, "nix.failed.drvs"); ok {
		t.Error("an interrupted run names no failed drv")
	}
}

func TestKilledRunHasNoErrorMessage(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, testfix.KilledRun()...), KillSelf: 15})
	r.run("fake-nix", "build")
	inv := r.mem.span(t, "nix.invocation")
	if _, ok := attrOf(inv, "nix.error.message"); ok {
		t.Error("a signal exit with no message sets no nix.error.message")
	}
}

func TestCredentialsInFirstErrorAreRedactedOnTheSpan(t *testing.T) {
	line := testfix.Msg(0, "\x1b[31;1merror:\x1b[0m unable to download 'https://user:token@host.example/a.tar.gz'")
	r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, line), Exit: 1})
	r.run("fake-nix", "build")
	v, _ := attrOf(r.mem.span(t, "nix.invocation"), "nix.error.message")
	if strings.Contains(v.AsString(), "token") || !strings.Contains(v.AsString(), "https://<redacted>@host.example/a.tar.gz") {
		t.Errorf("nix.error.message = %q", v.AsString())
	}
}

func TestWaitForLockGoldenSetsWaitCountAndNoMetric(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("wait-for-lock.jsonl")})
	if code := r.run("fake-nix", "build"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if v, ok := attrOf(r.mem.span(t, "nix.invocation"), "nix.build.wait.count"); !ok || v.AsInt64() != 1 {
		t.Errorf("nix.build.wait.count = %v (found %v)", v, ok)
	}
	for k := range r.mem.metricAttrKeys(t) {
		if strings.HasPrefix(k, "nix.") {
			t.Errorf("metric label %q", k)
		}
	}
}

// nix.error.message, nix.failed.drvs and the other diagnostics are span
// attributes only: a failing run carrying all of them labels no metric with
// any nix.* key, and failure_kind is the only new label.
func TestDiagnosticsAreNeverMetricLabels(t *testing.T) {
	for _, c := range failureScenarios {
		r := newRig(t, fakecmd.Spec{Fixture: writeFixture(t, c.lines...), Exit: c.exit, KillSelf: c.kill})
		r.run("fake-nix", "build")
		for k := range r.mem.metricAttrKeys(t) {
			switch k {
			case "outcome", "status", "command", "subcommand", "failure_kind":
			default:
				t.Errorf("%s: unexpected metric label %q", c.name, k)
			}
		}
	}
}
