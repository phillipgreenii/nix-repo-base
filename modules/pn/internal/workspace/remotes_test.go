package workspace

import (
	"context"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestReadGitRemotes_Simple(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"origin\tssh://git@github.com/owner/repo.git (fetch)\n" +
				"origin\tssh://git@github.com/owner/repo.git (push)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	want := map[string]string{"origin": "ssh://git@github.com/owner/repo.git"}
	if len(got) != len(want) || got["origin"] != want["origin"] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadGitRemotes_MultipleRemotes(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"bitbucket\tgit@bitbucket.org:phillipgreenii/homelab.git (fetch)\n" +
				"bitbucket\tgit@bitbucket.org:phillipgreenii/homelab.git (push)\n" +
				"origin\tssh://git@synfra.twistcone.us:222/twistcone/homelab.git (fetch)\n" +
				"origin\tssh://git@synfra.twistcone.us:222/twistcone/homelab.git (push)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	if got["origin"] != "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git" {
		t.Errorf("origin: got %q", got["origin"])
	}
	if got["bitbucket"] != "git@bitbucket.org:phillipgreenii/homelab.git" {
		t.Errorf("bitbucket: got %q", got["bitbucket"])
	}
}

func TestReadGitRemotes_NotARepo_ReturnsEmpty(t *testing.T) {
	runner := exec.NewFakeRunner()
	// No scripted response → FakeRunner returns error.
	got, err := readGitRemotes(context.Background(), runner, "/notarepo")
	if err != nil {
		t.Fatalf("readGitRemotes should swallow git failure; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}
