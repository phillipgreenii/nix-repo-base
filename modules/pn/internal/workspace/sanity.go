package workspace

import "fmt"

// checkRemoteAgreement returns nil when every remote declared in the toml
// also exists in git with the same URL. Untracked git remotes (in git but
// not in toml) are tolerated — users may keep personal remotes. An empty
// gitRemotes map is tolerated too (e.g., a fresh clone before remotes are
// set up, or readGitRemotes swallowed a git failure).
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
			if got != rm.URL {
				return fmt.Errorf("repo %q: remote %q url mismatch — toml=%q git=%q", repoName, rm.Name, rm.URL, got)
			}
		}
		return nil
	}
	// Single-URL form -> implicit origin
	if got, ok := gitRemotes["origin"]; ok {
		if got != cfg.URL {
			return fmt.Errorf("repo %q: origin url mismatch — toml=%q git=%q", repoName, cfg.URL, got)
		}
	}
	return nil
}
