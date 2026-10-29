package main

// Real-nix tests of `pn workspace flake-check --no-build` (bead pg2-8uc2d), and
// of pre-land-eval as its caller.
//
// Every fixture is real flakes in real git repositories under one workspace root,
// driven through the real pn binary this package builds and the host's real nix.
// Nothing is mocked: a mock of nix would only prove the argument plumbing (the Go
// unit tests do that), not what nix does with the arguments, which is the point
// (global rule V-3). They need a working nix daemon, which a nix build sandbox does
// not have, so they SKIP there (and where nix is absent); a developer or an agent
// run executes them.
//
// The fixtures pin, one test each:
//   - the check evaluates the derivation and builds nothing, and writes no flake.lock;
//   - a source tree copied into the store during evaluation (the case where nix's
//     own read-only `--no-build` says "path '...' is not valid") gives the same
//     verdict on a cold and a warm store;
//   - a consumer whose lock pins a sibling that lacks an option the consumer needs
//     FAILS against its locked inputs and PASSES with pn's sibling overrides;
//   - the working tree, a worktree path and --rev select what is evaluated;
//   - the experiment log line has the documented shape for a pass and a failure;
//   - pre-land-eval, with the real pn behind it, gives the verdicts the land step
//     reads.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
)

const preLandEvalScript = "../../../pg-hooks/pre-land-eval/pre-land-eval.sh"

const fixtureSiblingFlake = `{
  description = "fixture sibling";
  outputs = _: { lib.options = { @@OPTIONS@@ }; };
}
`

// The consumer throws unless the sibling's options include dockerState. Its check
// derivation's builder FAILS, so a build of it can never be mistaken for success.
const fixtureConsumerFlake = `{
  description = "fixture consumer";
  inputs.sibling.url = "git+file://@@SIBLING@@";
  outputs = { self, sibling }:
    let
      sys = "@@SYSTEM@@";
      verdict =
        if sibling.lib.options ? dockerState then "ok"
        else throw "The option phillipgreenii.observability.dockerState does not exist (fixture)";
    in {
      checks.${sys}.cfg = derivation {
        name = "cfg-${verdict}-@@UNIQ@@";
        system = sys;
        builder = "/bin/sh";
        args = [ "-c" "exit 1" ];
      };
    };
}
`

// A flake with no inputs whose check derivation is whatever @@BODY@@ evaluates to.
const fixtureLoneFlake = `{
  description = "fixture lone repo";
  outputs = { self }:
    let sys = "@@SYSTEM@@"; in {
      checks.${sys}.cfg = derivation {
        name = "cfg-@@UNIQ@@";
        system = sys;
        builder = "/bin/sh";
        args = [ "-c" "exit 1" @@ARGS@@ ];
      };
      @@EXTRA@@
    };
}
`

// nixEnv is the environment for every subprocess the test starts: no GIT_*
// location variables, no global git config, no inherited workspace root, telemetry
// off, and nix's and pn's per-user directories redirected into the test's temp dir.
func nixEnv(scratch string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_"),
			strings.HasPrefix(kv, "PN_WORKSPACE_ROOT="),
			strings.HasPrefix(kv, "WORKSPACE_ROOT="),
			strings.HasPrefix(kv, "XDG_CACHE_HOME="),
			strings.HasPrefix(kv, "XDG_CONFIG_HOME="),
			strings.HasPrefix(kv, "XDG_STATE_HOME="),
			strings.HasPrefix(kv, "XDG_DATA_HOME="):
		default:
			env = append(env, kv)
		}
	}
	env = append(
		env,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"XDG_CACHE_HOME="+filepath.Join(scratch, "xdg-cache"),
		"XDG_CONFIG_HOME="+filepath.Join(scratch, "xdg-config"),
		"XDG_STATE_HOME="+filepath.Join(scratch, "xdg-state"),
		"XDG_DATA_HOME="+filepath.Join(scratch, "xdg-data"),
		"OTEL_SDK_DISABLED=true", "PG_NIX_LOG_DISABLE=1",
		"NIX_CONFIG=experimental-features = nix-command flakes\n",
	)
	return append(env, extra...)
}

// pathWithout returns PATH with every entry that holds an executable named tool
// removed, so a pn installed on the developer's machine cannot leak into a run
// that must not see one.
func pathWithout(tool string) string {
	var keep []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if st, err := os.Stat(filepath.Join(dir, tool)); err == nil && !st.IsDir() {
			continue
		}
		keep = append(keep, dir)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// requireRealNix skips unless a working nix is reachable: not inside a nix
// builder (NIX_BUILD_TOP: no daemon), nix on PATH, and a trivial flakes-enabled
// evaluation succeeds.
func requireRealNix(t *testing.T, scratch string) {
	t.Helper()
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("inside a nix builder: there is no nix daemon to evaluate against")
	}
	nix, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix is not on PATH")
	}
	probe := exec.Command(nix, "eval", "--expr", "1")
	probe.Env = nixEnv(scratch)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("nix is on PATH but cannot evaluate (%v): %s", err, strings.TrimSpace(string(out)))
	}
}

func runIn(t *testing.T, dir string, env []string, name string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("running %s %v: %v", name, args, err)
	}
	return so.String(), se.String(), code
}

func fileDigest(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// nbEnv is a fixture workspace: the scratch dir, the workspace root holding the
// repos, the environment for pn, and the nix system.
type nbEnv struct {
	scratch, root, sys string
	env                []string
	uniq               string
}

func newNBEnv(t *testing.T) *nbEnv {
	t.Helper()
	scratch := t.TempDir()
	if real, err := filepath.EvalSymlinks(scratch); err == nil {
		scratch = real
	}
	requireRealNix(t, scratch)
	sys, _, code := runIn(t, scratch, nixEnv(scratch), "nix", "eval", "--raw", "--impure", "--expr", "builtins.currentSystem")
	if code != 0 || sys == "" {
		t.Fatalf("cannot learn the nix system (exit %d)", code)
	}
	pnDir := filepath.Dir(pnBinary)
	return &nbEnv{
		scratch: scratch,
		root:    filepath.Join(scratch, "ws"),
		sys:     sys,
		env:     nixEnv(scratch, "PATH="+pnDir+string(os.PathListSeparator)+pathWithout("pn")),
		uniq:    fmt.Sprintf("%d", time.Now().UnixNano()),
	}
}

func (e *nbEnv) log() string {
	return filepath.Join(e.scratch, "xdg-state", "pn", "flake-check-no-build.jsonl")
}

func (e *nbEnv) pn(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	return runIn(t, dir, e.env, pnBinary, append([]string{"workspace"}, args...)...)
}

// writeWorkspace writes the pn-workspace.toml that makes repos siblings.
func (e *nbEnv) writeWorkspace(t *testing.T, terminal string, repos map[string]string) {
	t.Helper()
	toml := "[workspace]\nid = \"pg2-8uc2d-fixture\"\nterminal = \"" + terminal + "\"\n"
	names := make([]string, 0, len(repos))
	for n := range repos {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		toml += "\n[repos." + n + "]\nurl = \"git+file://" + repos[n] + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(e.root, "pn-workspace.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *nbEnv) logLines(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(e.log())
	if err != nil {
		t.Fatalf("no experiment log at %s: %v", e.log(), err)
	}
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

// lone creates a one-repo workspace whose flake is fixtureLoneFlake with the given
// extra arguments to the builder and extra outputs.
func (e *nbEnv) lone(t *testing.T, args, extra string, files map[string]string) *gitfixture.Repo {
	t.Helper()
	repo, err := gitfixture.NewRepo(context.Background(), e.root, gitfixture.RepoOptions{Suite: t.Name(), Name: "lone"})
	if err != nil {
		t.Fatal(err)
	}
	flake := strings.NewReplacer("@@SYSTEM@@", e.sys, "@@UNIQ@@", e.uniq, "@@ARGS@@", args, "@@EXTRA@@", extra).Replace(fixtureLoneFlake)
	all := map[string]string{"flake.nix": flake}
	for k, v := range files {
		all[k] = v
	}
	if _, err := repo.Commit(context.Background(), "lone: flake", all); err != nil {
		t.Fatal(err)
	}
	e.writeWorkspace(t, "lone", map[string]string{"lone": repo.Dir})
	return repo
}

func TestFlakeCheckNoBuild_RealNix_EvaluatesTheDerivationAndBuildsNothing(t *testing.T) {
	e := newNBEnv(t)
	repo := e.lone(t, "", "", nil)
	out, se, code := e.pn(t, repo.Dir, "flake-check", "--no-build", "lone")
	if code != 0 {
		t.Fatalf("flake-check --no-build: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, se)
	}
	// It evaluated the derivation: the checks set reports it.
	if want := "checks." + e.sys + ": ok, 1 derivations evaluated"; !strings.Contains(out, want) {
		t.Errorf("stdout should report the evaluated check (%q):\n%s", want, out)
	}
	// And it built nothing: the check's output path is NOT in the store, and a build of it fails.
	outPath, se2, c := runIn(t, repo.Dir, e.env, "nix", "eval", "--raw", ".#checks."+e.sys+".cfg.outPath")
	if c != 0 {
		t.Fatalf("cannot read the check's outPath: %s", se2)
	}
	if _, _, c := runIn(t, repo.Dir, e.env, "nix", "path-info", outPath); c == 0 {
		t.Errorf("%s is in the store: the check was BUILT", outPath)
	}
	if _, _, c := runIn(t, repo.Dir, e.env, "nix", "build", "--no-link", ".#checks."+e.sys+".cfg"); c == 0 {
		t.Fatal("the fixture check must fail when built, or this test proves nothing")
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "flake.lock")); err == nil {
		t.Error("a flake.lock appeared: the check must never write one")
	}
	if st, _, _ := runIn(t, repo.Dir, e.env, "git", "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("the working tree is dirty after the run:\n%s", st)
	}
}

func TestFlakeCheckNoBuild_RealNix_ASourceCopyAtEvalTimeGivesTheSameVerdictOnAColdAndAWarmStore(t *testing.T) {
	ctx := context.Background()
	e := newNBEnv(t)
	// builtins.path copies ./data into the store during evaluation and the flake READS the
	// copy. The copy's store path depends on the name and the content, both unique to this run,
	// so the first evaluation starts COLD. In nix's own read-only `flake check --no-build` the
	// copy is never written, so reading it fails with "path '...' is not valid" (measured on
	// nix 2.34.9, 2026-10-23).
	copyName := "fixture-data-" + e.uniq
	repo := e.lone(
		t,
		`(builtins.readFile "${builtins.path { path = ./data; name = "`+copyName+`"; }}/payload.txt")`,
		"",
		map[string]string{"data/payload.txt": "payload one " + e.uniq + "\n"},
	)
	verdict := func(label string) {
		t.Helper()
		start := time.Now()
		out, se, code := e.pn(t, repo.Dir, "flake-check", "--no-build", "lone")
		t.Logf("%s: exit %d in %s", label, code, time.Since(start).Round(time.Millisecond))
		if code != 0 {
			t.Fatalf("%s: exit %d, want 0 (the same verdict every time)\nstdout:\n%s\nstderr:\n%s", label, code, out, se)
		}
	}

	// 1. COLD: this copy has never been made.
	verdict("cold store")
	// 2. WARM: the first run left the copy in the store.
	verdict("warm store")
	// 3. COLD AGAIN: the payload changes, so the copy is a different store path that does not exist.
	if _, err := repo.Commit(ctx, "lone: new payload", map[string]string{"data/payload.txt": "payload two " + e.uniq + "\n"}); err != nil {
		t.Fatal(err)
	}
	// The premise, probed on this cold store rather than asserted (it is nix's behavior, not pn's,
	// and a newer nix may change it): nix's own read-only mode cannot read a copy it did not write.
	_, se, c := runIn(t, repo.Dir, e.env, "nix", "flake", "check", "--no-build")
	t.Logf("premise probe: nix's own `flake check --no-build` on the cold store exited %d (expected non-zero, 'is not valid'): %s", c, lastErrorLine(se))
	verdict("cold store again, after the store changed")
	// 4. The same state again, now warm: unchanged.
	verdict("warm store again")
}

func lastErrorLine(s string) string {
	last := ""
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "error:") {
			last = strings.TrimSpace(l)
		}
	}
	return last
}

// driftFixture is the sibling-drift case that made nix-ziprecruiter fail pre-land-eval
// ("The option ...dockerState does not exist"): the consumer's flake.lock pins the sibling at
// its FIRST commit (no dockerState), and the local sibling clone moved on to a second commit
// that has it. It returns the consumer and sibling repositories.
func (e *nbEnv) driftFixture(t *testing.T) (consumer, sibling *gitfixture.Repo) {
	t.Helper()
	ctx := context.Background()
	sibling, err := gitfixture.NewRepo(ctx, e.root, gitfixture.RepoOptions{Suite: t.Name(), Name: "sibling"})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err = sibling.NewSibling(ctx, "consumer", gitfixture.RepoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sibling.Commit(ctx, "sibling: no options", map[string]string{
		"flake.nix": strings.Replace(fixtureSiblingFlake, "@@OPTIONS@@", "", 1),
	}); err != nil {
		t.Fatal(err)
	}
	consumerFlake := strings.NewReplacer("@@SIBLING@@", sibling.Dir, "@@SYSTEM@@", e.sys, "@@UNIQ@@", e.uniq).Replace(fixtureConsumerFlake)
	if _, err := consumer.Commit(ctx, "consumer: flake", map[string]string{"flake.nix": consumerFlake}); err != nil {
		t.Fatal(err)
	}
	if _, se, code := runIn(t, consumer.Dir, e.env, "nix", "flake", "lock"); code != 0 {
		t.Fatalf("nix flake lock failed (exit %d): %s", code, se)
	}
	lockText, err := os.ReadFile(filepath.Join(consumer.Dir, "flake.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Commit(ctx, "consumer: lock pins the old sibling", map[string]string{"flake.lock": string(lockText)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sibling.Commit(ctx, "sibling: adds dockerState", map[string]string{
		"flake.nix": strings.Replace(fixtureSiblingFlake, "@@OPTIONS@@", "dockerState = true;", 1),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Client.Run(ctx, "config", "pgii-integrate-branch.preLandEval", "true"); err != nil {
		t.Fatal(err)
	}
	e.writeWorkspace(t, "consumer", map[string]string{"sibling": sibling.Dir, "consumer": consumer.Dir})
	return consumer, sibling
}

func TestFlakeCheckNoBuild_RealNix_SiblingDriftFailsAgainstTheLockAndPassesWithPnsOverrides(t *testing.T) {
	ctx := context.Background()
	e := newNBEnv(t)
	consumer, sibling := e.driftFixture(t)
	lockPath := filepath.Join(consumer.Dir, "flake.lock")
	lockBefore := fileDigest(t, lockPath)
	siblingHead, err := sibling.Client.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Plain nix eval of the drvPaths WITHOUT overrides, against the locked (old) sibling: FAILS.
	_, se, code := runIn(t, consumer.Dir, e.env, "nix", "eval", "--no-write-lock-file", "--json",
		".#checks."+e.sys, "--apply", noBuildApply)
	if code == 0 || !strings.Contains(se, "dockerState") {
		t.Fatalf("without overrides the check must fail on the missing option: exit %d\n%s", code, se)
	}

	// pn workspace flake-check --no-build, ONE repo: PASSES (the local sibling has the option).
	out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "consumer")
	if code != 0 {
		t.Fatalf("with pn's overrides: exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out, se)
	}
	if !strings.Contains(out, "--override-input sibling git+file://"+sibling.Dir) {
		t.Errorf("the reproduce line should carry the override pn computed:\n%s", out)
	}
	// The whole workspace, in topological order (the sibling first): also passes.
	out, se, code = e.pn(t, consumer.Dir, "flake-check", "--no-build")
	if code != 0 {
		t.Fatalf("whole workspace: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, se)
	}
	if i, j := strings.Index(out, "flake-check --no-build sibling"), strings.Index(out, "flake-check --no-build consumer"); i < 0 || j < 0 || i > j {
		t.Errorf("repos must be checked in topological order, sibling then consumer:\n%s", out)
	}

	// No push, no relock, no lock write: nothing moved.
	if got := fileDigest(t, lockPath); got != lockBefore {
		t.Error("flake.lock changed: the check must never write it")
	}
	if st, _, _ := runIn(t, consumer.Dir, e.env, "git", "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("the consumer working tree is dirty after the runs:\n%s", st)
	}
	if head, err := sibling.Client.Run(ctx, "rev-parse", "HEAD"); err != nil || !bytes.Equal(head, siblingHead) {
		t.Errorf("the sibling HEAD moved (err %v)", err)
	}
	if remotes, _, _ := runIn(t, consumer.Dir, e.env, "git", "remote"); strings.TrimSpace(remotes) != "" {
		t.Errorf("the fixture has a remote %q: a push would have been possible", remotes)
	}
	if _, err := os.Stat(filepath.Join(e.root, "pn-workspace.lock.json")); err == nil {
		t.Error("pn wrote pn-workspace.lock.json: the check must write nothing")
	}
}

func TestFlakeCheckNoBuild_RealNix_AFailureExitsTenAndTheLogLinesHaveTheDocumentedShape(t *testing.T) {
	e := newNBEnv(t)
	// A check whose evaluation throws: the evaluation of the derivation path fails.
	e.lone(t, `(throw "boom-from-the-fixture")`, "", nil)
	repoDir := filepath.Join(e.root, "lone")

	// One passing run (a different, healthy repo is not needed: --rev of the broken repo fails, so
	// use the healthy sibling flake from the drift fixture below). First, the failing run.
	out, se, code := e.pn(t, repoDir, "flake-check", "--no-build", "--caller", "ff-1c", "lone")
	if code != 10 {
		t.Fatalf("a failing check exits 10 (1 is the generic error), got %d\nstdout:\n%s\nstderr:\n%s", code, out, se)
	}
	if !strings.Contains(out+se, "boom-from-the-fixture") {
		t.Errorf("the nix error must be shown:\n%s\n%s", out, se)
	}
	recs := e.logLines(t)
	if len(recs) != 1 {
		t.Fatalf("one line per evaluated repo: got %d", len(recs))
	}
	rec := recs[0]
	wantKeys := []string{"caller", "duration_s", "failed_sets", "first_error", "nix_exit", "outcome", "repo", "ts"}
	var gotKeys []string
	for k := range rec {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("log line keys = %v, want %v", gotKeys, wantKeys)
	}
	if rec["repo"] != "lone" || rec["caller"] != "ff-1c" || rec["outcome"] != "failed" || rec["nix_exit"] != float64(1) {
		t.Errorf("unexpected failing record: %v", rec)
	}
	if !reflect.DeepEqual(rec["failed_sets"], []any{"checks"}) {
		t.Errorf("failed_sets = %v, want [checks]", rec["failed_sets"])
	}
	fe, _ := rec["first_error"].(string)
	if !strings.HasPrefix(fe, "error:") || !strings.Contains(fe, "boom-from-the-fixture") || len([]rune(fe)) > 300 {
		t.Errorf("first_error must be the first `error:` line of nix's stderr, at most 300 characters: %q", fe)
	}
	if ts, _ := rec["ts"].(string); ts == "" {
		t.Error("ts is empty")
	} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("ts %q is not RFC 3339: %v", ts, err)
	}
	if d, ok := rec["duration_s"].(float64); !ok || d <= 0 {
		t.Errorf("duration_s = %v, want a positive number of seconds", rec["duration_s"])
	}

	// An unwritable state directory does not fail the check: replace XDG_STATE_HOME by a file.
	blocker := filepath.Join(e.scratch, "state-is-a-file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{}, e.env...), "XDG_STATE_HOME="+blocker)
	_, se, code = runIn(t, repoDir, env, pnBinary, "workspace", "flake-check", "--no-build", "lone")
	if code != 10 {
		t.Errorf("an unwritable state dir must not change the verdict (still 10 for the failing repo), got %d", code)
	}
	if !strings.Contains(se, "cannot log the run") {
		t.Errorf("an unwritable state dir should be reported on stderr:\n%s", se)
	}
}

func TestFlakeCheckNoBuild_RealNix_APassingRunLogsOneOkLine(t *testing.T) {
	e := newNBEnv(t)
	consumer, _ := e.driftFixture(t)
	out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "consumer")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, se)
	}
	recs := e.logLines(t)
	if len(recs) != 1 {
		t.Fatalf("want 1 line, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	if rec["repo"] != "consumer" || rec["caller"] != "manual" || rec["outcome"] != "ok" || rec["nix_exit"] != float64(0) || rec["first_error"] != "" {
		t.Errorf("unexpected passing record: %v", rec)
	}
	if !reflect.DeepEqual(rec["failed_sets"], []any{}) {
		t.Errorf("failed_sets = %#v, want an empty list", rec["failed_sets"])
	}
	// A second run appends a second line, never rewrites.
	if _, _, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "consumer"); code != 0 {
		t.Fatalf("second run failed: %d", code)
	}
	if recs := e.logLines(t); len(recs) != 2 {
		t.Errorf("the log is append-only: want 2 lines after 2 runs, got %d", len(recs))
	}
}

func TestFlakeCheckNoBuild_RealNix_TheRepoUnderTestIsTheWorkingTreeAWorktreePathOrARev(t *testing.T) {
	ctx := context.Background()
	e := newNBEnv(t)
	consumer, sibling := e.driftFixture(t)
	headSHA, err := consumer.Client.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(headSHA))

	// A tracked-file edit in the canonical working tree that breaks evaluation, uncommitted.
	broken := strings.Replace(fixtureConsumerFlake, `sibling.lib.options ? dockerState`, `sibling.lib.options ? noSuchOption`, 1)
	broken = strings.NewReplacer("@@SIBLING@@", sibling.Dir, "@@SYSTEM@@", e.sys, "@@UNIQ@@", e.uniq).Replace(broken)
	if err := os.WriteFile(filepath.Join(consumer.Dir, "flake.nix"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	// By default the WORKING TREE is evaluated: the dirty edit fails the check.
	if out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "consumer"); code != 10 {
		t.Errorf("the dirty working tree must be what is evaluated: exit %d\n%s\n%s", code, out, se)
	}
	// --rev evaluates the COMMIT; the dirty edit is ignored: passes (what the land step wants).
	if out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "--rev", "HEAD", "consumer"); code != 0 {
		t.Errorf("--rev HEAD must ignore the dirty edit: exit %d\n%s\n%s", code, out, se)
	}
	if out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "--rev", head, consumer.Dir); code != 0 {
		t.Errorf("--rev <sha> by path: exit %d\n%s\n%s", code, out, se)
	}
	// An unresolvable rev is a usage error: exit 2, nothing evaluated.
	if _, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "--rev", "no-such-ref", "consumer"); code != 2 || !strings.Contains(se, "does not resolve") {
		t.Errorf("an unresolvable --rev exits 2: got %d\n%s", code, se)
	}
	// Restore the canonical tree, then edit a WORKTREE (kept outside the repo) and select it by path.
	if _, err := consumer.Client.Run(ctx, "checkout", "--", "flake.nix"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(e.scratch, "consumer-wt")
	if _, err := consumer.Client.Run(ctx, "worktree", "add", "-q", "-b", "wt-branch", wt); err != nil {
		t.Fatal(err)
	}
	wtBroken := strings.NewReplacer("@@SIBLING@@", sibling.Dir, "@@SYSTEM@@", e.sys, "@@UNIQ@@", e.uniq).Replace(
		strings.Replace(fixtureConsumerFlake, `sibling.lib.options ? dockerState`, `sibling.lib.options ? noSuchOption`, 1),
	)
	if err := os.WriteFile(filepath.Join(wt, "flake.nix"), []byte(wtBroken), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", wt); code != 10 {
		t.Errorf("a worktree PATH evaluates that worktree (broken): exit %d\n%s\n%s", code, out, se)
	}
	if out, se, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", "consumer"); code != 0 {
		t.Errorf("the canonical clone is untouched by the worktree edit: exit %d\n%s\n%s", code, out, se)
	}
	// A path that is no workspace repo is a usage error.
	if _, _, code := e.pn(t, consumer.Dir, "flake-check", "--no-build", e.scratch); code != 2 {
		t.Errorf("a non-member path exits 2, got %d", code)
	}
}

func TestPreLandEval_RealNix_ThroughRealPnGivesTheVerdictsTheLandStepReads(t *testing.T) {
	ctx := context.Background()
	e := newNBEnv(t)
	script, err := os.ReadFile(preLandEvalScript)
	if err != nil {
		t.Skipf("pre-land-eval source is not beside this module: %v", err)
	}
	sut := filepath.Join(e.scratch, "pre-land-eval")
	// The packaged tool gets this preamble from mkBashScript; the raw source has none.
	if err := os.WriteFile(sut, append([]byte("#!/usr/bin/env bash\nset -euo pipefail\n"), script...), 0o755); err != nil {
		t.Fatal(err)
	}
	consumer, sibling := e.driftFixture(t)

	// The drifted consumer: ok, because pre-land-eval asks pn for the local siblings.
	out, se, code := runIn(t, e.scratch, e.env, sut, consumer.Dir, "HEAD")
	if code != 0 || strings.TrimSpace(out) != "ok" {
		t.Fatalf("exit %d stdout %q, want exit 0 ok\nstderr:\n%s", code, out, se)
	}
	if !strings.Contains(se, "--override-input sibling git+file://"+sibling.Dir) {
		t.Errorf("stderr should carry pn's reproduce line with the override:\n%s", se)
	}
	if recs := e.logLines(t); len(recs) != 1 || recs[0]["caller"] != "ff-1c" {
		t.Errorf("pre-land-eval must log as ff-1c: %v", recs)
	}

	// A commit that breaks evaluation: eval-failed, exit 10, the error on stderr.
	broken := strings.NewReplacer("@@SIBLING@@", sibling.Dir, "@@SYSTEM@@", e.sys, "@@UNIQ@@", e.uniq).Replace(
		strings.Replace(fixtureConsumerFlake, `sibling.lib.options ? dockerState`, `sibling.lib.options ? noSuchOption`, 1),
	)
	if _, err := consumer.Commit(ctx, "consumer: break evaluation", map[string]string{"flake.nix": broken}); err != nil {
		t.Fatal(err)
	}
	out, se, code = runIn(t, e.scratch, e.env, sut, consumer.Dir, "HEAD")
	if code != 10 || strings.TrimSpace(out) != "eval-failed" {
		t.Fatalf("exit %d stdout %q, want exit 10 eval-failed\nstderr:\n%s", code, out, se)
	}
	if !strings.Contains(se, "dockerState") {
		t.Errorf("stderr should carry the missing-option error:\n%s", se)
	}
	// The previous commit is still fine, evaluated by ref while HEAD is broken.
	if out, se, code = runIn(t, e.scratch, e.env, sut, consumer.Dir, "HEAD~1"); code != 0 || strings.TrimSpace(out) != "ok" {
		t.Errorf("HEAD~1: exit %d stdout %q\n%s", code, out, se)
	}
	// An unresolvable ref and a repo outside the workspace are usage errors.
	if out, _, code = runIn(t, e.scratch, e.env, sut, consumer.Dir, "no-such-ref"); code != 2 || strings.TrimSpace(out) != "" {
		t.Errorf("an unresolvable ref: exit %d stdout %q, want exit 2 and no word", code, out)
	}
	// No pn at all: 127, no word.
	noPn := nixEnv(e.scratch, "PATH="+pathWithout("pn"))
	if out, se, code = runIn(t, e.scratch, noPn, sut, consumer.Dir, "HEAD~1"); code != 127 || strings.TrimSpace(out) != "" || !strings.Contains(se, "pn not installed") {
		t.Errorf("no pn: exit %d stdout %q\n%s", code, out, se)
	}
}

// noBuildApply is the expression the flag evaluates; the drift test runs the same one by hand.
const noBuildApply = `set: builtins.mapAttrs (n: v: v.drvPath or null) set`
