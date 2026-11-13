package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Tests of the NixOS activation platform (ADR 0043): the systemd-run wrapped
// activation, the switch-only action allowlist, the nixos-version check, the
// no-argument probe and the detached-failure message. Everything runs against
// the fake runner; nothing real is built, probed or activated.

// newNixOSEnv is a trusted workspace over a fake NixOS system, with the systemd
// seam set to systemd.
func newNixOSEnv(t *testing.T, systemd bool) *asUserEnv {
	t.Helper()
	e := newAsUserEnv(t, nixosTOML, nil)
	installAsUserSeams(e.w, e.sys.store, withSystemd(systemd))
	e.f.sys = newFakeNixOSSystem(t, e.sys.store, fakeHash2)
	return e
}

func (e *asUserEnv) applyNixOS(ctx context.Context, out io.Writer) error {
	// No Builder: the platform comes from activate_command, never from the host.
	return e.w.Apply(ctx, out, ApplyOptions{Force: true})
}

// --- activateArgv -----------------------------------------------------------------

func TestActivateArgv_Table(t *testing.T) {
	const out = "/nix/store/" + fakeHash + "-sys"
	vars := templateVars{}
	darwin := &asUserPlan{activateTmpl: darwinActivateTmpl, platform: darwinPlatform(t)}
	nixos := &asUserPlan{activateTmpl: nixosActivateTmpl, platform: nixosPlatform(t)}
	nixosSD := &asUserPlan{activateTmpl: nixosActivateTmpl, platform: nixosPlatform(t), systemd: true}
	cases := []struct {
		name string
		plan *asUserPlan
		want string
	}{
		{"darwin is literal", darwin, "sudo " + out + "/sw/bin/darwin-rebuild activate"},
		{"nixos without systemd", nixos, nixosActivateWant(false, out)},
		{"nixos with systemd", nixosSD, nixosActivateWant(true, out)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.plan.activateArgv(vars, out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("got  %s\nwant %s", strings.Join(got, " "), tc.want)
			}
		})
	}
	// Darwin equals substituteActivateCommand exactly, even when the systemd
	// decision is true (a darwin plan never wraps).
	darwinSD := &asUserPlan{activateTmpl: darwinActivateTmpl, platform: darwinPlatform(t), systemd: true}
	want, err := substituteActivateCommand(darwinActivateTmpl, withOut(vars, out))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := darwinSD.activateArgv(vars, out)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("darwin activateArgv = %q, want substituteActivateCommand's %q", got, want)
	}
	// An invalid template is an error, as before.
	bad := &asUserPlan{activateTmpl: "sudo {out}/x/{nope}", platform: nixosPlatform(t)}
	if _, err := bad.activateArgv(vars, out); err == nil {
		t.Error("an invalid template was accepted")
	}
}

// What the dry run shows, what the apply-log header records and what runs are the
// same argv, for each seam value.
func TestActivateArgv_DryRunHeaderAndRunAgree(t *testing.T) {
	for _, systemd := range []bool{false, true} {
		t.Run(fmt.Sprintf("systemd=%v", systemd), func(t *testing.T) {
			e := newNixOSEnv(t, systemd)
			var out bytes.Buffer
			if err := e.applyNixOS(context.Background(), &out); err != nil {
				t.Fatalf("Apply: %v\n%s", err, out.String())
			}
			o := e.f.sys.out
			want := nixosActivateWant(systemd, o)
			ran := e.f.rootCalls()
			if got := argvOf(ran[len(ran)-1]); got != want {
				t.Fatalf("ran %s, want %s", got, want)
			}
			header, _, _ := strings.Cut(e.applyLogText(t), applyLogOutput)
			if !strings.Contains(header, "activate command: "+nixosActivateWant(systemd, "<out>")) {
				t.Errorf("header does not show the run's activate argv with <out>:\n%s", header)
			}
			// The dry-run lines come from the same function.
			plan := &asUserPlan{activateTmpl: nixosActivateTmpl, platform: nixosPlatform(t), systemd: systemd}
			lines := plan.describeSteps(&rebuildPlan{})
			if !strings.Contains(strings.Join(lines, "\n"), "activate command: "+nixosActivateWant(systemd, "<out>")) {
				t.Errorf("dry-run lines = %q", lines)
			}
		})
	}
}

// --- the full sequence, root step options -----------------------------------------

func TestAsUser_NixOSRootStepsPinOptionsAndTrailer(t *testing.T) {
	for _, systemd := range []bool{false, true} {
		t.Run(fmt.Sprintf("systemd=%v", systemd), func(t *testing.T) {
			e := newNixOSEnv(t, systemd)
			if err := e.applyNixOS(context.Background(), io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != "none" {
				t.Errorf("failed_step = %q", got)
			}
			root := e.f.rootCalls()
			if len(root) != 2 {
				t.Fatalf("root calls = %d:\n%s", len(root), dumpCalls(root))
			}
			for _, c := range root {
				if c.Opts.Env != nil {
					t.Errorf("root step %s passes Env %v: the variables travel as env -i argv", argvOf(c), c.Opts.Env)
				}
				if strings.Contains(strings.Join(c.Args, " "), "LOCALE_ARCHIVE") || strings.Contains(strings.Join(c.Args, " "), "NIXOS_NO_CHECK") {
					t.Errorf("a root argv forwards a variable pn deliberately does not: %s", argvOf(c))
				}
			}
			probe := e.f.significant()[1]
			if probe.Name != e.f.sys.out+"/bin/switch-to-configuration" || len(probe.Args) != 0 || probe.Opts.WrapNix || probe.Opts.Dir == "" {
				t.Errorf("probe call = %s %+v, want the unwrapped no-argument script in the run dir", argvOf(probe), probe.Opts)
			}
		})
	}
}

// --- D2: the action allowlist -----------------------------------------------------

func TestPlatformForActivate_NixOSActionAllowlist(t *testing.T) {
	cases := []struct {
		tmpl   string
		wantOK bool
	}{
		{"sudo {out}/bin/switch-to-configuration switch", true},
		{"sudo {out}/bin/switch-to-configuration boot", false},
		{"sudo {out}/bin/switch-to-configuration test", false},
		{"sudo {out}/bin/switch-to-configuration dry-activate", false},
		{"sudo {out}/bin/switch-to-configuration check", false},
		{"sudo {out}/bin/switch-to-configuration", false},
		{"sudo {out}/bin/switch-to-configuration switch --install-bootloader", false},
		{"sudo {out}/bin/switch-to-configuration switch extra", false},
	}
	for _, tc := range cases {
		t.Run(tc.tmpl, func(t *testing.T) {
			_, err := platformForActivate(tc.tmpl)
			if tc.wantOK != (err == nil) {
				t.Fatalf("err = %v, wantOK = %v", err, tc.wantOK)
			}
			if err != nil && !strings.Contains(err.Error(), "always sets the system profile first") {
				t.Errorf("the error does not explain why: %v", err)
			}
		})
	}
	// Darwin templates get no new validation.
	for _, tmpl := range []string{darwinActivateTmpl, "sudo {out}/sw/bin/darwin-rebuild switch", "sudo {out}/sw/bin/darwin-rebuild"} {
		if _, err := platformForActivate(tmpl); err != nil {
			t.Errorf("darwin template %q rejected: %v", tmpl, err)
		}
	}
}

// A rejected action fails in the preflight with no process started.
func TestAsUser_NixOSRejectedActionFailsBeforeAnyProcess(t *testing.T) {
	for _, action := range []string{"boot", "test", "dry-activate", "check", "switch --install-bootloader"} {
		t.Run(action, func(t *testing.T) {
			toml := strings.Replace(nixosTOML, "{out}/bin/switch-to-configuration switch", "{out}/bin/switch-to-configuration "+action, 1)
			e := newAsUserEnv(t, toml, nil)
			err := e.w.Apply(context.Background(), io.Discard, ApplyOptions{Force: true})
			if err == nil || !strings.Contains(err.Error(), "only the switch action is accepted") {
				t.Fatalf("err = %v", err)
			}
			if n := len(e.f.significant()); n != 0 {
				t.Errorf("commands ran: %s", dumpCalls(e.f.significant()))
			}
			if e.applied(t) {
				t.Error("applied despite the rejection")
			}
		})
	}
}

// --- D3: the probe parser ---------------------------------------------------------

func TestParseSwitchToConfigurationUsage(t *testing.T) {
	cases := []struct {
		name     string
		exit     int
		stdout   string
		stderr   string
		wantErr  string // "" = accepted
		wantQuot string // text the error must quote
	}{
		{"captured usage of a real system", 1, "", nixosUsage, "", ""},
		{"reworded but keyworded", 1, "", "usage: s-t-c <switch|boot>\n", "", ""},
		{"exit 0", 0, "", nixosUsage, "exited 0", "Usage: switch-to-configuration"},
		{"exit 2", 2, "", nixosUsage, "exited 2", "Usage: switch-to-configuration"},
		{"stdout non-empty", 1, "oops", nixosUsage, "stdout", ""},
		{"no switch word", 1, "", "Usage: boot only\n", "does not mention", "Usage: boot only"},
		{"no boot word", 1, "", "Usage: switch only\n", "does not mention", "Usage: switch only"},
		{"empty stderr", 1, "", "", "does not mention", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseSwitchToConfigurationUsage(tc.exit, []byte(tc.stdout), []byte(tc.stderr))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if tc.wantQuot != "" && !strings.Contains(err.Error(), tc.wantQuot) {
				t.Errorf("err = %v does not quote %q", err, tc.wantQuot)
			}
		})
	}
}

// Probe outcomes through the real flow: timeout and could-not-run, each a hard
// probe failure naming the manual command, with no root step.
func TestAsUser_NixOSProbeFailures(t *testing.T) {
	cases := []struct {
		name     string
		probe    func(context.Context, exec.RunOptions) (exec.Result, error)
		wantText string
		wantExit string
	}{
		{"usage lacks the switch action", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte("Usage: boot\n")})
		}, "does not mention the switch and boot actions", "1"},
		{"exit 0", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return exec.Result{ExitCode: 0, Stderr: []byte(nixosUsage)}, nil
		}, "exited 0, want 1", ""},
		{"could not run", func(context.Context, exec.RunOptions) (exec.Result, error) {
			return exec.Result{}, errors.New("fork/exec: no such file")
		}, "the probe could not run", ""},
		{"timeout", func(c context.Context, _ exec.RunOptions) (exec.Result, error) {
			<-c.Done()
			return exec.Result{ExitCode: -1}, &exec.CommandError{Name: "p", Result: exec.Result{ExitCode: -1}}
		}, "did not finish within", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := probeTimeout
			probeTimeout = 50 * time.Millisecond
			t.Cleanup(func() { probeTimeout = old })
			e := newNixOSEnv(t, true)
			e.f.probe = tc.probe
			err := e.applyNixOS(context.Background(), io.Discard)
			if err == nil || !strings.Contains(err.Error(), "apply failed: probe:") || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err = %v, want a probe failure containing %q", err, tc.wantText)
			}
			for _, want := range []string{"switch-to-configuration switch", "a pn check, not a system fault", e.f.sys.out + "/bin/switch-to-configuration with no arguments"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "darwin") {
				t.Errorf("NixOS probe error names darwin: %v", err)
			}
			if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != "probe" {
				t.Errorf("failed_step = %q", got)
			}
			if n := len(e.f.rootCalls()); n != 0 {
				t.Errorf("root steps ran after a failed probe: %d", n)
			}
			if e.applied(t) {
				t.Error("applied despite a failed probe")
			}
		})
	}
}

// The darwin probe error text stays byte-identical.
func TestAsUser_DarwinProbeErrorTextUnchanged(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	e.f.probe = func(context.Context, exec.RunOptions) (exec.Result, error) {
		return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte("nothing useful\n")})
	}
	err := e.apply(context.Background(), io.Discard)
	want := `apply failed: probe: the built system has no usable 'darwin-rebuild activate' (first usage line "nothing useful" does not start with darwin-rebuild)`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
}

// --- D4: nixos-version ------------------------------------------------------------

func TestNixOSCheckOut_NixosVersion(t *testing.T) {
	store := t.TempDir()
	cases := []struct {
		name   string
		break_ func(out string)
		want   string
	}{
		{"present", func(string) {}, ""},
		{"missing", func(out string) { _ = os.Remove(out + "/nixos-version") }, "no nixos-version"},
		{"a directory", func(out string) {
			_ = os.Remove(out + "/nixos-version")
			_ = os.Mkdir(out+"/nixos-version", 0o755)
		}, "not a regular file"},
		{"unreadable", func(out string) { _ = os.Chmod(out+"/nixos-version", 0) }, "not readable"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" && os.Getuid() == 0 {
				t.Skip("root reads a mode 0 file")
			}
			sys := newFakeNixOSSystem(t, store, fmt.Sprintf("%031dz", i))
			tc.break_(sys.out)
			err := validateBuildOut(sys.out, store, nixosPlatform(t))
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("rejected: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// --- failures at every step, D1b --------------------------------------------------

func TestAsUser_NixOSFailureMapping(t *testing.T) {
	cases := []struct {
		name     string
		systemd  bool
		set      func(e *asUserEnv)
		wantText string
		wantStep string
		wantRoot int
	}{
		{"build", true, func(e *asUserEnv) {
			e.f.build = func(context.Context, exec.RunOptions) (exec.Result, error) { return cmdErr("nixos-rebuild", nil, 3) }
		}, "apply failed: build:", "build", 0},
		{"read-result", true, func(e *asUserEnv) {
			e.f.build = func(context.Context, exec.RunOptions) (exec.Result, error) { return exec.Result{}, nil }
		}, "apply failed: read build result:", "read-result", 0},
		{"set-profile", true, func(e *asUserEnv) {
			e.f.setProfile = func(context.Context, []string) (exec.Result, error) { return cmdErr("sudo", nil, 4) }
		}, "apply failed: set system profile:", "set-profile", 1},
		{"activate under systemd", true, func(e *asUserEnv) {
			e.f.activate = func(context.Context, []string) (exec.Result, error) { return cmdErr("sudo", nil, 1) }
		}, "activation may still be running in unit nixos-rebuild-switch-to-configuration", "activate", 2},
		{"activate without systemd", false, func(e *asUserEnv) {
			e.f.activate = func(context.Context, []string) (exec.Result, error) { return cmdErr("sudo", nil, 1) }
		}, "apply failed: activate:", "activate", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newNixOSEnv(t, tc.systemd)
			tc.set(e)
			err := e.applyNixOS(context.Background(), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err = %v, want text %q", err, tc.wantText)
			}
			if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != tc.wantStep {
				t.Errorf("failed_step = %q, want %q", got, tc.wantStep)
			}
			if n := len(e.f.rootCalls()); n != tc.wantRoot {
				t.Errorf("root calls = %d, want %d", n, tc.wantRoot)
			}
			if e.applied(t) {
				t.Error("markApplied written on a failure")
			}
			// nvd and the fsmonitor restart are post steps: not reached.
			for _, c := range e.f.calls {
				if c.Name == "nvd" || c.Name == "pkill" {
					t.Errorf("post step %s ran after a failure", c.Name)
				}
			}
			if tc.wantStep == "activate" {
				note := strings.Contains(err.Error(), "systemctl status nixos-rebuild-switch-to-configuration") &&
					strings.Contains(err.Error(), "journalctl -u nixos-rebuild-switch-to-configuration")
				if note != tc.systemd {
					t.Errorf("detached-failure note present = %v, want %v: %v", note, tc.systemd, err)
				}
			}
		})
	}
}

// Exit 4 of switch-to-configuration: the system HAS switched; the message says so
// and no markApplied is written (as nixos-rebuild treats it). Pinned against the
// fake runner only.
func TestAsUser_NixOSActivateExit4Message(t *testing.T) {
	e := newNixOSEnv(t, true)
	e.f.activate = func(context.Context, []string) (exec.Result, error) { return cmdErr("sudo", nil, 4) }
	err := e.applyNixOS(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "systemctl --failed") || !strings.Contains(err.Error(), "do not re-run apply") {
		t.Fatalf("err = %v", err)
	}
	if e.applied(t) {
		t.Error("markApplied written after exit 4")
	}
}

// A cancel before set-profile starts no root step; one after it lets both finish.
func TestAsUser_NixOSCancel(t *testing.T) {
	t.Run("before set-profile", func(t *testing.T) {
		e := newNixOSEnv(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		e.f.probe = func(context.Context, exec.RunOptions) (exec.Result, error) {
			cancel()
			return cmdErrResult("p", nil, exec.Result{ExitCode: 1, Stderr: []byte(nixosUsage)})
		}
		err := e.applyNixOS(ctx, io.Discard)
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "before any root step started") {
			t.Fatalf("err = %v", err)
		}
		if n := len(e.f.rootCalls()); n != 0 {
			t.Errorf("root steps ran: %d", n)
		}
	})
	t.Run("after set-profile started", func(t *testing.T) {
		e := newNixOSEnv(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		e.f.setProfile = func(context.Context, []string) (exec.Result, error) {
			cancel()
			return exec.Result{}, nil
		}
		err := e.applyNixOS(ctx, io.Discard)
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "ran to completion") {
			t.Fatalf("err = %v", err)
		}
		if n := len(e.f.rootCalls()); n != 2 {
			t.Errorf("root steps = %d, want both", n)
		}
	})
}

// --- D7 and the systemd seam ------------------------------------------------------

func TestAsUser_StaleApplyCommandHintOnlyInTheUnsetError(t *testing.T) {
	toml := strings.Replace(nixosTOML, "activate_command = \"sudo {out}/bin/switch-to-configuration switch\"\n", "apply_command = \"sudo nixos-rebuild switch\"\n", 1)
	e := newAsUserEnv(t, toml, nil)
	err := e.w.Apply(context.Background(), io.Discard, ApplyOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "workspace.activate_command is not set") ||
		!strings.Contains(err.Error(), "a leftover apply_command is ignored") ||
		!strings.Contains(err.Error(), "{out}/bin/switch-to-configuration switch") {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.f.significant()); n != 0 {
		t.Errorf("commands ran: %s", dumpCalls(e.f.significant()))
	}
}

func TestSystemdPresent(t *testing.T) {
	dir := t.TempDir()
	if !systemdPresent(dir) {
		t.Error("an existing directory is not reported present")
	}
	if systemdPresent(filepath.Join(dir, "absent")) {
		t.Error("a missing path is reported present")
	}
}

// Darwin never consults the systemd seam: panicking seams change nothing.
func TestAsUser_DarwinNeverConsultsTheSystemdSeam(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	e.w.asUserSeams.systemdPresent = func() bool { panic("darwin consulted the systemd seam") }
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
}

// Tier R: the production default is a stat of /run/systemd/system.
func TestProductionSystemdPresentMatchesTheHost(t *testing.T) {
	// The package-wide guard replaced the default; recover the real one.
	saved := productionSystemdPresent
	t.Cleanup(func() { productionSystemdPresent = saved })
	productionSystemdPresent = func() bool { return systemdPresent(systemdRunDir) }
	_, err := os.Stat(systemdRunDir)
	if got := (&Workspace{}).systemdPresent(); got != (err == nil) {
		t.Errorf("default seam = %v, os.Stat(%s) error = %v", got, systemdRunDir, err)
	}
}

// Tier R, opt-in: the parser against the host's real switch-to-configuration.
// Operator-run only: it executes /run/current-system/bin/switch-to-configuration
// with no arguments as the user (it prints usage and exits 1; the lock file is
// taken only after argument handling). Skipped unless PN_NIXOS_REAL_PROBE=1, off
// NixOS and as root.
func TestRealProbe_NixOSParserAcceptsTheHostScript(t *testing.T) {
	if os.Getenv("PN_NIXOS_REAL_PROBE") != "1" {
		t.Skip("SKIP: opt-in; the operator sets PN_NIXOS_REAL_PROBE=1 to run the host's switch-to-configuration with no arguments")
	}
	if os.Getuid() == 0 {
		t.Skip("SKIP: the probe is run as an unprivileged user, never as root")
	}
	script := "/run/current-system/bin/switch-to-configuration"
	if _, err := os.Stat(script); err != nil {
		t.Skip("SKIP: not a NixOS host")
	}
	var stdout, stderr bytes.Buffer
	cmd := osexec.CommandContext(context.Background(), script)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *osexec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	if perr := parseSwitchToConfigurationUsage(code, stdout.Bytes(), stderr.Bytes()); perr != nil {
		t.Errorf("the parser rejects the host script's usage (exit %d): %v\nstderr: %s", code, perr, stderr.String())
	}
}
