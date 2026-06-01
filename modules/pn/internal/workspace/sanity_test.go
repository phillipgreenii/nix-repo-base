package workspace

import "testing"

func TestCheckRemoteAgreement_SingleURL_Matches(t *testing.T) {
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{"origin": "github:o/foo"}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckRemoteAgreement_SingleURL_Disagrees(t *testing.T) {
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{"origin": "github:o/bar"}
	err := checkRemoteAgreement("foo", cfg, gitRemotes)
	if err == nil {
		t.Fatal("expected disagreement error")
	}
}

func TestCheckRemoteAgreement_Remotes_AllMatch(t *testing.T) {
	cfg := RepoConfig{Remotes: []Remote{
		{Name: "origin", URL: "github:o/foo"},
		{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
	}}
	gitRemotes := map[string]string{
		"origin": "github:o/foo",
		"mirror": "https://github.com/o/foo-mirror",
	}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckRemoteAgreement_Remotes_MissingFromGit(t *testing.T) {
	cfg := RepoConfig{Remotes: []Remote{
		{Name: "origin", URL: "github:o/foo"},
		{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
	}}
	gitRemotes := map[string]string{"origin": "github:o/foo"}
	err := checkRemoteAgreement("foo", cfg, gitRemotes)
	if err == nil {
		t.Fatal("expected missing-remote error")
	}
}

func TestCheckRemoteAgreement_ExtraGitRemotes_AreIgnored(t *testing.T) {
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{
		"origin":   "github:o/foo",
		"personal": "git@github.com:me/foo.git",
	}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("unexpected error: extra git remotes should be ignored: %v", err)
	}
}

func TestCheckRemoteAgreement_NoGitRemotes_IsTolerated(t *testing.T) {
	// e.g. fresh clone, no remotes yet; readGitRemotes returned empty
	// after a swallowed error. Don't fail discovery on this.
	cfg := RepoConfig{URL: "github:o/foo"}
	if err := checkRemoteAgreement("foo", cfg, map[string]string{}); err != nil {
		t.Errorf("empty git remotes should be tolerated: %v", err)
	}
}

func TestCheckRemoteAgreement_Remotes_URLMismatch(t *testing.T) {
	cfg := RepoConfig{Remotes: []Remote{{Name: "origin", URL: "github:o/foo"}}}
	gitRemotes := map[string]string{"origin": "github:o/WRONG"}
	err := checkRemoteAgreement("foo", cfg, gitRemotes)
	if err == nil {
		t.Fatal("expected URL mismatch error")
	}
}

func TestCheckRemoteAgreement_Remotes_NoGitRemotes_IsTolerated(t *testing.T) {
	// Empty git remotes are tolerated even in the multi-remote form (e.g.,
	// fresh clone before remotes are set up).
	cfg := RepoConfig{Remotes: []Remote{
		{Name: "origin", URL: "github:o/foo"},
		{Name: "mirror", URL: "github:o/foo-mirror"},
	}}
	if err := checkRemoteAgreement("foo", cfg, map[string]string{}); err != nil {
		t.Errorf("empty git remotes should be tolerated for multi-remote configs too: %v", err)
	}
}

func TestCheckRemoteAgreement_SingleURL_NoOriginOtherRemotesPass(t *testing.T) {
	// Single-URL form with no origin but other (personal) remotes: pass.
	// Toml is the source of truth; personal remotes are tolerated.
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{
		"personal": "git@github.com:me/foo.git",
	}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("single-URL with no origin but other remotes should pass: %v", err)
	}
}

func TestCheckRemoteAgreement_SingleURL_FlakeURL_AgreesWithSSHRemote(t *testing.T) {
	// The toml uses the github: flake form; git remote -v reports the same
	// repo via the ssh-shorthand form. These should be treated as
	// equivalent (slug-based comparison) — no error.
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{"origin": "git@github.com:o/foo.git"}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("github: flake URL should agree with same-slug ssh remote: %v", err)
	}
}

func TestCheckRemoteAgreement_Remotes_FlakeURL_AgreesWithHTTPSRemote(t *testing.T) {
	// Same equivalence for the multi-remote form.
	cfg := RepoConfig{Remotes: []Remote{
		{Name: "origin", URL: "github:o/foo"},
	}}
	gitRemotes := map[string]string{"origin": "https://github.com/o/foo.git"}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("github: flake URL should agree with same-slug https remote: %v", err)
	}
}

func TestCheckRemoteAgreement_NonGithubURLs_StillCompareLiterally(t *testing.T) {
	// For non-github URLs (no slug extractable), we fall back to literal
	// string comparison. Mismatching Forgejo URLs should still error.
	cfg := RepoConfig{URL: "ssh://git@synfra.twistcone.us:222/twistcone/foo.git"}
	gitRemotes := map[string]string{"origin": "ssh://git@synfra.twistcone.us:222/twistcone/BAR.git"}
	err := checkRemoteAgreement("foo", cfg, gitRemotes)
	if err == nil {
		t.Fatal("expected error: mismatched non-github URLs (no slug, literal compare)")
	}
}
