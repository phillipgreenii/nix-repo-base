//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"testing"
)

// fakeTB records cleanups and logs so the test controls when they run.
type fakeTB struct {
	failed   bool
	cleanups []func()
	logs     []string
}

func (f *fakeTB) Helper()      {}
func (f *fakeTB) Name() string { return "TestFake/sub" }
func (f *fakeTB) Failed() bool { return f.failed }
func (f *fakeTB) Cleanup(fn func()) {
	f.cleanups = append(f.cleanups, fn)
}

func (f *fakeTB) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

// runCleanups mimics testing: last registered runs first.
func (f *fakeTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

func TestPreservedTempDir_FailureKeepsDir(t *testing.T) {
	f := &fakeTB{}
	dir := preservedTempDir(f)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f.failed = true
	f.runCleanups()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir should survive a failed test: %v", err)
	}
	if len(f.logs) != 1 || f.logs[0] != "preserved temp dir: "+dir {
		t.Errorf("logs = %q, want the preserved-path line", f.logs)
	}
}

func TestPreservedTempDir_SuccessRemovesDir(t *testing.T) {
	f := &fakeTB{}
	dir := preservedTempDir(f)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f.runCleanups()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should be removed after a passing test, stat err = %v", err)
	}
	if len(f.logs) != 0 {
		t.Errorf("unexpected logs: %q", f.logs)
	}
}
