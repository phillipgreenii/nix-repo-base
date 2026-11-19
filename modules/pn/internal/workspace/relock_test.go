package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const (
	revA = "1111111111111111111111111111111111111111"
	revB = "2222222222222222222222222222222222222222"
	revC = "3333333333333333333333333333333333333333"
)

// mkLock builds a flake.lock whose root node has the given inputs JSON and
// whose other nodes are the given JSON members (`"name":{...},...`).
func mkLock(rootInputs, nodes string) string {
	if nodes != "" {
		nodes = "," + nodes
	}
	return `{"version":7,"root":"root","nodes":{"root":{"inputs":` + rootInputs + `}` + nodes + `}}`
}

func lockedNode(name, rev string, lm int) string {
	return fmt.Sprintf(`%q:{"locked":{"rev":%q,"lastModified":%d}}`, name, rev, lm)
}

// relockLock builds a lock with the given direct root inputs (name -> rev).
func relockLock(lm int, revs map[string]string) string {
	inputs := map[string]string{}
	var nodes []string
	for n, rev := range revs {
		inputs[n] = n
		nodes = append(nodes, lockedNode(n, rev, lm))
	}
	b, _ := json.Marshal(inputs)
	return mkLock(string(b), strings.Join(nodes, ","))
}

func TestDiffFlakeLocks(t *testing.T) {
	sib := func(rev string, lm int) string { return lockedNode("sib", rev, lm) }
	tests := []struct {
		name          string
		before, after string
		want          []inputChange
		wantErr       string
	}{
		{
			"rev moved", mkLock(`{"sib":"sib"}`, sib(revA, 1)), mkLock(`{"sib":"sib"}`, sib(revB, 2)),
			[]inputChange{{"sib", "1111111", "2222222"}},
			"",
		},
		{"lastModified only", mkLock(`{"sib":"sib"}`, sib(revA, 1)), mkLock(`{"sib":"sib"}`, sib(revA, 9)), nil, ""},
		{
			"follows input", mkLock(`{"sib":"sib","f":["sib","x"]}`, sib(revA, 1)), mkLock(`{"sib":"sib","f":["sib","x"]}`, sib(revB, 1)),
			[]inputChange{{"sib", "1111111", "2222222"}},
			"",
		},
		{
			"new root input", mkLock(`{"sib":"sib"}`, sib(revA, 1)),
			mkLock(`{"sib":"sib","newin":"newin"}`, sib(revA, 1)+","+lockedNode("newin", revC, 1)),
			[]inputChange{{"newin", "(new)", "3333333"}},
			"",
		},
		{
			"removed root input", mkLock(`{"sib":"sib","old":"old"}`, sib(revA, 1)+","+lockedNode("old", revC, 1)),
			mkLock(`{"sib":"sib"}`, sib(revA, 1)),
			[]inputChange{{"old", "3333333", "(removed)"}},
			"",
		},
		{
			"tarball input without rev",
			mkLock(`{"t":"t"}`, `"t":{"locked":{"type":"tarball","narHash":"sha256-abcdefghijklmnopqrstuvwx"}}`),
			mkLock(`{"t":"t"}`, `"t":{"locked":{"type":"tarball","narHash":"sha256-ABCDEFGHIJKLMNOPQRSTUVWX"}}`),
			[]inputChange{{"t", "abcdefghijkl", "ABCDEFGHIJKL"}},
			"",
		},
		{
			"structure only", mkLock(`{"sib":"sib"}`, sib(revA, 1)), mkLock(`{"sib":"sib","f":["sib","x"]}`, sib(revA, 1)),
			[]inputChange{{"(lock structure)", "", ""}},
			"",
		},
		{"malformed JSON", "{", mkLock(`{}`, ""), nil, "parse"},
		{"missing node", mkLock(`{"sib":"sib"}`, sib(revA, 1)), mkLock(`{"sib":"sib","ghost":"ghost"}`, sib(revA, 1)), nil, "ghost"},
		{
			"custom root key",
			`{"version":7,"root":"main","nodes":{"main":{"inputs":{"sib":"sib"}},` + sib(revA, 1) + `}}`,
			`{"version":7,"root":"main","nodes":{"main":{"inputs":{"sib":"sib"}},` + sib(revB, 1) + `}}`,
			[]inputChange{{"sib", "1111111", "2222222"}},
			"",
		},
		{
			"transitive lastModified churn",
			mkLock(`{"sib":"sib"}`, `"sib":{"inputs":{"dep":"dep"},"locked":{"rev":"`+revA+`"}},`+lockedNode("dep", revB, 1)),
			mkLock(`{"sib":"sib"}`, `"sib":{"inputs":{"dep":"dep"},"locked":{"rev":"`+revA+`"}},`+lockedNode("dep", revB, 77)), nil, "",
		},
		{
			"three moved sorted by name",
			relockLock(1, map[string]string{"zeta": revA, "alpha": revA, "mid": revA}),
			relockLock(2, map[string]string{"zeta": revB, "alpha": revB, "mid": revB}),
			[]inputChange{{"alpha", "1111111", "2222222"}, {"mid", "1111111", "2222222"}, {"zeta", "1111111", "2222222"}},
			"",
		},
		{
			"path input without rev or narHash",
			mkLock(`{"p":"p"}`, `"p":{"locked":{"type":"path","path":"./x"}}`),
			mkLock(`{"p":"p"}`, `"p":{"locked":{"type":"path","path":"./y"}}`),
			[]inputChange{{"p", "(no rev)", "(no rev)"}},
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := diffFlakeLocks([]byte(tc.before), []byte(tc.after))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("diffFlakeLocks: %v", err)
			}
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlakeUpdateCommitMessage(t *testing.T) {
	got := flakeUpdateCommitMessage([]inputChange{
		{"gone", "3333333", "(removed)"},
		{"newin", "(new)", "3333333"},
		{"sib", "1111111", "2222222"},
	})
	want := "chore(deps): nix flake update\n\ngone 3333333 -> (removed)\nnewin (new) -> 3333333\nsib 1111111 -> 2222222"
	if got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	structural := flakeUpdateCommitMessage([]inputChange{{"(lock structure)", "", ""}})
	if want := "chore(deps): nix flake update\n\n(lock structure) changed"; structural != want {
		t.Errorf("structural message = %q, want %q", structural, want)
	}
	// Name order regardless of the order given.
	unordered := flakeUpdateCommitMessage([]inputChange{{"b", "1", "2"}, {"a", "1", "2"}})
	if !strings.Contains(unordered, "a 1 -> 2\nb 1 -> 2") {
		t.Errorf("not in name order: %q", unordered)
	}
}

// --- relockAllInputs against real git ----------------------------------------

func relockRun(t *testing.T, r *fsNixRunner, dir, flakeRel string) (int, string, error) {
	t.Helper()
	ws := &Workspace{runner: r}
	var out strings.Builder
	n, err := ws.relockAllInputs(context.Background(), &out, "foo", dir, flakeRel)
	return n, out.String(), err
}

func noSpaceStep() nixStep {
	res := exec.Result{ExitCode: 1, Stderr: []byte("error: writing to file: No space left on device")}
	return nixStep{res: res, err: &exec.CommandError{Name: "nix", Result: res}}
}

func failStep(stderr string) nixStep {
	res := exec.Result{ExitCode: 1, Stderr: []byte(stderr)}
	return nixStep{res: res, err: &exec.CommandError{Name: "nix", Result: res}}
}

func readFileT(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRelockAll_BumpsAndCommits(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }}
	n, _, err := relockRun(t, r, dir, "flake.nix")
	if err != nil {
		t.Fatalf("relock: %v", err)
	}
	if n != 1 {
		t.Errorf("changed = %d, want 1", n)
	}
	calls := r.nixCalls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].Args, []string{"flake", "update", "--refresh"}) || calls[0].Dir != dir {
		t.Errorf("nix calls = %+v", calls)
	}
	if got := headSubject(t, dir); got != "chore(deps): nix flake update" {
		t.Errorf("subject = %q", got)
	}
	if files := commitFiles(t, dir); !reflect.DeepEqual(files, []string{"flake.lock"}) {
		t.Errorf("files = %v", files)
	}
	assertCleanTree(t, dir)
}

func TestRelockAll_NoCommitOnLastModifiedChurn(t *testing.T) {
	orig := relockLock(1, map[string]string{"sib": revA})
	dir, writeLock := propEnv(t, "flake.nix", orig)
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(relockLock(99, map[string]string{"sib": revA})) }}
	n, _, err := relockRun(t, r, dir, "flake.nix")
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0, nil", n, err)
	}
	if c := commitCount(t, dir); c != 1 {
		t.Errorf("commit count = %d, want 1", c)
	}
	assertCleanTree(t, dir)
}

func TestRelockAll_NixWroteNothing(t *testing.T) {
	dir, _ := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner()}
	n, _, err := relockRun(t, r, dir, "flake.nix")
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if c := commitCount(t, dir); c != 1 {
		t.Errorf("commit count = %d, want 1", c)
	}
	assertCleanTree(t, dir)
}

func TestRelockAll_StructuralOnlyChangeCommits(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", mkLock(`{"sib":"sib"}`, lockedNode("sib", revA, 1)))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(mkLock(`{"sib":"sib","f":["sib","x"]}`, lockedNode("sib", revA, 1)))
	}}
	n, _, err := relockRun(t, r, dir, "flake.nix")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if c := commitCount(t, dir); c != 2 {
		t.Errorf("commit count = %d, want 2", c)
	}
	if body := runGitT(t, dir, "log", "-1", "--format=%b"); !strings.Contains(body, "(lock structure) changed") {
		t.Errorf("body = %q", body)
	}
}

func TestRelockAll_NewInputCommits(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(relockLock(1, map[string]string{"sib": revA, "newin": revC}))
	}}
	n, _, err := relockRun(t, r, dir, "flake.nix")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if c := commitCount(t, dir); c != 2 {
		t.Errorf("commit count = %d, want 2", c)
	}
	if body := runGitT(t, dir, "log", "-1", "--format=%b"); !strings.Contains(body, "newin (new) -> 3333333") {
		t.Errorf("body = %q", body)
	}
}

func TestRelockAll_NoFlakeLockIsNoOp(t *testing.T) {
	dir, _ := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	if err := os.Remove(filepath.Join(dir, "flake.lock")); err != nil {
		t.Fatal(err)
	}
	r := &fsNixRunner{real: exec.NewRealRunner()}
	n, out, err := relockRun(t, r, dir, "flake.nix")
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %+v, want none", r.calls)
	}
	if !strings.Contains(out, "no flake.lock — skipping (pn does not bootstrap a lock)") {
		t.Errorf("out = %q", out)
	}
}

func TestRelockAll_RetriesOnce(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner(), steps: []nixStep{
		failStep("unable to download"),
		{mutate: func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }},
	}}
	n, out, err := relockRun(t, r, dir, "flake.nix")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(r.nixCalls()) != 2 {
		t.Errorf("nix calls = %d, want 2", len(r.nixCalls()))
	}
	if c := commitCount(t, dir); c != 2 {
		t.Errorf("commit count = %d, want 2", c)
	}
	if !strings.Contains(out, "    [foo] relock all flake inputs: attempt 1 failed, retrying once") {
		t.Errorf("out = %q", out)
	}
}

func TestRelockAll_CancelledContextNoRetry(t *testing.T) {
	orig := relockLock(1, map[string]string{"sib": revA})
	dir, writeLock := propEnv(t, "flake.nix", orig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := failStep("interrupted")
	st.mutate = func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }
	st.cancel = cancel
	r := &fsNixRunner{real: exec.NewRealRunner(), steps: []nixStep{st}}
	ws := &Workspace{runner: r}
	_, err := ws.relockAllInputs(ctx, io.Discard, "foo", dir, "flake.nix")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, errRelockRestore) {
		t.Errorf("err wraps errRelockRestore: %v", err)
	}
	if len(r.nixCalls()) != 1 {
		t.Errorf("nix calls = %d, want 1", len(r.nixCalls()))
	}
	if got := readFileT(t, filepath.Join(dir, "flake.lock")); got != orig {
		t.Errorf("flake.lock not restored: %q", got)
	}
}

func TestRelockAll_RestoresLockOnEveryErrorAfterNix(t *testing.T) {
	orig := relockLock(1, map[string]string{"sib": revA})
	moved := relockLock(2, map[string]string{"sib": revB})
	tests := []struct {
		name    string
		newLock string
		setup   func(t *testing.T)
		check   func(t *testing.T, err error)
	}{
		{"unparseable new lock", "not json", func(t *testing.T) {}, func(t *testing.T, err error) {
			if err == nil {
				t.Error("want error")
			}
		}},
		{"openGitMutator fails", moved, func(t *testing.T) { stubGitMutatorOpener(t, map[string]*fakeGitMutator{}) }, func(t *testing.T, err error) {
			if err == nil {
				t.Error("want error")
			}
		}},
		{"add fails", moved, func(t *testing.T) {
			prev := openGitMutator
			openGitMutator = func(ctx context.Context, dir string) (gitMutator, error) {
				m, err := prev(ctx, dir)
				return addFailMutator{m}, err
			}
			t.Cleanup(func() { openGitMutator = prev })
		}, func(t *testing.T, err error) {
			if err == nil {
				t.Error("want error")
			}
		}},
		{"write-back fails", "not json", func(t *testing.T) {
			prev := writeLockFile
			writeLockFile = func(string, []byte, os.FileMode) error { return errors.New("disk on fire") }
			t.Cleanup(func() { writeLockFile = prev })
		}, func(t *testing.T, err error) {
			if !errors.Is(err, errRelockRestore) {
				t.Errorf("err = %v, want errRelockRestore", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, writeLock := propEnv(t, "flake.nix", orig)
			tc.setup(t)
			r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(tc.newLock) }}
			_, _, err := relockRun(t, r, dir, "flake.nix")
			tc.check(t, err)
			if tc.name == "write-back fails" {
				return // the write-back is what failed; the file is expectedly dirty
			}
			if got := readFileT(t, filepath.Join(dir, "flake.lock")); got != orig {
				t.Errorf("flake.lock not restored: %q", got)
			}
		})
	}
}

func TestRelockAll_DisablesFsmonitorForNix(t *testing.T) {
	tests := []struct {
		name      string
		count     string
		set       bool
		idx       string
		wantCount string
	}{
		{"count already 1", "1", true, "1", "2"},
		{"unset", "", false, "0", "1"},
		{"non-numeric counts as 0", "junk", true, "0", "1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("GIT_CONFIG_COUNT", tc.count)
			} else {
				t.Setenv("GIT_CONFIG_COUNT", "")
				os.Unsetenv("GIT_CONFIG_COUNT")
			}
			dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
			r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }}
			if _, _, err := relockRun(t, r, dir, "flake.nix"); err != nil {
				t.Fatal(err)
			}
			env := r.nixCalls()[0].Env
			if env["GIT_CONFIG_KEY_"+tc.idx] != "core.fsmonitor" || env["GIT_CONFIG_VALUE_"+tc.idx] != "false" || env["GIT_CONFIG_COUNT"] != tc.wantCount {
				t.Errorf("env = %v", env)
			}
		})
	}
}

func TestRelockAll_RemovesStaleFsmonitorSocket(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *gitAnswer
		wantStop int
	}{
		{"fsmonitor true", &gitAnswer{out: "true\n"}, 1},
		{"fsmonitor unset", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
			gitDir := runGitT(t, dir, "rev-parse", "--absolute-git-dir")
			sock := filepath.Join(gitDir, "fsmonitor--daemon.ipc")
			if err := os.WriteFile(sock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			socketAtNix := true
			r := &fsNixRunner{real: exec.NewRealRunner(), fsmonConfig: tc.cfg, gitDir: &gitAnswer{out: gitDir + "\n"}}
			r.steps = []nixStep{{mutate: func() {
				_, err := os.Stat(sock)
				socketAtNix = err == nil
			}}}
			if _, _, err := relockRun(t, r, dir, "flake.nix"); err != nil {
				t.Fatal(err)
			}
			if socketAtNix {
				t.Error("socket still present when nix ran")
			}
			stops, nixAt := 0, -1
			for i, c := range r.calls {
				if c.Name == "nix" {
					nixAt = i
				}
				if c.Name == "git" && strings.HasSuffix(strings.Join(c.Args, " "), "fsmonitor--daemon stop") {
					stops++
					if nixAt >= 0 {
						t.Errorf("stop recorded after the nix call")
					}
				}
			}
			if stops != tc.wantStop {
				t.Errorf("stop calls = %d, want %d", stops, tc.wantStop)
			}
		})
	}
}

func TestRelockAll_FailsAfterRetry(t *testing.T) {
	orig := relockLock(1, map[string]string{"sib": revA})
	dir, writeLock := propEnv(t, "flake.nix", orig)
	rewrite := func(s nixStep) nixStep {
		s.mutate = func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }
		return s
	}
	r := &fsNixRunner{real: exec.NewRealRunner(), steps: []nixStep{rewrite(failStep("unable to download")), rewrite(failStep("unable to download"))}}
	_, _, err := relockRun(t, r, dir, "flake.nix")
	if err == nil {
		t.Fatal("want error")
	}
	if len(r.nixCalls()) != 2 {
		t.Errorf("nix calls = %d, want 2", len(r.nixCalls()))
	}
	if c := commitCount(t, dir); c != 1 {
		t.Errorf("commit count = %d", c)
	}
	if got := readFileT(t, filepath.Join(dir, "flake.lock")); got != orig {
		t.Errorf("flake.lock not restored: %q", got)
	}
	assertCleanTree(t, dir)
}

func TestRelockAll_NoSpaceAbortsWithoutRetry(t *testing.T) {
	orig := relockLock(1, map[string]string{"sib": revA})
	dir, writeLock := propEnv(t, "flake.nix", orig)
	st := noSpaceStep()
	st.mutate = func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }
	r := &fsNixRunner{real: exec.NewRealRunner(), steps: []nixStep{st}}
	_, _, err := relockRun(t, r, dir, "flake.nix")
	if !errors.Is(err, errRelockNoSpace) {
		t.Fatalf("err = %v, want errRelockNoSpace", err)
	}
	if len(r.nixCalls()) != 1 {
		t.Errorf("nix calls = %d, want 1", len(r.nixCalls()))
	}
	if got := readFileT(t, filepath.Join(dir, "flake.lock")); got != orig {
		t.Errorf("flake.lock not restored: %q", got)
	}
	assertCleanTree(t, dir)
}

func TestRelockAll_CommitFailureRestoresCleanTree(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'config file not found' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }}
	_, _, err := relockRun(t, r, dir, "flake.nix")
	if err == nil {
		t.Fatal("want error")
	}
	if c := commitCount(t, dir); c != 1 {
		t.Errorf("commit count = %d", c)
	}
	assertCleanTree(t, dir)
}

func TestRelockAll_FlakeInSubdir(t *testing.T) {
	dir, writeLock := propEnv(t, "nix/flake.nix", relockLock(1, map[string]string{"sib": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }}
	n, _, err := relockRun(t, r, dir, "nix/flake.nix")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := r.nixCalls()[0].Dir; got != filepath.Join(dir, "nix") {
		t.Errorf("nix Dir = %q", got)
	}
	if files := commitFiles(t, dir); !reflect.DeepEqual(files, []string{"nix/flake.lock"}) {
		t.Errorf("files = %v", files)
	}
}

func TestRelockAll_GitMutatorFailures(t *testing.T) {
	tests := []struct {
		name string
		fake *fakeGitMutator
	}{
		{"commit fails to start", &fakeGitMutator{commitErr: errors.New("cannot start")}},
		{"reset also fails", &fakeGitMutator{commitErr: errors.New("cannot start"), resetHardErr: errors.New("reset boom")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
			stubGitMutatorOpener(t, map[string]*fakeGitMutator{dir: tc.fake})
			r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() { writeLock(relockLock(2, map[string]string{"sib": revB})) }}
			_, _, err := relockRun(t, r, dir, "flake.nix")
			if err == nil {
				t.Fatal("want error")
			}
			if tc.fake.resetHardCalls != 1 {
				t.Errorf("ResetHard calls = %d, want 1", tc.fake.resetHardCalls)
			}
		})
	}
}

func TestRelockAll_NoSpaceOnRetry(t *testing.T) {
	dir, _ := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner(), steps: []nixStep{failStep("unable to download"), noSpaceStep()}}
	_, _, err := relockRun(t, r, dir, "flake.nix")
	if !errors.Is(err, errRelockNoSpace) {
		t.Fatalf("err = %v", err)
	}
	if len(r.nixCalls()) != 2 {
		t.Errorf("nix calls = %d, want 2", len(r.nixCalls()))
	}
}

func TestRelockAll_PrintsSuccessLine(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA, "nixpkgs": revA}))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(relockLock(2, map[string]string{"sib": revB, "nixpkgs": revB}))
	}}
	_, out, err := relockRun(t, r, dir, "flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  → foo: nix flake update moved 2 input(s): nixpkgs, sib\n"; !strings.Contains(out, want) {
		t.Errorf("out = %q, want it to contain %q", out, want)
	}
	if !bytes.Contains([]byte(out), []byte("nixpkgs, sib")) {
		t.Errorf("names not in order")
	}
}

// When rev-parse fails or prints nothing, the socket removal is skipped: the
// real git dir's socket stays and no path is built relative to the current
// directory (a file of the same name there must survive).
func TestRelockAll_SkipsSocketRemovalWithoutGitDir(t *testing.T) {
	tests := []struct {
		name string
		ans  *gitAnswer
	}{
		{"rev-parse fails", &gitAnswer{err: errors.New("fatal: not a git repository")}},
		{"rev-parse prints nothing", &gitAnswer{out: "\n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := propEnv(t, "flake.nix", relockLock(1, map[string]string{"sib": revA}))
			sock := filepath.Join(runGitT(t, dir, "rev-parse", "--absolute-git-dir"), "fsmonitor--daemon.ipc")
			if err := os.WriteFile(sock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			stray := filepath.Join(cwd, "fsmonitor--daemon.ipc")
			if err := os.WriteFile(stray, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Chdir(cwd)
			r := &fsNixRunner{real: exec.NewRealRunner(), gitDir: tc.ans}
			if _, _, err := relockRun(t, r, dir, "flake.nix"); err != nil {
				t.Fatalf("relock: %v", err)
			}
			for _, p := range []string{sock, stray} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("%s was removed: %v", p, err)
				}
			}
		})
	}
}
