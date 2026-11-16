// internal/workspace/doctor_checks_spool.go
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

const (
	// collectorLabel is the launchd label of the OTel collector that drains the
	// user spool.
	collectorLabel = "com.phillipg.otel-stack.otelcol"

	// spoolStuckAfter is the age of the oldest user-spool file past which the
	// spool is reported stuck. The collector deletes a file after reading it
	// (delete_after_read), so a file this old was never read.
	spoolStuckAfter = 30 * time.Minute
)

// clock is the doctor clock; tests inject a fixed one.
func (env *doctorEnv) clock() time.Time {
	if env.now != nil {
		return env.now()
	}
	return time.Now()
}

// getenvOrEmpty reads the environment through the injectable getenv; a nil
// getenv is an empty environment, so tests never see the real one.
func (env *doctorEnv) getenvOrEmpty(k string) string {
	if env.getenv == nil {
		return ""
	}
	return env.getenv(k)
}

// collectorSpoolDir is the directory the collector watches:
// ${XDG_STATE_HOME:-$HOME/.local/state}/otel-spool; "" if it cannot be known.
func (env *doctorEnv) collectorSpoolDir() string {
	if x := env.getenvOrEmpty("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "otel-spool")
	}
	if h := env.getenvOrEmpty("HOME"); h != "" {
		return filepath.Join(h, ".local", "state", "otel-spool")
	}
	return ""
}

// spoolHealthFindings judges the user spool when spool telemetry is on and the
// directory is usable (spoolFinding reports an unusable one). Every finding is
// a warning. The root spool is deliberately not judged: nothing deletes its
// files on the collector side, so its age proves nothing.
func (ws *Workspace) spoolHealthFindings(ctx context.Context, env *doctorEnv) []Finding {
	sp := env.spool
	if sp == nil || sp.Forced != "" || !sp.Enabled || sp.Err != nil {
		return nil
	}
	warn := func(msg, manual string) Finding {
		return Finding{CheckID: telemetryCheckID, Severity: SevWarning, Message: msg, Manual: manual}
	}
	var out []Finding

	// launchctl through the Runner seam: a non-zero exit is "not loaded"; any
	// other failure (no launchctl on this machine) is unknown and says nothing.
	loaded := ws.collectorLoaded(ctx)
	notLoaded := loaded != nil && !*loaded

	if age, name, ok := oldestSpoolFile(sp.Dir, env.clock()); ok && age > spoolStuckAfter {
		msg := fmt.Sprintf("telemetry spool is stuck: %s in %s is %s old (more than %s); the collector deletes files after reading them (delete_after_read), so it is unread, or the collector skipped it because of a fingerprint collision caused by a writer bug; collector launchd label %s",
			name, sp.Dir, age.Round(time.Minute), spoolStuckAfter, collectorLabel)
		if notLoaded {
			msg += " (launchctl reports the label is not loaded)"
		}
		out = append(out, warn(msg, "launchctl list "+collectorLabel))
	} else if notLoaded {
		out = append(out, warn(
			fmt.Sprintf("telemetry is enabled but the collector %s is not loaded; files accumulate in %s up to the sweep bounds with no collector", collectorLabel, sp.Dir),
			"launchctl list "+collectorLabel+"  # then enable the otel-stack collector and apply",
		))
	}

	if want := env.collectorSpoolDir(); want != "" && filepath.Clean(sp.Dir) != want {
		out = append(out, warn(
			fmt.Sprintf("the resolved user spool directory %s differs from %s, the only directory the collector watches (a --otel-spool-dir or spool_dir override is never read), so telemetry piles up unread", sp.Dir, want),
			"remove spool_dir from telemetry.toml (phillipgreenii.pn.telemetry.spool_dir) and any --otel-spool-dir, then apply",
		))
	}

	if env.getenv != nil {
		if file, _ := telemetrycfg.LoadFile(telemetrycfg.FilePath(env.getenv)); file.Endpoint != "" {
			out = append(out, warn(
				"telemetry.toml still has an endpoint key; it is ignored and no longer enables telemetry",
				"unset phillipgreenii.pn.telemetry.endpoint and apply",
			))
		}
	}
	return out
}

// collectorLoaded reports whether launchd has the collector label loaded: nil
// when that cannot be determined.
func (ws *Workspace) collectorLoaded(ctx context.Context) *bool {
	_, err := ws.runner.Run(ctx, "launchctl", []string{"list", collectorLabel}, exec.RunOptions{})
	var ce *exec.CommandError
	switch {
	case err == nil:
		t := true
		return &t
	case errors.As(err, &ce):
		f := false
		return &f
	}
	return nil
}

// oldestSpoolFile returns the age and name of the oldest completed regular
// file in dir (".tmp" files are writes in flight); ok is false when there is
// none or the directory cannot be read.
func oldestSpoolFile(dir string, now time.Time) (age time.Duration, name string, ok bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, "", false
	}
	var oldest time.Time
	for _, e := range ents {
		if !e.Type().IsRegular() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if !ok || fi.ModTime().Before(oldest) {
			oldest, name, ok = fi.ModTime(), e.Name(), true
		}
	}
	if !ok {
		return 0, "", false
	}
	return now.Sub(oldest), name, true
}
