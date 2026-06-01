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
