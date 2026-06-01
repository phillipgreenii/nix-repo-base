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
