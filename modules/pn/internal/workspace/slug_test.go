package workspace

import "testing"

func TestExtractGithubSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// github: flake refs
		{"github:phillipgreenii/nix-overlay", "phillipgreenii/nix-overlay"},
		{"github:phillipgreenii/nix-overlay/main", "phillipgreenii/nix-overlay"},
		{"github:o/r/sub/dir", "o/r"},
		// https
		{"https://github.com/owner/repo", "owner/repo"},
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo/", "owner/repo"},
		{"https://github.com/owner/repo/tree/main", "owner/repo"},
		// ssh shorthand
		{"git@github.com:owner/repo.git", "owner/repo"},
		{"git@github.com:owner/repo", "owner/repo"},
		// ssh url
		{"ssh://git@github.com/owner/repo", "owner/repo"},
		{"ssh://git@github.com/owner/repo.git", "owner/repo"},
		// Query-string / fragment do not leak into the slug
		{"https://github.com/owner/repo?foo=bar", "owner/repo"},
		{"https://github.com/owner/repo#section", "owner/repo"},
		{"ssh://git@github.com/owner/repo?q=1", "owner/repo"},
		{"github:owner/repo?ref=main", "owner/repo"},
		// Non-matches
		{"git@bitbucket.org:phillipgreenii/homelab.git", ""},
		{"ssh://git@synfra.twistcone.us:222/twistcone/homelab.git", ""},
		{"https://gitlab.com/owner/repo", ""},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ExtractGithubSlug(tc.in)
			if got != tc.want {
				t.Errorf("ExtractGithubSlug(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalSlug_ExplicitOverride(t *testing.T) {
	r := RepoConfig{
		URL:  "github:o/foo",
		Slug: "o/canonical",
	}
	if got := CanonicalSlug(r); got != "o/canonical" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/canonical")
	}
}

func TestCanonicalSlug_SingleURL(t *testing.T) {
	r := RepoConfig{URL: "github:o/foo"}
	if got := CanonicalSlug(r); got != "o/foo" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/foo")
	}
}

func TestCanonicalSlug_RemotesWithOrigin(t *testing.T) {
	r := RepoConfig{
		Remotes: []Remote{
			{Name: "bitbucket", URL: "git@bitbucket.org:o/foo.git"},
			{Name: "origin", URL: "github:o/foo"},
		},
	}
	if got := CanonicalSlug(r); got != "o/foo" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/foo")
	}
}

func TestCanonicalSlug_RemotesWithoutOrigin_FirstWins(t *testing.T) {
	r := RepoConfig{
		Remotes: []Remote{
			{Name: "github", URL: "github:o/foo"},
			{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
		},
	}
	if got := CanonicalSlug(r); got != "o/foo" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/foo")
	}
}

func TestCanonicalSlug_NonGithubURL_Empty(t *testing.T) {
	r := RepoConfig{URL: "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"}
	if got := CanonicalSlug(r); got != "" {
		t.Errorf("CanonicalSlug: got %q, want empty", got)
	}
}

func TestSlugSet_UnionOfAllRemotesPlusExplicit(t *testing.T) {
	r := RepoConfig{
		Slug: "explicit/slug",
		Remotes: []Remote{
			{Name: "origin", URL: "github:o/foo"},
			{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
			{Name: "forgejo", URL: "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"}, // no slug
		},
	}
	got := SlugSet(r)
	want := map[string]bool{
		"explicit/slug": true,
		"o/foo":         true,
		"o/foo-mirror":  true,
	}
	if len(got) != len(want) {
		t.Fatalf("SlugSet size: got %d, want %d (got: %v)", len(got), len(want), got)
	}
	for s := range want {
		if !got[s] {
			t.Errorf("SlugSet missing %q (got: %v)", s, got)
		}
	}
}

func TestSlugSet_SingleURL(t *testing.T) {
	r := RepoConfig{URL: "github:o/foo"}
	got := SlugSet(r)
	if !got["o/foo"] || len(got) != 1 {
		t.Errorf("SlugSet: got %v, want {o/foo}", got)
	}
}

func TestCanonicalSlug_ExplicitSlugWinsOverRemotes(t *testing.T) {
	// Explicit Slug field wins even when Remotes are populated.
	r := RepoConfig{
		Slug: "override/wins",
		Remotes: []Remote{
			{Name: "origin", URL: "github:o/foo"},
		},
	}
	if got := CanonicalSlug(r); got != "override/wins" {
		t.Errorf("CanonicalSlug: got %q, want override/wins", got)
	}
}

func TestCanonicalSlug_NonGithubOrigin_NoFallthrough(t *testing.T) {
	// Documented behavior: origin remote with a non-GitHub URL returns ""
	// without falling through to a github mirror in the same Remotes list.
	r := RepoConfig{
		Remotes: []Remote{
			{Name: "origin", URL: "ssh://git@synfra.twistcone.us:222/twistcone/foo.git"},
			{Name: "mirror", URL: "github:o/foo"},
		},
	}
	if got := CanonicalSlug(r); got != "" {
		t.Errorf("CanonicalSlug: got %q, want empty (no fallthrough from non-GitHub origin)", got)
	}
}

func TestCanonicalSlug_ZeroValue_Empty(t *testing.T) {
	if got := CanonicalSlug(RepoConfig{}); got != "" {
		t.Errorf("CanonicalSlug(empty RepoConfig): got %q, want empty", got)
	}
}

func TestSlugSet_ZeroValue_Empty(t *testing.T) {
	got := SlugSet(RepoConfig{})
	if len(got) != 0 {
		t.Errorf("SlugSet(empty RepoConfig): got %v, want empty map", got)
	}
}
