package workspace

import (
	"testing"
)

func TestCanonicalURL_TableDriven(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// github: shorthand
		{"github shorthand", "github:phillipgreenii/nix-personal", "github.com/phillipgreenii/nix-personal"},
		{"github shorthand with branch", "github:owner/repo/main", "github.com/owner/repo"},
		{"github shorthand simple", "github:p/r", "github.com/p/r"},

		// https://
		{"https github no .git", "https://github.com/owner/repo", "github.com/owner/repo"},
		{"https github with .git", "https://github.com/owner/repo.git", "github.com/owner/repo"},
		{"https other host", "https://other.host/path/repo.git", "other.host/path/repo"},
		{"https other host no git", "https://other.host/path/repo", "other.host/path/repo"},

		// ssh://
		{"ssh with user and port", "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git", "synfra.twistcone.us/twistcone/homelab"},
		{"ssh with user no port", "ssh://git@github.com/owner/repo.git", "github.com/owner/repo"},
		{"ssh no user no port", "ssh://github.com/owner/repo.git", "github.com/owner/repo"},

		// git@host:path (SCP-like)
		{"git scp github", "git@github.com:phillipgreenii/nix-personal", "github.com/phillipgreenii/nix-personal"},
		{"git scp github with .git", "git@github.com:owner/repo.git", "github.com/owner/repo"},
		{"git scp other host", "git@other.host:path/repo.git", "other.host/path/repo"},

		// git+ssh://
		{"git+ssh github", "git+ssh://git@github.com/phillipgreenii/nix-personal.git", "github.com/phillipgreenii/nix-personal"},
		{"git+ssh other host", "git+ssh://git@other.host/path/repo.git", "other.host/path/repo"},

		// git+https://
		{"git+https github", "git+https://github.com/phillipgreenii/nix-personal.git", "github.com/phillipgreenii/nix-personal"},
		{"git+https no .git", "git+https://github.com/owner/repo", "github.com/owner/repo"},

		// path: (local — returns "")
		{"path relative", "path:./local/repo", ""},
		{"path absolute", "path:/absolute/local/repo", ""},

		// Edge cases
		{"empty string", "", ""},
		{"trailing slash https", "https://github.com/owner/repo/", "github.com/owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalURL(tt.input)
			if got != tt.want {
				t.Errorf("canonicalURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCanonicalURL_CrossFormEquality verifies that different URL forms for the
// same repo canonicalize identically.
func TestCanonicalURL_CrossFormEquality(t *testing.T) {
	// The real monorepod nix-personal case: github: shorthand vs git+ssh://.
	pairs := [][]string{
		{
			"github:phillipgreenii/nix-personal",
			"https://github.com/phillipgreenii/nix-personal.git",
			"git@github.com:phillipgreenii/nix-personal",
			"git+ssh://git@github.com/phillipgreenii/nix-personal.git",
			"git+https://github.com/phillipgreenii/nix-personal.git",
		},
		{
			"github:p/r",
			"https://github.com/p/r.git",
			"git@github.com:p/r",
			"git+ssh://git@github.com/p/r.git",
			"git+https://github.com/p/r.git",
		},
	}

	for _, group := range pairs {
		canonical := canonicalURL(group[0])
		for _, u := range group[1:] {
			got := canonicalURL(u)
			if got != canonical {
				t.Errorf("cross-form equality failed:\n  canonicalURL(%q) = %q\n  canonicalURL(%q) = %q",
					group[0], canonical, u, got)
			}
		}
	}
}

// TestCanonicalURL_DifferentRepos verifies that different repos do NOT
// canonicalize identically.
func TestCanonicalURL_DifferentRepos(t *testing.T) {
	pairs := [][2]string{
		{"github:owner/repo-a", "github:owner/repo-b"},
		{"github:owner-a/repo", "github:owner-b/repo"},
		{"https://host-a.com/owner/repo.git", "https://host-b.com/owner/repo.git"},
		{"ssh://git@host.com/path/repo.git", "ssh://git@host.com/other/repo.git"},
	}
	for _, pair := range pairs {
		a := canonicalURL(pair[0])
		b := canonicalURL(pair[1])
		if a == b {
			t.Errorf("expected different canonical forms for %q and %q; both got %q",
				pair[0], pair[1], a)
		}
	}
}

// TestCanonicalURL_NixPersonalRealWorldCase verifies the specific real-world
// fixture from the monorepod workspace: homelab's flake.nix references
// nix-personal via git+ssh://, while pn-workspace.toml may use github: form.
func TestCanonicalURL_NixPersonalRealWorldCase(t *testing.T) {
	tomlURL := "github:phillipgreenii/nix-personal"
	flakeURL := "git+ssh://git@github.com/phillipgreenii/nix-personal.git"

	a := canonicalURL(tomlURL)
	b := canonicalURL(flakeURL)
	if a != b {
		t.Errorf("real-world nix-personal case: github: form %q canonical=%q != git+ssh:// form %q canonical=%q",
			tomlURL, a, flakeURL, b)
	}
	if a == "" {
		t.Errorf("expected non-empty canonical form, got empty")
	}
}

// TestCanonicalURL_SSHWithPort verifies ssh:// with port (e.g. homelab's synfra remote).
func TestCanonicalURL_SSHWithPort(t *testing.T) {
	withPort := "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"
	withoutPort := "ssh://git@synfra.twistcone.us/twistcone/homelab.git"

	a := canonicalURL(withPort)
	b := canonicalURL(withoutPort)
	if a != b {
		t.Errorf("ssh with/without port should canonicalize identically:\n  with port: %q\n  without port: %q",
			a, b)
	}
	want := "synfra.twistcone.us/twistcone/homelab"
	if a != want {
		t.Errorf("ssh with port canonical = %q, want %q", a, want)
	}
}

// TestDisplayURL verifies the displayURL helper returns the display URL from a RepoConfig.
func TestDisplayURL(t *testing.T) {
	t.Run("single url", func(t *testing.T) {
		r := RepoConfig{URL: "github:owner/foo"}
		if got := displayURL(r); got != "github:owner/foo" {
			t.Errorf("displayURL = %q, want %q", got, "github:owner/foo")
		}
	})
	t.Run("multi-remote origin preferred", func(t *testing.T) {
		r := RepoConfig{
			Remotes: []Remote{
				{Name: "fork", URL: "git@github.com:fork/repo.git"},
				{Name: "origin", URL: "git@github.com:owner/repo.git"},
			},
		}
		if got := displayURL(r); got != "git@github.com:owner/repo.git" {
			t.Errorf("displayURL = %q, want origin URL", got)
		}
	})
	t.Run("multi-remote first when no origin", func(t *testing.T) {
		r := RepoConfig{
			Remotes: []Remote{
				{Name: "fork", URL: "git@github.com:fork/repo.git"},
			},
		}
		if got := displayURL(r); got != "git@github.com:fork/repo.git" {
			t.Errorf("displayURL = %q, want first remote URL", got)
		}
	})
	t.Run("empty config", func(t *testing.T) {
		r := RepoConfig{}
		if got := displayURL(r); got != "" {
			t.Errorf("displayURL of empty config = %q, want empty", got)
		}
	})
}
