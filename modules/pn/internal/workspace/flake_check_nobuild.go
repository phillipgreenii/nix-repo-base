package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// `pn workspace flake-check --no-build` is the land check: it evaluates each
// repo's flake WITHOUT building anything, against the LOCAL workspace siblings
// (the --override-input set flake-check already derives), so a repo whose locked
// siblings lag the local clones gets a true answer with no push, no relock and no
// flake.lock write.
//
// It is NOT nix's own `nix flake check --no-build`. nix runs that mode
// read-only, so any source tree copied into the store during evaluation reads
// back as "path '...' is not valid" unless the same copy is already there: the
// verdict then depends on store warmth. The mechanism here is the drvPath
// evaluation (`nix eval --json <flake>#<set>.<system> --apply <drvPath of each>`
// for checks, packages and devShells), which realises nothing but is not
// read-only, so source copies and import from derivation both work.
//
// A failure is a failure: there is no retry and no inconclusive class (operator
// ruling, 2026-10-23). The one thing the evaluation tolerates is a flake that
// does not provide a set at all (a flake with no devShells).

// noBuildLogName is the experiment log's file name, under <state>/pn/.
const noBuildLogName = "flake-check-no-build.jsonl"

// noBuildSets are the flake output sets whose derivation paths are evaluated.
var noBuildSets = []string{"checks", "packages", "devShells"}

// noBuildApply maps every attribute of a set to its derivation path.
const noBuildApply = `set: builtins.mapAttrs (n: v: v.drvPath or null) set`

// firstErrorMax is the longest first_error the experiment log records.
const firstErrorMax = 300

// Callers the experiment log distinguishes.
const (
	CallerManual = "manual"
	CallerFF1c   = "ff-1c"
)

// UsageError marks a bad invocation (an unknown caller, a repo that is no
// workspace member, a ref that does not resolve), as opposed to a check that ran
// and failed. The CLI exits 2 for it.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// CheckFailedExitCode is the process exit status of a check that ran and
// reported failures (flake-check --no-build with a failed evaluation, doctor
// with findings). Exit 1 stays the generic, unplanned error and carries no
// branchable meaning; 2 is a bad invocation (operator ruling 2026-10-28 on
// pg2-9jk3g5, bead pg2-wrujsm). pre-land-eval's own eval-failed code is the same
// number.
const CheckFailedExitCode = 10

// CheckFailedError marks a check that ran to completion and reported failures,
// as opposed to a pn failure of its own (a lock error, an untrusted hook) or a
// bad invocation (UsageError). cli.ExitCode maps it to CheckFailedExitCode.
type CheckFailedError struct{ Msg string }

func (e *CheckFailedError) Error() string { return e.Msg }

var (
	errorLineRE  = regexp.MustCompile(`^\s*error: .+`)
	absentErrRE  = regexp.MustCompile(`^\s*error: flake '.*' does not provide attribute `)
	noBuildStamp = func() time.Time { return time.Now().UTC() }
)

// nixSystem names this machine's nix system from the Go runtime, not from an
// impure builtins.currentSystem.
func nixSystem() (string, error) {
	var arch, osName string
	switch runtime.GOARCH {
	case "arm64":
		arch = "aarch64"
	case "amd64":
		arch = "x86_64"
	default:
		return "", fmt.Errorf("cannot derive a nix system from GOARCH %q", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin":
		osName = "darwin"
	case "linux":
		osName = "linux"
	default:
		return "", fmt.Errorf("cannot derive a nix system from GOOS %q", runtime.GOOS)
	}
	return arch + "-" + osName, nil
}

// noBuildLogPath is ${XDG_STATE_HOME:-$HOME/.local/state}/pn/flake-check-no-build.jsonl,
// or "" when neither variable names a place.
func noBuildLogPath() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "pn", noBuildLogName)
}

// noBuildRecord is ONE line of the experiment log (bead pg2-8uc2d, reviewed by
// pg2-z1r54): one evaluated repo.
type noBuildRecord struct {
	Timestamp  string   `json:"ts"`
	Repo       string   `json:"repo"`
	Caller     string   `json:"caller"`
	DurationS  float64  `json:"duration_s"`
	Outcome    string   `json:"outcome"` // ok | failed
	NixExit    int      `json:"nix_exit"`
	FailedSets []string `json:"failed_sets"`
	FirstError string   `json:"first_error"`
}

// appendNoBuildRecord appends rec as one JSON line. An unwritable state
// directory never fails the check: the problem is reported on errOut and nothing
// else changes.
func appendNoBuildRecord(errOut io.Writer, rec noBuildRecord) {
	path := noBuildLogPath()
	if path == "" {
		fmt.Fprintln(errOut, "pn: flake-check --no-build: no XDG_STATE_HOME or HOME; the run is not logged")
		return
	}
	line, err := json.Marshal(rec)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = f.Write(append(line, '\n'))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		fmt.Fprintf(errOut, "pn: flake-check --no-build: cannot log the run to %s: %v\n", path, err)
	}
}

// firstErrorLine returns the first `error: <message>` line of nix's stderr
// (the bare `error:` that opens a trace carries no message), cut to
// firstErrorMax characters.
func firstErrorLine(stderr []byte) string {
	for _, l := range strings.Split(string(stderr), "\n") {
		if errorLineRE.MatchString(l) {
			return truncateRunes(strings.TrimSpace(l), firstErrorMax)
		}
	}
	for _, l := range strings.Split(string(stderr), "\n") {
		if strings.TrimSpace(l) != "" {
			return truncateRunes(strings.TrimSpace(l), firstErrorMax)
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// setAbsent reports whether a failed nix eval only says the flake does not
// provide the requested set (every error message is nix's "does not provide
// attribute" one): a flake with no devShells, for example, is not a failure.
func setAbsent(stderr []byte) bool {
	n := 0
	for _, l := range strings.Split(string(stderr), "\n") {
		if !errorLineRE.MatchString(l) {
			continue
		}
		if !absentErrRE.MatchString(l) {
			return false
		}
		n++
	}
	return n > 0
}

// resolveRepoTarget maps a repo key or a path to the repo key and the directory
// to evaluate. A key means <root>/<key>; a path means that directory itself, so a
// linked worktree of the repo is evaluated, not the canonical clone.
func (ws *Workspace) resolveRepoTarget(ctx context.Context, arg string) (key, dir string, err error) {
	key, err = ws.resolveRepoKey(ctx, arg)
	if err != nil {
		return "", "", &UsageError{Msg: err.Error()}
	}
	if arg == key {
		return key, filepath.Join(ws.root, key), nil
	}
	abs, aerr := filepath.Abs(arg)
	if aerr != nil {
		return "", "", aerr
	}
	return key, abs, nil
}

// pureRef is the immutable flake reference of commit sha in the git repository at dir.
func pureRef(dir, sha string) string {
	enc := (&url.URL{Path: dir}).EscapedPath()
	enc = strings.NewReplacer("?", "%3F", "#", "%23").Replace(enc)
	return "git+file://" + enc + "?rev=" + sha
}

// flakeCheckNoBuild is FlakeCheck for opts.NoBuild.
func (ws *Workspace) flakeCheckNoBuild(ctx context.Context, out io.Writer, errOut io.Writer, opts FlakeCheckOptions) error {
	caller := opts.Caller
	if caller == "" {
		caller = CallerManual
	}
	if caller != CallerManual && caller != CallerFF1c {
		return &UsageError{Msg: fmt.Sprintf("unknown --caller %q (want %s or %s)", opts.Caller, CallerManual, CallerFF1c)}
	}
	if opts.Rev != "" && opts.Repo == "" {
		return &UsageError{Msg: "--rev needs one repo: pass the repo key or a path to it"}
	}
	system, err := nixSystem()
	if err != nil {
		return err
	}
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}

	type target struct{ name, dir string }
	var targets []target
	if opts.Repo != "" {
		key, dir, err := ws.resolveRepoTarget(ctx, opts.Repo)
		if err != nil {
			return err
		}
		targets = []target{{key, dir}}
	} else {
		for _, n := range ws.topoAlpha(ctx) {
			targets = append(targets, target{n, filepath.Join(ws.root, n)})
		}
	}

	// ONE derivation of the override set, shared with build/apply/flake-check,
	// against the effective lock so it is non-empty even before `pn workspace lock`.
	lk, _, err := ws.effectiveLock(ctx)
	if err != nil {
		return fmt.Errorf("compute the workspace lock: %w", err)
	}

	var failed []string
	for i, t := range targets {
		ref := "."
		if opts.Rev != "" {
			sha, err := ws.resolveCommit(ctx, t.dir, opts.Rev)
			if err != nil {
				return err
			}
			ref = pureRef(t.dir, sha)
		}
		overrides := ws.overrideInputArgsForLock(lk, t.name, overrideOpts{ExcludeRepo: t.name})
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "  --== flake-check --no-build %s ==--  \n", t.name)
		var rec noBuildRecord
		_ = inRepoSpan(ctx, t.name, func(ctx context.Context) error {
			rec = ws.evalDrvPaths(ctx, out, t.name, t.dir, ref, system, overrides, opts.NoIFD)
			if rec.Outcome != "ok" {
				return fmt.Errorf("flake check --no-build failed in %s", t.name)
			}
			return nil
		})
		rec.Caller = caller
		appendNoBuildRecord(errOut, rec)
		if rec.Outcome != "ok" {
			failed = append(failed, t.name)
		}
	}
	if len(failed) > 0 {
		return &CheckFailedError{Msg: fmt.Sprintf("flake check --no-build failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))}
	}
	return nil
}

// resolveCommit resolves rev to a commit sha in the repository at dir.
func (ws *Workspace) resolveCommit(ctx context.Context, dir, rev string) (string, error) {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{commit}"}, exec.RunOptions{})
	sha := strings.TrimSpace(string(res.Stdout))
	if err != nil || sha == "" {
		return "", &UsageError{Msg: fmt.Sprintf("ref does not resolve to a commit in %s: %s", dir, rev)}
	}
	return sha, nil
}

// evalDrvPaths evaluates the derivation paths of checks, packages and devShells
// for system in the flake ref (run from dir), with the sibling overrides, and
// returns the experiment-log record for the repo.
func (ws *Workspace) evalDrvPaths(ctx context.Context, out io.Writer, name, dir, ref, system string, overrides []string, noIFD bool) noBuildRecord {
	start := time.Now()
	rec := noBuildRecord{Timestamp: noBuildStamp().Format(time.RFC3339), Repo: name, Outcome: "ok", FailedSets: []string{}}
	base := []string{"eval", "--extra-experimental-features", "nix-command flakes", "--no-write-lock-file"}
	if noIFD {
		base = append(base, "--option", "allow-import-from-derivation", "false")
	}
	base = append(base, overrides...)
	for _, set := range noBuildSets {
		attr := ref + "#" + set + "." + system
		args := append(append([]string{}, base...), "--json", attr, "--apply", noBuildApply)
		fmt.Fprintf(out, "  reproduce: (cd %s && nix %s)\n", dir, shellJoin(args))
		var stderr bytes.Buffer
		t0 := time.Now()
		res, err := ws.runner.Run(ctx, "nix", args, exec.RunOptions{Dir: dir, Stderr: io.MultiWriter(out, &stderr), WrapNix: true})
		dt := time.Since(t0).Round(100 * time.Millisecond)
		if err == nil {
			fmt.Fprintf(out, "  %s.%s: ok, %d derivations evaluated (%s)\n", set, system, bytes.Count(res.Stdout, []byte(`.drv"`)), dt)
			continue
		}
		captured := res.Stderr
		if len(captured) == 0 {
			captured = stderr.Bytes()
		}
		if setAbsent(captured) {
			fmt.Fprintf(out, "  %s.%s: not provided by the flake, skipped (%s)\n", set, system, dt)
			continue
		}
		fmt.Fprintf(out, "  %s.%s: FAILED (%s)\n", set, system, dt)
		if rec.Outcome == "ok" {
			rec.NixExit = res.ExitCode
			if rec.NixExit == 0 {
				rec.NixExit = -1
			}
			rec.FirstError = firstErrorLine(captured)
			if rec.FirstError == "" {
				rec.FirstError = truncateRunes(err.Error(), firstErrorMax)
			}
		}
		rec.Outcome = "failed"
		rec.FailedSets = append(rec.FailedSets, set)
	}
	rec.DurationS = float64(time.Since(start).Milliseconds()) / 1000
	if rec.Outcome != "ok" {
		fmt.Fprintf(out, "  eval failed in: %s\n", strings.Join(rec.FailedSets, " "))
	}
	return rec
}

// shellJoin quotes args for a copy-paste reproduce line.
func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`#?*&;|<>(){}[]!~") {
			q[i] = a
			continue
		}
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}
