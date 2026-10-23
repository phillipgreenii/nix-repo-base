package main

// Real-nix test of pre-land-eval's sibling overrides (bead pg2-8uc2d).
//
// The fixture is two real flakes in two real git repositories under one workspace
// root: a SIBLING that exports lib.options, and a CONSUMER that depends on it and
// throws unless the sibling's options include dockerState. The consumer's
// flake.lock pins the sibling at its FIRST commit (no dockerState); the local
// sibling clone then moves on to a second commit that has it. That is the drift
// that made nix-ziprecruiter fail pre-land-eval ("The option ...dockerState does
// not exist"): the locked sibling lags the local one.
//
// The test drives the real script (modules/pg-hooks/pre-land-eval) with the real
// pn binary this package builds and the host's real nix. Nothing is mocked: a
// mock of nix would only prove the script's plumbing, not that the override set
// reaches nix and changes the result (global rule V-3). It needs a working nix
// daemon, which a nix build sandbox does not have, so it SKIPS there (and where
// nix is absent); a developer or an agent run executes it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
)

const preLandEvalScript = "../../../pg-hooks/pre-land-eval/pre-land-eval.sh"

const fixtureSiblingFlake = `{
  description = "fixture sibling";
  outputs = _: { lib.options = { %s }; };
}
`

const fixtureConsumerFlake = `{
  description = "fixture consumer";
  inputs.sibling.url = "git+file://%s";
  outputs = { self, sibling }:
    let
      sys = "%s";
      verdict =
        if sibling.lib.options ? dockerState then "ok"
        else throw "The option phillipgreenii.observability.dockerState does not exist (fixture)";
    in {
      checks.${sys}.cfg = derivation {
        name = "cfg-${verdict}";
        system = sys;
        builder = "/bin/sh";
        args = [ "-c" "exit 0" ];
      };
    };
}
`

// nixEnv is the environment for every subprocess the test starts: no GIT_*
// location variables, no global git config, no inherited workspace root, telemetry
// off, and nix's per-user caches redirected into the test's temp dir.
func nixEnv(scratch string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_"),
			strings.HasPrefix(kv, "PN_WORKSPACE_ROOT="),
			strings.HasPrefix(kv, "WORKSPACE_ROOT="),
			strings.HasPrefix(kv, "XDG_CACHE_HOME="),
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

func TestPreLandEval_RealNix_SiblingOverridesFixLockedSiblingDrift(t *testing.T) {
	ctx := context.Background()
	scratch := t.TempDir()
	requireRealNix(t, scratch)

	script, err := os.ReadFile(preLandEvalScript)
	if err != nil {
		t.Skipf("pre-land-eval source is not beside this module: %v", err)
	}
	sut := filepath.Join(scratch, "pre-land-eval")
	// The packaged tool gets this preamble from mkBashScript; the raw source has none.
	if err := os.WriteFile(sut, append([]byte("#!/usr/bin/env bash\nset -euo pipefail\n"), script...), 0o755); err != nil {
		t.Fatal(err)
	}

	// The workspace root holds the two repos side by side.
	root := filepath.Join(scratch, "ws")
	sibling, err := gitfixture.NewRepo(ctx, root, gitfixture.RepoOptions{Suite: t.Name(), Name: "sibling"})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := sibling.NewSibling(ctx, "consumer", gitfixture.RepoOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sys, _, code := runIn(t, scratch, nixEnv(scratch), "nix", "eval", "--raw", "--impure", "--expr", "builtins.currentSystem")
	if code != 0 || sys == "" {
		t.Fatalf("cannot learn the nix system (exit %d)", code)
	}

	// 1. The sibling at its OLD revision, and the consumer locked against it.
	if _, err := sibling.Commit(ctx, "sibling: no options", map[string]string{
		"flake.nix": strings.Replace(fixtureSiblingFlake, "%s", "", 1),
	}); err != nil {
		t.Fatal(err)
	}
	consumerFlake := strings.Replace(strings.Replace(fixtureConsumerFlake, "%s", sibling.Dir, 1), "%s", sys, 1)
	if _, err := consumer.Commit(ctx, "consumer: flake", map[string]string{"flake.nix": consumerFlake}); err != nil {
		t.Fatal(err)
	}
	if _, se, code := runIn(t, consumer.Dir, nixEnv(scratch), "nix", "flake", "lock"); code != 0 {
		t.Fatalf("nix flake lock failed (exit %d): %s", code, se)
	}
	lockText, err := os.ReadFile(filepath.Join(consumer.Dir, "flake.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Commit(ctx, "consumer: lock pins the old sibling", map[string]string{"flake.lock": string(lockText)}); err != nil {
		t.Fatal(err)
	}

	// 2. The LOCAL sibling clone moves ahead and gains the option; the lock does not.
	if _, err := sibling.Commit(ctx, "sibling: adds dockerState", map[string]string{
		"flake.nix": strings.Replace(fixtureSiblingFlake, "%s", "dockerState = true;", 1),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Client.Run(ctx, "config", "pgii-integrate-branch.preLandEval", "true"); err != nil {
		t.Fatal(err)
	}

	// 3. The workspace that makes the two repos siblings.
	toml := "[workspace]\nid = \"pg2-8uc2d-fixture\"\nterminal = \"consumer\"\n\n" +
		"[repos.sibling]\nurl = \"git+file://" + sibling.Dir + "\"\n\n" +
		"[repos.consumer]\nurl = \"git+file://" + consumer.Dir + "\"\n"
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(consumer.Dir, "flake.lock")
	lockBefore := fileDigest(t, lockPath)
	siblingHead, err := sibling.Client.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// The tool, with pn first on PATH and with no pn at all.
	pnDir := filepath.Dir(pnBinary)
	pathWithPn := pnDir + string(os.PathListSeparator) + pathWithout("pn")
	envWithPn := nixEnv(scratch, "PATH="+pathWithPn)
	envNoPn := nixEnv(scratch, "PATH="+pathWithout("pn"))

	// WITHOUT overrides the locked, old sibling is what is evaluated: it FAILS.
	out, se, code := runIn(t, scratch, envWithPn, sut, "--no-overrides", consumer.Dir, "HEAD")
	if code != 10 || strings.TrimSpace(out) != "eval-failed" {
		t.Fatalf("--no-overrides: exit %d stdout %q, want exit 10 eval-failed\nstderr:\n%s", code, out, se)
	}
	if !strings.Contains(se, "dockerState") {
		t.Errorf("--no-overrides: stderr should carry the missing-option error:\n%s", se)
	}

	// With no pn on PATH the tool degrades to the locked inputs, with one note.
	out, se, code = runIn(t, scratch, envNoPn, sut, consumer.Dir, "HEAD")
	if code != 10 || strings.TrimSpace(out) != "eval-failed" {
		t.Fatalf("no pn: exit %d stdout %q, want exit 10 eval-failed\nstderr:\n%s", code, out, se)
	}
	if strings.Count(se, "pn is not on PATH") != 1 {
		t.Errorf("no pn: want exactly one fallback note, got:\n%s", se)
	}

	// WITH the overrides pn prints, the local sibling is evaluated: it PASSES.
	out, se, code = runIn(t, scratch, envWithPn, sut, consumer.Dir, "HEAD")
	if code != 0 || strings.TrimSpace(out) != "ok" {
		t.Fatalf("with pn overrides: exit %d stdout %q, want exit 0 ok\nstderr:\n%s", code, out, se)
	}
	if !strings.Contains(se, "--override-input sibling git+file://"+sibling.Dir) {
		t.Errorf("the reproduce line should carry the override pn computed:\n%s", se)
	}
	if !strings.Contains(se, "pinned 1 workspace sibling input(s)") {
		t.Errorf("stderr should report one pinned sibling:\n%s", se)
	}

	// pn's own entry point: the same absolute git+file override, one argument per line.
	pnOut, pnErr, pnCode := runIn(t, consumer.Dir, envWithPn, pnBinary, "workspace", "overrides", consumer.Dir)
	if pnCode != 0 {
		t.Fatalf("pn workspace overrides: exit %d: %s", pnCode, pnErr)
	}
	if want := "--override-input\nsibling\ngit+file://" + sibling.Dir + "\n"; pnOut != want {
		t.Errorf("pn workspace overrides printed %q, want %q", pnOut, want)
	}
	// A path that is not a workspace repo exits 2.
	if _, _, c := runIn(t, consumer.Dir, envWithPn, pnBinary, "workspace", "overrides", scratch); c != 2 {
		t.Errorf("pn workspace overrides <non-member>: exit %d, want 2", c)
	}

	// No push, no relock, no lock write: nothing moved.
	if got := fileDigest(t, lockPath); got != lockBefore {
		t.Error("flake.lock changed: the check must never write it")
	}
	if st, _, _ := runIn(t, consumer.Dir, nixEnv(scratch), "git", "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("the consumer working tree is dirty after the runs:\n%s", st)
	}
	if head, err := sibling.Client.Run(ctx, "rev-parse", "HEAD"); err != nil || !bytes.Equal(head, siblingHead) {
		t.Errorf("the sibling HEAD moved (err %v)", err)
	}
	if remotes, _, _ := runIn(t, consumer.Dir, nixEnv(scratch), "git", "remote"); strings.TrimSpace(remotes) != "" {
		t.Errorf("the fixture has a remote %q: a push would have been possible", remotes)
	}
	if _, err := os.Stat(filepath.Join(root, "pn-workspace.lock.json")); err == nil {
		t.Error("pn wrote pn-workspace.lock.json: the overrides verb must be read-only")
	}
}
