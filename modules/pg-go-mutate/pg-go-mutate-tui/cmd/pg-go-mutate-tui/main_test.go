package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/metrics"
)

// binPath is the compiled test binary shared by every test in this file.
//
// NOTE: this deliberately does NOT drive the CLI via `go run .` as the plan's
// literal transcription did. `go run` does not propagate the child process's
// real exit code to its own caller — on ANY non-zero exit it always exits 1
// itself (it only *prints* "exit status N" as text; verified empirically
// against this repo's toolchain: `go run` wrapping a program that returns 2
// reports back exec.ExitError.ExitCode() == 1, never 2). That makes
// TestMissingRootExitsTwo unwinnable through `go run` no matter what main.go
// returns, so the test is rewritten to build the real binary once and exec it
// directly, which propagates exit codes correctly.
var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "pg-go-mutate-tui-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmpDir, "pg-go-mutate-tui")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: build failed:", err, string(out))
		os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

func TestHelpExitsZero(t *testing.T) {
	cmd := exec.Command(binPath, "--help")
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help should exit 0, got: %v", err)
	}
}

func TestMissingRootExitsTwo(t *testing.T) {
	cmd := exec.Command(binPath)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("missing --root should exit 2, got: %v", err)
	}
}

// writeFakePgGoMutate puts an executable "pg-go-mutate" that prints a fixed
// JSON document and exits 0 first on PATH, so runViaPgGoMutate never needs
// the real engine.
func writeFakePgGoMutate(t *testing.T, output string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "pg-go-mutate"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// listTree returns every path (files and dirs) under root, relative to it.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestRunViaPgGoMutateWritesReportUnderStateDir pins the pg2-beo1v fix: the
// per-file report MUST land under the tui's own state directory, never next
// to the source file inside the scanned (often canonical) repo -- a stray
// untracked file there dirties the clone and blocks update-locks.
func TestRunViaPgGoMutateWritesReportUnderStateDir(t *testing.T) {
	const fakeOut = `{"fake":"report"}`
	writeFakePgGoMutate(t, fakeOut)

	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	project := t.TempDir()
	pkgDir := filepath.Join(project, "internal", "cli")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(pkgDir, "root.go")
	if err := os.WriteFile(file, []byte("package cli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := listTree(t, project)

	stateDir, err := xdgStateHome()
	if err != nil {
		t.Fatal(err)
	}
	reportsDir := filepath.Join(stateDir, "pg-go-mutate-tui", "reports")
	status, reportPath, err := runViaPgGoMutate(metrics.New(), reportsDir)(file)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if status != "done" {
		t.Errorf("status = %q, want done", status)
	}

	if after := listTree(t, project); !slices.Equal(before, after) {
		t.Errorf("scanned project tree changed:\nbefore: %v\nafter:  %v", before, after)
	}
	if !strings.HasPrefix(reportPath, stateHome+string(os.PathSeparator)) {
		t.Errorf("report path %q is not under XDG_STATE_HOME %q", reportPath, stateHome)
	}
	got, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if string(got) != fakeOut {
		t.Errorf("report contents = %q, want %q", got, fakeOut)
	}
}

// TestReportPathForDistinguishesProjects: two scanned projects whose packages
// share a relative path must not share a report path.
func TestReportPathForDistinguishesProjects(t *testing.T) {
	reportsDir := t.TempDir()
	a, err := reportPathFor(reportsDir, "/work/repo-a/internal/cli/root.go")
	if err != nil {
		t.Fatal(err)
	}
	b, err := reportPathFor(reportsDir, "/work/repo-b/internal/cli/root.go")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("distinct projects share report path %q", a)
	}
	// "/" -> "__" (not "_") so "/a/b_c" and "/a_b/c" cannot collide either.
	c, _ := reportPathFor(reportsDir, "/a/b_c/x.go")
	d, _ := reportPathFor(reportsDir, "/a_b/c/x.go")
	if c == d {
		t.Fatalf("underscore/slash paths collide at %q", c)
	}
	want := filepath.Join(reportsDir, "__work__repo-a__internal__cli", "root.go.json")
	if a != want {
		t.Errorf("report path = %q, want %q", a, want)
	}
}
