package workspace

import "regexp"

var (
	// github:owner/repo  OR  github:owner/repo/anything
	reGithubFlake = regexp.MustCompile(`^github:([^/]+)/([^/?#]+?)(?:\.git)?(?:/[^?#]*)?(?:[?#].*)?$`)
	// https://github.com/owner/repo  with optional .git and/or trailing /...
	reGithubHTTPS = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/?#]+?)(?:\.git)?(?:/[^?#]*)?(?:[?#].*)?$`)
	// git@github.com:owner/repo  with optional .git
	reGithubSSHShorthand = regexp.MustCompile(`^git@github\.com:([^/]+)/([^/?#]+?)(?:\.git)?(?:[?#].*)?$`)
	// ssh://git@github.com/owner/repo  with optional .git and trailing path
	reGithubSSHURL = regexp.MustCompile(`^ssh://git@github\.com/([^/]+)/([^/?#]+?)(?:\.git)?(?:/[^?#]*)?(?:[?#].*)?$`)
)

// ExtractGithubSlug returns the "owner/repo" form for any github URL form
// recognized by this package. Returns the empty string for any non-github
// input (Forgejo, Bitbucket, GitLab, malformed, etc).
//
// Forms accepted:
//   - github:owner/repo  (and github:owner/repo/path)
//   - https://github.com/owner/repo  (with optional .git, with optional /tree/...)
//   - git@github.com:owner/repo[.git]
//   - ssh://git@github.com/owner/repo[.git]
func ExtractGithubSlug(url string) string {
	for _, re := range []*regexp.Regexp{
		reGithubFlake, reGithubHTTPS, reGithubSSHShorthand, reGithubSSHURL,
	} {
		if m := re.FindStringSubmatch(url); m != nil {
			return m[1] + "/" + m[2]
		}
	}
	return ""
}

// CanonicalSlug returns the single canonical slug for a repo, per the rules
// in the design doc §5.1:
//
//  1. RepoConfig.Slug wins if set.
//  2. Else if Remotes has an entry named "origin", derive from its URL.
//  3. Else if Remotes is non-empty, derive from the first entry.
//  4. Else (URL set) derive from URL.
//  5. Else (derivation fails) return empty string.
func CanonicalSlug(r RepoConfig) string {
	if r.Slug != "" {
		return r.Slug
	}
	if len(r.Remotes) > 0 {
		for _, rm := range r.Remotes {
			if rm.Name == "origin" {
				return ExtractGithubSlug(rm.URL)
			}
		}
		return ExtractGithubSlug(r.Remotes[0].URL)
	}
	return ExtractGithubSlug(r.URL)
}

// SlugSet returns the set of all slugs that identify a repo. Used for graph
// edge matching: any input URL whose slug is in some repo's SlugSet refers
// to that repo. Per design §5.2 the set is the union of slugs from every
// remote (or the single URL) plus the explicit Slug override if set.
func SlugSet(r RepoConfig) map[string]bool {
	out := make(map[string]bool, 4)
	if r.Slug != "" {
		out[r.Slug] = true
	}
	if len(r.Remotes) > 0 {
		for _, rm := range r.Remotes {
			if s := ExtractGithubSlug(rm.URL); s != "" {
				out[s] = true
			}
		}
	} else if s := ExtractGithubSlug(r.URL); s != "" {
		out[s] = true
	}
	return out
}
