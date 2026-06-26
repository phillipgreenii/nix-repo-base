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

func TestParseStoreConfig(t *testing.T) {
	cases := []struct {
		name      string
		input     []byte
		wantDays  int
		wantCount int
		wantDirs  []string
	}{
		{
			name:      "full TOML with all fields",
			input:     []byte("search_dirs = [\"/a\", \"/b\"]\nkeep_days = 7\nkeep_count = 1\n"),
			wantDays:  7,
			wantCount: 1,
			wantDirs:  []string{"/a", "/b"},
		},
		{
			name:      "keep_days and keep_count absent defaults to 14 and 3",
			input:     []byte("search_dirs = [\"/x\"]\n"),
			wantDays:  14,
			wantCount: 3,
			wantDirs:  []string{"/x"},
		},
		{
			name:      "malformed TOML defaults with nil search_dirs",
			input:     []byte("this is not valid toml [[["),
			wantDays:  14,
			wantCount: 3,
			wantDirs:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := parseStoreConfig(tc.input)
			if c.KeepDays != tc.wantDays {
				t.Errorf("KeepDays = %d, want %d", c.KeepDays, tc.wantDays)
			}
			if c.KeepCount != tc.wantCount {
				t.Errorf("KeepCount = %d, want %d", c.KeepCount, tc.wantCount)
			}
			if len(c.SearchDirs) != len(tc.wantDirs) {
				t.Errorf("SearchDirs = %v, want %v", c.SearchDirs, tc.wantDirs)
			} else {
				for i, d := range tc.wantDirs {
					if c.SearchDirs[i] != d {
						t.Errorf("SearchDirs[%d] = %q, want %q", i, c.SearchDirs[i], d)
					}
				}
			}
		})
	}
}
