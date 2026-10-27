package beads

// Real-bd regression guard for the per-package triage bead (bead pg2-tkm9g0,
// global rule V-3): what bd ACCEPTS (a body of tens of KB piped on stdin, a
// comma-joined label list, `--silent` output, priority and type) and what it
// STORES (labels, priority, type, body) are the real tool's semantics, so a
// mock of bd cannot prove them. fakeBd in beads_test.go covers only the pure
// body logic.
//
// Hermetic store: the test runs `bd init` inside a fixture repository from
// x/gittest, in its own temp tree, and asserts .beads/metadata.json says
// "dolt_mode": "embedded", a private on-disk engine owned by that directory.
// The environment handed to bd is an ALLOWLIST (PATH and TMPDIR plus a private
// HOME), so no BEADS_*/BD_* variable, no GIT_* variable of an outer hook and no
// user-level bd or dolt configuration can reach it; BEADS_DOLT_AUTO_START=0 is
// pinned so bd never spawns a dolt server. No dolt server is started, the
// shared server (port 25252) and ~/.local/share/beads-dolt are never read, and
// the real tracker is never written. `bd metrics off` runs first in the private
// HOME so the run sends no usage metrics anywhere.
//
// A host without `bd` on PATH (the nix sandbox of the go-tests check) skips it.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
)

// realBDDeadline is a hang guard, not a performance assertion: `bd init` takes
// seconds on an idle host and longer under load.
const realBDDeadline = 3 * time.Minute

// hermeticBDPassthrough is the complete list of parent-environment names a bd
// child may inherit. None selects a git repository or a beads workspace.
var hermeticBDPassthrough = map[string]bool{"PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "LANG": true, "TZ": true, "TERM": true}

func hermeticBDEnv(home string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if hermeticBDPassthrough[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return append(out, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "BEADS_DOLT_AUTO_START=0")
}

type realBD struct {
	t    *testing.T
	repo string
	env  []string
}

func (r *realBD) run(args ...string) []byte {
	r.t.Helper()
	cmd := exec.Command("bd", args...)
	cmd.Dir = r.repo
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		r.t.Fatalf("bd %v: %v", args, err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			r.t.Fatalf("bd %v: %v\n%s", args, err, stderr.String())
		}
	case <-time.After(realBDDeadline):
		_ = cmd.Process.Kill()
		r.t.Fatalf("bd %v: no result within %v", args, realBDDeadline)
	}
	return stdout.Bytes()
}

func newRealBD(t *testing.T) *realBD {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pg-go-mutate-tui-beads"})
	r := &realBD{t: t, repo: repo.Dir, env: hermeticBDEnv(t.TempDir())}
	r.run("metrics", "off")
	r.run("init", "--prefix", "tt", "--non-interactive", "-q", "--skip-agents", "--skip-hooks")

	raw, err := os.ReadFile(filepath.Join(r.repo, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		DoltMode string `json:"dolt_mode"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil || meta.DoltMode != "embedded" {
		t.Fatalf("the test store must be an embedded, directory-private dolt engine; metadata = %s (%v)", raw, err)
	}
	return r
}

type listedBead struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    int      `json:"priority"`
	IssueType   string   `json:"issue_type"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
}

// triageBeads lists every bead in the hermetic store labeled go-test-gaps.
func (r *realBD) triageBeads() []listedBead {
	r.t.Helper()
	var beads []listedBead
	if err := json.Unmarshal(r.run("list", "--json", "-n", "0", "--all", "--label", "go-test-gaps"), &beads); err != nil {
		r.t.Fatal(err)
	}
	return beads
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makePackage writes a one-module, two-file package and one report per file in
// pg-go-mutate's --json worklist shape.
func makePackage(t *testing.T, repo, pkg string, rowsInA int) (a, b, reportA, reportB string) {
	t.Helper()
	module := filepath.Join(repo, "modules", "gate")
	writeFile(t, filepath.Join(module, "go.mod"), "module fixture\n\ngo 1.22\n")
	dir := filepath.Join(module, filepath.FromSlash(pkg))
	a, b = filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go")
	writeFile(t, a, "package p\n")
	writeFile(t, b, "package p\n")

	type row struct {
		File        string `json:"file"`
		Line        int    `json:"line"`
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	var rows []row
	for i := 1; i <= rowsInA; i++ {
		rows = append(rows, row{File: "", Line: i, Type: "conditional", Description: "`!=` -> `==` " + strings.Repeat("x", 30)})
	}
	rep := func(survivors []row) string {
		raw, err := json.Marshal(map[string]any{
			"survivors":       survivors,
			"statistics":      map[string]any{"killed": 7, "survived": len(survivors), "survivedActionable": len(survivors), "survivedNoOp": 0, "notViable": 0, "timedOut": 0, "errors": 0},
			"buildTagsNotRun": nil,
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	reportA = filepath.Join(t.TempDir(), "a.go.json")
	reportB = filepath.Join(t.TempDir(), "b.go.json")
	writeFile(t, reportA, rep(rows))
	writeFile(t, reportB, rep(nil))
	return a, b, reportA, reportB
}

func TestRealBDFilesExactlyOneP3TriageBeadWhenAPackageCompletes(t *testing.T) {
	r := newRealBD(t)
	writeFile(t, filepath.Join(r.repo, "CLAUDE.md"),
		"# Rules\n\nEvery bead filed against this repo MUST carry the repo label `base`.\n")

	t.Run("a package with rows", func(t *testing.T) {
		a, b, reportA, reportB := makePackage(t, r.repo, "internal/gate", 3)
		l := ledger.New(filepath.Join(t.TempDir(), "ledger.jsonl"))
		fl := &Filer{
			Ledger:    l,
			NewClient: func(root string) BdClient { return &CLIClient{Dir: root, Env: r.env, Actor: "pg-go-mutate-tui-test"} },
			FiledPath: filepath.Join(t.TempDir(), "triage-filed.jsonl"),
		}

		if err := l.Append(ledger.Record{FilePath: a, PackageHash: "h1", Status: "done", ReportPath: reportA}); err != nil {
			t.Fatal(err)
		}
		fl.OnRecorded(a)
		if got := r.triageBeads(); len(got) != 0 {
			t.Fatalf("a bead was filed with one of two files recorded: %+v", got)
		}

		if err := l.Append(ledger.Record{FilePath: b, PackageHash: "h1", Status: "done", ReportPath: reportB}); err != nil {
			t.Fatal(err)
		}
		fl.OnRecorded(b)
		fl.OnRecorded(a) // a duplicate completion event
		fl.OnRecorded(b)

		got := r.triageBeads()
		if len(got) != 1 {
			t.Fatalf("want exactly one triage bead, got %d: %+v", len(got), got)
		}
		bead := got[0]
		if bead.Title != "go-test-gaps triage: internal/gate" {
			t.Errorf("title = %q", bead.Title)
		}
		if bead.Priority != 3 {
			t.Errorf("priority = %d, want 3", bead.Priority)
		}
		if bead.IssueType == "epic" || bead.IssueType != "task" {
			t.Errorf("issue type = %q, want task (never an epic)", bead.IssueType)
		}
		if bead.Status != "open" {
			t.Errorf("status = %q, want open", bead.Status)
		}
		if len(bead.Labels) != 2 || !(contains(bead.Labels, "go-test-gaps") && contains(bead.Labels, "base")) {
			t.Errorf("labels = %v, want exactly go-test-gaps and base", bead.Labels)
		}
		for _, want := range []string{
			"`internal/gate`",
			"`modules/gate`",
			"`internal/gate/a.go:1` | `!=` -> `==` ",
			"`internal/gate/a.go:3` |",
			reportA,
			reportB,
			HowToWorkThisBead,
		} {
			if !strings.Contains(bead.Description, want) {
				t.Errorf("stored body lacks %q\n%s", want, bead.Description)
			}
		}
		for _, banned := range []string{"survived", "killed", "score", "percent", "%", "tally"} {
			if strings.Contains(strings.ToLower(bead.Description), banned) {
				t.Errorf("stored body contains %q", banned)
			}
		}
	})

	t.Run("rows over the bead body cap become report paths", func(t *testing.T) {
		a, b, reportA, reportB := makePackage(t, r.repo, "internal/big", 2000)
		l := ledger.New(filepath.Join(t.TempDir(), "ledger.jsonl"))
		fl := &Filer{
			Ledger:    l,
			NewClient: func(root string) BdClient { return &CLIClient{Dir: root, Env: r.env} },
			FiledPath: filepath.Join(t.TempDir(), "triage-filed.jsonl"),
		}
		for _, rec := range []ledger.Record{
			{FilePath: a, PackageHash: "h1", Status: "done", ReportPath: reportA},
			{FilePath: b, PackageHash: "h1", Status: "done", ReportPath: reportB},
		} {
			if err := l.Append(rec); err != nil {
				t.Fatal(err)
			}
			fl.OnRecorded(rec.FilePath)
		}
		var big *listedBead
		for _, bead := range r.triageBeads() {
			if bead.Title == "go-test-gaps triage: internal/big" {
				big = &bead
			}
		}
		if big == nil {
			t.Fatal("the over-cap package was not filed")
		}
		if len(big.Description) > 65535 {
			t.Fatalf("stored body is %d bytes, over bd's cap", len(big.Description))
		}
		if strings.Contains(big.Description, "`internal/big/a.go:7` |") {
			t.Error("rows must be dropped when they exceed the cap")
		}
		for _, want := range []string{reportA, reportB, "too large for one bead body", HowToWorkThisBead} {
			if !strings.Contains(big.Description, want) {
				t.Errorf("over-cap body lacks %q", want)
			}
		}
	})
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
