package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNeedsRebuild_Force(t *testing.T) {
	w := &Workspace{runner: exec.NewFakeRunner()}
	got, err := w.needsRebuild(context.Background(), []repoDir{{keyPath: "/x", gitDir: "/x"}}, true, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("force should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_DirtyTree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte(" M file\n")}, nil)
	w := &Workspace{runner: f}
	got, err := w.needsRebuild(context.Background(), []repoDir{{keyPath: "/repo", gitDir: "/repo"}}, false, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("dirty tree should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_ReadsNewStore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const dir = "/repo" // a fake repo dir; the runner is faked, so it need not exist
	f := exec.NewFakeRunner()
	// clean working tree; HEAD == the applied_ref we seed below
	f.AddResponse("git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc123\n")}, nil)
	w := &Workspace{runner: f}
	// seed the new store so HEAD matches -> should SKIP
	if err := writeAppliedState(dir, AppliedState{AppliedRef: "abc123"}); err != nil {
		t.Fatal(err)
	}
	rebuild, err := w.needsRebuild(context.Background(), []repoDir{{keyPath: dir, gitDir: dir}}, false, &bytes.Buffer{})
	if err != nil || rebuild {
		t.Fatalf("clean + matching applied_ref should skip rebuild; rebuild=%v err=%v", rebuild, err)
	}
}

func TestCheckNixDaemon_ErrorPath(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, &exec.CommandError{Name: "nix", Args: []string{"eval"}, Result: exec.Result{ExitCode: 1}})
	w := &Workspace{runner: f}
	if err := w.checkNixDaemon(context.Background()); err == nil {
		t.Fatal("expected daemon-check error")
	}
}

func TestMarkApplied_WriteFailIsReturned(t *testing.T) {
	// Point XDG_DATA_HOME at a regular file (not a dir) so writeAppliedState's
	// MkdirAll under it fails.
	bad := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", bad)
	const dir = "/repo"
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	f.AddResponse("git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	w := &Workspace{runner: f}
	dirs := []repoDir{{name: "leaf", keyPath: dir, gitDir: dir}}
	if err := w.markApplied(context.Background(), dirs, "leaf", dir, nil, io.Discard); err == nil {
		t.Fatal("markApplied must return the store-write error (fail-closed)")
	}
}

// depSpec describes one non-terminal repo for markAppliedFixture. alias is the
// terminal's flake input name for it (empty ⇒ the terminal does not consume it as
// an input at all, so no lock edge); rev is what the terminal's flake.lock pins
// for that alias (empty ⇒ the lock node carries no rev); url overrides the default
// remote so same-named repos under different owners can be built. noClone declares
// the repo in the toml and the lock but creates NO directory for it, which is the
// one state where the apply's override set is a STRICT subset of the lock edges:
// nix cannot be pointed at a clone that is not there, so that input is genuinely
// resolved from the terminal's flake.lock.
type depSpec struct {
	alias, rev, url string
	noClone         bool
}

// markAppliedFixture builds a workspace on disk for markApplied tests. The
// terminal is "leaf"; every entry in deps becomes a [repos.<key>] repo AND (when
// it has an alias) a pn-workspace.lock.json edge leaf --alias--> key — the same
// edge set apply derives its --override-input flags from — with leaf's flake.lock
// pinning that alias to the entry's rev. The FakeRunner is scripted so every
// repo's HEAD is "<key>-head" and every working tree is clean.
func markAppliedFixture(t *testing.T, deps map[string]depSpec) (*Workspace, *exec.FakeRunner, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	leafDir := mkRepoDir(t, root, "leaf")

	toml := "\n[workspace]\nterminal = \"leaf\"\n\n[repos.leaf]\nurl = \"github:owner/leaf\"\n"
	lockRepos := []string{`"leaf": {"flake_path": "flake.nix", "remote_url": "github:owner/leaf"}`}
	var lockEdges, rootInputs, lockNodes []string
	f := exec.NewFakeRunner()

	for _, key := range sortedDepKeys(deps) {
		d := deps[key]
		if !d.noClone {
			mkRepoDir(t, root, key)
		}
		url := d.url
		if url == "" {
			url = "github:owner/" + key
		}
		toml += fmt.Sprintf("\n[repos.%s]\nurl = %q\n", key, url)
		lockRepos = append(lockRepos, fmt.Sprintf(`%q: {"flake_path": "flake.nix", "remote_url": %q}`, key, url))
		if d.alias == "" {
			continue
		}
		lockEdges = append(lockEdges, fmt.Sprintf(`{"consumer":"leaf","alias":%q,"target":%q}`, d.alias, key))
		node := key + "-node"
		rootInputs = append(rootInputs, fmt.Sprintf("%q: %q", d.alias, node))
		locked := "{}"
		if d.rev != "" {
			locked = fmt.Sprintf(`{"rev": %q}`, d.rev)
		}
		lockNodes = append(lockNodes, fmt.Sprintf(`%q: {"locked": %s}`, node, locked))
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml)
	writeFile(t, filepath.Join(root, LockFileName), fmt.Sprintf(
		`{"terminal":"leaf","order":[],"repos":{%s},"edges":[%s]}`,
		strings.Join(lockRepos, ","), strings.Join(lockEdges, ","),
	))
	writeTerminalFlakeLock(t, leafDir, rootInputs, lockNodes)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, key := range append(sortedDepKeys(deps), "leaf") {
		dir := filepath.Join(root, key)
		f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte(key + "-head\n")}, nil)
		f.AddResponse("git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	}
	return w, f, leafDir
}

// writeTerminalFlakeLock writes the terminal's flake.lock: root.inputs[alias]
// names a node whose locked.rev is the rev the apply built that input from.
func writeTerminalFlakeLock(t *testing.T, leafDir string, rootInputs, lockNodes []string) {
	t.Helper()
	nodes := append([]string{fmt.Sprintf(`"root": {"inputs": {%s}}`, strings.Join(rootInputs, ","))}, lockNodes...)
	writeFile(t, filepath.Join(leafDir, "flake.lock"), fmt.Sprintf(`{"root":"root","nodes":{%s}}`, strings.Join(nodes, ",")))
}

func sortedDepKeys(deps map[string]depSpec) []string {
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runMarkApplied drives markApplied over every declared repo and returns what it
// wrote to out. The override set is derived exactly as Apply derives it — one
// resolveOverridesFor over the same lock — so these producer tests exercise the
// real relationship between the flags nix is given and the record written, rather
// than a hand-written map that could disagree with either.
func runMarkApplied(t *testing.T, w *Workspace, terminalNixDir string) string {
	t.Helper()
	return runMarkAppliedWith(t, w, terminalNixDir, overriddenInputs(w.resolveOverridesFor("leaf", overrideOpts{})))
}

// runMarkAppliedWith is runMarkApplied with an EXPLICIT override set, for the
// states the lock-derived set cannot produce in one fixture. The set is a
// parameter of markApplied precisely because it is sampled at the TOP of Apply,
// before the build, while markApplied runs after it: the two can legitimately
// disagree when a clone appears or vanishes inside that window, and the recorded
// evidence must describe the flags nix was actually given.
func runMarkAppliedWith(t *testing.T, w *Workspace, terminalNixDir string, overridden map[string]string) string {
	t.Helper()
	var out bytes.Buffer
	if err := w.markApplied(context.Background(), w.allRepoDirs(nil), "leaf", terminalNixDir, overridden, &out); err != nil {
		t.Fatalf("markApplied: %v", err)
	}
	return out.String()
}

func appliedStateOf(t *testing.T, w *Workspace, name string) AppliedState {
	t.Helper()
	st, ok, err := readAppliedState(w.appliedStateKeyPath(name))
	if err != nil || !ok {
		t.Fatalf("read applied state for %s: ok=%v err=%v", name, ok, err)
	}
	return st
}

// TestMarkApplied_RecordsTerminalLockedRevs is the PRODUCER half of the pg2-ft60a
// fix. A repo the terminal consumes as a flake input reaches the built system only
// via the terminal's flake.lock, so the apply MUST record the rev that lock pinned
// ("locked000") ALONGSIDE this checkout's local HEAD ("dep-head"). Recording only
// the local HEAD is the whole defect: with local HEAD ahead of the locked rev, an
// unpushed commit reported as applied and released a gated verification bead
// (pg2-c40r4).
//
// Against the pre-change code this fails: there is no locked_revs at all.
func TestMarkApplied_RecordsTerminalLockedRevs(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep": {alias: "depalias", rev: "locked000"},
	})
	runMarkApplied(t, w, leafDir)

	st := appliedStateOf(t, w, "dep")
	if st.Schema != appliedStateSchema {
		t.Fatalf("schema = %d, want %d — consumers branch on it to tell 'no lock info' from 'not an input'",
			st.Schema, appliedStateSchema)
	}
	if st.AppliedRef != "dep-head" {
		t.Fatalf("applied_ref = %q, want the local HEAD %q — ADR 0025 ADDS locked_revs and must NOT "+
			"redefine applied_ref (needsRebuild keys on it)", st.AppliedRef, "dep-head")
	}
	rev, isInput := st.LockedRevs["dep"]
	if !isInput {
		t.Fatalf("locked_revs = %v, want an entry for the terminal flake input %q", st.LockedRevs, "dep")
	}
	if rev != "locked000" {
		t.Fatalf("locked_revs[dep] = %q, want the terminal's LOCKED rev %q (local HEAD %q is not in the "+
			"built system until it is pushed and relocked)", rev, "locked000", "dep-head")
	}
}

// TestMarkApplied_TerminalHasNoLockedRevEntry pins the clause that keeps
// terminal-repo gates sound without a special case: the apply builds the terminal
// from its LOCAL directory ({terminal_nix_dir}), so there is no lock rev to record
// for it and the consumer's lock condition is SKIPPED on a missing entry.
func TestMarkApplied_TerminalHasNoLockedRevEntry(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep": {alias: "depalias", rev: "locked000"},
	})
	runMarkApplied(t, w, leafDir)

	st := appliedStateOf(t, w, "leaf")
	if st.AppliedRef != "leaf-head" {
		t.Fatalf("terminal applied_ref = %q, want its local HEAD %q", st.AppliedRef, "leaf-head")
	}
	if _, isInput := st.LockedRevs["leaf"]; isInput {
		t.Fatalf("locked_revs must have NO entry for the terminal itself; got %v", st.LockedRevs)
	}
	// The apply's whole map is recorded in every record, so the terminal's record
	// still describes the build (it just makes no claim about the terminal).
	if st.LockedRevs["dep"] != "locked000" {
		t.Fatalf("locked_revs = %v, want the apply's full input map recorded in every record", st.LockedRevs)
	}
}

// TestMarkApplied_NotATerminalInputHasNoEntry covers a workspace repo that is not
// a flake input of the terminal: no alias, so no edge, so no entry — the same
// legitimate skip the terminal gets. This is the case whose value ("no entry") the
// schema version has to distinguish from an OLD record's absent map.
func TestMarkApplied_NotATerminalInputHasNoEntry(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{"dep": {}})
	runMarkApplied(t, w, leafDir)

	st := appliedStateOf(t, w, "dep")
	if _, isInput := st.LockedRevs["dep"]; isInput {
		t.Fatalf("a repo with no terminal edge must get NO locked_revs entry; got %v", st.LockedRevs)
	}
	if st.Schema != appliedStateSchema {
		t.Fatalf("schema = %d, want %d so 'no entry' reads as evidence, not as absence of evidence",
			st.Schema, appliedStateSchema)
	}
}

// TestMarkApplied_UnresolvableLockRevFailsClosedAudibly covers an input the
// terminal DOES declare but whose lock node carries no rev (a follows-only or
// path: input, or an unreadable flake.lock). The entry MUST still be written, with
// an EMPTY rev: dropping it would downgrade "the apply cannot say what it built
// this from" into the indistinguishable "not an input" skip, which is fail-OPEN.
// And it must be audible, because a silent unprovable apply is this bead's defect.
//
// The override set is explicitly EMPTY, which is what makes this the genuinely
// LOCK-BUILT case — the only case where an unresolvable rev has a consequence, and
// therefore the only case that warrants the warning (pg2-14yqh; the overridden
// counterpart is the next test).
func TestMarkApplied_UnresolvableLockRevFailsClosedAudibly(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep": {alias: "depalias"}, // edge exists; lock node has no rev
	})
	out := runMarkAppliedWith(t, w, leafDir, nil)

	st := appliedStateOf(t, w, "dep")
	rev, isInput := st.LockedRevs["dep"]
	if !isInput || rev != "" {
		t.Fatalf("locked_revs[dep] = %q present=%v; want a PRESENT entry with an EMPTY rev (fail closed)",
			rev, isInput)
	}
	if !strings.Contains(out, "depalias") || !strings.Contains(out, "stays blocked") {
		t.Fatalf("fail-closed must be observable, not silent; out=%q", out)
	}
}

// TestMarkApplied_UnresolvableLockRevOnAnOverriddenInputIsSilent is the other half
// of the warning's contract, and it exists because the warning makes a CLAIM ("a
// pn:applied gate on dep stays blocked") that stops being true once the apply
// overrode the input: the build read the local clone, so `pb`'s condition 2 is
// skipped for it and nothing is blocked (bead pg2-14yqh). Warning anyway would fire
// on every such apply and train the operator to ignore the warning — the failure
// mode agent-support ADR 0046 argues against for the gate's own reporting.
//
// The record itself is unchanged: the empty locked_revs entry is STILL written, so
// the fail-closed evidence survives for a later apply that does not override.
func TestMarkApplied_UnresolvableLockRevOnAnOverriddenInputIsSilent(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep": {alias: "depalias"}, // edge exists; lock node has no rev
	})
	out := runMarkApplied(t, w, leafDir) // dep's clone exists ⇒ derived set overrides it

	if strings.Contains(out, "stays blocked") {
		t.Fatalf("an OVERRIDDEN input's unresolvable lock rev blocks nothing, so it must not warn; out=%q", out)
	}
	st := appliedStateOf(t, w, "dep")
	if rev, isInput := st.LockedRevs["dep"]; !isInput || rev != "" {
		t.Fatalf("locked_revs[dep] = %q present=%v; the fail-closed entry must still be RECORDED — only the "+
			"warning is conditional", rev, isInput)
	}
	if _, wasOverridden := st.OverriddenInputs["dep"]; !wasOverridden {
		t.Fatalf("overridden_inputs = %v, want an entry for dep", st.OverriddenInputs)
	}
}

// TestMarkApplied_RecordsWhatTheApplyOverrode is the PRODUCER half of the pg2-14yqh
// ruling: option (c) makes `pb`'s gate condition 2 conditional on whether the repo
// was actually OVERRIDDEN in that apply, and nothing in the applied-state carried
// that fact. The recorded key set MUST be the apply's override set, and the value
// MUST be the local flake URL nix was pointed at — the same string the
// `--override-input` flag carried, so record and flag cannot describe different
// builds.
//
// Against the pre-change code this fails: there is no overridden_inputs at all, and
// the schema is 2.
func TestMarkApplied_RecordsWhatTheApplyOverrode(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep": {alias: "depalias", rev: "locked000"},
	})
	runMarkApplied(t, w, leafDir)

	st := appliedStateOf(t, w, "dep")
	if st.Schema != appliedStateSchema || appliedStateSchema < 3 {
		t.Fatalf("schema = %d, want %d (>= 3) — a consumer tells 'not overridden' from 'no override "+
			"information' by the version, not by the map being empty", st.Schema, appliedStateSchema)
	}
	url, wasOverridden := st.OverriddenInputs["dep"]
	if !wasOverridden {
		t.Fatalf("overridden_inputs = %v, want an entry for dep: apply passes --override-input for every "+
			"terminal lock edge whose clone exists, so dep was built from the LOCAL clone, not from "+
			"locked_revs[dep]=%q", st.OverriddenInputs, st.LockedRevs["dep"])
	}
	if want := "git+file://" + w.appliedStateKeyPath("dep"); url != want {
		t.Fatalf("overridden_inputs[dep] = %q, want %q — the recorded value must be the flake URL the "+
			"--override-input flag carried", url, want)
	}
	// The apply's whole map goes into every record, exactly as locked_revs does.
	if _, wasOverridden := appliedStateOf(t, w, "leaf").OverriddenInputs["dep"]; !wasOverridden {
		t.Fatalf("the terminal's record must carry the apply's full override map too")
	}
	// The TERMINAL is never overridden — it is built from its own directory, has no
	// self-edge, and so appears in neither map.
	if _, wasOverridden := appliedStateOf(t, w, "leaf").OverriddenInputs["leaf"]; wasOverridden {
		t.Fatalf("overridden_inputs must have NO entry for the terminal itself")
	}
}

// TestMarkApplied_MissingCloneIsNotOverridden pins the ONE state in which the
// override set is a STRICT subset of the terminal's lock edges, and it is the state
// that keeps `pb`'s condition 2 from becoming dead code: nix cannot be pointed at a
// clone that does not exist, so that input really is resolved from the terminal's
// flake.lock and its locked rev really is what the build carries.
func TestMarkApplied_MissingCloneIsNotOverridden(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"cloned":  {alias: "clonedalias", rev: "locked111"},
		"missing": {alias: "missingalias", rev: "locked222", noClone: true},
	})
	runMarkApplied(t, w, leafDir)

	st := appliedStateOf(t, w, "leaf")
	if _, isInput := st.LockedRevs["missing"]; !isInput {
		t.Fatalf("locked_revs = %v, want an entry for the un-cloned terminal input: the lock EDGE exists "+
			"whether or not the clone does", st.LockedRevs)
	}
	if _, wasOverridden := st.OverriddenInputs["missing"]; wasOverridden {
		t.Fatalf("overridden_inputs = %v must NOT contain an un-cloned repo — nix was given no "+
			"--override-input for it, so it was built from the lock", st.OverriddenInputs)
	}
	if _, wasOverridden := st.OverriddenInputs["cloned"]; !wasOverridden {
		t.Fatalf("overridden_inputs = %v, want an entry for the cloned input", st.OverriddenInputs)
	}
}

// TestMarkApplied_SameRepoNameDifferentOwners pins the OWNER half of the mapping
// decision. Two workspace repos can share a repo NAME under different owners —
// this workspace really does — so a mapping keyed on the repo name, or on
// flake.lock's `locked.repo` alone, would cross them. Mapping through the workspace
// lock's per-edge alias (whose edges were matched on the full host/owner/repo
// canonical URL) keeps them distinct: each repo MUST get the rev pinned for ITS
// OWN alias.
func TestMarkApplied_SameRepoNameDifferentOwners(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep-a": {alias: "alias-a", rev: "reva0000", url: "github:ownerA/dep"},
		"dep-b": {alias: "alias-b", rev: "revb0000", url: "github:ownerB/dep"},
	})
	runMarkApplied(t, w, leafDir)
	for repo, want := range map[string]string{"dep-a": "reva0000", "dep-b": "revb0000"} {
		if got := appliedStateOf(t, w, repo).LockedRevs[repo]; got != want {
			t.Fatalf("locked_revs[%s] = %q, want %q — same-named repos under different owners must "+
				"not be crossed", repo, got, want)
		}
	}
}

// TestMarkApplied_RecordsLockAtApplyTimeNotLater is the ORDERING-HOLE test on the
// producer side, and it is the reason the ruling records the lock WITH the apply
// rather than reading it at query time. Apply at T1 with the lock pinning "rev-t1";
// relock to "rev-t2" at T2 > T1 WITHOUT applying. The T1 record MUST still say
// "rev-t1" — otherwise the later relock retroactively makes the T1 build look as
// though it contained code only the T2 lock names, which is the same false resolve
// in a narrower window.
func TestMarkApplied_RecordsLockAtApplyTimeNotLater(t *testing.T) {
	w, _, leafDir := markAppliedFixture(t, map[string]depSpec{
		"dep": {alias: "depalias", rev: "rev-t1"},
	})
	runMarkApplied(t, w, leafDir) // the T1 apply

	// T2: the terminal is relocked forward. No apply runs.
	writeTerminalFlakeLock(t, leafDir,
		[]string{`"depalias": "dep-node"`}, []string{`"dep-node": {"locked": {"rev": "rev-t2"}}`})

	if got := appliedStateOf(t, w, "dep").LockedRevs["dep"]; got != "rev-t1" {
		t.Fatalf("locked_revs[dep] = %q after a later relock, want the T1 apply's %q — the recorded "+
			"value must describe the build that ran, not the lock as it stands now", got, "rev-t1")
	}
	// Asserted through the PUBLISHED projection too, because that is what a consumer
	// reads. A `pn workspace info` that resolved the rev LIVE instead of reporting
	// the recorded one would be the rejected "is the lock NOW past the commit?"
	// design, and it would report rev-t2 here.
	info, err := w.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, r := range info.Repos {
		if r.Name != "dep" {
			continue
		}
		if !r.TerminalInput || r.LockedRev != "rev-t1" {
			t.Fatalf("info dep: terminal_input=%v locked_rev=%q; want true/%q — info must publish the "+
				"rev RECORDED with the apply, never the current lock", r.TerminalInput, r.LockedRev, "rev-t1")
		}
	}
}
