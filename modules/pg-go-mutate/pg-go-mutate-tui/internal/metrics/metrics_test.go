package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecordedMetricsAppearInExposition(t *testing.T) {
	r := New()
	r.RecordCommandExecution("succeeded")
	r.RecordRunResult("done")
	r.ObserveRunDuration(2 * time.Second)
	r.SetFilesByStatus("proj", "pkg", "done", 5)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	body := w.Body.String()

	for _, want := range []string{
		"pg_go_mutate_tui_command_execution_total",
		"pg_go_mutate_tui_run_result_total",
		"pg_go_mutate_tui_run_duration_seconds",
		"pg_go_mutate_tui_files_by_status",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected exposition to contain %q, got:\n%s", want, body)
		}
	}
}

func TestExpositionNeverContainsASurvivorOrKillMetric(t *testing.T) {
	r := New()
	r.RecordCommandExecution("succeeded")
	r.RecordRunResult("done")
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	body := strings.ToLower(w.Body.String())
	for _, forbidden := range []string{"survivor", "survived", "killed", "mutation_score"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("exposition must never contain a score-shaped metric, found %q", forbidden)
		}
	}
}
