package workspace

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Build-plan size (bead pg2-sdbhh4.10).
//
// nix prints its plan before any build starts:
//
//	these 423 derivations will be built:
//	  /nix/store/<hash>-name.drv
//	  ...
//	these 12 paths will be fetched (10.5 MiB download, 50.2 MiB unpacked):
//	  /nix/store/<hash>-name
//
// (or `this derivation will be built:` / `this path will be fetched (...)`; ANSI
// colors may be present). planTap watches the stream a rebuild verb hands to
// the child and parses that message, so the plan can be recorded in OTel and a
// large one can be warned about at the moment nix prints it. It NEVER alters,
// delays, drops or reorders a byte of the child's output, and NOTHING here stops
// or signals the child: the operator ruled (2026-10-26) to monitor plan size
// first and decide on aborting later, from the data (bead pg2-sdbhh4.21).

const (
	// defaultWarnBuilds is the workspace.warn_builds default: a plan above it
	// prints one warning line. 400 is the OTel alert point; the warning is
	// deliberately earlier.
	defaultWarnBuilds = 300
	// warnDerivationNames is how many derivation names the warning lists.
	warnDerivationNames = 10
	// maxPlanLine bounds the parser's partial-line buffer so a child that never
	// prints a newline cannot make pn buffer without limit.
	maxPlanLine = 64 << 10
)

var (
	reANSI         = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	reBuildHeader  = regexp.MustCompile(`^(?:this derivation|these ([0-9]+) derivations) will be built`)
	reFetchHeader  = regexp.MustCompile(`^(?:this path|these ([0-9]+) paths) will be fetched`)
	reDownloadSize = regexp.MustCompile(`\(([0-9][0-9.,]*) (B|KiB|MiB|GiB|TiB) download`)
	reStorePrefix  = regexp.MustCompile(`^/nix/store/[0-9a-z]{32}-`)
)

// planStats is what the parser has seen so far in one run.
type planStats struct {
	// Builds and Fetches sum every plan message of the run (a run normally
	// prints one of each).
	Builds  int
	Fetches int
	// DownloadMiB sums the download sizes the fetch messages carried; HasDownload
	// is false when none carried one.
	DownloadMiB float64
	HasDownload bool
	// Derivations are the first warnDerivationNames derivation names listed.
	Derivations []string
}

// planParser incrementally parses nix's plan message from arbitrary chunks.
type planParser struct {
	stats   planStats
	partial []byte // an unterminated line
	skip    bool   // the current line overflowed maxPlanLine; ignore it
	state   planState
	open    bool // a plan block (header + its lists) is in progress
	closed  bool // a plan block just ended; consumed by takeClosed
}

type planState int

const (
	planIdle planState = iota
	planInBuildList
	planInFetchList
)

// feed consumes a chunk. Chunk boundaries may fall anywhere, including inside
// a line or an ANSI sequence.
func (p *planParser) feed(chunk []byte) {
	for _, b := range chunk {
		if b == '\n' || b == '\r' {
			if !p.skip {
				p.line(string(p.partial))
			}
			p.partial = p.partial[:0]
			p.skip = false
			continue
		}
		if p.skip {
			continue
		}
		if len(p.partial) >= maxPlanLine {
			p.skip = true
			p.partial = p.partial[:0]
			continue
		}
		p.partial = append(p.partial, b)
	}
}

// flush ends the stream: a trailing unterminated line is processed and an open
// plan block is closed.
func (p *planParser) flush() {
	if !p.skip && len(p.partial) > 0 {
		p.line(string(p.partial))
	}
	p.partial = p.partial[:0]
	p.skip = false
	p.closeBlock()
}

// takeClosed reports whether a plan block ended since the last call.
func (p *planParser) takeClosed() bool {
	c := p.closed
	p.closed = false
	return c
}

func (p *planParser) closeBlock() {
	if p.open {
		p.open = false
		p.closed = true
	}
	p.state = planIdle
}

func (p *planParser) line(raw string) {
	s := strings.TrimSpace(reANSI.ReplaceAllString(raw, ""))
	if s == "" {
		return
	}
	if p.state != planIdle && strings.HasPrefix(s, "/nix/store/") {
		if p.state == planInBuildList && len(p.stats.Derivations) < warnDerivationNames {
			p.stats.Derivations = append(p.stats.Derivations, reStorePrefix.ReplaceAllString(s, ""))
		}
		return
	}
	if m := reBuildHeader.FindStringSubmatch(s); m != nil {
		p.stats.Builds += countOf(m[1])
		p.state, p.open = planInBuildList, true
		return
	}
	if m := reFetchHeader.FindStringSubmatch(s); m != nil {
		p.stats.Fetches += countOf(m[1])
		if mib, ok := downloadMiB(s); ok {
			p.stats.DownloadMiB += mib
			p.stats.HasDownload = true
		}
		p.state, p.open = planInFetchList, true
		return
	}
	// Any other line ends the plan block.
	p.closeBlock()
}

// countOf is the count of a "this <thing>" (empty capture: 1) or "these N
// <things>" header.
func countOf(captured string) int {
	if captured == "" {
		return 1
	}
	n, err := strconv.Atoi(captured)
	if err != nil {
		return 0
	}
	return n
}

// downloadMiB extracts the download size of a fetch header, in MiB.
func downloadMiB(header string) (float64, bool) {
	m := reDownloadSize.FindStringSubmatch(header)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "B":
		v /= 1 << 20
	case "KiB":
		v /= 1 << 10
	case "GiB":
		v *= 1 << 10
	case "TiB":
		v *= 1 << 20
	}
	return v, true
}

// planTap is a pass-through io.Writer that feeds a planParser and, above the
// warn threshold, writes ONE warning line (once per run) to the same
// destination, only ever at a line boundary. dst's bytes are exactly the
// child's bytes, with the warning line the only addition.
type planTap struct {
	mu          sync.Mutex
	dst         io.Writer
	warnBuilds  int // warn when the run's builds exceed this; <= 0 disables
	parser      planParser
	atLineStart bool
	warned      bool
	pending     bool // a warning is due but the stream is mid-line
}

// newPlanTap wraps dst. warnBuilds <= 0 disables the warning (the parsing and
// the recorded stats are unaffected).
func newPlanTap(dst io.Writer, warnBuilds int) *planTap {
	return &planTap{dst: dst, warnBuilds: warnBuilds, atLineStart: true}
}

// Write forwards p to dst unchanged and returns dst's result, then observes it.
func (t *planTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, err := t.dst.Write(p)
	if n > 0 {
		t.atLineStart = p[n-1] == '\n'
		t.parser.feed(p[:n])
		t.observe()
	}
	return n, err
}

// finish ends the stream: it completes the parse and emits a warning that was
// still waiting for a line boundary. Call once after the child has exited.
func (t *planTap) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parser.flush()
	t.observe()
	if t.pending {
		if !t.atLineStart {
			_, _ = io.WriteString(t.dst, "\n")
		}
		t.emitWarning()
	}
}

// stats is the run's plan so far.
func (t *planTap) stats() planStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parser.stats
}

// observe decides, when a plan block has just ended, whether a warning is due,
// and emits a due one as soon as the stream is at a line boundary.
func (t *planTap) observe() {
	if t.parser.takeClosed() && !t.warned && t.warnBuilds > 0 && t.parser.stats.Builds > t.warnBuilds {
		t.pending = true
	}
	if t.pending && t.atLineStart {
		t.emitWarning()
	}
}

func (t *planTap) emitWarning() {
	t.pending = false
	t.warned = true
	_, _ = io.WriteString(t.dst, planWarning(t.parser.stats, t.warnBuilds)+"\n")
}

// planWarning is the one summary line: the plan's size, the first derivation
// names and how to proceed. It never recommends a pn flag that does not exist.
func planWarning(s planStats, threshold int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pn: WARNING: nix plans to build %d derivations and fetch %d paths", s.Builds, s.Fetches)
	if s.HasDownload {
		fmt.Fprintf(&b, " (%.1f MiB download)", s.DownloadMiB)
	}
	fmt.Fprintf(&b, ", above workspace.warn_builds=%d.", threshold)
	if len(s.Derivations) > 0 {
		fmt.Fprintf(&b, " First %d: %s.", len(s.Derivations), strings.Join(s.Derivations, ", "))
	}
	b.WriteString(" pn does not stop the run. To cap CPU for a one-off, stop it (Ctrl-C) and run the nix command with --cores N.")
	return b.String()
}
