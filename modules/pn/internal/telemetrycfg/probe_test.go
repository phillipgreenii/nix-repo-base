package telemetrycfg

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostPort(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:4318":         "127.0.0.1:4318",
		"http://127.0.0.1:4318":  "127.0.0.1:4318",
		"https://collector:4443": "collector:4443",
		"http://collector":       "collector:80",
		"https://collector":      "collector:443",
		"collector":              "collector:4318",
		"http://[::1]:4318":      "[::1]:4318",
	} {
		got, err := hostPort(in)
		if err != nil || got != want {
			t.Errorf("hostPort(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := hostPort("http://"); err == nil {
		t.Error("expected error for empty host")
	}
}

func TestProbe_ReachableAndNot(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().String()
	if err := Probe(context.Background(), "http://"+addr, time.Second); err != nil {
		t.Fatalf("reachable collector: %v", err)
	}
	_ = ln.Close()
	if err := Probe(context.Background(), "http://"+addr, time.Second); err == nil {
		t.Fatal("expected error once the listener is closed")
	}
}

// TestPrepare_UnreachableUnderVerbose: enabled + -v + dead endpoint prints the
// one-line message to stderr and turns telemetry off for the run.
func TestPrepare_UnreachableUnderVerbose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens here now

	var stderr bytes.Buffer
	res := Prepare(context.Background(),
		[]string{"-v", "--otlp-endpoint", "http://" + addr, "workspace", "status"},
		envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()}), false, &stderr)
	want := "telemetry disabled: collector unreachable (http://" + addr + ")\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q; want %q", stderr.String(), want)
	}
	if res.Enabled || res.Endpoint != "" {
		t.Fatalf("unreachable collector must leave telemetry off: %+v", res)
	}
}

// TestPrepare_NoProbeWithoutVerbose: a dead endpoint without -v is silent and
// the probe is never attempted (no latency, no output).
func TestPrepare_NoProbeWithoutVerbose(t *testing.T) {
	var conns atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			_ = c.Close()
		}
	}()
	var stderr bytes.Buffer
	res := Prepare(context.Background(), []string{"--otlp-endpoint", "http://" + ln.Addr().String(), "workspace"},
		envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()}), false, &stderr)
	if !res.Enabled || stderr.Len() != 0 {
		t.Fatalf("expected enabled+silent, got %+v / %q", res, stderr.String())
	}
	time.Sleep(50 * time.Millisecond) // allow any stray connection to land
	if n := conns.Load(); n != 0 {
		t.Fatalf("probe ran without -v: %d connections", n)
	}
}

// TestNoOpPath_ZeroConnectionsNoGoroutinesNoFiles is the Null Object contract:
// with telemetry forced off or unconfigured, resolving it (even under -v) opens
// ZERO connections to a listener on a random port, leaks no goroutine and
// creates no file under HOME.
func TestNoOpPath_ZeroConnectionsNoGoroutinesNoFiles(t *testing.T) {
	var conns atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			_ = c.Close()
		}
	}()
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}

	before := runtime.NumGoroutine()
	for _, args := range [][]string{
		{"-v", "workspace", "status"},
		{"-v", "--no-telemetry", "--otlp-endpoint", "http://" + ln.Addr().String(), "workspace", "status"},
	} {
		var stderr bytes.Buffer
		res := Prepare(context.Background(), args, envMap(env), false, &stderr)
		if res.Enabled {
			t.Fatalf("args %v: telemetry unexpectedly enabled: %+v", args, res)
		}
		if stderr.Len() != 0 {
			t.Fatalf("args %v: disabled path wrote to stderr: %q", args, stderr.String())
		}
	}
	// Forced off by the environment, with an endpoint env pointing at the listener.
	env[EnvEndpoint] = "http://" + ln.Addr().String()
	env[EnvSDKDisabled] = "true"
	if res := Prepare(context.Background(), []string{"-v"}, envMap(env), false, &bytes.Buffer{}); res.Enabled {
		t.Fatalf("OTEL_SDK_DISABLED=true must force off: %+v", res)
	}

	time.Sleep(50 * time.Millisecond)
	if n := conns.Load(); n != 0 {
		t.Fatalf("no-op path opened %d connection(s); want 0", n)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("files created under HOME: %s", strings.Join(names, ", "))
	}
}
