// Package nixlog turns nix's --json-log-path activity stream into a small set
// of finished Activity values. It is the activity/state machine of W-7 and
// W-9: pure (no I/O, no OTel), driven line by line with a read-time stamp,
// and tested against the ADR 0028 golden files.
//
// The behaviours below come from the Phase 1 spike (ADR 0028):
//
//   - nix events carry no timestamps (F4), so the caller stamps each line.
//   - Result types 101 (build log line) and 105 (progress) are dropped before
//     JSON decoding when a cheap byte check allows it, and otherwise after.
//   - Build failure has no flag on `stop` (F6): it is inferred from a level-0
//     message (raw_msg when present, else msg; ANSI stripped) naming the drv.
//     A drv whose dependency failed never starts, so it is emitted from the
//     message alone. Errors can arrive long after the stop, so a stopped
//     build is HELD until a message names it or the run ends.
//   - A type-111 activity names the OUTPUT path in its text, not the drv.
//   - Whatever is still open at the end is closed with an error status.
package nixlog

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Nix activity types the wrapper turns into spans.
const (
	TypeBuild       = 105
	TypeSubstitute  = 108
	TypeBuildWaiter = 111
)

// Kind identifies the span an Activity becomes.
type Kind int

// The three activity kinds that produce spans.
const (
	KindBuild Kind = iota + 1
	KindSubstitute
	KindWait
)

// Error types set on failed activities (OTel error.type values).
const (
	ErrBuildFailed      = "build_failed"
	ErrDependencyFailed = "dependency_failed"
	ErrAborted          = "aborted"
)

// Activity is one finished (or force-closed) nix activity.
type Activity struct {
	Kind Kind
	// Name is the human name: the derivation or store path name, store hash
	// removed. Path is the full store path (derivation for builds).
	Name, Path string
	// Server is the cache host for substitutions.
	Server     string
	Start, End time.Time
	Failed     bool
	// ErrorType is one of the Err* constants when Failed.
	ErrorType string
	Reason    string
	// NoSpan is true for substitutions shorter than the span threshold: they
	// are still counted in metrics but get no span.
	NoSpan bool
	// Instant is true when the activity never started (dependency failure):
	// Start == End == the message's stamp.
	Instant bool
}

// Sink receives finished activities.
type Sink interface {
	Emit(Activity)
}

// Options tune the Processor.
type Options struct {
	// MinSubstituteSpan: substitutions shorter than this (and successful) set
	// Activity.NoSpan. Zero means every substitution gets a span.
	MinSubstituteSpan time.Duration
}

// Summary is what the Processor learned beyond activities.
type Summary struct {
	// PlanBuild / PlanFetch come from the "will be built / fetched" messages.
	PlanBuild, PlanFetch int
	// Lines counts complete lines offered; Dropped counts those rejected by
	// the cheap 101/105 check; Malformed counts undecodable lines.
	Lines, Dropped, Malformed int
}

type open struct {
	act   Activity
	order uint64
}

type held struct {
	act   Activity
	order uint64
}

// Processor is the state machine. It is not safe for concurrent use.
type Processor struct {
	sink Sink
	opts Options

	active map[uint64]*open
	// stopped builds waiting for a failure message or the end of the run.
	held []held
	// failed remembers drvs a message already named, to emit each once.
	failed map[string]string
	seq    uint64
	sum    Summary
}

// NewProcessor returns a Processor feeding sink.
func NewProcessor(sink Sink, opts Options) *Processor {
	return &Processor{
		sink:   sink,
		opts:   opts,
		active: make(map[uint64]*open),
		failed: make(map[string]string),
	}
}

// Summary returns the counters and plan counts gathered so far.
func (p *Processor) Summary() Summary { return p.sum }

type event struct {
	Action string            `json:"action"`
	ID     uint64            `json:"id"`
	Type   int               `json:"type"`
	Level  int               `json:"level"`
	Text   string            `json:"text"`
	Fields []json.RawMessage `json:"fields"`
	Msg    string            `json:"msg"`
	RawMsg string            `json:"raw_msg"`
}

var (
	prefixResult = []byte(`{"action":"result"`)
	suffix101    = []byte(`"type":101}`)
	suffix105    = []byte(`"type":105}`)
)

// FastDrop reports whether line is certainly a type 101/105 result, using only
// byte comparisons (nix emits keys alphabetically, so "type" is last). A false
// result is inconclusive, not a keep decision: Line still decodes and drops by
// type.
func FastDrop(line []byte) bool {
	return bytes.HasPrefix(line, prefixResult) &&
		(bytes.HasSuffix(line, suffix101) || bytes.HasSuffix(line, suffix105))
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes SGR colour sequences (nix colours text even when stdout is
// not a terminal).
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

var (
	reCannotBuild = regexp.MustCompile(`Cannot build '(/[^']+\.drv)'`)
	reReason      = regexp.MustCompile(`Reason: ([^\n]*?)\.?(?:\n|$)`)
	reQuoted      = regexp.MustCompile(`'(/[^']+)'`)
	rePlanBuild   = regexp.MustCompile(`^(?:this derivation|these (\d+) derivations) will be built`)
	rePlanFetch   = regexp.MustCompile(`^(?:this path|these (\d+) paths) will be fetched`)
)

// Line feeds one complete line (no trailing newline), stamped at read time.
func (p *Processor) Line(line []byte, at time.Time) {
	p.sum.Lines++
	if FastDrop(line) {
		p.sum.Dropped++
		return
	}
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		p.sum.Malformed++
		return
	}
	switch ev.Action {
	case "start":
		p.start(&ev, at)
	case "stop":
		p.stop(ev.ID, at)
	case "msg":
		p.msg(&ev, at)
	case "result":
		// Types 101 and 105 are dropped; the rest carry nothing we need.
	}
}

func (p *Processor) start(ev *event, at time.Time) {
	a := Activity{Start: at}
	switch ev.Type {
	case TypeBuild:
		a.Kind = KindBuild
		a.Path = stringField(ev.Fields, 0)
		if a.Path == "" {
			a.Path = firstQuoted(StripANSI(ev.Text))
		}
		a.Name = StorePathName(a.Path)
	case TypeSubstitute:
		a.Kind = KindSubstitute
		a.Path = stringField(ev.Fields, 0)
		a.Name = StorePathName(a.Path)
		a.Server = Host(stringField(ev.Fields, 1))
	case TypeBuildWaiter:
		a.Kind = KindWait
		a.Path = firstQuoted(StripANSI(ev.Text))
		a.Name = StorePathName(a.Path)
	default:
		return
	}
	p.seq++
	p.active[ev.ID] = &open{act: a, order: p.seq}
}

func (p *Processor) stop(id uint64, at time.Time) {
	o, ok := p.active[id]
	if !ok {
		return
	}
	delete(p.active, id)
	a := o.act
	a.End = at
	switch a.Kind {
	case KindBuild:
		if reason, bad := p.failed[a.Path]; bad {
			markFailed(&a, reason)
			p.sink.Emit(a)
			return
		}
		p.held = append(p.held, held{a, o.order})
	case KindSubstitute:
		a.NoSpan = p.opts.MinSubstituteSpan > 0 && a.End.Sub(a.Start) < p.opts.MinSubstituteSpan
		p.sink.Emit(a)
	default:
		p.sink.Emit(a)
	}
}

func (p *Processor) msg(ev *event, at time.Time) {
	text := ev.RawMsg
	if text == "" {
		text = ev.Msg
	}
	text = StripANSI(text)
	if ev.Level == 0 {
		if m := reCannotBuild.FindStringSubmatch(text); m != nil {
			reason := "build failed"
			if r := reReason.FindStringSubmatch(text); r != nil {
				reason = r[1]
			}
			p.buildFailed(m[1], reason, at)
		}
		return
	}
	if m := rePlanBuild.FindStringSubmatch(text); m != nil {
		p.sum.PlanBuild += countOrOne(m[1])
	} else if m := rePlanFetch.FindStringSubmatch(text); m != nil {
		p.sum.PlanFetch += countOrOne(m[1])
	}
}

func countOrOne(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return 1
}

// buildFailed applies a failure message to a drv in whatever state it is in.
func (p *Processor) buildFailed(drv, reason string, at time.Time) {
	if _, seen := p.failed[drv]; seen {
		return
	}
	p.failed[drv] = reason
	for i, h := range p.held {
		if h.act.Path == drv {
			markFailed(&h.act, reason)
			p.held = append(p.held[:i], p.held[i+1:]...)
			p.sink.Emit(h.act)
			return
		}
	}
	for _, o := range p.active {
		if o.act.Kind == KindBuild && o.act.Path == drv {
			return // stop will see p.failed and emit it failed
		}
	}
	// Never started (a dependency failed): attribute it from the message.
	a := Activity{Kind: KindBuild, Path: drv, Name: StorePathName(drv), Start: at, End: at, Instant: true}
	markFailed(&a, reason)
	p.sink.Emit(a)
}

func markFailed(a *Activity, reason string) {
	a.Failed = true
	a.Reason = reason
	a.ErrorType = ErrBuildFailed
	if strings.Contains(reason, "dependency failed") || strings.Contains(reason, "dependencies failed") {
		a.ErrorType = ErrDependencyFailed
	}
}

// Finish ends the run at end (W-9 c): held builds are released as successful,
// and every still-open activity is closed with an error status.
func (p *Processor) Finish(end time.Time) {
	for _, h := range p.held {
		p.sink.Emit(h.act)
	}
	p.held = nil
	rest := make([]*open, 0, len(p.active))
	for _, o := range p.active {
		rest = append(rest, o)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].order < rest[j].order })
	for _, o := range rest {
		a := o.act
		a.End = end
		a.Failed = true
		a.ErrorType = ErrAborted
		a.Reason = "nix exited before the activity stopped"
		p.sink.Emit(a)
	}
	p.active = make(map[uint64]*open)
}

func stringField(fields []json.RawMessage, i int) string {
	if i >= len(fields) {
		return ""
	}
	var s string
	if err := json.Unmarshal(fields[i], &s); err != nil {
		return ""
	}
	return s
}

func firstQuoted(s string) string {
	if m := reQuoted.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// StorePathName returns the name part of a store path or drv path: the base
// name without the 32-character hash prefix and without a ".drv" suffix.
func StorePathName(p string) string {
	b := path.Base(p)
	if p == "" {
		return ""
	}
	if len(b) > 33 && b[32] == '-' {
		b = b[33:]
	}
	return strings.TrimSuffix(b, ".drv")
}

// Host returns the hostname of a cache URL, or the input when it is not one.
func Host(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return raw
}
