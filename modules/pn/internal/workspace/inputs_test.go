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

func TestReadFlakeInputs_ExtractsAllGithubURLs(t *testing.T) {
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
	// Result is a map[inputName]url (only top-level inputs; nested url values
	// are returned under their nested key but we keep simple semantics —
	// match the bash by capturing only top-level input names).
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
