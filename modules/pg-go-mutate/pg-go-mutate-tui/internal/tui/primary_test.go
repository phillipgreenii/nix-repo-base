package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPrimaryShowsQueueDepthPlainWhenAboveLowMark(t *testing.T) {
	s := PrimaryState{QueueDepth: 47, LowWatermark: 20, HighWatermark: 80}
	out := RenderPrimary(s)
	if !strings.Contains(out, "queue 47 pending") {
		t.Fatalf("expected plain queue count, got:\n%s", out)
	}
	if strings.Contains(out, "retrying in") {
		t.Fatal("must not show a retry countdown while above the low mark")
	}
}

func TestRenderPrimaryShowsRetryCountdownOnlyWhenBelowLowMarkAndSet(t *testing.T) {
	d := 4*time.Minute + 12*time.Second
	s := PrimaryState{QueueDepth: 14, LowWatermark: 20, HighWatermark: 80, RetryCountdown: &d}
	out := RenderPrimary(s)
	if !strings.Contains(out, "retrying in 04:12") {
		t.Fatalf("expected the retry countdown when below the low mark, got:\n%s", out)
	}
}

func TestRenderPrimaryShowsEachActiveRunWithElapsed(t *testing.T) {
	s := PrimaryState{Active: []ActiveRun{{File: "pb/internal/gate/handler.go", Elapsed: 42 * time.Second}}}
	out := RenderPrimary(s)
	if !strings.Contains(out, "pb/internal/gate/handler.go") || !strings.Contains(out, "00:42") {
		t.Fatalf("expected active run with elapsed time, got:\n%s", out)
	}
}

func TestRenderPrimaryShowsProjectTreeWithCounts(t *testing.T) {
	s := PrimaryState{Projects: []ProjectNode{
		{Name: "pb", Included: true, Packages: 6, Files: 58, Tests: 51},
	}}
	out := RenderPrimary(s)
	if !strings.Contains(out, "pb") || !strings.Contains(out, "58") {
		t.Fatalf("expected project row with file count, got:\n%s", out)
	}
}
