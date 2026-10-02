//go:build smoke

package smoke

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// --- S37: per-clone hook bundle acceptance matrix (pg2-pla9d.10) ---
//
// The scenario's setup.sh builds a canonical workspace (producer + consumer)
// and a stand-in `nix` that lays out the installer's on-disk shape; command.txt
// then bootstraps it (init, allow, clone, lock, status) and adds the workforest
// set feature-x. This function drives the matrix from there.

// s37Run runs `name args...` in dir with env and returns trimmed combined
// output, failing the test on a non-zero exit.
func s37Run(t *testing.T, env []string, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("S37: %s %v in %s: %v\n%s", name, args, dir, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

// s37Commit commits a new file in dir (the stand-in pre-commit stub runs) and
// returns the stand-in runner's log line for that commit.
func s37Commit(t *testing.T, env []string, dir, hookLog, file string) string {
	t.Helper()
	before := s37Lines(t, hookLog)
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s37Run(t, env, dir, "git", "add", file)
	s37Run(t, env, dir, "git", "commit", "-q", "-m", "S37 "+file)
	after := s37Lines(t, hookLog)
	if len(after) != len(before)+1 {
		t.Fatalf("S37: committing %s in %s must run the pre-commit stand-in exactly once; log went from %d to %d lines:\n%s",
			file, dir, len(before), len(after), strings.Join(after, "\n"))
	}
	return after[len(after)-1]
}

// s37Lines returns the non-empty lines of a file ("" and nil when absent).
func s37Lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// s37Field extracts key=value from a stand-in runner line (values hold no space).
func s37Field(line, key string) string {
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}

func s37WantState(t *testing.T, what, dir string, want workspace.HookBundleState) workspace.HookBundleInfo {
	t.Helper()
	got, info, err := workspace.ReadHookBundleState(dir)
	if err != nil {
		t.Fatalf("S37: %s: ReadHookBundleState(%s): %v", what, dir, err)
	}
	if got != want {
		t.Fatalf("S37: %s: hook bundle state = %q, want %q (%+v)", what, got, want, info)
	}
	return info
}

func assertS37HookBundleMatrix(t *testing.T, wsRoot, pnBin string, env []string) {
	t.Helper()
	real, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	canon := filepath.Join(real, "consumer")
	setDir := filepath.Join(real, ".workforests", "feature-x")
	setConsumer := filepath.Join(setDir, "consumer")
	setProducer := filepath.Join(setDir, "producer")
	plain := filepath.Join(real, "plain-wt")
	hookLog := filepath.Join(wsRoot, "hook-runs.log")
	nixLog := filepath.Join(wsRoot, "nix-calls.log")
	canonPg := filepath.Join(canon, ".git", "pg-hooks")
	setPg := filepath.Join(canon, ".git", "worktrees", "consumer", "pg-hooks")

	// 1. The installs pn ran: the canonical one without installer args, the
	// set's with --private and the set's producer pin.
	var canonCalls, setCalls []string
	for _, l := range s37Lines(t, nixLog) {
		switch {
		case strings.Contains(l, "FLAKE="+canon+" "), strings.Contains(l, "FLAKE="+filepath.Join(wsRoot, "consumer")+" "):
			canonCalls = append(canonCalls, l)
		case strings.Contains(l, "FLAKE="+setConsumer+" "), strings.Contains(l, "FLAKE="+filepath.Join(wsRoot, ".workforests", "feature-x", "consumer")+" "):
			setCalls = append(setCalls, l)
		default:
			t.Errorf("S37: install call for an unexpected flake dir: %s", l)
		}
	}
	if len(canonCalls) != 1 {
		t.Fatalf("S37: want exactly one canonical install (the gate skips the rest); got %d:\n%s", len(canonCalls), strings.Join(s37Lines(t, nixLog), "\n"))
	}
	if !strings.Contains(canonCalls[0], "PRIVATE=0") {
		t.Errorf("S37: the canonical install must not be private: %s", canonCalls[0])
	}
	if strings.Contains(canonCalls[0], "OVERRIDES= ") {
		t.Errorf("S37: the canonical install must carry no recorded pins: %s", canonCalls[0])
	}
	if len(setCalls) != 1 || !strings.Contains(setCalls[0], "PRIVATE=1") ||
		!strings.Contains(setCalls[0], "producer="+filepath.Join(wsRoot, ".workforests", "feature-x", "producer")) {
		t.Fatalf("S37: want one private set install pinned to the set's producer; got %v", setCalls)
	}
	s37WantState(t, "canonical clone", canon, workspace.HookBundlePresent)
	info := s37WantState(t, "set consumer", setConsumer, workspace.HookBundlePresent)
	if !info.Private || !info.Linked {
		t.Errorf("S37: the set consumer must be on its own private bundle: %+v", info)
	}

	// 2. A plain worktree (no pn): it rides the shared bundle.
	s37Run(t, env, canon, "git", "worktree", "add", "-q", "-b", "plain", plain)
	if info := s37WantState(t, "plain worktree", plain, workspace.HookBundlePresent); info.Private {
		t.Errorf("S37: a plain worktree must use the shared bundle: %+v", info)
	}

	// 3. Each tree commits with the stand-in hook; each acts on its own tree.
	line := s37Commit(t, env, canon, hookLog, "from-canonical.txt")
	if s37Field(line, "tree") != canon || s37Field(line, "version") != "v1" ||
		!strings.HasPrefix(s37Field(line, "bundle"), canonPg+"/") {
		t.Errorf("S37: canonical commit ran the wrong hook: %s", line)
	}
	sharedBundle := s37Field(line, "bundle")
	line = s37Commit(t, env, plain, hookLog, "from-plain.txt")
	if s37Field(line, "tree") != plain || s37Field(line, "version") != "v1" || s37Field(line, "bundle") != sharedBundle {
		t.Errorf("S37: the plain worktree must run in its own tree on the shared bundle %s: %s", sharedBundle, line)
	}
	line = s37Commit(t, env, setConsumer, hookLog, "from-set.txt")
	if s37Field(line, "tree") != setConsumer || s37Field(line, "version") != "v1" ||
		!strings.HasPrefix(s37Field(line, "bundle"), setPg+"/") {
		t.Errorf("S37: the set must run in its own tree on its private bundle under %s: %s", setPg, line)
	}
	privateBefore := s37Field(line, "bundle")

	// 4. The producer changes inside the set (the canonical producer does not).
	if err := os.WriteFile(filepath.Join(setProducer, "hook-version"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s37Run(t, env, setProducer, "git", "add", "hook-version")
	s37Run(t, env, setProducer, "git", "commit", "-q", "-m", "S37 producer v2")
	s37WantState(t, "set consumer after a producer change", setConsumer, workspace.HookBundleStale)
	s37WantState(t, "canonical clone after a producer change in the set", canon, workspace.HookBundlePresent)
	s37WantState(t, "plain worktree after a producer change in the set", plain, workspace.HookBundlePresent)

	// 5. The next pn event in the set rebuilds ONLY the set's private bundle
	// (the gate re-keys on the changed override); a second event is a no-op.
	callsBefore := len(s37Lines(t, nixLog))
	// Trust is keyed by the root path as written (no symlink resolution), so the
	// set root pn sees must be spelled the way `workforest add` spelled it.
	rawSetDir := filepath.Join(wsRoot, ".workforests", "feature-x")
	setEnvFull := setEnv(env, "PN_WORKSPACE_ROOT", rawSetDir)
	for i := 0; i < 2; i++ {
		want := callsBefore + 1
		r := runCommand(t, pnBin, rawSetDir, []string{"workspace", "status"}, setEnvFull)
		if r.ExitCode != 0 {
			t.Fatalf("S37: workspace status in the set (run %d) exited %d\nstdout: %s\nstderr: %s", i+1, r.ExitCode, r.Stdout, r.Stderr)
		}
		if got := len(s37Lines(t, nixLog)); got != want {
			t.Fatalf("S37: after status run %d: %d install calls, want %d\n%s\nstdout: %s\nstderr: %s", i+1, got, want, strings.Join(s37Lines(t, nixLog), "\n"), r.Stdout, r.Stderr)
		}
	}
	s37WantState(t, "set consumer after the rebuild", setConsumer, workspace.HookBundlePresent)

	// 6. The set now runs the new producer from a NEW private generation; the
	// canonical clone and the plain worktree still run the shared v1 bundle.
	line = s37Commit(t, env, setConsumer, hookLog, "from-set-2.txt")
	if s37Field(line, "version") != "v2" || s37Field(line, "bundle") == privateBefore ||
		!strings.HasPrefix(s37Field(line, "bundle"), setPg+"/") {
		t.Errorf("S37: after the producer change the set must run v2 from a new private generation (was %s): %s", privateBefore, line)
	}
	line = s37Commit(t, env, canon, hookLog, "from-canonical-2.txt")
	if s37Field(line, "version") != "v1" || s37Field(line, "bundle") != sharedBundle {
		t.Errorf("S37: the producer change in the set leaked into the canonical clone: %s", line)
	}
	line = s37Commit(t, env, plain, hookLog, "from-plain-2.txt")
	if s37Field(line, "version") != "v1" || s37Field(line, "bundle") != sharedBundle {
		t.Errorf("S37: the producer change in the set leaked into the plain worktree: %s", line)
	}

	// 7. Nothing was written into any working tree, and no core.hooksPath.
	for what, tree := range map[string]string{
		"canonical consumer": canon, "plain worktree": plain, "set consumer": setConsumer,
		"canonical producer": filepath.Join(real, "producer"), "set producer": setProducer,
	} {
		if st := s37Run(t, env, tree, "git", "status", "--porcelain", "--ignored"); st != "" {
			t.Errorf("S37: %s working tree has untracked, modified or ignored files:\n%s", what, st)
		}
		for _, name := range []string{".pre-commit-config.yaml", ".githooks", "pg-hooks"} {
			if _, err := os.Lstat(filepath.Join(tree, name)); err == nil {
				t.Errorf("S37: %s: %s was written into the working tree", what, name)
			}
		}
	}
	for what, dir := range map[string]string{"canonical consumer": canon, "set consumer": setConsumer} {
		cmd := exec.Command("git", "config", "--local", "--get", "core.hooksPath")
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.Output(); err == nil {
			t.Errorf("S37: %s has core.hooksPath set to %q", what, strings.TrimSpace(string(out)))
		}
	}
}
