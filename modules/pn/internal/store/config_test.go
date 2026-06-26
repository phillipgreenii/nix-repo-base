package store

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStoreTOML(t *testing.T, env Env, body string) {
	t.Helper()
	dir := filepath.Join(env.configHome(), "pn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_DefaultsWhenAbsent(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	c := LoadConfig(env)
	if c.KeepDays != 14 || c.KeepCount != 3 || len(c.SearchDirs) != 0 {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadConfig_ReadsValues(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	writeStoreTOML(t, env, "search_dirs = [\"/a\", \"/b\"]\nkeep_days = 7\nkeep_count = 1\n")
	c := LoadConfig(env)
	if c.KeepDays != 7 || c.KeepCount != 1 {
		t.Fatalf("values wrong: %+v", c)
	}
	if len(c.SearchDirs) != 2 || c.SearchDirs[0] != "/a" || c.SearchDirs[1] != "/b" {
		t.Fatalf("search_dirs wrong: %+v", c.SearchDirs)
	}
}

func TestLoadConfig_DefaultsWhenKeyAbsent(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	writeStoreTOML(t, env, "search_dirs = []\n")
	c := LoadConfig(env)
	if c.KeepDays != 14 || c.KeepCount != 3 {
		t.Fatalf("expected key defaults, got %+v", c)
	}
}
