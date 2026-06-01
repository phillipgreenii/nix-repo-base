package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestReadFlakeInputs_NoFlakeFile_ReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := readFlakeInputs(context.Background(), exec.NewFakeRunner(), root)
	if err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice when flake.nix missing; got %v", got)
	}
}

func TestReadFlakeInputs_ExtractsURLsForAllTopLevelInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	runner := exec.NewFakeRunner()
	runner.AddResponse("nix",
		[]string{"eval", "--json", "--file", filepath.Join(root, "flake.nix"), "inputs"},
		exec.Result{Stdout: []byte(`{
		  "nixpkgs":    { "url": "github:NixOS/nixpkgs/master" },
		  "overlay":    { "url": "github:phillipgreenii/nix-overlay" },
		  "homelab":    { "url": "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git" },
		  "nested":     { "inputs": { "thing": { "url": "github:foo/bar" } } }
		}`)},
		nil)

	got, err := readFlakeInputs(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	// Result is a map[inputName]url for every top-level input that has a
	// "url" string field. "nested" has no top-level url field and is omitted.
	want := map[string]string{
		"nixpkgs": "github:NixOS/nixpkgs/master",
		"overlay": "github:phillipgreenii/nix-overlay",
		"homelab": "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git",
	}
	if len(got) != len(want) {
		t.Fatalf("input count: got %d (%v) want %d (%v)", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("inputs[%q] = %q; want %q", k, got[k], v)
		}
	}
}

func TestReadFlakeInputs_NixEvalFailure_ReturnsEmptyWithNoError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	runner := exec.NewFakeRunner()
	// No scripted response -> FakeRunner.Run returns error.
	got, err := readFlakeInputs(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("readFlakeInputs should swallow nix eval failure and continue; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty inputs map on nix eval failure; got %v", got)
	}
}

func TestReadFlakeInputs_StringValuedInputIsSkipped(t *testing.T) {
	// Some flake.nix files declare inputs as bare strings rather than
	// objects (e.g. `inputs.nixpkgs = "github:NixOS/nixpkgs";`). nix eval
	// then emits them as JSON strings. Our function expects objects with a
	// `url` field, so string-valued inputs must be silently skipped (not
	// error out) — verify.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	runner := exec.NewFakeRunner()
	runner.AddResponse("nix",
		[]string{"eval", "--json", "--file", filepath.Join(root, "flake.nix"), "inputs"},
		exec.Result{Stdout: []byte(`{
		  "nixpkgs": "github:NixOS/nixpkgs",
		  "overlay": { "url": "github:o/overlay" }
		}`)},
		nil)
	got, err := readFlakeInputs(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	if _, ok := got["nixpkgs"]; ok {
		t.Errorf("string-valued input should be skipped; got %v", got)
	}
	if got["overlay"] != "github:o/overlay" {
		t.Errorf("overlay should be extracted; got %v", got)
	}
}

func TestReadFlakeInputs_EmptyStdout_ReturnsEmpty(t *testing.T) {
	// A successful nix eval with empty stdout should produce an empty
	// inputs map, not an error.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	runner := exec.NewFakeRunner()
	runner.AddResponse("nix",
		[]string{"eval", "--json", "--file", filepath.Join(root, "flake.nix"), "inputs"},
		exec.Result{Stdout: []byte{}},
		nil)
	got, err := readFlakeInputs(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on empty stdout; got %v", got)
	}
}
