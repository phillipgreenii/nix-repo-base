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
