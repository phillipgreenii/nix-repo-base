package main

// Unmatched --override-input guard against the REAL nix binary and a hermetic
// local path-flake fixture (no network; --offline). A mock cannot prove that
// nix's own warning text reaches the json log the guard reads, so these tests
// skip only when nix is unusable here, never substitute a fake.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// overrideFixture writes a consumer flake whose single input is
// phillipgreenii-dep and a replacement dep flake, and returns their paths.
func overrideFixture(t *testing.T) (consumer, dep string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir()) // nix refuses a symlinked path: flake
	if err != nil {
		t.Fatal(err)
	}
	dep = filepath.Join(root, "dep")
	consumer = filepath.Join(root, "consumer")
	for dir, body := range map[string]string{
		dep:      `{ outputs = { self }: { v = 2; }; }`,
		consumer: fmt.Sprintf(`{ inputs.phillipgreenii-dep.url = "path:%s"; outputs = { self, phillipgreenii-dep }: { v = phillipgreenii-dep.v; }; }`, dep),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return consumer, dep
}

func nixEvalArgs(consumer, inputName, dep string) []string {
	return []string{
		"nix", "--offline", "--extra-experimental-features", "nix-command flakes",
		"eval", consumer + "#v", "--no-write-lock-file",
		"--override-input", inputName, "path:" + dep,
	}
}

func TestUnmatchedOverrideInputFailsARealNixRun(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix is not installed")
	}
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("inside a nix builder: no nested store operations")
	}
	consumer, dep := overrideFixture(t)

	// telemetry on (a loopback collector) and off (no endpoint) must agree.
	col := newCollector(t)
	for name, extra := range map[string][]string{
		"telemetry on":  {"OTEL_EXPORTER_OTLP_ENDPOINT=" + col.srv.URL},
		"telemetry off": nil,
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := env(t, append(extra, "PATH="+filepath.Dir(nix)+":/usr/bin:/bin")...)

			good := runWrapper(t, e, nixEvalArgs(consumer, "phillipgreenii-dep", dep)...)
			if good.code != 0 {
				t.Skipf("real nix eval is not usable in this environment (exit %d): %s", good.code, good.stderr)
			}
			if strings.TrimSpace(good.stdout) != "2" {
				t.Fatalf("valid override: stdout %q, want the overridden value 2", good.stdout)
			}
			if strings.Contains(good.stderr, "pg-nix-log-wrapped:") {
				t.Errorf("valid override must not trigger the guard: %q", good.stderr)
			}

			bad := runWrapper(t, e, nixEvalArgs(consumer, "bogus-input", dep)...)
			if bad.code != 3 {
				t.Fatalf("bogus override: exit %d, want 3; stderr %q", bad.code, bad.stderr)
			}
			for _, want := range []string{"'bogus-input'", "does not match any input", "phillipgreenii-*"} {
				if !strings.Contains(bad.stderr, want) {
					t.Errorf("stderr lacks %q: %q", want, bad.stderr)
				}
			}

			// The same run without the flag at all is untouched (exit 0).
			plain := runWrapper(t, e, "nix", "--offline", "--extra-experimental-features", "nix-command flakes",
				"eval", consumer+"#v", "--no-write-lock-file")
			if plain.code != 0 || strings.Contains(plain.stderr, "pg-nix-log-wrapped:") {
				t.Errorf("no --override-input: exit %d stderr %q", plain.code, plain.stderr)
			}

			// The escape hatch restores the old exit 0.
			allowed := runWrapper(t, append(e, "PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE=1"), nixEvalArgs(consumer, "bogus-input", dep)...)
			if allowed.code != 0 {
				t.Errorf("escape hatch: exit %d, want 0", allowed.code)
			}
		})
	}
}
