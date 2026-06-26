package jira

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile_parsesTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("base_url=\"https://x.atlassian.net\"\nemail=\"e@x\"\ndefault_limit=50\n[secret]\nsource=\"command\"\ncommand=[\"sec\",\"-w\"]\n"), 0o600)
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://x.atlassian.net" || c.Email != "e@x" || c.DefaultLimit != 50 {
		t.Errorf("bad parse: %+v", c)
	}
	if c.Secret.Source != "command" || len(c.Secret.Command) != 2 {
		t.Errorf("bad secret parse: %+v", c.Secret)
	}
}

func TestLoadFile_missingIsZero(t *testing.T) {
	c, err := LoadFile(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if c.BaseURL != "" {
		t.Errorf("expected zero config, got %+v", c)
	}
}

func TestMerge_overWins_defaultLimitFallsBack(t *testing.T) {
	base := DefaultConfig() // DefaultLimit = 100
	over := Config{BaseURL: "u", Email: "e"}
	got := base.Merge(over)
	if got.BaseURL != "u" || got.Email != "e" || got.DefaultLimit != 100 {
		t.Errorf("merge precedence wrong: %+v", got)
	}
}
