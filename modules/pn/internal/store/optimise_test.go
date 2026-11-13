package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const (
	acBatt      = "Now drawing from 'AC Power'\n"
	batteryBatt = "Now drawing from 'Battery Power'\n"
	// 100 GiB and 1 GiB available, in 1K blocks (column 4 of df -k).
	dfRoomy = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s7 500000000 1000 104857600 1% /nix\n"
	dfTight = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s7 500000000 1000 1048576 99% /nix\n"
)

// optFixture scripts a live run's non-optimise calls plus the gate probes.
func optFixture(t *testing.T, batt, load, df string) (Env, *exec.FakeRunner) {
	t.Helper()
	env, f := deepcleanFixture(t, true)
	if batt != "" {
		f.AddResponse("pmset", []string{"-g", "batt"}, exec.Result{Stdout: []byte(batt)}, nil)
	}
	if load != "" {
		f.AddResponse("sysctl", []string{"-n", "vm.loadavg"}, exec.Result{Stdout: []byte(load)}, nil)
	}
	if df != "" {
		f.AddResponse("df", []string{"-k", "/nix"}, exec.Result{Stdout: []byte(df)}, nil)
	}
	name, args := optimiseArgv(runtime.GOOS)
	f.AddResponse(name, args, exec.Result{}, nil)
	env.NixStoreDir = t.TempDir()
	return env, f
}

func optCalled(f *exec.FakeRunner) bool {
	for _, c := range f.Calls() {
		if c.Name == "nice" {
			return true
		}
	}
	return false
}

func runDC(t *testing.T, env Env, f *exec.FakeRunner, o DeepCleanOptions) (string, error) {
	t.Helper()
	o.Keep = -1
	var out, errOut bytes.Buffer
	err := NewWithEnv(f, env).DeepClean(context.Background(), &out, &errOut, o)
	return out.String(), err
}

func TestDeepClean_DefaultDoesNotOptimise(t *testing.T) {
	env, f := optFixture(t, "", "", "")
	out, err := runDC(t, env, f, DeepCleanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if optCalled(f) || strings.Contains(out, "Optimising") {
		t.Errorf("default run must not optimise:\n%s", out)
	}
	for _, c := range f.Calls() {
		if c.Name == "pmset" || c.Name == "sysctl" {
			t.Errorf("default run must not probe the idle gate: %s", c.Name)
		}
	}
}

func TestDeepClean_OptimiseRunsNicedAfterGCWhenIdle(t *testing.T) {
	env, f := optFixture(t, acBatt, "{ 0.50 1.00 2.00 }\n", dfRoomy)
	if err := os.MkdirAll(filepath.Join(env.NixStoreDir, ".links"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.NixStoreDir, ".links", "abc"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runDC(t, env, f, DeepCleanOptions{Optimise: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	calls := f.Calls()
	gc := callIndex(calls, "sudo", "nix-store", "--gc")
	opt := -1
	for i, c := range calls {
		if c.Name == "nice" {
			opt = i
		}
	}
	if gc < 0 || opt < gc {
		t.Fatalf("optimise (%d) must run after gc (%d)", opt, gc)
	}
	if calls[opt].Args[0] != "-n" || calls[opt].Args[1] != "10" {
		t.Errorf("must be niced: %v", calls[opt].Args)
	}
	if !strings.Contains(out, "started ") || !strings.Contains(out, "finished after") ||
		strings.Count(out, ".links entries: 1") != 2 {
		t.Errorf("start/end progress with .links count missing:\n%s", out)
	}
}

func TestDeepClean_OptimiseIdleGateSkips(t *testing.T) {
	cases := []struct {
		name, batt, load, df, want string
	}{
		{"battery", batteryBatt, "", "", "not on AC power"},
		{"load above 4", acBatt, "{ 4.00 1 1 }", "", "load average"},
		{"load far above", acBatt, "{ 103.40 90 80 }", "", "load average"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, f := optFixture(t, c.batt, c.load, c.df)
			out, err := runDC(t, env, f, DeepCleanOptions{Optimise: true})
			if !IsOptimiseSkipped(err) {
				t.Fatalf("want skip error, got %v", err)
			}
			if optCalled(f) {
				t.Error("optimise must not run")
			}
			if !strings.Contains(out, c.want) || !strings.Contains(out, "--force") {
				t.Errorf("message must say why and mention --force:\n%s", out)
			}
			if !strings.Contains(out, "Store after:") {
				t.Errorf("GC and summary must still complete:\n%s", out)
			}
		})
	}
}

func TestDeepClean_ForceOverridesIdleGate(t *testing.T) {
	// No pmset/sysctl scripted: --force must not probe them.
	env, f := optFixture(t, "", "", dfRoomy)
	out, err := runDC(t, env, f, DeepCleanOptions{Optimise: true, Force: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !optCalled(f) {
		t.Error("--force must run optimise")
	}
}

func TestDeepClean_FreeSpaceCheckRefuses(t *testing.T) {
	for _, force := range []bool{false, true} {
		env, f := optFixture(t, acBatt, "{ 0.1 0.1 0.1 }", dfTight)
		out, err := runDC(t, env, f, DeepCleanOptions{Optimise: true, Force: force})
		if !IsOptimiseSkipped(err) {
			t.Fatalf("force=%v: want refusal, got %v", force, err)
		}
		if optCalled(f) || !strings.Contains(out, "less than the required 10 GiB") {
			t.Errorf("force=%v: must refuse with reason:\n%s", force, out)
		}
	}
}

func TestDeepClean_FreeSpaceThresholdFromConfig(t *testing.T) {
	env, f := optFixture(t, acBatt, "{ 0.1 0.1 0.1 }", dfTight) // 1 GiB free
	writeStoreConfig(t, env, 14, 3, nil)
	cfgPath := filepath.Join(env.configHome(), "pn", "store.toml")
	b, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, append(b, []byte("optimise_min_free_gb = 1\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runDC(t, env, f, DeepCleanOptions{Optimise: true}); err != nil {
		t.Fatal(err)
	}
	if !optCalled(f) {
		t.Error("1 GiB free must pass a 1 GiB minimum")
	}
}

func TestDeepClean_OptimiseFailureIsAnError(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	f.AddResponse("pmset", []string{"-g", "batt"}, exec.Result{Stdout: []byte(acBatt)}, nil)
	f.AddResponse("sysctl", []string{"-n", "vm.loadavg"}, exec.Result{Stdout: []byte("{ 0.1 0.1 0.1 }")}, nil)
	f.AddResponse("df", []string{"-k", "/nix"}, exec.Result{Stdout: []byte(dfRoomy)}, nil)
	env.NixStoreDir = t.TempDir()
	name, args := optimiseArgv(runtime.GOOS)
	f.AddResponse(name, args, exec.Result{}, errors.New("boom"))
	_, err := runDC(t, env, f, DeepCleanOptions{Optimise: true})
	if err == nil || IsOptimiseSkipped(err) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want real failure, got %v", err)
	}
}

func TestDeepClean_DryRunMentionsOptimise(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	out, err := runDC(t, env, f, DeepCleanOptions{DryRun: true, Optimise: true})
	if err != nil || !strings.Contains(out, "Would then run nix store optimise") {
		t.Fatalf("err=%v\n%s", err, out)
	}
}

func TestOptimiseArgv(t *testing.T) {
	n, a := optimiseArgv("darwin", "--store", "/x")
	if n != "nice" || strings.Join(a, " ") != "-n 10 taskpolicy -b nix store optimise --store /x" {
		t.Errorf("darwin: %s %v", n, a)
	}
	_, a = optimiseArgv("linux")
	if strings.Join(a, " ") != "-n 10 nix store optimise" {
		t.Errorf("linux: %v", a)
	}
}

func TestParseLoadAvg(t *testing.T) {
	for in, want := range map[string]float64{"{ 1.23 4 5 }": 1.23, "{ 103.40 1 1 }\n": 103.4, "0.5 1 1": 0.5} {
		if got, ok := parseLoadAvg(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := parseLoadAvg("garbage"); ok {
		t.Error("garbage must not parse")
	}
}

func osIsDarwin() bool { return runtime.GOOS == "darwin" }
