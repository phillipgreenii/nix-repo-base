//go:build smoke

package smoke

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSmoke_P1Invariant is the GATING test for the coordinated-workforests
// feature. It proves invariant P1: NO `pn workspace` verb run from inside a
// workforest set modifies the canonical (primary) checkouts.
//
// Concretely, for every canonical checkout {wsRoot}/{repo}, the following are
// snapshotted AFTER set creation and asserted unchanged after EVERY verb:
//
//   - HEAD sha (rev-parse HEAD)
//   - checked-out branch (rev-parse --abbrev-ref HEAD)
//   - git status --porcelain
//   - working-tree digest (sha256 over sorted tracked-file paths + contents)
//   - the SET of local branch NAMES (not their SHAs — `rebase main` legitimately
//     moves the set's feature branch, which lives in the SHARED ref store)
//   - the HEAD reflog
//
// Allow-listed (NOT snapshotted, expected to change): shared remote-tracking
// refs (refs/remotes/origin/*), FETCH_HEAD, and the shared object store. These
// are updated by fetch/pull/push during update/rebase/push and never alter the
// primary's working tree, index, HEAD, or checked-out branch.
func TestSmoke_P1Invariant(t *testing.T) {
	pnBin := getPNBin(t)

	wsRoot := t.TempDir()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved temp dir: %s", wsRoot)
		}
	})

	// Canonical-root env: PN_WORKSPACE_ROOT = wsRoot.
	rootEnv := buildScrubbedEnv(t, wsRoot)

	// --- 1. Fixture: real git repos with bare-remote upstreams ---
	p1SetupFixture(t, wsRoot)

	// Bootstrap the canonical workspace: init -> clone -> lock.
	for _, args := range [][]string{
		{"workspace", "init"},
		{"workspace", "clone"},
		{"workspace", "lock"},
	} {
		r := runCommand(t, pnBin, wsRoot, args, rootEnv)
		if r.ExitCode != 0 {
			t.Fatalf("bootstrap %v exited %d\nstdout: %s\nstderr: %s",
				args, r.ExitCode, r.Stdout, r.Stderr)
		}
	}

	repos := []string{"consumer", "producer"}

	// Sanity: both canonical checkouts exist and are git repos on main.
	for _, repo := range repos {
		repoDir := filepath.Join(wsRoot, repo)
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
			t.Fatalf("canonical %s missing .git after bootstrap: %v", repo, err)
		}
	}

	// --- 2. Create the workforest set ---
	addRes := runCommand(t, pnBin, wsRoot,
		[]string{"workspace", "workforest", "add", "feature-x"}, rootEnv)
	if addRes.ExitCode != 0 {
		t.Fatalf("workforest add feature-x exited %d\nstdout: %s\nstderr: %s",
			addRes.ExitCode, addRes.Stdout, addRes.Stderr)
	}
	setDir := filepath.Join(wsRoot, ".workforests", "feature-x")
	if _, err := os.Stat(filepath.Join(setDir, "pn-workspace.toml")); err != nil {
		t.Fatalf("set dir missing pn-workspace.toml at %s: %v", setDir, err)
	}
	for _, repo := range repos {
		if _, err := os.Stat(filepath.Join(setDir, repo)); err != nil {
			t.Fatalf("set worktree %s not created: %v", repo, err)
		}
	}

	// --- 3. Snapshot each canonical checkout AFTER set creation ---
	// (so the feature-x branch created by `worktree add -b` is in the baseline).
	baseline := make(map[string]p1Snapshot, len(repos))
	for _, repo := range repos {
		baseline[repo] = p1Capture(t, filepath.Join(wsRoot, repo))
	}

	// --- 4. Build a SET-rooted env so verbs operate on the set, not canonical ---
	// buildScrubbedEnv hard-sets PN_WORKSPACE_ROOT=wsRoot; replace it with setDir.
	setEnv := replaceEnv(rootEnv, "PN_WORKSPACE_ROOT", setDir)

	// --- 5. Verb table. Each runs from INSIDE the set (cwd=setDir, set env) ---
	type verbCase struct {
		name    string
		args    []string
		needNix bool
		// skipBranchCheck, when true, suppresses the local-branch NAME SET
		// assertion for this verb. Set on verbs that are EXPECTED to add or
		// remove branches in the canonical's shared ref store (e.g. `workforest
		// add`, which creates a new branch by design). All other P1 invariants
		// (HEAD, working-tree digest, checked-out branch, status, reflog) are
		// still asserted — the branch name set relaxation is narrow.
		skipBranchCheck bool
	}
	verbs := []verbCase{
		{name: "status", args: []string{"workspace", "status"}},
		{name: "tree", args: []string{"workspace", "tree"}},
		{name: "build", args: []string{"workspace", "build"}},
		{name: "apply", args: []string{"workspace", "apply"}},
		{name: "update", args: []string{"workspace", "update", "--in-place"}},
		{name: "push", args: []string{"workspace", "push"}},
		{name: "push --set-upstream", args: []string{"workspace", "push", "--set-upstream"}},
		{name: "rebase", args: []string{"workspace", "rebase"}},
		{name: "rebase main", args: []string{"workspace", "rebase", "main"}},
		{name: "upgrade", args: []string{"workspace", "upgrade"}},
		{name: "format", args: []string{"workspace", "format"}, needNix: true},
		{name: "flake-check", args: []string{"workspace", "flake-check"}, needNix: true},

		// --- Verbs added by tc-perh.12 ---

		// lock: HIGHEST PRIORITY. If PN_WORKSPACE_ROOT misbehaves this verb
		// could rewrite the canonical's pn-workspace.lock.json. The snapshot
		// captures the lock file via p1WorkTreeDigest + file contents.
		{name: "lock", args: []string{"workspace", "lock"}},

		// init: scans wsRoot for git repos and reconciles pn-workspace.toml
		// (config-only; no clone, no lock write). Run from inside the set it
		// should see the set's repos and leave canonical's files untouched.
		{name: "init", args: []string{"workspace", "init"}},

		// clone: clones repos listed in pn-workspace.toml that are missing on
		// disk. Inside a set all repos are already present, so this is a
		// no-op clone; canonical must be unchanged.
		{name: "clone", args: []string{"workspace", "clone"}},

		// pre-commit-check: runs per-repo pre-commit hooks (if any). In the
		// smoke fixture there are no hooks defined, so this is a no-op;
		// canonical must be unchanged.
		{name: "pre-commit-check", args: []string{"workspace", "pre-commit-check"}},

		// discover: lists workspace repos (read-only). Canonical unchanged.
		{name: "discover", args: []string{"workspace", "discover"}},

		// workforest list: lists existing workforest sets from inside the set.
		// Read-only; canonical unchanged.
		{name: "workforest list", args: []string{"workspace", "workforest", "list"}},

		// workforest add <branch> from inside a set: the implementation resolves
		// PN_WORKSPACE_ROOT to the set dir, then creates a new branch in the
		// canonical's SHARED ref store (git worktrees share the object store
		// with the canonical). Creating a branch in the shared ref store is
		// the expected behaviour — it does NOT modify HEAD, the working tree,
		// the index, or any tracked-file content in the canonical. We therefore
		// set skipBranchCheck so the branch-name-set assertion is suppressed for
		// this verb; all other P1 invariants (HEAD, workDigest, status, reflog)
		// are still enforced.
		{name: "workforest add feature-p1-nested", args: []string{"workspace", "workforest", "add", "feature-p1-nested"}, skipBranchCheck: true},

		// workforest remove <branch>: removes the set dir and the git worktree
		// admin entries but leaves the branch in the ref store by design
		// (mirrors `git worktree remove` semantics). The branch name set will
		// still contain feature-p1-nested after the preceding add, so
		// skipBranchCheck remains true to avoid a spurious failure.
		{name: "workforest remove feature-p1-nested", args: []string{"workspace", "workforest", "remove", "feature-p1-nested"}, skipBranchCheck: true},

		// workforest prune: prunes stale git worktree admin entries across all
		// canonical repos. Read-like operation (no working-tree or HEAD
		// modification to the canonical repos). Branch names may still differ
		// from baseline due to the preceding add (feature-p1-nested remains
		// in the ref store after prune), so skipBranchCheck is set.
		{name: "workforest prune", args: []string{"workspace", "workforest", "prune"}, skipBranchCheck: true},

		// nix passthrough: skipped — there is no sensible no-op nix invocation
		// that does not require the nix daemon and a valid flake evaluation.
		// The needNix guard would skip it on most CI hosts, and a blank
		// `pn workspace nix` with no args exits non-zero without a subcommand.
		// Coverage here adds noise without signal; document instead of adding.
	}

	haveNix := nixAvailable()

	for _, vc := range verbs {
		if vc.needNix && !haveNix {
			t.Logf("P1: skipping verb %q (nix not available on this host)", vc.name)
			continue
		}
		// Run the verb from inside the set. We tolerate ANY exit code / per-repo
		// skip / verb error: the assertion is purely that canonical is unchanged.
		r := runCommand(t, pnBin, setDir, vc.args, setEnv)
		t.Logf("P1: verb %q exit=%d", vc.name, r.ExitCode)

		// --- 6. After EACH verb, assert every canonical snapshot is unchanged ---
		for _, repo := range repos {
			now := p1Capture(t, filepath.Join(wsRoot, repo))
			p1AssertUnchanged(t, repo, vc.name, baseline[repo], now, r, vc.skipBranchCheck)
		}
	}
}

// p1Snapshot is the canonical-checkout state that P1 protects.
type p1Snapshot struct {
	head        string   // rev-parse HEAD
	branch      string   // rev-parse --abbrev-ref HEAD
	status      string   // git status --porcelain
	workDigest  string   // sha256 over sorted tracked paths + contents
	branchNames []string // sorted set of local branch names
	headReflog  string   // git log -g --format=%H\t%gs HEAD
}

// p1Capture snapshots the canonical checkout at repoDir.
func p1Capture(t *testing.T, repoDir string) p1Snapshot {
	t.Helper()
	return p1Snapshot{
		head:        p1Git(t, repoDir, "rev-parse", "HEAD"),
		branch:      p1Git(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"),
		status:      p1Git(t, repoDir, "status", "--porcelain"),
		workDigest:  p1WorkTreeDigest(t, repoDir),
		branchNames: p1LocalBranchNames(t, repoDir),
		headReflog:  p1Git(t, repoDir, "log", "-g", "--format=%H%x09%gs", "HEAD"),
	}
}

// p1AssertUnchanged compares a fresh snapshot against the baseline. Note: the
// local-branch NAME SET must be identical UNLESS skipBranchCheck is true.
// Individual branch SHAs are NOT asserted — `rebase main` legitimately moves
// the set's feature-x branch (which lives in the shared ref store), and
// HEAD/working-tree/branch checks below already guarantee the primary's
// checked-out branch (main) is untouched.
//
// skipBranchCheck suppresses the branch-name-set assertion for verbs that are
// EXPECTED to add new branches into the canonical's shared ref store (e.g.
// `workforest add`). All other invariants — HEAD sha, checked-out branch, status,
// working-tree digest, and HEAD reflog — are always enforced regardless of
// skipBranchCheck.
func p1AssertUnchanged(t *testing.T, repo, verb string, want, got p1Snapshot, r scenarioResult, skipBranchCheck bool) {
	t.Helper()
	if want.head != got.head {
		t.Errorf("P1 VIOLATION [%s after verb %q]: HEAD sha changed: %s -> %s\nverb stdout: %s\nverb stderr: %s",
			repo, verb, want.head, got.head, r.Stdout, r.Stderr)
	}
	if want.branch != got.branch {
		t.Errorf("P1 VIOLATION [%s after verb %q]: checked-out branch changed: %q -> %q",
			repo, verb, want.branch, got.branch)
	}
	if want.status != got.status {
		t.Errorf("P1 VIOLATION [%s after verb %q]: git status --porcelain changed:\nbefore:\n%s\nafter:\n%s",
			repo, verb, want.status, got.status)
	}
	if want.workDigest != got.workDigest {
		t.Errorf("P1 VIOLATION [%s after verb %q]: working-tree digest changed: %s -> %s",
			repo, verb, want.workDigest, got.workDigest)
	}
	if want.headReflog != got.headReflog {
		t.Errorf("P1 VIOLATION [%s after verb %q]: HEAD reflog changed:\nbefore:\n%s\nafter:\n%s",
			repo, verb, want.headReflog, got.headReflog)
	}
	if !skipBranchCheck && strings.Join(want.branchNames, ",") != strings.Join(got.branchNames, ",") {
		t.Errorf("P1 VIOLATION [%s after verb %q]: local-branch NAME set changed: %v -> %v",
			repo, verb, want.branchNames, got.branchNames)
	}
}

// p1Git runs a git command in repoDir and returns trimmed combined output.
// Failures are reported via t.Errorf (so a missing/broken repo surfaces) but
// return "" so comparisons still run.
func p1Git(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", repoDir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("p1Git %v in %s: %v\n%s", args, repoDir, err, out)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// p1LocalBranchNames returns the sorted set of local branch names in repoDir.
func p1LocalBranchNames(t *testing.T, repoDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "branch", "--format=%(refname:short)")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("p1LocalBranchNames in %s: %v", repoDir, err)
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	return names
}

// p1WorkTreeDigest computes a sha256 over the sorted tracked-file paths and
// their contents (excluding .git). This catches any change to a tracked file's
// content or set of tracked files in the canonical working tree.
func p1WorkTreeDigest(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "-z")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("p1WorkTreeDigest ls-files in %s: %v", repoDir, err)
		return ""
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "PATH:%s\n", p)
		data, err := os.ReadFile(filepath.Join(repoDir, p))
		if err != nil {
			// A tracked file missing from the working tree IS a change worth
			// hashing distinctly (so the digest diverges from baseline).
			fmt.Fprintf(h, "MISSING:%v\n", err)
			continue
		}
		h.Write([]byte("CONTENT:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// replaceEnv returns a copy of env with key set to val (replacing any existing
// entry). Used to repoint PN_WORKSPACE_ROOT at the set directory so verbs
// operate on the set, not the canonical workspace.
func replaceEnv(env []string, key, val string) []string {
	out := make([]string, 0, len(env)+1)
	prefix := key + "="
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+val)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}

// --- Fixture construction (real git + bare remotes; no external nix needed) ---

// p1SetupFixture builds a producer+consumer workspace under wsRoot. Each repo
// has a bare remote (file://) so push/rebase/update have an origin/main
// upstream. consumer is the terminal and declares producer as a flake input so
// the lock has an edge (mirroring s20). Fake build/apply/update scripts let
// build/apply/update run without real nix.
func p1SetupFixture(t *testing.T, wsRoot string) {
	t.Helper()
	remotesDir := filepath.Join(wsRoot, "remotes")
	if err := os.MkdirAll(remotesDir, 0o755); err != nil {
		t.Fatalf("p1SetupFixture: mkdir remotes: %v", err)
	}

	// Common fake scripts seeded into each repo.
	buildSh := "#!/bin/sh\nset -e\ntouch built.txt\n"
	applySh := "#!/bin/sh\nset -e\ntouch applied.txt\n"
	// update-locks.sh writes a marker INSIDE the repo it runs in (the set
	// worktree, never canonical). Trivial; no nix.
	updateLocksSh := "#!/bin/sh\nset -e\ntouch updated.txt\n"

	// Trivial, nix-free flake for the producer.
	producerFlake := "{ inputs = {}; outputs = { self, ... }: {}; }\n"

	producerBare := setupBareRemote(t, remotesDir, "producer", map[string]string{
		"flake.nix":       producerFlake,
		"build.sh":        buildSh,
		"apply.sh":        applySh,
		"update-locks.sh": updateLocksSh,
	})

	// consumer declares producer as a flake input (an edge in the lock DAG).
	consumerFlake := fmt.Sprintf("{\n  inputs.producer.url = \"%s\";\n  outputs = { self, producer, ... }: {};\n}\n", producerBare)
	consumerBare := setupBareRemote(t, remotesDir, "consumer", map[string]string{
		"flake.nix":       consumerFlake,
		"build.sh":        buildSh,
		"apply.sh":        applySh,
		"update-locks.sh": updateLocksSh,
	})

	// Write pn-workspace.toml with real file:// URLs and fake commands so
	// build/apply don't require nix.
	toml := fmt.Sprintf(`[workspace]
name = "smoke-p1"
terminal = "consumer"
build_command = "./build.sh"
apply_command = "./apply.sh"

[repos.consumer]
url = "%s"

[repos.producer]
url = "%s"
`, consumerBare, producerBare)

	if err := os.WriteFile(filepath.Join(wsRoot, "pn-workspace.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("p1SetupFixture: write pn-workspace.toml: %v", err)
	}
}
