package traceexplain_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/traceexplain"
)

func render(t *testing.T, rep traceexplain.Report, header string) string {
	t.Helper()
	var b bytes.Buffer
	if err := traceexplain.RenderText(&b, rep, header); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// inOrder asserts every want appears as a whole line, in the given order.
func inOrder(t *testing.T, out string, want ...string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	i := 0
	for _, w := range want {
		for i < len(lines) && lines[i] != w {
			i++
		}
		if i == len(lines) {
			t.Fatalf("line %q missing or out of order in:\n%s", w, out)
		}
		i++
	}
}

func TestRenderText(t *testing.T) {
	out := render(t, analyzeFixture(t, "update-two-repos.json", 2), "")
	inOrder(
		t, out,
		"pn workspace update (scope full), trace "+testTraceID+", started 2025-11-18T12:00:00Z, wall 17m30s",
		"Slowest: a 10m00s (57%) — step update-deps-x 6m40s; top local build slow-drv 5m00s",
		"  a  wall 10m00s  relock 10s  git 10s  unattributed 0s",
		"    update-locks 9m40s (setup 10s, finalize 1m00s), between steps 10s",
		"    step update-deps-x 6m40s ok",
		"    step nix-flake-update 1m40s ok",
		"    nix develop x1 9m36s",
		"    nix develop 9m36s (plan: 2 builds, 1 fetch)",
		"  b  wall 6m40s  relock 0s  git 5s  unattributed 1m40s",
		"    update-locks 4m55s (setup 0s, finalize 0s)",
		"    no step data: update-locks.sh printed no UL_STEP lines (it exited before its first step, or its lib predates UL_STEP)",
		"    nix develop x1 4m53s",
		"    nix develop 4m53s (plan: 0 builds, 0 fetches)",
		"Outside repos: 50s (hooks: post update 40s)",
		"Nix (summed across parallel work, can exceed wall): substitutes 20s, build-lock wait 40s",
		"Top local builds:",
		"  slow-drv  5m00s  a / update-deps-x",
		"  fast-drv  30s  a / nix-flake-update",
		"Anomalies:",
		"  "+anomalyB,
		"  "+anomalyOrphans,
	)
	if strings.Contains(out, "INCOMPLETE") {
		t.Error("complete trace printed INCOMPLETE")
	}
}

func TestRenderText_FlakeScopeRepoLine(t *testing.T) {
	out := render(t, analyzeFixture(t, "flake-scope.json", 5), "")
	inOrder(t, out, "  c  wall 1m40s  relock 1m30s  git 10s  unattributed 5s")
	if strings.Contains(out, "no step data") {
		t.Errorf("flake-scope repo printed a no-step-data line:\n%s", out)
	}
}

func TestRenderText_Incomplete(t *testing.T) {
	rep, err := traceexplain.Analyze(testTraceID, loadSpans(t, "incomplete.json"), 5, true)
	if err != nil {
		t.Fatal(err)
	}
	out := render(t, rep, "Explaining trace X")
	lines := strings.Split(out, "\n")
	if lines[0] != "INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded" || lines[1] != "Explaining trace X" {
		t.Errorf("first lines = %q", lines[:2])
	}
	if want := "Partial trace " + testTraceID + ", started "; !strings.Contains(out, "\n"+want) {
		t.Errorf("missing partial title %q in:\n%s", want, out)
	}
	if strings.Contains(out, "Trace, trace") {
		t.Errorf("old partial title present:\n%s", out)
	}
	if n := strings.Count(out, "INCOMPLETE TRACE"); n != 1 {
		t.Errorf("INCOMPLETE TRACE appears %d times, want 1", n)
	}
}

func TestRenderText_DurationFormats(t *testing.T) {
	rep := traceexplain.Report{
		TraceID: "t", Verb: "workspace update", WallSeconds: 4800,
		Repos: []traceexplain.RepoReport{{Name: "a", WallSeconds: 39.6, GitSeconds: 59.4, RelockSeconds: 3599}},
	}
	out := render(t, rep, "")
	inOrder(t, out, "  a  wall 40s  relock 59m59s  git 59s  unattributed 0s")
	if !strings.Contains(out, "wall 1h20m\n") {
		t.Errorf("1h20m missing:\n%s", out)
	}
}

func TestRenderText_NoBuildsNoAnomalies(t *testing.T) {
	out := render(t, traceexplain.Report{TraceID: "t", Verb: "workspace update", WallSeconds: 10}, "")
	for _, h := range []string{"Top local builds", "Anomalies"} {
		if strings.Contains(out, h) {
			t.Errorf("output contains %q heading:\n%s", h, out)
		}
	}
}

func TestRenderText_NixAggregateAndCap(t *testing.T) {
	var calls []traceexplain.NixCall
	for i := 1; i <= 7; i++ { // 7 develop calls of 10s..70s, then 1 build of 5s
		calls = append(calls, traceexplain.NixCall{Subcommand: "develop", Seconds: float64(i * 10)})
	}
	calls = append(calls, traceexplain.NixCall{Subcommand: "build", Seconds: 5})
	rep := traceexplain.Report{
		TraceID: "t", Verb: "workspace update", WallSeconds: 500,
		Repos: []traceexplain.RepoReport{{
			Name: "a", WallSeconds: 400, Nix: calls,
			UpdateLocks: &traceexplain.ULReport{Seconds: 300, SetupSeconds: 5, FinalizeSeconds: 7},
		}},
	}
	out := render(t, rep, "")
	inOrder(
		t, out,
		"    update-locks 5m00s (setup 5s, finalize 7s)",
		"    nix develop x7 4m40s",
		"    nix build x1 5s",
		"    nix develop 1m10s (plan: 0 builds, 0 fetches)",
		"    nix develop 1m00s (plan: 0 builds, 0 fetches)",
		"    nix develop 50s (plan: 0 builds, 0 fetches)",
		"    nix develop 40s (plan: 0 builds, 0 fetches)",
		"    nix develop 30s (plan: 0 builds, 0 fetches)",
		"    (+3 more)",
	)
	if strings.Contains(out, "between steps") {
		t.Errorf("zero between-steps printed:\n%s", out)
	}
	if strings.Contains(out, "nix develop 20s") || strings.Contains(out, "nix build 5s") {
		t.Errorf("lines beyond cap printed:\n%s", out)
	}
}
