package main

// Shim-mode process tests: the real binary (this test binary) is entered
// through symlinks named nix / darwin-rebuild, in front of a stub "real" tool
// that is a shell script. Nothing needs a real nix, a daemon or a network:
// the collector is a loopback httptest server.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
)

// writeStub installs dir/name, a script that records its $0, arguments and
// environment into rec and exits with exit. dir is created.
func writeStub(t *testing.T, dir, name, rec string, exit int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n{\n echo \"argv0=$0\"\n for a in \"$@\"; do echo \"arg=$a\"; done\n echo ---\n env\n} > '" + rec + "'\nexit " + itoa(exit) + "\n"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeFakeStub installs dir/name as a script that records $0 and then
// replaces itself with the fake command (driven by FAKE_SPEC).
func writeFakeStub(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec '"+fakes.Command(t, "fake-nix")+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func itoa(n int) string { return strconv.Itoa(n) }

// linkShim makes dir/name a symlink to target (the wrapper binary or a copy).
func linkShim(t *testing.T, dir, name, target string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// copyWrapper copies the wrapper binary to dir/pg-nix-log-wrapped: a
// DIFFERENT wrapper build, with a different inode.
func copyWrapper(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "pg-nix-log-wrapped")
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

type recorded struct {
	argv0 string
	args  []string
	env   []string
}

func readRec(t *testing.T, rec string) (recorded, bool) {
	t.Helper()
	b, err := os.ReadFile(rec)
	if err != nil {
		return recorded{}, false
	}
	head, envPart, _ := strings.Cut(string(b), "---\n")
	var r recorded
	for _, l := range strings.Split(strings.TrimSpace(head), "\n") {
		if v, ok := strings.CutPrefix(l, "argv0="); ok {
			r.argv0 = v
		} else if v, ok := strings.CutPrefix(l, "arg="); ok {
			r.args = append(r.args, v)
		}
	}
	r.env = envSet(strings.Split(strings.TrimSpace(envPart), "\n"))
	return r, true
}

// envSet drops the variables a POSIX sh adds on its own.
func envSet(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "_", "PWD", "OLDPWD", "SHLVL", "":
			continue
		}
		out = append(out, kv)
	}
	slices.Sort(out)
	return out
}

func runProg(t *testing.T, prog string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), prog, args...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return result{out.String(), errb.String(), code, err}
}

func TestShimDispatchRealBinary(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		args    []string
		env     []string
		wrapped bool
	}{
		{"allowlisted build", "nix", []string{"build", ".#x"}, nil, true},
		{"flag before verb", "nix", []string{"-L", "build", ".#x"}, nil, true},
		{"flake check", "nix", []string{"flake", "check"}, nil, true},
		{"darwin-rebuild switch", "darwin-rebuild", []string{"switch"}, nil, true},
		{"darwin-rebuild activate", "darwin-rebuild", []string{"activate"}, nil, false},
		{"off-allowlist eval", "nix", []string{"eval", ".#x"}, nil, false},
		{"--version", "nix", []string{"--version"}, nil, false},
		{"--help", "nix", []string{"--help"}, nil, false},
		{"completion call", "nix", []string{"build", "--op"}, []string{"NIX_GET_COMPLETIONS=2"}, false},
		{"no subcommand", "nix", nil, nil, false},
		{"unknown subcommand", "nix", []string{"frobnicate"}, nil, false},
		{"value-flag argv", "nix", []string{"--option", "foo", "build", "eval", ".#x"}, nil, false},
		{"disabled", "nix", []string{"build"}, []string{"PG_NIX_LOG_DISABLE=1"}, false},
		{"guard set", "nix", []string{"build"}, []string{"PG_NIX_LOG_ACTIVE=1"}, false},
	}
	for _, c := range cases {
		for _, debug := range []bool{false, true} {
			name := c.name
			if debug {
				name += " (debug)"
			}
			t.Run(name, func(t *testing.T) {
				col := newCollector(t)
				base := t.TempDir()
				rec := filepath.Join(base, "rec")
				realDir, shimDir := filepath.Join(base, "real"), filepath.Join(base, "shim")
				writeStub(t, realDir, c.tool, rec, 7)
				linkShim(t, shimDir, c.tool, wrapper)
				extra := append([]string{
					"PATH=" + shimDir + ":" + realDir + ":/usr/bin:/bin",
					"OTEL_EXPORTER_OTLP_ENDPOINT=" + col.srv.URL,
				}, c.env...)
				if debug {
					extra = append(extra, "PG_NIX_LOG_DEBUG=1")
				}
				e, _ := env(t, extra...)
				res := runProg(t, filepath.Join(shimDir, c.tool), e, c.args...)
				if res.code != 7 {
					t.Fatalf("exit %d (want the stub's 7), stderr %q", res.code, res.stderr)
				}
				if res.stdout != "" {
					t.Errorf("stdout = %q", res.stdout)
				}
				got, ok := readRec(t, rec)
				if !ok {
					t.Fatal("the real command did not run")
				}
				if got.argv0 != filepath.Join(realDir, c.tool) || !slices.Equal(got.args, c.args) {
					t.Errorf("real command saw argv0=%q args=%q", got.argv0, got.args)
				}
				invs := len(col.spansNamed("nix.invocation"))
				if c.wrapped {
					if invs != 1 {
						t.Errorf("nix.invocation spans = %d, want 1", invs)
					}
					if !slices.Contains(got.env, "PG_NIX_LOG_ACTIVE=1") {
						t.Error("wrapped command lacks the guard")
					}
					if debug && !strings.Contains(res.stderr, "shim: wrapping") {
						t.Errorf("debug stderr = %q", res.stderr)
					}
					return
				}
				if invs != 0 {
					t.Errorf("a passthrough produced %d spans", invs)
				}
				want := envSet(e)
				if !debug && c.name == "guard set" {
					// the caller set it; it must still be there, once
					want = envSet(e)
				}
				if !slices.Equal(got.env, want) {
					t.Errorf("passthrough environment differs:\n got %v\nwant %v", got.env, want)
				}
				if debug {
					lines := strings.Split(strings.TrimSpace(res.stderr), "\n")
					if len(lines) != 1 || !strings.Contains(lines[0], "shim: passthrough") {
						t.Errorf("debug stderr = %q", res.stderr)
					}
				} else if res.stderr != "" {
					t.Errorf("silent passthrough printed %q", res.stderr)
				}
			})
		}
	}
}

func TestOwnNameBehaviourIsUnchanged(t *testing.T) {
	// Under its own name the binary still parses its flags: an unknown one is
	// a usage error (exit 2), which shim mode must never produce.
	e, _ := env(t)
	res := runWrapper(t, e, "--version")
	if res.code != 2 {
		t.Errorf("exit = %d, want 2; stderr %q", res.code, res.stderr)
	}
}

func TestDarwinRebuildKeepsItsSwBinSuffix(t *testing.T) {
	for _, verb := range []string{"build", "activate"} {
		t.Run(verb, func(t *testing.T) {
			col := newCollector(t)
			base := t.TempDir()
			rec := filepath.Join(base, "rec")
			realDir := filepath.Join(base, "system", "sw", "bin")
			writeStub(t, realDir, "darwin-rebuild", rec, 0)
			shimDir := filepath.Join(base, "shim")
			linkShim(t, shimDir, "darwin-rebuild", wrapper)
			e, _ := env(t, "PATH="+shimDir+":"+realDir+":/usr/bin:/bin", "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL)
			if res := runProg(t, filepath.Join(shimDir, "darwin-rebuild"), e, verb); res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			got, _ := readRec(t, rec)
			if !strings.HasSuffix(got.argv0, "/sw/bin/darwin-rebuild") {
				t.Errorf("$0 = %q", got.argv0)
			}
			if (verb == "build") != (len(col.spansNamed("nix.invocation")) == 1) {
				t.Errorf("wrapped = %v for %s", len(col.spansNamed("nix.invocation")) == 1, verb)
			}
		})
	}
}

func TestExplicitWrapperBehindAShimYieldsOneInvocation(t *testing.T) {
	for _, differentBuild := range []bool{false, true} {
		name := "same wrapper binary"
		if differentBuild {
			name = "different wrapper build"
		}
		t.Run(name, func(t *testing.T) {
			col := newCollector(t)
			base := t.TempDir()
			rec := filepath.Join(base, "rec")
			realDir, shimDir := filepath.Join(base, "real"), filepath.Join(base, "shim")
			writeStub(t, realDir, "nix", rec, 0)
			target := wrapper
			if differentBuild {
				target = copyWrapper(t, filepath.Join(base, "build2"))
			}
			linkShim(t, shimDir, "nix", target)
			e, _ := env(t, "PATH="+shimDir+":"+realDir+":/usr/bin:/bin", "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL)
			cmd, _, errb := wrapperCmd(e, "nix", "build")
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v: %s", err, errb)
			}
			if n := len(col.spansNamed("nix.invocation")); n != 1 {
				t.Errorf("nix.invocation spans = %d, want 1", n)
			}
			got, ok := readRec(t, rec)
			if !ok || got.argv0 != filepath.Join(realDir, "nix") {
				t.Errorf("real stub not reached directly: %+v", got)
			}
		})
	}
}

func TestTwoShimsDoNotLoop(t *testing.T) {
	base := t.TempDir()
	buildA, buildB := copyWrapper(t, filepath.Join(base, "buildA")), copyWrapper(t, filepath.Join(base, "buildB"))
	shimA, shimB := filepath.Join(base, "shimA"), filepath.Join(base, "shimB")
	linkShim(t, shimA, "nix", buildA)
	linkShim(t, shimB, "nix", buildB)
	col := newCollector(t)
	path := shimA + ":" + shimB + ":/usr/bin:/bin"
	e, _ := env(t, "PATH="+path, "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL)
	done := make(chan result, 1)
	go func() { done <- runProg(t, filepath.Join(shimA, "nix"), e, "build") }()
	select {
	case res := <-done:
		if res.code != 127 {
			t.Errorf("exit %d, want 127; stderr %q", res.code, res.stderr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("two shims looped")
	}
	// With the real tool present, the chain reaches it.
	rec := filepath.Join(base, "rec")
	realDir := filepath.Join(base, "real")
	writeStub(t, realDir, "nix", rec, 0)
	e, _ = env(t, "PATH="+shimA+":"+shimB+":"+realDir+":/usr/bin:/bin", "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL)
	if res := runProg(t, filepath.Join(shimA, "nix"), e, "build"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if got, ok := readRec(t, rec); !ok || got.argv0 != filepath.Join(realDir, "nix") {
		t.Errorf("real tool not reached: %+v", got)
	}
}

func TestShimSignalsAndExitCodes(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wrapped bool
	}{
		{"wrapped run", []string{"build"}, true},
		{"direct run", []string{"eval", "."}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			realDir, shimDir := filepath.Join(base, "real"), filepath.Join(base, "shim")
			writeFakeStub(t, realDir, "nix")
			linkShim(t, shimDir, "nix", wrapper)
			spec := fakecmd.Spec{
				Ready: filepath.Join(base, "ready"), SignalLog: filepath.Join(base, "signals"),
				HoldForMS: 20000, DieOnSignal: true, ExitOnINT: false,
			}
			col := newCollector(t)
			e, _ := env(t, "PATH="+shimDir+":"+realDir+":/usr/bin:/bin", "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL, spec.Env())
			cmd := exec.CommandContext(context.Background(), filepath.Join(shimDir, "nix"), c.args...)
			cmd.Env = e
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			waitFor(t, 20*time.Second, func() bool { _, err := os.Stat(spec.Ready); return err == nil }, "the real command to be ready")
			// SIGINT is not forwarded by a wrapped run (the shared process
			// group delivers it); only a direct run, being the real command,
			// receives it from this pid-targeted signal.
			_ = cmd.Process.Signal(syscall.SIGINT)
			time.Sleep(100 * time.Millisecond)
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("timed out waiting for exit")
			}
			code := cmd.ProcessState.ExitCode()
			if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
			if code != 128+int(syscall.SIGTERM) {
				t.Errorf("exit = %d, want 143", code)
			}
			b, _ := os.ReadFile(spec.SignalLog)
			sigs := strings.Fields(string(b))
			if !slices.Contains(sigs, "TERM") {
				t.Errorf("SIGTERM never reached the real command: %v", sigs)
			}
			if c.wrapped && slices.Contains(sigs, "INT") {
				t.Errorf("a wrapped run forwarded SIGINT: %v", sigs)
			}
		})
	}
}
