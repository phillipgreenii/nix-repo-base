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

func TestReadGitRemotes_URLWithSpacesIsPreserved(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"localfs\tfile:///path/with a space/repo.git (fetch)\n" +
				"localfs\tfile:///path/with a space/repo.git (push)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	if got["localfs"] != "file:///path/with a space/repo.git" {
		t.Errorf("URL with spaces should be preserved; got %q", got["localfs"])
	}
}

func TestReadGitRemotes_MalformedLinesSkipped(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"\n" +
				"no-tab-line\n" +
				"name-but-no-suffix\tjust-a-url\n" +
				"good\thttps://github.com/o/r (fetch)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	if len(got) != 1 || got["good"] != "https://github.com/o/r" {
		t.Errorf("expected only the 'good' line to parse; got %v", got)
	}
}

func TestReadGitRemotes_AsymmetricFetchPush_FetchWins(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"origin\thttps://github.com/o/r (fetch)\n" +
				"origin\tssh://git@github.com/o/r.git (push)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	if got["origin"] != "https://github.com/o/r" {
		t.Errorf("fetch URL should win; got %q", got["origin"])
	}
}
