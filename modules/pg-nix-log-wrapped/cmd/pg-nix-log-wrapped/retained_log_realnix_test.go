package main

// The retained log against the REAL nix binary (pg2-sdbhh4.1). A fake cannot
// prove the two assumptions the design rests on: that nix writes json-log-path
// into the file the wrapper pre-created (appending through the same inode),
// and that nix emits keys in the order the type-suffix filter assumes. These
// tests skip only when nix is unusable here, never substitute a fake, and run
// nix with --offline against a throwaway derivation (no network).

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/logdir"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/nixlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/retain"
)

// progressBuildArgs builds a unique trivial local derivation: it emits start,
// stop, msg and (type 104/105/106) progress lines, which an eval-only run
// would not.
func progressBuildArgs() []string {
	expr := fmt.Sprintf(`derivation { name = "pgnlw-retained-%d"; system = builtins.currentSystem; builder = "/bin/sh"; args = [ "-c" "echo retained > $out" ]; }`, time.Now().UnixNano())
	return []string{"nix", "build", "--impure", "--no-link", "--offline", "--extra-experimental-features", "nix-command flakes", "--expr", expr}
}

func realNix(t *testing.T) string {
	t.Helper()
	nix, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix is not installed")
	}
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("inside a nix builder: no nested store operations")
	}
	return nix
}

var (
	resultLine   = regexp.MustCompile(`^\{"action":"result".*"type":(\d+)\}$`)
	progressType = regexp.MustCompile(`"type":10[56]\}$`)
)

func isProgressResult(l string) bool {
	return strings.HasPrefix(l, `{"action":"result"`) && progressType.MatchString(l)
}

func TestRealNixWritesProgressIntoThePreCreatedScratchFileAndFinalizeFiltersIt(t *testing.T) {
	nix := realNix(t)
	dir := t.TempDir()
	files, err := logdir.Create(dir, time.Now(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	args := progressBuildArgs()
	cmd := exec.Command(nix, args[1:]...)
	cmd.Env = []string{
		"PATH=" + filepath.Dir(nix) + ":/usr/bin:/bin", "HOME=" + t.TempDir(),
		"NIX_CONFIG=json-log-path = " + files.RawPath,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("nix build is not usable in this environment: %v: %s", err, out)
	}

	// Read through the handle opened BEFORE nix ran: this fails if nix
	// replaced the file (rename) or truncated it instead of appending.
	fi, err := files.Raw.Stat()
	if err != nil {
		t.Fatal(err)
	}
	rawBytes, err := io.ReadAll(io.NewSectionReader(files.Raw, 0, fi.Size()))
	if err != nil || len(rawBytes) == 0 {
		t.Fatalf("nothing in the pre-created scratch file via its original handle: %d bytes, %v", len(rawBytes), err)
	}
	var progress, results int
	for _, l := range strings.Split(strings.TrimSpace(string(rawBytes)), "\n") {
		if strings.HasPrefix(l, `{"action":"result"`) {
			results++
			if !resultLine.MatchString(l) {
				t.Errorf("result line does not end with its type key (key order assumption broken): %s", l)
			}
		}
		if isProgressResult(l) {
			progress++
		}
		// The suffix check must catch EVERY result of type 105/106 nix wrote.
		if strings.HasPrefix(l, `{"action":"result"`) && (strings.Contains(l, `"type":105`) || strings.Contains(l, `"type":106`)) != nixlog.RetainDrop([]byte(l)) {
			t.Errorf("RetainDrop disagrees with the line's type: %s", l)
		}
	}
	if progress == 0 {
		t.Fatalf("real nix wrote no type-105/106 result into the scratch file (%d result lines): the test would be vacuous", results)
	}

	if err := retain.Finalize(files.RawPath, files.OutPath, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(files.OutPath)
	if len(got) == 0 {
		t.Fatal("retained log is empty")
	}
	var starts, stops, msgs int
	for _, l := range strings.Split(strings.TrimSpace(string(got)), "\n") {
		if isProgressResult(l) {
			t.Errorf("retained log holds a progress result: %s", l)
		}
		switch {
		case strings.HasPrefix(l, `{"action":"start"`):
			starts++
		case strings.HasPrefix(l, `{"action":"stop"`):
			stops++
		case strings.HasPrefix(l, `{"action":"msg"`):
			msgs++
		}
	}
	if starts == 0 || stops == 0 || msgs == 0 {
		t.Errorf("retained log lost activity lines: start=%d stop=%d msg=%d", starts, stops, msgs)
	}
	if len(got) >= len(rawBytes) {
		t.Errorf("retained %d bytes is not smaller than raw %d", len(got), len(rawBytes))
	}
}

func TestWrapperRetainsAFilteredLogOfARealNixRun(t *testing.T) {
	nix := realNix(t)
	col := newCollector(t)
	e, home := env(t, "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL, "PATH="+filepath.Dir(nix)+":/usr/bin:/bin")
	res := runWrapper(t, e, progressBuildArgs()...)
	if res.code != 0 {
		t.Skipf("nix build is not usable in this environment (exit %d): %s", res.code, res.stderr)
	}
	logDir := filepath.Join(home, "state", "pn", "nix-logs")
	if raws, _ := filepath.Glob(filepath.Join(logDir, "*.raw")); len(raws) != 0 {
		t.Errorf("scratch file left behind after a real run: %v", raws)
	}
	logs, _ := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("retained logs = %v", logs)
	}
	got, err := os.ReadFile(logs[0])
	if err != nil || len(got) == 0 {
		t.Fatalf("retained log: %d bytes, %v", len(got), err)
	}
	for _, l := range bytes.Split(bytes.TrimSpace(got), []byte("\n")) {
		if isProgressResult(string(l)) {
			t.Errorf("retained log holds a progress result: %s", l)
		}
	}
	if !bytes.Contains(got, []byte(`{"action":"start"`)) || !bytes.Contains(got, []byte(`{"action":"stop"`)) {
		t.Error("retained log lost start/stop lines")
	}
	// nix.log.file names the retained .jsonl, never the scratch .raw.
	col.mu.Lock()
	defer col.mu.Unlock()
	var seen bool
	for _, s := range col.spans {
		if s.Name != "nix.invocation" {
			continue
		}
		seen = true
		if v, _ := attrStr(s.Attributes, "nix.log.file"); v != logs[0] || !strings.HasSuffix(v, ".jsonl") {
			t.Errorf("nix.log.file = %q, want %q", v, logs[0])
		}
	}
	if !seen {
		t.Error("no nix.invocation span reached the collector")
	}
}
