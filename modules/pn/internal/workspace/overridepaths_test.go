package workspace

import "testing"

func TestParseOverridePaths_FlagOnly(t *testing.T) {
	t.Setenv("PN_WORKSPACE_OVERRIDE_PATHS", "")
	m, err := parseOverridePaths([]string{"repo-a=/abs/a", " repo-b = /abs/b "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["repo-a"] != "/abs/a" || m["repo-b"] != "/abs/b" {
		t.Errorf("got %#v", m)
	}
}

func TestParseOverridePaths_FlagOverridesEnv(t *testing.T) {
	t.Setenv("PN_WORKSPACE_OVERRIDE_PATHS", "repo-a=/abs/env,repo-c=/abs/c")
	m, err := parseOverridePaths([]string{"repo-a=/abs/flag"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["repo-a"] != "/abs/flag" {
		t.Errorf("flag should override env: got %q", m["repo-a"])
	}
	if m["repo-c"] != "/abs/c" {
		t.Errorf("env entry lost: got %q", m["repo-c"])
	}
}

func TestParseOverridePaths_Errors(t *testing.T) {
	t.Setenv("PN_WORKSPACE_OVERRIDE_PATHS", "")
	if _, err := parseOverridePaths([]string{"noequals"}); err == nil {
		t.Error("expected error for missing =")
	}
	if _, err := parseOverridePaths([]string{"=/abs"}); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := parseOverridePaths([]string{"a=rel/path"}); err == nil {
		t.Error("expected error for non-absolute path")
	}
}
