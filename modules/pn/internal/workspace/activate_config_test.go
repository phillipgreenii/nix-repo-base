package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Config surface of the build-as-user apply flow (bead pg2-sdbhh4.9, ADR 0038):
// the optional [workspace].activate_command template and its {out} placeholder.

const activateCmdV1 = "sudo {out}/sw/bin/darwin-rebuild activate"

func activateTOML(extra string) string {
	return `
[workspace]
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"
` + extra + `
[repos.leaf]
url = "github:owner/leaf"
`
}

func TestActivateCommand_ParsesTheV1Value(t *testing.T) {
	cfg, err := ParseConfig([]byte(activateTOML(`activate_command = "` + activateCmdV1 + `"`)))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.ActivateCommandTemplate(); got != activateCmdV1 {
		t.Errorf("ActivateCommandTemplate = %q, want %q", got, activateCmdV1)
	}
}

func TestActivateCommand_AbsentMeansUnset(t *testing.T) {
	cfg, err := ParseConfig([]byte(activateTOML("")))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := cfg.ActivateCommandTemplate(); got != "" {
		t.Errorf("ActivateCommandTemplate = %q, want empty when the key is absent", got)
	}
	var nilCfg *WorkspaceConfig
	if nilCfg.ActivateCommandTemplate() != "" {
		t.Error("a nil config must report no activate_command")
	}
}

// {out} is accepted ONLY in activate_command; build_command and apply_command
// keep rejecting it as an unknown placeholder.
func TestActivateCommand_OutIsValidOnlyThere(t *testing.T) {
	for _, field := range []string{"build_command", "apply_command"} {
		toml := `
[workspace]
terminal = "leaf"
` + field + ` = "darwin-rebuild build --flake {out}"

[repos.leaf]
url = "github:owner/leaf"
`
		_, err := ParseConfig([]byte(toml))
		if err == nil || !strings.Contains(err.Error(), "unknown placeholder") || !strings.Contains(err.Error(), "out") {
			t.Errorf("%s with {out}: err = %v, want an unknown-placeholder error naming out", field, err)
		}
	}
}

func TestActivateCommand_ShapeRules(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string // substring; "" = accepted
	}{
		{"v1 value", "sudo {out}/sw/bin/darwin-rebuild activate", ""},
		{"other program under out", "sudo {out}/activate", ""},
		{"out as a later argument too", "sudo {out}/sw/bin/darwin-rebuild activate --flake {terminal_nix_dir}", ""},
		{"not sudo", "{out}/sw/bin/darwin-rebuild activate", "argv[0] must be sudo"},
		{"doas", "doas {out}/sw/bin/darwin-rebuild activate", "argv[0] must be sudo"},
		{"sudo alone", "sudo", "argv[0] must be sudo"},
		{"sudo option", "sudo -E {out}/sw/bin/darwin-rebuild activate", "must be one element beginning with {out}/"},
		{"program not under out", "sudo /usr/bin/true", "must be one element beginning with {out}/"},
		{"out not at the start of the program", "sudo /x/{out}/sw/bin/darwin-rebuild activate", "must be one element beginning with {out}/"},
		{"out without a slash", "sudo {out} activate", "must be one element beginning with {out}/"},
		{"unknown placeholder", "sudo {out}/sw/bin/darwin-rebuild activate {bogus}", "unknown placeholder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(activateTOML(`activate_command = "` + tc.value + `"`)))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("rejected %q: %v", tc.value, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("%q: err = %v, want substring %q", tc.value, err, tc.wantErr)
			}
		})
	}
}

// {out} is substituted AFTER the argv split, from the validated string.
func TestSubstituteActivateCommand(t *testing.T) {
	out := "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-darwin-system-26.05.abc"
	got, err := substituteActivateCommand(activateCmdV1, templateVars{Out: out})
	if err != nil {
		t.Fatalf("substituteActivateCommand: %v", err)
	}
	want := []string{"sudo", out + "/sw/bin/darwin-rebuild", "activate"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// substituteCommand (build/apply) still rejects {out}.
func TestSubstituteCommand_RejectsOut(t *testing.T) {
	if _, err := substituteCommand("darwin-rebuild build {out}", templateVars{}); err == nil {
		t.Fatal("substituteCommand accepted {out}; it is valid only in activate_command")
	}
}

// The new key survives pn's own writers: an older writer that does not know the
// key would drop it on init / doctor --fix / the enforcer, silently switching a
// machine back to the legacy flow.
func TestActivateCommand_RoundTripsThroughTheWriters(t *testing.T) {
	cfg, err := ParseConfig([]byte(activateTOML(`activate_command = "` + activateCmdV1 + `"`)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	atomic := filepath.Join(dir, "atomic.toml")
	if err := writeConfigTOMLAtomicMode(atomic, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "to.toml")
	if err := writeConfigTOMLTo(to, cfg); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{atomic, to} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseConfig(data)
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(p), err, data)
		}
		if got := back.ActivateCommandTemplate(); got != activateCmdV1 {
			t.Errorf("%s dropped or changed activate_command: %q\n%s", filepath.Base(p), got, data)
		}
	}
	// And the init writer (Workspace.writeConfigTOMLAtomic in init.go).
	initRoot := t.TempDir()
	if err := (&Workspace{root: initRoot, config: cfg}).writeConfigTOMLAtomic(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(initRoot, ConfigFileName))
	back, err := ParseConfig(data)
	if err != nil || back.ActivateCommandTemplate() != activateCmdV1 {
		t.Errorf("init writer dropped activate_command: %v %q\n%s", err, back.ActivateCommandTemplate(), data)
	}
}

// EnforceKeys is key-scoped for activate_command like the other two.
func TestEnforceKeys_ActivateCommand(t *testing.T) {
	p := writeTemp(t, "pn-workspace.toml", realWorldTOML, 0o600)

	// Empty leaves the key untouched (here: absent) and writes nothing.
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "", "")
	if err != nil || changed {
		t.Fatalf("empty activate value: changed=%v err=%v, want a no-op", changed, err)
	}

	changed, err = EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "", activateCmdV1)
	if err != nil || !changed {
		t.Fatalf("set activate: changed=%v err=%v, want a write", changed, err)
	}
	cfg, err := loadConfigFile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.ActivateCommand != activateCmdV1 {
		t.Errorf("activate_command = %q, want %q", cfg.Workspace.ActivateCommand, activateCmdV1)
	}
	// Everything else preserved.
	if cfg.Workspace.ApplyCommand == "" || cfg.Workspace.BuildCommand == "" || cfg.Workspace.Terminal == "" {
		t.Errorf("enforcing activate_command disturbed other keys: %+v", cfg.Workspace)
	}

	// Idempotent.
	if changed, err = EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "", activateCmdV1); err != nil || changed {
		t.Errorf("second enforce: changed=%v err=%v, want a no-op", changed, err)
	}

	// An empty value afterwards does NOT delete the existing key (rollback is the
	// operator's edit, ADR 0038).
	if changed, err = EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "", ""); err != nil || changed {
		t.Errorf("empty enforce after set: changed=%v err=%v, want a no-op", changed, err)
	}
	cfg, _ = loadConfigFile(t, p)
	if cfg.Workspace.ActivateCommand != activateCmdV1 {
		t.Errorf("an empty value deleted activate_command: %q", cfg.Workspace.ActivateCommand)
	}
}

// A value pn itself would reject at parse time is refused, never written.
func TestEnforceKeys_RejectsABadActivateCommand(t *testing.T) {
	p := writeTemp(t, "pn-workspace.toml", realWorldTOML, 0o600)
	before, _ := os.ReadFile(p)
	if _, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "", "sudo -E {out}/x"); err == nil {
		t.Fatal("EnforceKeys accepted an activate_command pn cannot parse")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("the file was modified despite the rejected value")
	}
}
