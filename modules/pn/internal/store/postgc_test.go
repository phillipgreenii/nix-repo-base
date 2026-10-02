package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// writePostGCConfig writes a store.toml body (raw TOML) for a post-GC test.
func writePostGCConfig(t *testing.T, env Env, body string) {
	t.Helper()
	writeStoreTOML(t, env, body)
}

func mkCacheDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "stale-bin"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func callIndex(calls []exec.Call, name string, args ...string) int {
	for i, c := range calls {
		if c.Name != name || len(c.Args) != len(args) {
			continue
		}
		match := true
		for j := range args {
			if c.Args[j] != args[j] {
				match = false
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestParseStoreConfig_PostGC(t *testing.T) {
	c := parseStoreConfig([]byte(`
post_gc_clear_dirs = ["~/.cache/a", "/abs/b"]
post_gc_commands = [["tool", "--clear"], ["other"]]
`))
	if len(c.PostGCClearDirs) != 2 || c.PostGCClearDirs[0] != "~/.cache/a" || c.PostGCClearDirs[1] != "/abs/b" {
		t.Errorf("PostGCClearDirs = %v", c.PostGCClearDirs)
	}
	if len(c.PostGCCommands) != 2 || strings.Join(c.PostGCCommands[0], " ") != "tool --clear" || strings.Join(c.PostGCCommands[1], " ") != "other" {
		t.Errorf("PostGCCommands = %v", c.PostGCCommands)
	}
	if d := parseStoreConfig([]byte("search_dirs = []\n")); d.hasPostGC() {
		t.Errorf("absent keys must mean no post-GC work, got %+v", d)
	}
}

func TestResolveClearDir(t *testing.T) {
	env := Env{Home: "/home-x/u"}
	ok := map[string]string{
		"~/.cache/a":     "/home-x/u/.cache/a",
		"/abs/b/":        "/abs/b",
		"~/.zuul/go-run": "/home-x/u/.zuul/go-run",
	}
	for in, want := range ok {
		got, err := env.resolveClearDir(in)
		if err != nil || got != want {
			t.Errorf("resolveClearDir(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "relative/dir", "~", "~/", "/", "/home-x/u", "~user/x"} {
		if got, err := env.resolveClearDir(bad); err == nil {
			t.Errorf("resolveClearDir(%q) = %q, want error", bad, got)
		}
	}
}

func TestDeepClean_PostGC_ClearsDirsAfterGCBeforeOptimise(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	present := filepath.Join(env.Home, ".cache", "present")
	absent := filepath.Join(env.Home, ".cache", "absent") // never created: missing dir
	mkCacheDir(t, present)
	writePostGCConfig(t, env, `post_gc_clear_dirs = ["~/.cache/present", "~/.cache/absent"]`+"\n")

	var out, errOut bytes.Buffer
	if err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{Keep: -1}); err != nil {
		t.Fatalf("a missing dir must not be an error: %v", err)
	}
	if _, err := os.Lstat(present); !os.IsNotExist(err) {
		t.Errorf("configured dir must be removed after GC (stat err=%v)", err)
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Errorf("absent dir must stay absent")
	}
	if !strings.Contains(out.String(), "Post-GC cleanup:") ||
		!strings.Contains(out.String(), present+": removed") ||
		!strings.Contains(out.String(), absent+": not present") {
		t.Errorf("post-GC report missing:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("missing dir must not warn on stderr: %q", errOut.String())
	}
}

func TestDeepClean_PostGC_DirsRemovedOnlyAfterGC(t *testing.T) {
	// The dir must still exist while `sudo nix-store --gc` runs and be gone
	// once DeepClean returns: observe it from inside the GC call.
	env, f := deepcleanFixture(t, true)
	dir := filepath.Join(env.Home, ".cache", "bins")
	mkCacheDir(t, dir)
	writePostGCConfig(t, env, `post_gc_clear_dirs = ["`+dir+`"]`+"\n")

	f.Reset()
	scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 10.0 GB (10737418240 Bytes)")
	scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 9.0 GB (9663676416 Bytes)")
	f.AddResponse("sudo", []string{"nix-store", "--gc"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"store", "optimise"}, exec.Result{}, nil)
	f.AddResponse("nix-store", []string{"--gc", "--print-roots"}, exec.Result{Stdout: []byte("")}, nil)

	var out, errOut bytes.Buffer
	var seen bool
	probe := &statProbeRunner{inner: f, path: dir, onGC: func(exists bool) { seen = exists }}
	if err := NewWithEnv(probe, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{Keep: -1}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Error("cache dir must still exist during the GC (cleared AFTER it)")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Error("cache dir must be gone after DeepClean")
	}
}

// statProbeRunner records whether path exists at the moment the GC call runs.
type statProbeRunner struct {
	inner exec.Runner
	path  string
	onGC  func(exists bool)
}

func (r *statProbeRunner) Run(ctx context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	if name == "sudo" && len(args) == 2 && args[1] == "--gc" {
		_, err := os.Lstat(r.path)
		r.onGC(err == nil)
	}
	return r.inner.Run(ctx, name, args, opts)
}

func TestDeepClean_PostGC_FailingCommandReportedOthersStillRun(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	dir := filepath.Join(env.Home, ".cache", "bins")
	mkCacheDir(t, dir)
	writePostGCConfig(t, env, `post_gc_clear_dirs = ["`+dir+`"]
post_gc_commands = [["bad-tool", "--x"], ["good-tool"]]
`)
	f.AddResponse("bad-tool", []string{"--x"}, exec.Result{}, errors.New("boom"))
	f.AddResponse("good-tool", nil, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{Keep: -1})
	if err == nil || !strings.Contains(err.Error(), "post-GC cleanup") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failing post-GC command must surface as an error, got %v", err)
	}
	calls := f.Calls()
	gc := callIndex(calls, "sudo", "nix-store", "--gc")
	bad := callIndex(calls, "bad-tool", "--x")
	good := callIndex(calls, "good-tool")
	opt := callIndex(calls, "nix", "store", "optimise")
	if !(gc >= 0 && bad > gc && good > bad && opt > good) {
		t.Errorf("want gc < bad-tool < good-tool < optimise, got gc=%d bad=%d good=%d opt=%d", gc, bad, good, opt)
	}
	if _, statErr := os.Lstat(dir); !os.IsNotExist(statErr) {
		t.Error("dirs are cleared before commands and must be gone")
	}
	if !strings.Contains(errOut.String(), "post-GC command failed") {
		t.Errorf("failure must be reported on stderr, got %q", errOut.String())
	}
	if !strings.Contains(out.String(), "Store after:") {
		t.Errorf("run must complete (optimise + summary) despite the failure:\n%s", out.String())
	}
}

func TestDeepClean_PostGC_RunsEvenWhenGCFails(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	env := Env{Home: home}
	dir := filepath.Join(home, ".cache", "bins")
	mkCacheDir(t, dir)
	writePostGCConfig(t, env, `post_gc_clear_dirs = ["`+dir+`"]`+"\n")

	f := exec.NewFakeRunner()
	scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 10.0 GB (10737418240 Bytes)")
	f.AddResponse("sudo", []string{"nix-store", "--gc"}, exec.Result{}, errors.New("gc died"))

	var out, errOut bytes.Buffer
	err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{Keep: -1})
	if err == nil || !strings.Contains(err.Error(), "nix-store --gc") {
		t.Fatalf("GC failure must be returned, got %v", err)
	}
	if _, statErr := os.Lstat(dir); !os.IsNotExist(statErr) {
		t.Error("a GC that died midway may have deleted linked paths: caches must still be cleared")
	}
	if callIndex(f.Calls(), "nix", "store", "optimise") != -1 {
		t.Error("optimise must not run after a failed GC")
	}
}

func TestDeepClean_PostGC_InvalidDirRefusedNotRemoved(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	keep := filepath.Join(env.Home, "keepme")
	mkCacheDir(t, keep)
	writePostGCConfig(t, env, `post_gc_clear_dirs = ["`+env.Home+`", "relative/dir"]`+"\n")

	var out, errOut bytes.Buffer
	err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{Keep: -1})
	if err == nil || !strings.Contains(err.Error(), "post-GC cleanup") {
		t.Fatalf("invalid entries must surface as an error, got %v", err)
	}
	if _, statErr := os.Lstat(keep); statErr != nil {
		t.Error("HOME contents must never be removed")
	}
}

func TestDeepClean_PostGC_DryRunListsButDoesNothing(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	dir := filepath.Join(env.Home, ".cache", "bins")
	mkCacheDir(t, dir)
	writePostGCConfig(t, env, `post_gc_clear_dirs = ["`+dir+`"]
post_gc_commands = [["some-tool", "--clear"]]
`)

	var out, errOut bytes.Buffer
	if err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{DryRun: true, Keep: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Error("dry-run must not remove the dir")
	}
	if callIndex(f.Calls(), "some-tool", "--clear") != -1 {
		t.Error("dry-run must not run post-GC commands")
	}
	for _, want := range []string{"Would run after GC:", "remove " + dir, "run: some-tool --clear"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDeepClean_PostGC_UnconfiguredPrintsNothing(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	var out, errOut bytes.Buffer
	if err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, DeepCleanOptions{Keep: -1}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Post-GC") {
		t.Errorf("no post-GC config must add no output:\n%s", out.String())
	}
}
