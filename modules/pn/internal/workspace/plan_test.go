package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Real shapes of nix's plan message (from apply logs 2026-10-21..25), with and
// without the ANSI colors nix adds on a tty.
const (
	storeA = "/nix/store/qxfgnnlpnznqqgqg5dq0vj3i53n5p6js-org.nixos.mermaid-ink.plist.drv"
	storeB = "/nix/store/fgnn2zpp56irbjcm4hbdlv7c2p5yy6az-launchd.drv"
	storeC = "/nix/store/6p65afcaqrhijlhd94ldjdkz4w6psghm-libfaketime-0.9.10"
)

func planText(builds int, fetchHeader string) string {
	var b strings.Builder
	if builds == 1 {
		b.WriteString("this derivation will be built:\n")
	} else if builds > 1 {
		fmt.Fprintf(&b, "these %d derivations will be built:\n", builds)
	}
	for i := 0; i < builds; i++ {
		fmt.Fprintf(&b, "  /nix/store/%032d-pkg%d.drv\n", i, i)
	}
	if fetchHeader != "" {
		b.WriteString(fetchHeader + "\n  " + storeC + "\n")
	}
	return b.String()
}

func parseAll(t *testing.T, s string) planStats {
	t.Helper()
	var p planParser
	p.feed([]byte(s))
	p.flush()
	return p.stats
}

func TestPlanParser_Shapes(t *testing.T) {
	ansi := func(s string) string { return "\x1b[35;1m" + s + "\x1b[0m" }
	cases := []struct {
		name         string
		in           string
		builds       int
		fetches      int
		download     float64
		hasDownload  bool
		firstDrvName string
	}{
		{"plural builds", "these 62 derivations will be built:\n  " + storeA + "\n  " + storeB + "\n", 62, 0, 0, false, "org.nixos.mermaid-ink.plist.drv"},
		{"singular build", "this derivation will be built:\n  " + storeB + "\n", 1, 0, 0, false, "launchd.drv"},
		{"ansi build header", ansi("these 5 derivations will be built:") + "\n  " + storeA + "\n", 5, 0, 0, false, "org.nixos.mermaid-ink.plist.drv"},
		{"plural fetch with download", "these 21 paths will be fetched (23.1 MiB download, 107.5 MiB unpacked):\n  " + storeC + "\n", 0, 21, 23.1, true, ""},
		{"singular fetch KiB", "this path will be fetched (6.1 KiB download, 37.3 KiB unpacked):\n  " + storeC + "\n", 0, 1, 6.1 / 1024, true, ""},
		{"GiB download", "these 1652 paths will be fetched (2.1 GiB download, 13.9 GiB unpacked):\n", 0, 1652, 2.1 * 1024, true, ""},
		{"fetch without size", "these 3 paths will be fetched:\n", 0, 3, 0, false, ""},
		{"ansi fetch", ansi("these 2 paths will be fetched (0.0 KiB download, 2.4 MiB unpacked):") + "\n", 0, 2, 0, true, ""},
		{"build then fetch", "these 173 derivations will be built:\n  " + storeA + "\nthese 21 paths will be fetched (23.1 MiB download, 107.5 MiB unpacked):\n  " + storeC + "\n", 173, 21, 23.1, true, "org.nixos.mermaid-ink.plist.drv"},
		{"no plan at all", "building '/nix/store/x.drv'...\nBuild successful.\n", 0, 0, 0, false, ""},
		{"CRLF and carriage returns", "these 7 derivations will be built:\r\n  " + storeA + "\r\n", 7, 0, 0, false, "org.nixos.mermaid-ink.plist.drv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAll(t, tc.in)
			if got.Builds != tc.builds || got.Fetches != tc.fetches || got.HasDownload != tc.hasDownload {
				t.Fatalf("got %+v, want builds=%d fetches=%d hasDownload=%v", got, tc.builds, tc.fetches, tc.hasDownload)
			}
			if d := got.DownloadMiB - tc.download; d > 1e-9 || d < -1e-9 {
				t.Errorf("download = %v MiB, want %v", got.DownloadMiB, tc.download)
			}
			if tc.firstDrvName != "" && (len(got.Derivations) == 0 || got.Derivations[0] != tc.firstDrvName) {
				t.Errorf("derivations = %v, want first %q", got.Derivations, tc.firstDrvName)
			}
		})
	}
}

// A plan with zero builds: nix prints only the fetch message (or nothing).
func TestPlanParser_ZeroBuilds(t *testing.T) {
	got := parseAll(t, "these 4 paths will be fetched (1.0 MiB download, 2.0 MiB unpacked):\n  "+storeC+"\n")
	if got.Builds != 0 || got.Fetches != 4 {
		t.Fatalf("got %+v", got)
	}
}

// The message may arrive split anywhere, including inside a line and inside an
// ANSI escape: every split of the same text must parse to the same result.
func TestPlanParser_SplitAcrossWrites(t *testing.T) {
	text := "\x1b[1mthese 12 derivations will be built:\x1b[0m\n  " + storeA + "\n  " + storeB +
		"\nthese 3 paths will be fetched (1.5 MiB download, 3.0 MiB unpacked):\n  " + storeC + "\nbuilding 'x'...\n"
	want := parseAll(t, text)
	if want.Builds != 12 || want.Fetches != 3 || !want.HasDownload {
		t.Fatalf("baseline wrong: %+v", want)
	}
	for size := 1; size < len(text); size += 3 {
		var p planParser
		for i := 0; i < len(text); i += size {
			end := i + size
			if end > len(text) {
				end = len(text)
			}
			p.feed([]byte(text[i:end]))
		}
		p.flush()
		if p.stats.Builds != want.Builds || p.stats.Fetches != want.Fetches ||
			p.stats.DownloadMiB != want.DownloadMiB || len(p.stats.Derivations) != len(want.Derivations) {
			t.Fatalf("chunk size %d: got %+v, want %+v", size, p.stats, want)
		}
	}
}

func TestPlanParser_OnlyFirstTenDerivationNamesKept(t *testing.T) {
	got := parseAll(t, planText(40, ""))
	if got.Builds != 40 || len(got.Derivations) != warnDerivationNames {
		t.Fatalf("builds=%d names=%d", got.Builds, len(got.Derivations))
	}
	if got.Derivations[0] != "pkg0.drv" || got.Derivations[9] != "pkg9.drv" {
		t.Errorf("names = %v", got.Derivations)
	}
}

func TestPlanParser_OverlongLineDoesNotGrowWithoutBound(t *testing.T) {
	var p planParser
	p.feed(bytes.Repeat([]byte("x"), 10*maxPlanLine))
	if len(p.partial) > maxPlanLine {
		t.Fatalf("partial buffer grew to %d", len(p.partial))
	}
	p.feed([]byte("\nthese 3 derivations will be built:\n"))
	p.flush()
	if p.stats.Builds != 3 {
		t.Fatalf("parser did not recover after the long line: %+v", p.stats)
	}
}

// failingWriter reports a write error after accepting n bytes.
type failingWriter struct{ err error }

func (f failingWriter) Write(p []byte) (int, error) { return 0, f.err }

// The tap forwards every byte unchanged, in order, below the threshold, however
// the stream is chunked, and returns the destination's own result.
func TestPlanTap_PassThroughIsByteIdentical(t *testing.T) {
	text := "evaluating...\n" + planText(24, "these 3 paths will be fetched (1.5 MiB download, 3.0 MiB unpacked):") +
		"\x1b[31mred\x1b[0m partial line without newline"
	for _, size := range []int{1, 7, 64, len(text)} {
		var dst bytes.Buffer
		tap := newPlanTap(&dst, defaultWarnBuilds)
		for i := 0; i < len(text); i += size {
			end := i + size
			if end > len(text) {
				end = len(text)
			}
			n, err := tap.Write([]byte(text[i:end]))
			if n != end-i || err != nil {
				t.Fatalf("Write = %d, %v", n, err)
			}
		}
		tap.finish()
		if dst.String() != text {
			t.Fatalf("chunk %d: output differs from input:\n got: %q\nwant: %q", size, dst.String(), text)
		}
		if st := tap.stats(); st.Builds != 24 || st.Fetches != 3 {
			t.Errorf("stats = %+v", st)
		}
	}
}

func TestPlanTap_PropagatesDestinationError(t *testing.T) {
	boom := errors.New("closed")
	tap := newPlanTap(failingWriter{boom}, defaultWarnBuilds)
	if n, err := tap.Write([]byte("hello\n")); n != 0 || !errors.Is(err, boom) {
		t.Fatalf("Write = %d, %v; want the destination's result", n, err)
	}
}

// The warning threshold is "above": exactly warn_builds does not warn, one more
// does; and the default is 300.
func TestPlanTap_WarnsAboveThresholdOnly(t *testing.T) {
	if defaultWarnBuilds != 300 {
		t.Fatalf("default warn threshold = %d, want 300", defaultWarnBuilds)
	}
	for _, tc := range []struct {
		builds int
		warn   bool
	}{{24, false}, {300, false}, {301, true}, {657, true}} {
		var dst bytes.Buffer
		tap := newPlanTap(&dst, defaultWarnBuilds)
		text := planText(tc.builds, "") + "building 'x'...\n"
		_, _ = tap.Write([]byte(text))
		tap.finish()
		got := strings.Count(dst.String(), "pn: WARNING")
		if tc.warn && got != 1 || !tc.warn && got != 0 {
			t.Errorf("%d builds: %d warnings, want warn=%v", tc.builds, got, tc.warn)
		}
		if !tc.warn && dst.String() != text {
			t.Errorf("%d builds: output altered below the threshold", tc.builds)
		}
	}
}

// When it warns, the child's bytes are all still there, in order, and the single
// warning line sits on a line boundary right after the plan block.
func TestPlanTap_WarningIsOneLineAtALineBoundary(t *testing.T) {
	plan := planText(423, "these 12 paths will be fetched (10.5 MiB download, 50.2 MiB unpacked):")
	text := "pre\n" + plan + "building 'x'...\nbuilding 'y'...\n"
	for _, size := range []int{1, 13, 100000} {
		var dst bytes.Buffer
		tap := newPlanTap(&dst, defaultWarnBuilds)
		for i := 0; i < len(text); i += size {
			end := i + size
			if end > len(text) {
				end = len(text)
			}
			_, _ = tap.Write([]byte(text[i:end]))
		}
		tap.finish()
		out := dst.String()
		if strings.Count(out, "pn: WARNING") != 1 {
			t.Fatalf("chunk %d: want exactly one warning:\n%s", size, out)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		var warn string
		var rest []string
		for _, l := range lines {
			if strings.HasPrefix(l, "pn: WARNING") {
				warn = l
				continue
			}
			rest = append(rest, l)
		}
		if strings.Join(rest, "\n")+"\n" != text {
			t.Fatalf("chunk %d: child bytes altered by the warning", size)
		}
		for _, want := range []string{"423 derivations", "12 paths", "10.5 MiB download", "pkg0.drv", "pkg9.drv", "workspace.warn_builds=300", "--cores N", "Ctrl-C"} {
			if !strings.Contains(warn, want) {
				t.Errorf("chunk %d: warning lacks %q: %s", size, want, warn)
			}
		}
		if strings.Contains(warn, "pkg10.drv") {
			t.Errorf("warning lists more than 10 names: %s", warn)
		}
	}
}

// A stream that ends mid-line right after a large plan still gets its warning,
// on its own line.
func TestPlanTap_WarnsAtStreamEnd(t *testing.T) {
	var dst bytes.Buffer
	tap := newPlanTap(&dst, defaultWarnBuilds)
	_, _ = tap.Write([]byte(planText(500, "")))
	tap.finish()
	if strings.Count(dst.String(), "pn: WARNING") != 1 {
		t.Fatalf("output:\n%s", dst.String())
	}
}

func TestPlanTap_ZeroThresholdDisablesWarning(t *testing.T) {
	var dst bytes.Buffer
	tap := newPlanTap(&dst, 0)
	text := planText(900, "") + "x\n"
	_, _ = tap.Write([]byte(text))
	tap.finish()
	if dst.String() != text {
		t.Fatal("a disabled warning must leave the output untouched")
	}
	if tap.stats().Builds != 900 {
		t.Errorf("stats must still be recorded: %+v", tap.stats())
	}
}
