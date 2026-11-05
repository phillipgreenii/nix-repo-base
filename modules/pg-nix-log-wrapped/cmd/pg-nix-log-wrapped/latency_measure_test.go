//go:build latency

package main

// One-off latency and sweep-cost measurement of the pg-nix-shim, opt-in behind
// the `latency` build tag so it is never part of the unit gate. It is not a
// regression guard and not a mutation-testing tool; its recorded result lives
// in the README ("Measured cost").
//
//	PG_MEASURE_SHIM_DIR=<built pg-nix-shim>/bin \
//	  go test -tags latency -run TestMeasureShim -v -timeout 30m ./cmd/pg-nix-log-wrapped
//
// The collector is a local stub OTLP/HTTP receiver (live) or a closed
// loopback port (dead). Added latency is the shimmed call minus the real nix
// called by absolute path, taken as interleaved pairs so machine drift cancels.
// PG_MEASURE_N paired runs per case (default 100); PG_MEASURE_LOOP runs for
// the sweep loop (default 300).

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1)*p + 0.5)
	return sorted[i]
}

func summarize(name string, ds []time.Duration) {
	s := slices.Clone(ds)
	slices.Sort(s)
	fmt.Printf("  %-34s n=%-4d p50=%-10s p95=%-10s\n", name, len(s), pct(s, .5).Round(100*time.Microsecond), pct(s, .95).Round(100*time.Microsecond))
}

func TestMeasureShim(t *testing.T) {
	shimDir := os.Getenv("PG_MEASURE_SHIM_DIR")
	if shimDir == "" {
		t.Skip("set PG_MEASURE_SHIM_DIR to the built pg-nix-shim bin directory")
	}
	realNixPath, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix is not installed")
	}
	realDir := filepath.Dir(realNixPath)
	n, loop := envInt("PG_MEASURE_N", 100), envInt("PG_MEASURE_LOOP", 300)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(nil)
	}))
	defer live.Close()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	deadURL := "http://" + l.Addr().String()
	_ = l.Close()

	home, state := t.TempDir(), t.TempDir()
	expr := `derivation { name = "pgnlw-measure-noop"; system = builtins.currentSystem; builder = "/bin/sh"; args = [ "-c" "echo noop > $out" ]; }`
	buildArgs := []string{"build", "--impure", "--no-link", "--offline", "--extra-experimental-features", "nix-command flakes", "--expr", expr}

	envFor := func(endpoint string) []string {
		return append(os.Environ(), "HOME="+home, "XDG_STATE_HOME="+state, "OTEL_EXPORTER_OTLP_ENDPOINT="+endpoint,
			"PATH="+shimDir+string(os.PathListSeparator)+realDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"PG_NIX_LOG_DISABLE=", "PG_NIX_LOG_ACTIVE=")
	}
	run := func(env []string, via string, args ...string) time.Duration {
		cmd := exec.Command(via, args...)
		cmd.Env = env
		start := time.Now()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", via, args, err, out)
		}
		return time.Since(start)
	}
	// The shimmed call names the shim's nix by absolute path (exec resolves a
	// bare name against the test process's PATH, not the child's); its own
	// lookup of the real nix then walks the child's PATH, as in real use.
	shimNix := filepath.Join(shimDir, "nix")
	pair := func(label, endpoint string, args ...string) {
		env := envFor(endpoint)
		run(env, realNixPath, args...) // realise the derivation and warm caches
		run(env, shimNix, args...)
		var base, shim, added []time.Duration
		for i := 0; i < n; i++ {
			b := run(env, realNixPath, args...)
			s := run(env, shimNix, args...)
			base, shim, added = append(base, b), append(shim, s), append(added, s-b)
		}
		fmt.Printf("%s\n", label)
		summarize("real nix (absolute path)", base)
		summarize("through the shim", shim)
		summarize("ADDED (shim - real, paired)", added)
	}
	fmt.Printf("pg-nix-shim latency: n=%d pairs per case, %s/%s, shim dir %s\n", n, "darwin", "arm64", shimDir)
	pair("no-op nix build, live collector (local stub OTLP receiver)", live.URL, buildArgs...)
	pair("no-op nix build, dead collector (closed loopback port)", deadURL, buildArgs...)
	pair("nix --version (off the allowlist, one hop)", live.URL, "--version")

	// Sweep cost: a loop of short allowlisted runs against a live stub. The
	// per-run sweep walks the whole log directory, so compare the first and
	// last tenth of the loop and report the directory it ends with.
	env := envFor(live.URL)
	var all []time.Duration
	for i := 0; i < loop; i++ {
		all = append(all, run(env, shimNix, buildArgs...))
	}
	tenth := loop / 10
	fmt.Printf("sweep loop: %d short allowlisted runs, live stub\n", loop)
	summarize("first tenth", all[:tenth])
	summarize("last tenth", all[loop-tenth:])
	var files int
	var bytes int64
	_ = filepath.WalkDir(state, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
			if fi, e := d.Info(); e == nil {
				bytes += fi.Size()
			}
		}
		return nil
	})
	fmt.Printf("  log directory after the loop: %d files, %d bytes\n", files, bytes)
}
