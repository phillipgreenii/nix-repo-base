package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverProjectsFindsGoModRoots(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "proj-a", "go.mod"), "module a\n")
	mustWrite(t, filepath.Join(root, "proj-b", "go.mod"), "module b\n")
	mustWrite(t, filepath.Join(root, "not-a-project", "readme.md"), "hi\n")

	projects, err := DiscoverProjects([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(projects), projects)
	}
}

func TestDiscoverPackagesGroupsFilesByDirectoryAndCarriesAnAbsPath(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module proj\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "other", "c.go"), "package other\n")

	packages, err := DiscoverPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d: %v", len(packages), packages)
	}
	for _, p := range packages {
		if p.PkgPath == "pkg" {
			if len(p.Files) != 2 {
				t.Fatalf("expected 2 non-test files in pkg, got %v", p.Files)
			}
			if p.AbsPath != filepath.Join(root, "pkg") {
				t.Fatalf("expected AbsPath to be openable by pkghash.Compute, got %q", p.AbsPath)
			}
		}
	}
}
