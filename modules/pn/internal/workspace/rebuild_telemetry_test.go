package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

// attrs returns a span's attributes by key.
func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value
	}
	return m
}

// gitProbeRunner answers apply's skip-gate probes (git status --porcelain,
// git rev-parse HEAD, git --exec-path) from fixed values and succeeds at
// everything else, so a verb runs end to end.
type gitProbeRunner struct {
	porcelain string
	head      string
}

func (r *gitProbeRunner) Run(_ context.Context, name string, args []string, _ exec.RunOptions) (exec.Result, error) {
	if name == "git" {
		for _, a := range args {
			switch a {
			case "status":
				return exec.Result{Stdout: []byte(r.porcelain)}, nil
			case "rev-parse":
				return exec.Result{Stdout: []byte(r.head + "\n")}, nil
			case "--exec-path":
				return exec.Result{Stdout: []byte("/g\n")}, nil
			}
		}
	}
	return exec.Result{}, nil
}

// decisionWS opens a two-repo workspace (leaf consumes dep, so one
// --override-input is emitted) on r, trusted and with a stubbed hostname.
func decisionWS(t *testing.T, r exec.Runner) *Workspace {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	writeFile(t, filepath.Join(root, LockFileName), applyLock)
	trustWS(t, root)
	w, err := Open(root, r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.hostnameFn = func() string { return "spanhost" }
	return w
}

// seedApplied records ref as the last applied HEAD of every repo.
func seedApplied(t *testing.T, w *Workspace, ref string) {
	t.Helper()
	for _, name := range []string{"dep", "leaf"} {
		if err := writeAppliedState(w.appliedStateKeyPath(name), AppliedState{AppliedRef: ref}); err != nil {
			t.Fatalf("seed applied state: %v", err)
		}
	}
}

// repoSpanAttrs runs verb under an in-memory exporter and returns the pn.repo
// span's attributes.
func repoSpanAttrs(t *testing.T, w *Workspace, verb string, force bool) map[string]attribute.Value {
	t.Helper()
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	var out bytes.Buffer
	var err error
	if verb == "build" {
		err = w.Build(ctx, &out, BuildOptions{Builder: "darwin-rebuild"})
	} else {
		err = w.Apply(ctx, &out, ApplyOptions{Force: force, Builder: "darwin-rebuild"})
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", verb, err, out.String())
	}
	spans := rec.ByName(telemetry.SpanRepo)
	if len(spans) != 1 {
		t.Fatalf("pn.repo spans = %d, want 1", len(spans))
	}
	return attrs(spans[0])
}

func wantAttr(t *testing.T, got map[string]attribute.Value, key string, want any) {
	t.Helper()
	v, ok := got[key]
	if !ok {
		t.Errorf("%s missing; have %v", key, got)
		return
	}
	switch w := want.(type) {
	case string:
		if v.AsString() != w {
			t.Errorf("%s = %q, want %q", key, v.AsString(), w)
		}
	case bool:
		if v.AsBool() != w {
			t.Errorf("%s = %v, want %v", key, v.AsBool(), w)
		}
	case int:
		if v.AsInt64() != int64(w) {
			t.Errorf("%s = %d, want %d", key, v.AsInt64(), w)
		}
	}
}

// Apply records the skip-gate verdict for every reason, plus the run context, on
// the pn.repo span: a skipped apply is distinguishable from a real rebuild.
func TestApplySpan_RebuildDecisionAndContext(t *testing.T) {
	cases := []struct {
		name      string
		porcelain string
		seed      string // applied ref to record ("" = no record)
		force     bool
		reason    string
		rebuild   bool
	}{
		{"forced", "", "", true, "forced", true},
		{"dirty", " M flake.nix\n", "h", false, "dirty", true},
		{"head_changed", "", "old", false, "head_changed", true},
		{"no_record", "", "", false, "no_record", true},
		{"unchanged", "", "h", false, "unchanged", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := decisionWS(t, &gitProbeRunner{porcelain: tc.porcelain, head: "h"})
			if tc.seed != "" {
				seedApplied(t, w, tc.seed)
			}
			got := repoSpanAttrs(t, w, "apply", tc.force)
			wantAttr(t, got, "pn.apply.rebuild", tc.rebuild)
			wantAttr(t, got, "pn.apply.rebuild_reason", tc.reason)
			wantAttr(t, got, "pn.apply.force", tc.force)
			wantAttr(t, got, "pn.terminal", "leaf")
			wantAttr(t, got, "pn.hostname", "spanhost")
			wantAttr(t, got, "pn.builder", "darwin-rebuild")
			wantAttr(t, got, "pn.override.count", 1)
		})
	}
}

// Build records the same read-only "apply would" decision (never forced) and
// the same run context.
func TestBuildSpan_ReadOnlyDecisionAndContext(t *testing.T) {
	cases := []struct {
		name      string
		porcelain string
		seed      string
		reason    string
		rebuild   bool
	}{
		{"dirty", " M flake.nix\n", "h", "dirty", true},
		{"head_changed", "", "old", "head_changed", true},
		{"no_record", "", "", "no_record", true},
		{"unchanged", "", "h", "unchanged", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := decisionWS(t, &gitProbeRunner{porcelain: tc.porcelain, head: "h"})
			if tc.seed != "" {
				seedApplied(t, w, tc.seed)
			}
			got := repoSpanAttrs(t, w, "build", false)
			wantAttr(t, got, "pn.apply.rebuild", tc.rebuild)
			wantAttr(t, got, "pn.apply.rebuild_reason", tc.reason)
			wantAttr(t, got, "pn.apply.force", false)
			wantAttr(t, got, "pn.terminal", "leaf")
			wantAttr(t, got, "pn.hostname", "spanhost")
			wantAttr(t, got, "pn.builder", "darwin-rebuild")
			wantAttr(t, got, "pn.override.count", 1)
		})
	}
}

// pn.hook spans: one per fired pre/post event, a child of the caller's span.
func hookSpans(t *testing.T, w *Workspace, phase HookPhase, cmd string, processed []string) (sdktrace.ReadOnlySpan, error) {
	t.Helper()
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, verb := tel.StartVerb(ctx, cmd)
	err := w.RunEventHooks(ctx, phase, cmd, processed, &bytes.Buffer{}, &bytes.Buffer{})
	verb.End(0, nil)
	hs := rec.ByName(telemetry.SpanHook)
	if len(hs) == 0 {
		return nil, err
	}
	if len(hs) != 1 {
		t.Fatalf("pn.hook spans = %d, want 1", len(hs))
	}
	if hs[0].Parent().SpanID() != rec.ByName(telemetry.SpanVerb)[0].SpanContext().SpanID() {
		t.Error("pn.hook is not a child of the caller's span")
	}
	return hs[0], err
}

func hookLock() *Lock {
	return &Lock{Repos: map[string]LockRepoEntry{"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"}}}
}

func TestRunEventHooks_SpanForPreAndPostHooks(t *testing.T) {
	for _, phase := range []HookPhase{HookPhasePre, HookPhasePost} {
		when, name := "pre", "pre-rebase"
		if phase == HookPhasePost {
			when, name = "post", "post-rebase"
		}
		t.Run(when, func(t *testing.T) {
			w := openHookWS(t,
				"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\""+name+"\"]\nrun=[\"hk\"]\n",
				[]string{"a"}, hookLock())
			w.runner.(*exec.FakeRunner).AddResponse("sh", []string{"-c", "hk"}, exec.Result{}, nil)
			span, err := hookSpans(t, w, phase, "rebase", []string{"a"})
			if err != nil || span == nil {
				t.Fatalf("err=%v span=%v", err, span)
			}
			a := attrs(span)
			wantAttr(t, a, "pn.hook.when", when)
			wantAttr(t, a, "pn.hook.name", "rebase")
			wantAttr(t, a, "process.exit.code", 0)
			if span.Status().Code == codes.Error {
				t.Error("successful hook span is Error")
			}
		})
	}
}

func TestRunEventHooks_SpanForFailingPreHook(t *testing.T) {
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"pre-rebase\"]\nrun=[\"gate\"]\n",
		[]string{"a"}, hookLock())
	failure := &exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 7}}
	w.runner.(*exec.FakeRunner).AddResponse("sh", []string{"-c", "gate"}, exec.Result{ExitCode: 7}, failure)
	span, err := hookSpans(t, w, HookPhasePre, "rebase", []string{"a"})
	if err == nil || span == nil {
		t.Fatalf("a failing pre-hook must abort and span; err=%v span=%v", err, span)
	}
	a := attrs(span)
	wantAttr(t, a, "pn.hook.when", "pre")
	wantAttr(t, a, "process.exit.code", 7)
	if span.Status().Code != codes.Error {
		t.Error("failing hook span is not Error")
	}
}

// A post-hook failure is warn-only for the command but still marks the span.
func TestRunEventHooks_SpanForFailingPostHook(t *testing.T) {
	for name, toml := range map[string]string{
		"workspace": "[[hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"hk\"]\n[repos.a]\nurl=\"github:o/a\"\n",
		"repo":      "[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"hk\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			w := openHookWS(t, toml, []string{"a"}, hookLock())
			failure := &exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 3}}
			w.runner.(*exec.FakeRunner).AddResponse("sh", []string{"-c", "hk"}, exec.Result{ExitCode: 3}, failure)
			span, err := hookSpans(t, w, HookPhasePost, "rebase", []string{"a"})
			if err != nil {
				t.Fatalf("post-hook failure must not propagate: %v", err)
			}
			if span == nil {
				t.Fatal("no pn.hook span")
			}
			wantAttr(t, attrs(span), "pn.hook.when", "post")
			wantAttr(t, attrs(span), "process.exit.code", 3)
			if span.Status().Code != codes.Error {
				t.Error("failing post-hook span is not Error")
			}
		})
	}
}

// An event with no matching hook starts no span (no noise on every verb).
func TestRunEventHooks_NoSpanWhenNothingFires(t *testing.T) {
	w := openHookWS(t, "[repos.a]\nurl=\"github:o/a\"\n", []string{"a"}, hookLock())
	span, err := hookSpans(t, w, HookPhasePre, "rebase", []string{"a"})
	if err != nil || span != nil {
		t.Fatalf("err=%v span=%v, want no span", err, span)
	}
}
