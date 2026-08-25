// Package metrics exposes operational Prometheus metrics for pg-go-mutate-tui.
//
// Deliberately absent: any method for recording a survivor/kill count or a
// mutation score. This tool must never expose a mutant survivor/kill count as
// a metric, so no such method exists here — not even as an unused stub. See
// TestExpositionNeverContainsASurvivorOrKillMetric for the durable guard.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds the operational metrics for pg-go-mutate-tui and exposes
// them via a Prometheus exposition handler. It is backed by a private
// prometheus.Registry (not the global default registerer), so multiple
// Registry instances — e.g. one per test — never leak state into each other.
type Registry struct {
	registry *prometheus.Registry

	commandExecutionTotal *prometheus.CounterVec
	runResultTotal        *prometheus.CounterVec
	runDurationSeconds    prometheus.Histogram
	filesByStatus         *prometheus.GaugeVec
}

// New constructs a Registry with all metrics registered on a fresh, private
// prometheus.Registry.
func New() *Registry {
	reg := prometheus.NewRegistry()

	r := &Registry{
		registry: reg,
		commandExecutionTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "pg_go_mutate_tui_command_execution_total",
				Help: "Total number of pg-go-mutate command executions, by outcome.",
			},
			[]string{"outcome"},
		),
		runResultTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "pg_go_mutate_tui_run_result_total",
				Help: "Total number of completed runs, by result status.",
			},
			[]string{"status"},
		),
		runDurationSeconds: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: "pg_go_mutate_tui_run_duration_seconds",
				Help: "Duration of runs, in seconds.",
			},
		),
		filesByStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "pg_go_mutate_tui_files_by_status",
				Help: "Current number of files by project, package, and status.",
			},
			[]string{"project", "pkg", "status"},
		),
	}

	reg.MustRegister(
		r.commandExecutionTotal,
		r.runResultTotal,
		r.runDurationSeconds,
		r.filesByStatus,
	)

	return r
}

// RecordCommandExecution increments the command-execution counter for the
// given outcome (e.g. "succeeded", "failed").
func (r *Registry) RecordCommandExecution(outcome string) {
	r.commandExecutionTotal.WithLabelValues(outcome).Inc()
}

// RecordRunResult increments the run-result counter for the given status
// (e.g. "done", "cancelled").
func (r *Registry) RecordRunResult(status string) {
	r.runResultTotal.WithLabelValues(status).Inc()
}

// ObserveRunDuration records a run's duration.
func (r *Registry) ObserveRunDuration(d time.Duration) {
	r.runDurationSeconds.Observe(d.Seconds())
}

// SetFilesByStatus sets the current file count for the given project,
// package, and status.
func (r *Registry) SetFilesByStatus(project, pkg, status string, count int) {
	r.filesByStatus.WithLabelValues(project, pkg, status).Set(float64(count))
}

// Handler returns an http.Handler serving this Registry's metrics in
// Prometheus exposition format, suitable for mounting at /metrics.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}
