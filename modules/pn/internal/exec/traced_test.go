package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

func TestTracedRunner_EmitsExecSpanWithExitCode(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, verb := tel.StartVerb(ctx, "workspace status")

	r := NewRealRunner()
	res, err := r.Run(ctx, "sh", []string{"-c", "echo out; echo err 1>&2; exit 3"}, RunOptions{})
	verb.End(0, nil)

	var ce *CommandError
	if !errors.As(err, &ce) || res.ExitCode != 3 {
		t.Fatalf("err=%v exit=%d, want CommandError exit 3", err, res.ExitCode)
	}
	es := rec.ByName("pn.exec")
	if len(es) != 1 {
		t.Fatalf("pn.exec spans = %d, want 1", len(es))
	}
	if es[0].Status().Code != codes.Error {
		t.Error("failed exec span is not Error")
	}
	var gotExit int64 = -1
	var gotName, gotErrType string
	for _, kv := range es[0].Attributes() {
		switch string(kv.Key) {
		case "process.exit.code":
			gotExit = kv.Value.AsInt64()
		case "process.executable.name":
			gotName = kv.Value.AsString()
		case "error.type":
			gotErrType = kv.Value.AsString()
		}
	}
	if gotExit != 3 || gotName != "sh" || gotErrType == "" {
		t.Errorf("attrs exit=%d name=%q error.type=%q", gotExit, gotName, gotErrType)
	}
	if es[0].Parent().SpanID() != rec.ByName("pn.verb")[0].SpanContext().SpanID() {
		t.Error("pn.exec is not a child of the span in ctx")
	}
}

// The decorator MUST NOT change Result buffering or the live tee.
func TestTracedRunner_PreservesBufferingAndTee(t *testing.T) {
	run := func(r Runner, ctx context.Context) (Result, string) {
		var live bytes.Buffer
		res, err := r.Run(ctx, "sh", []string{"-c", "echo out; echo err 1>&2"}, RunOptions{Stdout: &live, Stderr: &live})
		if err != nil {
			t.Fatal(err)
		}
		return res, live.String()
	}
	plainRes, plainLive := run(&realRunner{}, context.Background())

	tel, _ := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	tracedRes, tracedLive := run(NewRealRunner(), ctx)

	if string(plainRes.Stdout) != string(tracedRes.Stdout) || string(plainRes.Stderr) != string(tracedRes.Stderr) || plainRes.ExitCode != tracedRes.ExitCode {
		t.Errorf("Result differs: plain=%+v traced=%+v", plainRes, tracedRes)
	}
	// stdout and stderr interleave non-deterministically in the tee; compare as sets of lines.
	if len(strings.Fields(plainLive)) != len(strings.Fields(tracedLive)) || !strings.Contains(tracedLive, "out") || !strings.Contains(tracedLive, "err") {
		t.Errorf("tee differs: plain=%q traced=%q", plainLive, tracedLive)
	}
}

func TestTracedRunner_StartFailureRecorded(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	_, err := NewRealRunner().Run(ctx, "pn-no-such-binary-xyz", nil, RunOptions{})
	if err == nil {
		t.Fatal("expected start error")
	}
	es := rec.ByName("pn.exec")
	if len(es) != 1 || es[0].Status().Code != codes.Error {
		t.Fatalf("want one Error pn.exec span, got %d", len(es))
	}
}

func TestTracedRunner_NoTelemetryInContextStillRuns(t *testing.T) {
	res, err := NewRealRunner().Run(context.Background(), "echo", []string{"hi"}, RunOptions{})
	if err != nil || !strings.Contains(string(res.Stdout), "hi") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// subprocessAllowlist names the only non-test files that may call os/exec
// directly instead of going through the traced exec.Runner. Anything else
// would be an unspanned subprocess.
var subprocessAllowlist = map[string]string{
	"internal/exec/exec.go": "the real runner itself",
	// Read-only `git rev-parse` / `config` probes that resolve the per-clone
	// hook bundle state (doctor, install gate), not routed through a Runner. A
	// known telemetry gap (epic pg2-kqrrs, like gitclient's git).
	"internal/workspace/hookbundle.go": "known gap: hook bundle state git probes",
}

var osExecCall = regexp.MustCompile(`\b\w*exec\.Command(Context)?\(`)

func TestNoSubprocessBypassesTheTracedRunner(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// The smoke harness is a test-only build-tagged package that drives
			// the pn binary and git fixtures; it is not product code.
			if rel == "internal/workspace/smoke" || strings.HasPrefix(rel, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		if _, ok := subprocessAllowlist[rel]; ok {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if osExecCall.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, trimmed))
			}
		}
		return nil
	})
	if werr != nil {
		t.Fatal(werr)
	}
	if len(offenders) > 0 {
		t.Errorf("os/exec used outside the traced Runner (add a Runner call, or justify in subprocessAllowlist):\n%s", strings.Join(offenders, "\n"))
	}
}
