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
