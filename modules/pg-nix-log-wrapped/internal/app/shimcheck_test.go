package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
)

// checkRig is a rig whose PATH holds, in order, a shim directory and a real
// directory, built from fixture symlinks. Dial succeeds, the root
// configuration path is an absent temp path unless a test sets one.
type checkRig struct {
	*rig
	shimDir, realDir, wrapper string
}

func newCheckRig(t *testing.T, shims ...string) *checkRig {
	t.Helper()
	r := newRig(t, fakecmd.Spec{})
	dir := t.TempDir()
	c := &checkRig{rig: r, shimDir: filepath.Join(dir, "shim", "bin"), realDir: filepath.Join(dir, "sw", "bin")}
	for _, d := range []string{c.shimDir, c.realDir, filepath.Join(dir, "pkg", "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c.wrapper = filepath.Join(dir, "pkg", "bin", "pg-nix-log-wrapped")
	mustWrite(t, c.wrapper, "#!/bin/sh\n")
	for _, tool := range []string{"nix", "darwin-rebuild"} {
		mustWrite(t, filepath.Join(c.realDir, tool), "#!/bin/sh\n")
	}
	for _, tool := range shims {
		if err := os.Symlink(c.wrapper, filepath.Join(c.shimDir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	r.setEnv("PATH", c.shimDir+":"+c.realDir)
	r.setEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	r.d.RootConfigPath = filepath.Join(dir, "absent.json")
	return c
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (c *checkRig) check(t *testing.T) (int, string) {
	t.Helper()
	code := c.run("--check")
	return code, c.stdout.String()
}

func wantLines(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

func TestCheckShimBothShims(t *testing.T) {
	c := newCheckRig(t, "nix", "darwin-rebuild")
	code, out := c.check(t)
	if code != 0 {
		t.Fatalf("code = %d\n%s", code, out)
	}
	wantLines(t, out,
		"shim: nix: shim at "+filepath.Join(c.shimDir, "nix"),
		"shim: darwin-rebuild: shim at "+filepath.Join(c.shimDir, "darwin-rebuild"),
		"shim: allowlist: darwin-rebuild build, darwin-rebuild check, darwin-rebuild switch, nix build, nix flake check\n",
		"shim: root-config: absent (",
		"shim: root-run: would pass through (no root configuration)",
		"status: telemetry=ok shim=ok\n")
	// The telemetry lines are unchanged and come first.
	if !strings.HasPrefix(out, "endpoint: http://127.0.0.1:4318 (from env)\nreachable: yes\nlog-dir: "+c.logDir+" (writable: yes)\nshim: nix:") {
		t.Errorf("existing lines changed or reordered:\n%s", out)
	}
}

func TestCheckShimOneMissing(t *testing.T) {
	c := newCheckRig(t, "nix") // darwin-rebuild resolves to the real binary
	code, out := c.check(t)
	if code != 1 {
		t.Fatalf("code = %d\n%s", code, out)
	}
	wantLines(t, out,
		"shim: nix: shim at ",
		"shim: darwin-rebuild: NOT a shim: "+filepath.Join(c.realDir, "darwin-rebuild"),
		"status: telemetry=ok shim=fail\n")
}

func TestCheckShimNotOnPath(t *testing.T) {
	c := newCheckRig(t, "nix", "darwin-rebuild")
	if err := os.Remove(filepath.Join(c.realDir, "darwin-rebuild")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.shimDir, "darwin-rebuild")); err != nil {
		t.Fatal(err)
	}
	code, out := c.check(t)
	wantLines(t, out, "shim: darwin-rebuild: NOT a shim: not found on PATH")
	if code != 1 {
		t.Errorf("code = %d", code)
	}
}

func TestCheckShimShadowedByRealBinary(t *testing.T) {
	c := newCheckRig(t, "nix", "darwin-rebuild")
	// A real nix ahead of the shim directory shadows the shim.
	c.setEnv("PATH", c.realDir+":"+c.shimDir)
	code, out := c.check(t)
	if code != 1 {
		t.Fatalf("code = %d\n%s", code, out)
	}
	wantLines(t, out, "shim: nix: NOT a shim: "+filepath.Join(c.realDir, "nix")+" is a real binary shadowing it")
}

func TestCheckShimNonRegularNameOnlyCountsWhenResolvedToWrapper(t *testing.T) {
	// A symlink named nix that resolves elsewhere is not a shim.
	c := newCheckRig(t)
	if err := os.Symlink(filepath.Join(c.realDir, "nix"), filepath.Join(c.shimDir, "nix")); err != nil {
		t.Fatal(err)
	}
	_, out := c.check(t)
	wantLines(t, out, "shim: nix: NOT a shim:")
}

// rootConfig installs a root configuration as a store symlink and returns
// its resolved path.
func (c *checkRig) rootConfig(t *testing.T, body string) string {
	t.Helper()
	store, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(store, "pg-nix-shim.json")
	mustWrite(t, cfg, body)
	link := filepath.Join(t.TempDir(), "etc-pg-nix-shim.json")
	if err := os.Symlink(cfg, link); err != nil {
		t.Fatal(err)
	}
	c.d.RootConfigPath, c.d.StorePrefix = link, store
	return cfg
}

func TestCheckShimRootConfigStoreSymlink(t *testing.T) {
	c := newCheckRig(t, "nix", "darwin-rebuild")
	cfg := c.rootConfig(t, `{"rootEndpoint":"http://127.0.0.1:4318"}`)
	code, out := c.check(t)
	if code != 0 {
		t.Fatalf("code = %d\n%s", code, out)
	}
	wantLines(t, out,
		"shim: root-config: "+c.d.RootConfigPath+" -> "+cfg+" (store regular file)",
		"shim: root-run: would wrap (rootEndpoint http://127.0.0.1:4318)")
}

func TestCheckShimRootConfigCopy(t *testing.T) {
	c := newCheckRig(t, "nix", "darwin-rebuild")
	// A regular file copy outside the store prefix: present, but not a store symlink.
	copyPath := filepath.Join(t.TempDir(), "pg-nix-shim.json")
	mustWrite(t, copyPath, `{"rootEndpoint":"http://x"}`)
	c.d.RootConfigPath = copyPath
	c.d.StorePrefix = t.TempDir()
	_, out := c.check(t)
	wantLines(t, out,
		"present but not usable: root configuration is not under the store prefix (not a store symlink)",
		"shim: root-run: would pass through (root configuration is not under the store prefix)")
}

func TestCheckShimRootConfigWithoutEndpoint(t *testing.T) {
	c := newCheckRig(t, "nix", "darwin-rebuild")
	c.rootConfig(t, `{"verbs":["run"]}`)
	_, out := c.check(t)
	wantLines(t, out, "shim: root-run: would pass through (root configuration has no rootEndpoint)")
}

func TestCheckShimAllowlistAdditions(t *testing.T) {
	t.Run("shim.toml and env add, never-wrapped ignored", func(t *testing.T) {
		c := newCheckRig(t, "nix", "darwin-rebuild")
		c.writeUserToml(t, "")
		mustWrite(t, filepath.Join(c.home, ".config", "pn", "shim.toml"), `verbs = ["run", "develop"]`)
		c.setEnv("PG_NIX_SHIM_VERBS", "eval, bogus")
		_, out := c.check(t)
		wantLines(t, out,
			"nix eval", "nix run",
			"shim: allowlist-ignored: develop, bogus (never wrapped or unknown)")
		if strings.Contains(out, "nix develop,") || strings.Contains(out, ", nix develop") {
			t.Errorf("never-wrapped verb listed as wrapped:\n%s", out)
		}
	})
	t.Run("root takes verbs from the root file only", func(t *testing.T) {
		c := newCheckRig(t, "nix", "darwin-rebuild")
		c.d.Euid = 0
		c.setEnv("PG_NIX_SHIM_VERBS", "run")
		c.rootConfig(t, `{"rootEndpoint":"http://x","verbs":["fmt"]}`)
		_, out := c.check(t)
		line := lineWith(out, "shim: allowlist:")
		if !strings.Contains(line, "nix fmt") || strings.Contains(line, "nix run") {
			t.Errorf("allowlist line = %q", line)
		}
	})
}

func lineWith(out, prefix string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, prefix) {
			return ln
		}
	}
	return ""
}

func TestCheckShimFailureKeepsTelemetryHealthSeparate(t *testing.T) {
	// Telemetry fine, shim missing: the status line says exactly that.
	c := newCheckRig(t)
	_, out := c.check(t)
	wantLines(t, out, "status: telemetry=ok shim=fail\n")
	// Telemetry down, shims fine.
	c = newCheckRig(t, "nix", "darwin-rebuild")
	c.setEnv("PG_NIX_LOG_DISABLE", "1")
	code, out := c.check(t)
	if code != 1 {
		t.Errorf("code = %d", code)
	}
	wantLines(t, out, "status: telemetry=fail shim=ok\n")
}
