// Package fakecmd is a test double for nix. It is imported only by tests.
//
// A test binary re-executes itself under a name starting with "fake-" (a copy
// or symlink of the test binary). TestMain calls Run first: when the process
// was started as a fake it never returns, otherwise it reports false and the
// tests proceed. Behaviour is described by a Spec in the FAKE_SPEC variable,
// which the wrapper passes through to CMD untouched.
//
// The fake finds the log file exactly the way nix would: the LAST
// "json-log-path = <absolute path>" line of NIX_CONFIG wins.
package fakecmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Spec says what the fake does.
type Spec struct {
	// Fixture is a JSONL file appended to the log; Lines are appended after it.
	Fixture string   `json:"fixture,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	// Partial is written WITHOUT a trailing newline after everything else.
	Partial string `json:"partial,omitempty"`
	// ChunkDelayMS writes the log in chunks of ChunkBytes with this pause.
	ChunkBytes   int `json:"chunk_bytes,omitempty"`
	ChunkDelayMS int `json:"chunk_delay_ms,omitempty"`
	// Stdout / Stderr are printed verbatim.
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	// Exit is the exit status. KillSelf, when non-zero, makes the fake die of
	// that signal instead (after writing everything).
	Exit     int `json:"exit,omitempty"`
	KillSelf int `json:"kill_self,omitempty"`
	// Dump receives a JSON {"argv", "nix_config", "env", "int_ignored"}.
	Dump string `json:"dump,omitempty"`
	// Ready is created once signal handlers are installed. SignalLog collects
	// one signal name per line. When HoldFor > 0 the fake then waits up to
	// that many milliseconds, XX	// TERM (or, if ExitOnINT, INT) .
	Ready        string `json:"ready,omitempty"`
	SignalLog    string `json:"signal_log,omitempty"`
	HoldForMS    int    `json:"hold_for_ms,omitempty"`
	ExitOnSignal int    `json:"exit_on_signal,omitempty"`
	ExitOnINT    bool   `json:"exit_on_int,omitempty"`
}

// Env returns the FAKE_SPEC assignment for spec.
func (s Spec) Env() string {
	b, _ := json.Marshal(s)
	return "FAKE_SPEC=" + string(b)
}

// IsFake reports whether this process was started as a fake.
func IsFake() bool { return strings.HasPrefix(filepath.Base(os.Args[0]), "fake-") }

// LogPath extracts the effective json-log-path from a NIX_CONFIG value.
func LogPath(nixConfig string) string {
	path := ""
	for _, l := range strings.Split(nixConfig, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "json-log-path = "); ok {
			path = v
		}
	}
	return path
}

// Run executes the fake and exits. Call it from TestMain; it returns false
// when this process is not a fake.
func Run() bool {
	if !IsFake() {
		return false
	}
	var s Spec
	if v := os.Getenv("FAKE_SPEC"); v != "" {
		if err := json.Unmarshal([]byte(v), &s); err != nil {
			fmt.Fprintln(os.Stderr, "fakecmd: bad FAKE_SPEC:", err)
			os.Exit(99)
		}
	}
	os.Exit(run(s))
	return true
}

func run(s Spec) int {
	if s.Dump != "" {
		b, _ := json.Marshal(map[string]any{
			"argv": os.Args, "nix_config": os.Getenv("NIX_CONFIG"), "env": os.Environ(),
			// Must be read before signal.Notify below, which re-enables the handler.
			"int_ignored": signal.Ignored(syscall.SIGINT),
		})
		_ = os.WriteFile(s.Dump, b, 0o600)
	}
	var sigs chan os.Signal
	if s.SignalLog != "" || s.HoldForMS > 0 {
		sigs = make(chan os.Signal, 16)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	}
	if s.Ready != "" {
		_ = os.WriteFile(s.Ready, []byte("ready"), 0o600)
	}

	if s.Stdout != "" {
		fmt.Fprint(os.Stdout, s.Stdout)
	}
	if s.Stderr != "" {
		fmt.Fprint(os.Stderr, s.Stderr)
	}
	writeLog(s)

	if s.HoldForMS > 0 {
		deadline := time.After(time.Duration(s.HoldForMS) * time.Millisecond)
		for {
			select {
			case sig := <-sigs:
				name := sigName(sig)
				appendLine(s.SignalLog, name)
				if sig == syscall.SIGTERM || sig == syscall.SIGHUP || (sig == syscall.SIGINT && s.ExitOnINT) {
					return s.ExitOnSignal
				}
			case <-deadline:
				return s.Exit
			}
		}
	}
	if s.KillSelf != 0 {
		_ = syscall.Kill(os.Getpid(), syscall.Signal(s.KillSelf))
		time.Sleep(5 * time.Second)
	}
	return s.Exit
}

func sigName(s os.Signal) string {
	switch s {
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGHUP:
		return "HUP"
	}
	return s.String()
}

var logMu sync.Mutex

func appendLine(path, line string) {
	if path == "" {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func writeLog(s Spec) {
	path := LogPath(os.Getenv("NIX_CONFIG"))
	if path == "" || !filepath.IsAbs(path) {
		return
	}
	var data []byte
	if s.Fixture != "" {
		b, err := os.ReadFile(s.Fixture)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakecmd:", err)
			os.Exit(98)
		}
		data = append(data, b...)
	}
	for _, l := range s.Lines {
		data = append(data, l...)
		data = append(data, '\n')
	}
	data = append(data, s.Partial...)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if s.ChunkBytes <= 0 {
		_, _ = f.Write(data)
		return
	}
	for len(data) > 0 {
		n := min(s.ChunkBytes, len(data))
		_, _ = f.Write(data[:n])
		data = data[n:]
		if s.ChunkDelayMS > 0 {
			time.Sleep(time.Duration(s.ChunkDelayMS) * time.Millisecond)
		}
	}
}

// Dir installs the running test binary as fake commands. Create one with
// NewDir in TestMain (after Run returns false) and remove it with Close.
type Dir struct {
	path string
	bin  string
	n    int
	mu   sync.Mutex
}

// NewDir copies the current executable into a fresh temporary directory.
func NewDir() (*Dir, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "fakecmd-*")
	if err != nil {
		return nil, err
	}
	d := &Dir{path: dir, bin: filepath.Join(dir, "fake-bin")}
	src, err := os.Open(exe)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	dst, err := os.OpenFile(d.bin, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return nil, err
	}
	return d, dst.Close()
}

// Close removes the directory.
func (d *Dir) Close() { _ = os.RemoveAll(d.path) }

// Path is the directory holding the fakes.
func (d *Dir) Path() string { return d.path }

// Command returns the absolute path of a fake named name (which must start
// with "fake-"), a symlink to the copied test binary. Calling it again with
// the same name returns the same path.
func (d *Dir) Command(t testing.TB, name string) string {
	t.Helper()
	if !strings.HasPrefix(name, "fake-") {
		t.Fatalf("fake command %q must start with fake-", name)
	}
	p := filepath.Join(d.path, name)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := os.Lstat(p); err != nil {
		if err := os.Symlink(d.bin, p); err != nil {
			t.Fatal(err)
		}
	}
	return p
}
