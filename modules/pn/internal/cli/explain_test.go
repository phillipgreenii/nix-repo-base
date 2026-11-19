package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/traceexplain"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// explainNow is the pinned clock for the explain tests.
var explainNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func explainID(n int) string { return fmt.Sprintf("%032x", n) }

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// evLine renders one events.jsonl record. An empty id or root omits the field.
func evLine(kind string, ts time.Time, root, id string) string {
	m := map[string]any{"time": ts.UTC().Format(time.RFC3339Nano), "level": "info", "kind": kind, "msg": kind}
	if root != "" {
		m["root"] = root
	}
	if id != "" {
		m["trace_id"] = id
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// explainEnv isolates the event log and the workspace root. It returns the
// root dir (which holds a pn-workspace.toml) and the events.jsonl path.
type explainEnv struct {
	root   string
	events string
}

func newExplainEnv(t *testing.T) explainEnv {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := mkExplainRoot(t)
	t.Setenv("PN_WORKSPACE_ROOT", root)
	prev := now
	now = func() time.Time { return explainNow }
	t.Cleanup(func() { now = prev })
	return explainEnv{root: root, events: filepath.Join(state, "pn", "events.jsonl")}
}

func mkExplainRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func (e explainEnv) write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tempoStub is an httptest Tempo that records the paths it was asked for.
type tempoStub struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newTempoStub(t *testing.T, status int, body []byte) *tempoStub {
	t.Helper()
	s := &tempoStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *tempoStub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runExplain(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runCobraCmd(t, append([]string{"explain"}, args...))
}

func TestExplainCmd_DefaultsToNewestRunForRoot(t *testing.T) {
	env := newExplainEnv(t)
	other := mkExplainRoot(t)
	t0 := explainNow.Add(-3 * time.Hour)
	env.write(
		t, env.events,
		evLine("run_start", t0, env.root, explainID(1)),
		evLine("run_start", t0.Add(time.Hour), other, explainID(2)),
		evLine("run_start", t0.Add(2*time.Hour), env.root, explainID(3)),
	)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)

	stdout, _, err := runExplain(t)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if got := srv.requests(); len(got) != 1 || got[0] != "/api/v2/traces/"+explainID(3) {
		t.Fatalf("requests = %v, want the newest run for the root", got)
	}
	want := "Explaining the newest 'pn workspace update' run for " + env.root + ": started " + rfc(t0.Add(2*time.Hour)) + " (1h0m0s ago)"
	if !strings.Contains(stdout, want) {
		t.Errorf("header missing %q in:\n%s", want, stdout)
	}
}

func TestExplainCmd_NewestRunWithoutTraceIDExits3(t *testing.T) {
	env := newExplainEnv(t)
	t0 := explainNow.Add(-5 * time.Hour)
	env.write(
		t, env.events,
		evLine("run_start", t0, env.root, explainID(1)),
		evLine("run_start", t0.Add(time.Hour), env.root, ""),
	)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)

	stdout, _, err := runExplain(t)
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d (%v), want 3", ExitCode(err), err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if len(srv.requests()) != 0 {
		t.Errorf("Tempo was asked for %v; the command must not skip back to an older run", srv.requests())
	}
	for _, want := range []string{
		"the newest update run for " + env.root + " (" + rfc(t0.Add(time.Hour)) + ") recorded no trace id",
		"~/.config/pn/telemetry.toml or pass --otel-spool-dir",
		"pass an older trace id from " + env.events,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
}

func TestExplainCmd_TextAndJSON(t *testing.T) {
	env := newExplainEnv(t)
	env.write(t, env.events, evLine("run_start", explainNow.Add(-time.Hour), env.root, explainID(1)))
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)

	stdout, _, err := runExplain(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Slowest:") || !strings.Contains(stdout, "pn workspace update") {
		t.Errorf("text report lacks repo lines:\n%s", stdout)
	}

	stdout, _, err = runExplain(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep traceexplain.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("--json output is not a Report: %v\n%s", err, stdout)
	}
	if len(rep.Repos) != 2 {
		t.Errorf("repos = %d, want 2", len(rep.Repos))
	}
	if strings.Contains(stdout, "Explaining") {
		t.Errorf("--json printed the header:\n%s", stdout)
	}
}

func TestExplainCmd_DefaultFoundOnlyInRotatedLog(t *testing.T) {
	env := newExplainEnv(t)
	other := mkExplainRoot(t)
	env.write(t, env.events, evLine("run_start", explainNow.Add(-time.Hour), other, explainID(2)))
	env.write(t, env.events+".1", evLine("run_start", explainNow.Add(-48*time.Hour), env.root, explainID(1)))
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)

	if _, _, err := runExplain(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.requests(); len(got) != 1 || got[0] != "/api/v2/traces/"+explainID(1) {
		t.Fatalf("requests = %v, want root A's id from the rotated log", got)
	}
}

func TestExplainCmd_HeaderNotesNewerRunElsewhere(t *testing.T) {
	env := newExplainEnv(t)
	other := mkExplainRoot(t)
	tb := explainNow.Add(-time.Hour)
	env.write(
		t, env.events,
		evLine("run_start", explainNow.Add(-2*time.Hour), env.root, explainID(1)),
		evLine("run_start", tb, other, explainID(2)),
	)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)

	stdout, _, err := runExplain(t)
	if err != nil {
		t.Fatal(err)
	}
	want := "(a newer run exists for " + other + " at " + rfc(tb) + ": pn workspace explain " + explainID(2) + ")"
	var header string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "Explaining") {
			header = l
		}
	}
	if !strings.HasSuffix(header, want) {
		t.Errorf("header %q does not end with %q", header, want)
	}
}

func TestExplainCmd_ExplicitIDHeader(t *testing.T) {
	newExplainEnv(t)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)
	stdout, _, err := runExplain(t, explainID(7))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Explaining trace "+explainID(7)+"\n") {
		t.Errorf("missing explicit-id header:\n%s", stdout)
	}
}

func TestExplainCmd_ExitCodes(t *testing.T) {
	X := explainID(1)
	end1m := explainNow.Add(-time.Minute)
	end8d := explainNow.Add(-8 * 24 * time.Hour)
	inc := fixture(t, "incomplete.json")
	two := fixture(t, "update-two-repos.json")

	type row struct {
		name    string
		events  func(root string) []string // nil: no events file
		rotated bool                       // write events to events.jsonl.1 instead
		args    []string
		status  int
		body    []byte
		closed  bool
		code    int
		msgs    func(root, events, url string) []string
		notMsgs []string
		stdout  []string // text mode, exit 0 only
		notOut  []string
	}
	started := func(root string) []string {
		return []string{evLine("run_start", end1m.Add(-10*time.Minute), root, X)}
	}
	ended := func(at time.Time) func(root string) []string {
		return func(root string) []string {
			return []string{evLine("run_start", at.Add(-10*time.Minute), root, X), evLine("run_end", at, root, X)}
		}
	}
	noRunMsg := func(root, events, _ string) []string {
		return []string{"no update run with a trace id for " + root + " in " + events + "; pass a trace id"}
	}
	rows := []row{
		{name: "no events file, no id", status: 404, code: 3, msgs: noRunMsg},
		{name: "empty events file, no id", events: func(string) []string { return []string{} }, status: 404, code: 3, msgs: noRunMsg},
		{
			name: "ended 1m ago, 404", events: ended(end1m), status: 404, code: 75,
			msgs: func(string, string, string) []string {
				return []string{"trace " + X + " is not in Tempo yet (the run ended " + rfc(end1m) + "); retry in a minute; if it persists, run 'pn workspace doctor'"}
			},
			notMsgs: []string{"--partial"},
		},
		{
			name: "ended 8d ago, 404", events: ended(end8d), status: 404, code: 3,
			msgs: func(string, string, string) []string {
				return []string{"trace " + X + " has expired from Tempo (it keeps up to 7 days; the run ended " + rfc(end8d) + ")"}
			},
		},
		{
			name: "run_start only, 404", events: started, status: 404, code: 3,
			msgs: func(string, string, string) []string {
				return []string{"run " + X + " has no run_end and none of its spans are in Tempo: it is still running, or was killed before exporting; nothing to explain yet"}
			},
		},
		{
			name: "run_start only, 404, partial", events: started, args: []string{"--partial"}, status: 404, code: 3,
			msgs: func(string, string, string) []string { return []string{"nothing to explain yet"} },
		},
		{
			name: "rotated log only, ended 1m ago, 404", events: ended(end1m), rotated: true, status: 404, code: 75,
			msgs: func(string, string, string) []string { return []string{"is not in Tempo yet"} },
		},
		{
			name: "explicit id, no events, 404", args: []string{X}, status: 404, code: 3,
			msgs: func(_, events, _ string) []string {
				return []string{"trace " + X + " is not in Tempo and no pn run in " + events + " recorded it; check the id, or pass --tempo-url for another Tempo"}
			},
		},
		{
			name: "ended, incomplete trace", events: ended(end1m), status: 200, body: inc, code: 75,
			msgs: func(string, string, string) []string {
				return []string{"trace " + X + " is not in Tempo yet (the run ended " + rfc(end1m) + "); retry in a minute; if it persists, run 'pn workspace doctor' or pass --partial"}
			},
		},
		{
			name: "ended, incomplete trace, partial", events: ended(end1m), args: []string{"--partial"}, status: 200, body: inc, code: 0,
			stdout: []string{"INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded\nExplaining"},
		},
		{
			name: "run_start only, incomplete trace", events: started, status: 200, body: inc, code: 4,
			msgs: func(string, string, string) []string {
				return []string{"run " + X + " has no run_end: it is still running or was killed; pass --partial to analyze what was recorded"}
			},
		},
		{
			name: "run_start only, incomplete trace, partial", events: started, args: []string{"--partial"}, status: 200, body: inc, code: 0,
			stdout: []string{"INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded\nExplaining the newest"},
		},
		{
			name: "explicit id, no events, incomplete trace", args: []string{X}, status: 200, body: inc, code: 4,
			msgs: func(_, events, _ string) []string {
				return []string{"trace " + X + " has no pn.verb span in Tempo and no pn run in " + events + " recorded it; pass --partial to analyze what was recorded"}
			},
		},
		{
			name: "explicit id, no events, incomplete trace, partial", args: []string{X, "--partial"}, status: 200, body: inc, code: 0,
			stdout: []string{"INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded\nExplaining trace " + X},
		},
		{
			name: "ended 1m ago, 200 with zero spans", events: ended(end1m), status: 200, body: []byte(`{"trace":{"resourceSpans":[]}}`), code: 75,
			msgs: func(string, string, string) []string {
				return []string{"trace " + X + " is not in Tempo yet (the run ended " + rfc(end1m) + "); retry in a minute; if it persists, run 'pn workspace doctor'"}
			},
			notMsgs: []string{"--partial"},
		},
		{
			name: "server closed", args: []string{X}, closed: true, code: 5,
			msgs: func(_, _, url string) []string { return []string{"cannot read trace " + X + " from " + url + ": "} },
		},
		{
			name: "server 500", args: []string{X}, status: 500, code: 5,
			msgs: func(_, _, url string) []string { return []string{"cannot read trace " + X + " from " + url + ": "} },
		},
		{
			name: "200 with invalid JSON", args: []string{X}, status: 200, body: []byte("not json"), code: 5,
			msgs: func(_, _, url string) []string { return []string{"cannot read trace " + X + " from " + url + ": "} },
		},
		{
			name: "bad id", args: []string{"xyz"}, code: 2,
			msgs: func(string, string, string) []string {
				return []string{`trace id must be 32 hex characters, got "xyz"`}
			},
		},
		{
			name: "top abc", args: []string{"--top", "abc"}, code: 2,
			msgs: func(string, string, string) []string { return []string{`--top must be a positive integer, got "abc"`} },
		},
		{
			name: "top -1", args: []string{"--top", "-1"}, code: 2,
			msgs: func(string, string, string) []string { return []string{`--top must be a positive integer, got "-1"`} },
		},
		{
			name: "two positionals", args: []string{X, explainID(2)}, code: 2,
			msgs: func(string, string, string) []string { return []string{"explain takes at most one trace id"} },
		},
		{
			name: "unknown flag", args: []string{"--bogus"}, code: 2,
			msgs: func(string, string, string) []string { return []string{"unknown flag: --bogus"} },
		},
		{
			name: "partial on complete trace", args: []string{X, "--partial"}, status: 200, body: two, code: 0,
			stdout: []string{"Slowest:"}, notOut: []string{"INCOMPLETE TRACE"},
		},
	}

	for _, r := range rows {
		for _, asJSON := range []bool{false, true} {
			name := r.name
			if asJSON {
				name += " (json)"
			}
			t.Run(name, func(t *testing.T) {
				env := newExplainEnv(t)
				if r.events != nil {
					path := env.events
					if r.rotated {
						path += ".1"
					}
					env.write(t, path, r.events(env.root)...)
				}
				srv := newTempoStub(t, r.status, r.body)
				url := srv.URL
				if r.closed {
					srv.Close()
				}
				t.Setenv("PG_TEMPO_URL", url)
				args := append([]string(nil), r.args...)
				if asJSON {
					args = append(args, "--json")
				}
				stdout, _, err := runExplain(t, args...)
				if got := ExitCode(err); got != r.code {
					t.Fatalf("exit = %d (%v), want %d", got, err, r.code)
				}
				if r.code != 0 {
					if stdout != "" {
						t.Errorf("stdout = %q, want empty on a non-zero exit", stdout)
					}
					if r.msgs != nil {
						for _, m := range r.msgs(env.root, env.events, url) {
							if !strings.Contains(err.Error(), m) {
								t.Errorf("message %q lacks %q", err.Error(), m)
							}
						}
					}
					for _, m := range r.notMsgs {
						if strings.Contains(err.Error(), m) {
							t.Errorf("message %q must not contain %q", err.Error(), m)
						}
					}
					return
				}
				if asJSON {
					return
				}
				for _, s := range r.stdout {
					if !strings.Contains(stdout, s) {
						t.Errorf("stdout lacks %q:\n%s", s, stdout)
					}
				}
				for _, s := range r.notOut {
					if strings.Contains(stdout, s) {
						t.Errorf("stdout must not contain %q:\n%s", s, stdout)
					}
				}
			})
		}
	}
}

func TestExplainCmd_TopFlag(t *testing.T) {
	newExplainEnv(t)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)
	stdout, _, err := runExplain(t, explainID(1), "--json", "--top", "1")
	if err != nil {
		t.Fatal(err)
	}
	var rep traceexplain.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.TopBuilds) != 1 {
		t.Errorf("top_builds = %d, want 1", len(rep.TopBuilds))
	}
}

func TestExplainCmd_TempoURLPrecedence(t *testing.T) {
	newExplainEnv(t)
	a := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	b := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))

	t.Setenv("PG_TEMPO_URL", a.URL)
	if _, _, err := runExplain(t, explainID(1), "--tempo-url", b.URL); err != nil {
		t.Fatal(err)
	}
	if len(a.requests()) != 0 || len(b.requests()) != 1 {
		t.Errorf("flag run: A=%v B=%v, want only B asked", a.requests(), b.requests())
	}
	if _, _, err := runExplain(t, explainID(1)); err != nil {
		t.Fatal(err)
	}
	if len(a.requests()) != 1 || len(b.requests()) != 1 {
		t.Errorf("env run: A=%v B=%v, want only A asked", a.requests(), b.requests())
	}
}

func TestTempoURL_Default(t *testing.T) {
	if got := tempoURL("", ""); got != "http://127.0.0.1:3200" {
		t.Errorf("tempoURL(\"\", \"\") = %q", got)
	}
	if got := tempoURL("", "http://e/"); got != "http://e" {
		t.Errorf("env = %q", got)
	}
}

func TestExplainCmd_NoWorkspaceUsesNewestOverall(t *testing.T) {
	env := newExplainEnv(t)
	t.Setenv("PN_WORKSPACE_ROOT", "")
	t.Chdir(t.TempDir())
	other := mkExplainRoot(t)
	env.write(
		t, env.events,
		evLine("run_start", explainNow.Add(-2*time.Hour), env.root, explainID(1)),
		evLine("run_start", explainNow.Add(-time.Hour), other, explainID(2)),
	)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)
	if _, _, err := runExplain(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.requests(); len(got) != 1 || got[0] != "/api/v2/traces/"+explainID(2) {
		t.Fatalf("requests = %v, want the newest overall", got)
	}

	if err := os.Remove(env.events); err != nil {
		t.Fatal(err)
	}
	_, _, err := runExplain(t)
	if ExitCode(err) != 3 || !strings.Contains(err.Error(), "<any root>") {
		t.Errorf("err = %v, want exit 3 naming <any root>", err)
	}
}

func TestExplainCmd_FindsRunStartWrittenByUpdate(t *testing.T) {
	env := newExplainEnv(t)
	if err := os.WriteFile(filepath.Join(env.root, workspace.ConfigFileName), []byte(`[workspace]
name = "test"
terminal = "myterm"

[repos.myterm]
url = "github:owner/myterm"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fr := exec.NewFakeRunner()
	repo := filepath.Join(env.root, "myterm")
	fr.AddResponse("git", []string{"-C", repo, "diff", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "rev-parse", "--abbrev-ref", "@{u}"},
		exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	fr.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "rev-parse", "HEAD"},
		exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)
	w, err := workspace.Open(env.root, fr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)

	lw, err := eventlog.New(eventlog.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	id := explainID(0xabc)
	lw.SetTraceID(id)
	if err := w.Update(context.Background(), io.Discard, workspace.UpdateOptions{Terminal: "myterm", Log: lw, InPlace: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	_ = lw.Close()

	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)
	if _, _, err := runExplain(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.requests(); len(got) != 1 || got[0] != "/api/v2/traces/"+id {
		t.Fatalf("requests = %v, want the id the update recorded", got)
	}
}

func TestExplainCmd_EventsReader(t *testing.T) {
	env := newExplainEnv(t)
	t.Setenv("PN_WORKSPACE_ROOT", "")
	t.Chdir(t.TempDir())
	big := `{"kind":"project_result","msg":"` + strings.Repeat("x", 100<<10) + `"}`
	// A pre-change run_start has no root field.
	legacy := evLine("run_start", explainNow.Add(-time.Hour), "", explainID(9))
	env.write(t, env.events, big, "not json at all {", legacy)
	srv := newTempoStub(t, 200, fixture(t, "update-two-repos.json"))
	t.Setenv("PG_TEMPO_URL", srv.URL)

	// No-root fallback picks the legacy run past the 100 KiB line and the garbage.
	if _, _, err := runExplain(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.requests(); len(got) != 1 || got[0] != "/api/v2/traces/"+explainID(9) {
		t.Fatalf("requests = %v", got)
	}

	// With a workspace root, the root-less record never matches.
	root := mkExplainRoot(t)
	t.Setenv("PN_WORKSPACE_ROOT", root)
	_, _, err := runExplain(t)
	if ExitCode(err) != 3 || !strings.Contains(err.Error(), "no update run with a trace id for "+root) {
		t.Errorf("err = %v, want exit 3 (a root-less run never matches a root)", err)
	}
}

// fixtureDir is absolute so tests that Chdir can still read the fixtures.
var fixtureDir = func() string {
	abs, err := filepath.Abs(filepath.Join("..", "traceexplain", "testdata"))
	if err != nil {
		panic(err)
	}
	return abs
}()
