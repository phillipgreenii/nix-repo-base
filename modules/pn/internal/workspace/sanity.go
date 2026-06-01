package workspace

import "fmt"

// checkRemoteAgreement returns nil when every remote declared in the toml
// also exists in git with the same URL. Untracked git remotes (in git but
// not in toml) are tolerated — users may keep personal remotes. An empty
// gitRemotes map is tolerated too (e.g., a fresh clone before remotes are
// set up, or readGitRemotes swallowed a git failure).
//
// URL comparison is slug-aware for GitHub URLs: if both the toml URL and the
// git remote URL resolve to the same GitHub "owner/repo" slug, they are
// considered equivalent (e.g., "github:o/foo" and "git@github.com:o/foo.git"
// both refer to the same repo). For non-GitHub URLs, exact string comparison
// is used.
func checkRemoteAgreement(repoName string, cfg RepoConfig, gitRemotes map[string]string) error {
	if len(gitRemotes) == 0 {
		return nil
	}
	if len(cfg.Remotes) > 0 {
		for _, rm := range cfg.Remotes {
			got, ok := gitRemotes[rm.Name]
			if !ok {
				return fmt.Errorf("repo %q: toml declares remote %q but git has none with that name", repoName, rm.Name)
			}
			if !urlsAgree(rm.URL, got) {
				return fmt.Errorf("repo %q: remote %q url mismatch — toml=%q git=%q", repoName, rm.Name, rm.URL, got)
			}
		}
		return nil
	}
	// Single-URL form -> implicit origin
	if got, ok := gitRemotes["origin"]; ok {
		if !urlsAgree(cfg.URL, got) {
			return fmt.Errorf("repo %q: origin url mismatch — toml=%q git=%q", repoName, cfg.URL, got)
		}
	}
	return nil
}

// urlsAgree reports whether two URL strings refer to the same repository.
// For GitHub URLs (any recognized format), comparison is by slug ("owner/repo").
// For non-GitHub URLs, exact string equality is used.
func urlsAgree(a, b string) bool {
	slugA := ExtractGithubSlug(a)
	slugB := ExtractGithubSlug(b)
	if slugA != "" && slugB != "" {
		return slugA == slugB
	}
	return a == b
}
