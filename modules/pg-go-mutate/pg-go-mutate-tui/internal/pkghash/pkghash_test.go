package pkghash

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestComputeIsStableAcrossIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "a_test.go", "package pkg\n")
	h1, err := Compute(dir)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Compute(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed with no content change: %s != %s", h1, h2)
	}
}

func TestComputeChangesWhenAnyFileChanges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "a_test.go", "package pkg\n")
	before, _ := Compute(dir)
	writeFile(t, dir, "a_test.go", "package pkg\n// changed\n")
	after, _ := Compute(dir)
	if before == after {
		t.Fatal("hash did not change when a test file changed")
	}
}

func TestComputeIsOrderIndependentOfFilesystemListing(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	writeFile(t, dir1, "a.go", "package pkg\n")
	writeFile(t, dir1, "b.go", "package pkg\n")
	writeFile(t, dir2, "b.go", "package pkg\n")
	writeFile(t, dir2, "a.go", "package pkg\n")
	h1, _ := Compute(dir1)
	h2, _ := Compute(dir2)
	if h1 != h2 {
		t.Fatal("hash depends on filesystem listing order, not sorted content")
	}
}
