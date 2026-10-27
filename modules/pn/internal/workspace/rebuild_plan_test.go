package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

// planRunner is a FakeRunner whose darwin-rebuild (build) and sudo (apply)
// calls stream scripted chunks to the Stdout/Stderr the verb handed it, as a
// real nix child would, and then return runErr. Every other call is the fake's.
type planRunner struct {
	*exec.FakeRunner
	chunks   []string
	toStderr bool // alternate chunks between Stdout and Stderr when true
	runErr   error
	ran      atomic.Int32
}

func (r *planRunner) Run(ctx context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	if name != "darwin-rebuild" && name != "sudo" {
		return r.FakeRunner.Run(ctx, name, args, opts)
	}
	r.ran.Add(1)
	for i, c := range r.chunks {
		w := opts.Stdout
		if r.toStderr && i%2 == 1 {
			w = opts.Stderr
		}
		if _, err := w.Write([]byte(c)); err != nil {
			return exec.Result{}, err
		}
	}
	return exec.Result{}, r.runErr
}

// planVerbEnv builds a hermetic one-repo workspace and returns a runner to
// script and the workspace.
type planVerbEnv struct {
	w      *Workspace
	runner *planRunner
}

func newPlanVerbEnv(t *testing.T, extraWorkspaceTOML string) *planVerbEnv {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "plantest"
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"
`+extraWorkspaceTOML+`
[repos.leaf]
url = "github:owner/leaf"
`)
	trustWS(t, root)
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	for i := 0; i < 3; i++ {
		f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("h\n")}, nil)
	}
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	pr := &planRunner{FakeRunner: f}
	w, err := Open(root, pr)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.hostnameFn = func() string { return "planhost" }
	return &planVerbEnv{w: w, runner: pr}
}

// run runs verb and returns the terminal output and the verb's error.
func (e *planVerbEnv) run(ctx context.Context, verb string) (string, error) {
	var out bytes.Buffer
	var err error
	switch verb {
	case "build":
		err = e.w.Build(ctx, &out, BuildOptions{})
	case "apply":
		err = e.w.Apply(ctx, &out, ApplyOptions{Force: true})
	}
	return out.String(), err
}

// applyLogOutput returns the `--== output ==--` section of the latest apply log.
func (e *planVerbEnv) applyLogOutput(t *testing.T) string {
	t.Helper()
	// trustWS re-points XDG_STATE_HOME, so the directory is resolved only now.
	data, err := os.ReadFile(filepath.Join(applyLogDir(), "latest"))
	if err != nil {
		t.Fatalf("read apply log: %v", err)
	}
	s := string(data)
	i := strings.Index(s, applyLogOutput+"\n")
	j := strings.Index(s, "\n"+applyLogTrailer)
	if i < 0 || j < 0 {
		t.Fatalf("apply log lacks output/trailer:\n%s", s)
	}
	return s[i+len(applyLogOutput)+1 : j]
}

var planVerbs = []string{"build", "apply"}

// smallPlanChunks is a realistic below-threshold run: a plan split across
// writes, on both streams, with ANSI.
func smallPlanChunks() []string {
	return []string{
		"\x1b[1mthese 24 derivations will be built:\x1b[0m\n  /nix/store/",
		"00000000000000000000000000000000-pkg0.drv\nthese 3 paths will be fetched (1.5 MiB download, 3.0 MiB unpacked):\n  ",
		"/nix/store/6p65afcaqrhijlhd94ldjdkz4w6psghm-libfaketime-0.9.10\nbuilding 'x'...\n",
		"Build successful.",
	}
}

// The tap leaves what reaches the terminal, and (apply) the apply log's output
// section, byte-identical to what the child wrote, for both verbs.
func TestRebuildPlanTap_OutputByteIdentical(t *testing.T) {
	for _, verb := range planVerbs {
		t.Run(verb, func(t *testing.T) {
			env := newPlanVerbEnv(t, "")
			env.runner.chunks = smallPlanChunks()
			env.runner.toStderr = true
			out, err := env.run(context.Background(), verb)
			if err != nil {
				t.Fatalf("%s: %v\n%s", verb, err, out)
			}
			child := strings.Join(env.runner.chunks, "")
			if !strings.Contains(out, child) {
				t.Fatalf("terminal output lacks the child's bytes verbatim:\n%q\nchild: %q", out, child)
			}
			if strings.Contains(out, "pn: WARNING") {
				t.Fatalf("no warning expected below the threshold:\n%s", out)
			}
			if verb == "apply" {
				if got := env.applyLogOutput(t); !strings.Contains(got, child) {
					t.Fatalf("apply log output differs from the child's bytes:\n%q\nchild: %q", got, child)
				}
			}
		})
	}
}

func spanAttr(t *testing.T, rec *telemetrytest.Recorder, key string) (attribute.Value, bool) {
	t.Helper()
	spans := rec.ByName(telemetry.SpanRepo)
	if len(spans) != 1 {
		t.Fatalf("pn.repo spans = %d, want 1", len(spans))
	}
	for _, a := range spans[0].Attributes() {
		if string(a.Key) == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

// pn.plan.build / pn.plan.fetch / pn.plan.download_mib land on the pn.repo span.
func TestRebuildPlan_SpanAttributes(t *testing.T) {
	for _, verb := range planVerbs {
		t.Run(verb, func(t *testing.T) {
			env := newPlanVerbEnv(t, "")
			env.runner.chunks = smallPlanChunks()
			tel, rec := telemetrytest.New()
			ctx := telemetry.WithTelemetry(context.Background(), tel)
			if out, err := env.run(ctx, verb); err != nil {
				t.Fatalf("%s: %v\n%s", verb, err, out)
			}
			if v, ok := spanAttr(t, rec, "pn.plan.build"); !ok || v.AsInt64() != 24 {
				t.Errorf("pn.plan.build = %v, %v; want 24", v, ok)
			}
			if v, ok := spanAttr(t, rec, "pn.plan.fetch"); !ok || v.AsInt64() != 3 {
				t.Errorf("pn.plan.fetch = %v, %v; want 3", v, ok)
			}
			if v, ok := spanAttr(t, rec, "pn.plan.download_mib"); !ok || v.AsFloat64() != 1.5 {
				t.Errorf("pn.plan.download_mib = %v, %v; want 1.5", v, ok)
			}
		})
	}
}

// With no plan message the run still reached the build step: builds and fetches
// are 0 and there is no download attribute.
func TestRebuildPlan_NoPlanRecordsZero(t *testing.T) {
	env := newPlanVerbEnv(t, "")
	env.runner.chunks = []string{"nothing to build\n"}
	tel, rec := telemetrytest.New()
	if _, err := env.run(telemetry.WithTelemetry(context.Background(), tel), "build"); err != nil {
		t.Fatal(err)
	}
	if v, ok := spanAttr(t, rec, "pn.plan.build"); !ok || v.AsInt64() != 0 {
		t.Errorf("pn.plan.build = %v, %v; want 0", v, ok)
	}
	if _, ok := spanAttr(t, rec, "pn.plan.download_mib"); ok {
		t.Error("pn.plan.download_mib must be absent when no fetch message carried a size")
	}
	h := planHistogram(t, rec)
	if len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 || h.DataPoints[0].Sum != 0 {
		t.Errorf("want one observation of 0, got %+v", h.DataPoints)
	}
}

func planHistogram(t *testing.T, rec *telemetrytest.Recorder) metricdata.Histogram[int64] {
	t.Helper()
	for _, sm := range rec.Metrics(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "pn.plan.builds" {
				continue
			}
			if m.Unit != "{derivation}" {
				t.Errorf("unit = %q, want {derivation}", m.Unit)
			}
			return m.Data.(metricdata.Histogram[int64])
		}
	}
	t.Fatal("pn.plan.builds was not recorded")
	return metricdata.Histogram[int64]{}
}

// pn.plan.builds: one observation per run, labelled with the verb only, with
// 400 an exact bucket edge.
func TestRebuildPlan_HistogramOncePerRunVerbLabelOnly(t *testing.T) {
	for _, verb := range planVerbs {
		t.Run(verb, func(t *testing.T) {
			env := newPlanVerbEnv(t, "")
			env.runner.chunks = smallPlanChunks()
			tel, rec := telemetrytest.New()
			if _, err := env.run(telemetry.WithTelemetry(context.Background(), tel), verb); err != nil {
				t.Fatal(err)
			}
			h := planHistogram(t, rec)
			if len(h.DataPoints) != 1 {
				t.Fatalf("datapoints = %d, want 1", len(h.DataPoints))
			}
			dp := h.DataPoints[0]
			if dp.Count != 1 || dp.Sum != 24 {
				t.Errorf("count=%d sum=%d, want one observation of 24", dp.Count, dp.Sum)
			}
			if got := dp.Attributes.Len(); got != 1 {
				t.Errorf("labels = %d, want only the verb", got)
			}
			if v, ok := dp.Attributes.Value("pn.verb"); !ok || v.AsString() != verb {
				t.Errorf("verb label = %v, want %q", v, verb)
			}
			found := false
			for _, b := range dp.Bounds {
				if b == 400 {
					found = true
				}
			}
			if !found {
				t.Errorf("bounds %v lack an exact 400 edge", dp.Bounds)
			}
		})
	}
}

// A run whose command fails still records its plan, and the verb returns the
// command's own error (exit code intact).
func TestRebuildPlan_FailedRunStillRecordsAndKeepsExitCode(t *testing.T) {
	for _, verb := range planVerbs {
		t.Run(verb, func(t *testing.T) {
			env := newPlanVerbEnv(t, "")
			env.runner.chunks = []string{planText(500, "")}
			env.runner.runErr = &exec.CommandError{Name: "darwin-rebuild", Result: exec.Result{ExitCode: 3}}
			tel, rec := telemetrytest.New()
			out, err := env.run(telemetry.WithTelemetry(context.Background(), tel), verb)
			var ce *exec.CommandError
			if !errors.As(err, &ce) || ce.Result.ExitCode != 3 {
				t.Fatalf("err = %v, want the command's exit 3 preserved", err)
			}
			if strings.Count(out, "pn: WARNING") != 1 {
				t.Errorf("a large plan warns even when the run fails:\n%s", out)
			}
			if h := planHistogram(t, rec); h.DataPoints[0].Sum != 500 {
				t.Errorf("sum = %d, want 500", h.DataPoints[0].Sum)
			}
		})
	}
}

// Above warn_builds: one warning per run; at or below: none. A plan of 5,000
// runs to completion (nothing stops or cancels the child) with the same exit
// code and every byte of its output.
func TestRebuildPlan_LargePlanWarnsOnceAndRunsToCompletion(t *testing.T) {
	for _, verb := range planVerbs {
		t.Run(verb, func(t *testing.T) {
			env := newPlanVerbEnv(t, "")
			big := planText(5000, "these 9 paths will be fetched (2.0 MiB download, 4.0 MiB unpacked):")
			env.runner.chunks = []string{big[:len(big)/2], big[len(big)/2:], "building 'x'...\n", "done\n"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out, err := env.run(ctx, verb)
			if err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			if ctx.Err() != nil {
				t.Fatal("the context must not be cancelled because of plan size")
			}
			if env.runner.ran.Load() != 1 {
				t.Fatalf("command ran %d times, want exactly 1", env.runner.ran.Load())
			}
			if strings.Count(out, "pn: WARNING") != 1 {
				t.Fatalf("want exactly one warning:\n%s", out[len(out)-600:])
			}
			if !strings.Contains(out, "5000 derivations") || !strings.Contains(out, "done\n") {
				t.Errorf("warning or completion missing")
			}
			if !strings.Contains(removeWarning(out), strings.Join(env.runner.chunks, "")) {
				t.Error("the child's output must survive in full around the warning")
			}
			if verb == "apply" && strings.Count(env.applyLogOutput(t), "pn: WARNING") != 1 {
				t.Error("the warning must also reach the apply log (it is part of what the operator saw)")
			}
		})
	}
}

func removeWarning(s string) string {
	var keep []string
	for _, l := range strings.SplitAfter(s, "\n") {
		if !strings.HasPrefix(l, "pn: WARNING") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "")
}

func TestRebuildPlan_ThresholdIsConfigurable(t *testing.T) {
	env := newPlanVerbEnv(t, "warn_builds = 20\n")
	env.runner.chunks = smallPlanChunks() // 24 builds
	out, err := env.run(context.Background(), "build")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "pn: WARNING") != 1 || !strings.Contains(out, "workspace.warn_builds=20") {
		t.Errorf("24 builds above warn_builds=20 must warn:\n%s", out)
	}
	env = newPlanVerbEnv(t, "warn_builds = 0\n")
	env.runner.chunks = []string{planText(5000, "")}
	out, _ = env.run(context.Background(), "build")
	if strings.Contains(out, "pn: WARNING") {
		t.Error("warn_builds = 0 disables the warning")
	}
}

// Both verbs share ONE implementation of the plan tap: they go through
// runPlanned, so a plan seen by one is seen by the other with the same result.
func TestRebuildPlan_BuildAndApplyAgree(t *testing.T) {
	results := map[string]int64{}
	for _, verb := range planVerbs {
		env := newPlanVerbEnv(t, "")
		env.runner.chunks = smallPlanChunks()
		tel, rec := telemetrytest.New()
		if _, err := env.run(telemetry.WithTelemetry(context.Background(), tel), verb); err != nil {
			t.Fatal(err)
		}
		results[verb] = planHistogram(t, rec).DataPoints[0].Sum
	}
	if results["build"] != results["apply"] {
		t.Fatalf("build and apply disagree: %v", results)
	}
}
