package pjira

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestLoadFile_parsesTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("base_url=\"https://x.atlassian.net\"\nemail=\"e@x\"\ndefault_limit=50\n[secret]\nsource=\"command\"\ncommand=[\"sec\",\"-w\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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

func TestLoadFile_malformedTOMLErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("base_url = \"unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(p); err == nil {
		t.Fatal("malformed TOML must be reported, not swallowed into a zero Config")
	}
}

// TestLoadFile_unreadablePathErrors pins the non-ErrNotExist read failure. Only a
// MISSING file is benign (it means "no config"); any other read error must be
// reported, or a config the user meant to apply is silently ignored.
func TestLoadFile_unreadablePathErrors(t *testing.T) {
	// A directory is readable-as-a-path but not as a file, so os.ReadFile fails
	// with something other than fs.ErrNotExist.
	if _, err := LoadFile(t.TempDir()); err == nil {
		t.Fatal("an unreadable config path must error, not be treated as an absent file")
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

// mergeField describes one Config field guarded by Merge: how to set it to a
// distinguishable value on each side, and how to read it back as a string so a
// single table covers the string, int and slice fields alike.
type mergeField struct {
	name     string
	setBase  func(*Config)
	setOver  func(*Config)
	get      func(Config) string
	wantBase string // expected value after a merge where only base carries it
	wantOver string // expected value after a merge where over carries it
}

// mergeFields enumerates every field Merge guards. Keep this in lockstep with
// Merge: one entry per `if over.X != zero` guard.
func mergeFields() []mergeField {
	return []mergeField{
		{
			name:     "BaseURL",
			setBase:  func(c *Config) { c.BaseURL = "https://base.example" },
			setOver:  func(c *Config) { c.BaseURL = "https://over.example" },
			get:      func(c Config) string { return c.BaseURL },
			wantBase: "https://base.example",
			wantOver: "https://over.example",
		},
		{
			name:     "Email",
			setBase:  func(c *Config) { c.Email = "base@example" },
			setOver:  func(c *Config) { c.Email = "over@example" },
			get:      func(c Config) string { return c.Email },
			wantBase: "base@example",
			wantOver: "over@example",
		},
		{
			// An int, so its zero value is 0 rather than "".
			name:     "DefaultLimit",
			setBase:  func(c *Config) { c.DefaultLimit = 7 },
			setOver:  func(c *Config) { c.DefaultLimit = 9 },
			get:      func(c Config) string { return strconv.Itoa(c.DefaultLimit) },
			wantBase: "7",
			wantOver: "9",
		},
		{
			name:     "Secret.Source",
			setBase:  func(c *Config) { c.Secret.Source = "env" },
			setOver:  func(c *Config) { c.Secret.Source = "file" },
			get:      func(c Config) string { return c.Secret.Source },
			wantBase: "env",
			wantOver: "file",
		},
		{
			name:     "Secret.EnvVar",
			setBase:  func(c *Config) { c.Secret.EnvVar = "BASE_VAR" },
			setOver:  func(c *Config) { c.Secret.EnvVar = "OVER_VAR" },
			get:      func(c Config) string { return c.Secret.EnvVar },
			wantBase: "BASE_VAR",
			wantOver: "OVER_VAR",
		},
		{
			name:     "Secret.Path",
			setBase:  func(c *Config) { c.Secret.Path = "/base/token" },
			setOver:  func(c *Config) { c.Secret.Path = "/over/token" },
			get:      func(c Config) string { return c.Secret.Path },
			wantBase: "/base/token",
			wantOver: "/over/token",
		},
		{
			// A slice: the guard is len(...) != 0, and the winning argv must
			// REPLACE the loser's rather than merge with it — hence the
			// deliberately different lengths.
			name:     "Secret.Command",
			setBase:  func(c *Config) { c.Secret.Command = []string{"base-cmd", "--flag"} },
			setOver:  func(c *Config) { c.Secret.Command = []string{"over-cmd"} },
			get:      func(c Config) string { return strings.Join(c.Secret.Command, " ") },
			wantBase: "base-cmd --flag",
			wantOver: "over-cmd",
		},
	}
}

// TestMerge_perFieldPrecedence pins the entire config-layering rule field by
// field. Two things are asserted per field, and BOTH matter: a non-zero over
// field WINS, and — the case the doc comment's "non-zero" wording exists for — a
// ZERO over field does NOT clobber a field the base already carries. Without the
// second, an empty override could silently blank a configured BaseURL and the
// suite would stay green.
func TestMerge_perFieldPrecedence(t *testing.T) {
	for _, f := range mergeFields() {
		t.Run(f.name, func(t *testing.T) {
			var baseOnly Config
			f.setBase(&baseOnly)
			if got := f.get(baseOnly.Merge(Config{})); got != f.wantBase {
				t.Errorf("base-only: a zero over field must NOT clobber base; %s = %q, want %q", f.name, got, f.wantBase)
			}

			var overOnly Config
			f.setOver(&overOnly)
			if got := f.get(Config{}.Merge(overOnly)); got != f.wantOver {
				t.Errorf("over-only: %s = %q, want %q", f.name, got, f.wantOver)
			}

			var base, over Config
			f.setBase(&base)
			f.setOver(&over)
			if got := f.get(base.Merge(over)); got != f.wantOver {
				t.Errorf("both-set: over must win; %s = %q, want %q", f.name, got, f.wantOver)
			}
		})
	}
}

// fullConfig is every Merge-guarded field set at once, so the whole-struct
// precedence tests below cannot pass by leaving a field untouched.
func fullConfig(t *testing.T, which func(mergeField) func(*Config)) Config {
	t.Helper()
	var c Config
	for _, f := range mergeFields() {
		which(f)(&c)
	}
	return c
}

// TestMerge_zeroOverIsIdentity pins the aggregate of the base-only rows: an
// entirely zero over is the identity, leaving every field of base intact.
func TestMerge_zeroOverIsIdentity(t *testing.T) {
	base := fullConfig(t, func(f mergeField) func(*Config) { return f.setBase })
	got := base.Merge(Config{})
	if !reflect.DeepEqual(got, base) {
		t.Errorf("Merge(zero) must be the identity;\n got %+v\nwant %+v", got, base)
	}
}

// TestMerge_fullOverWinsEverywhere pins the aggregate of the both-set rows: when
// over carries every field, the result is over — no field falls back to base.
func TestMerge_fullOverWinsEverywhere(t *testing.T) {
	base := fullConfig(t, func(f mergeField) func(*Config) { return f.setBase })
	over := fullConfig(t, func(f mergeField) func(*Config) { return f.setOver })
	got := base.Merge(over)
	if !reflect.DeepEqual(got, over) {
		t.Errorf("a fully populated over must win outright;\n got %+v\nwant %+v", got, over)
	}
}
