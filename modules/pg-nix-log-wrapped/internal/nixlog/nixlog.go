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
//     The one exception is progress (105) of the aggregate activities
//     (type 104 Builds, type 103 CopyPaths) and of substitutions (type 108,
//     or the type-100 CopyPath child that carries its bytes): the LAST value
//     of those is folded into memory (pg2-sdbhh4.2). The id is read from the
//     byte suffix, so progress of every other activity is still skipped
//     without a decode.
//   - Build failure has no flag on `stop` (F6): it is inferred from a level-0
//     message (raw_msg when present, else msg; ANSI stripped) naming the drv.
//     The run's first `error:` message, hash mismatches, ignored eval-cache
//     errors and database-busy warnings are read from the same messages
//     (pg2-sdbhh4.4); the `error:` prefix exists only on msg, never raw_msg.
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

	// Activity types the Processor remembers for progress totals.
	typeCopyPath  = 100
	typeCopyPaths = 103
	typeBuilds    = 104
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
	ErrHashMismatch     = "hash_mismatch"

	// ErrEval, ErrSignal and ErrOther complete the closed set of run-level
	// failure kinds (see FailureKinds); they never appear on an Activity.
	ErrEval   = "eval_error"
	ErrSignal = "signal"
	ErrOther  = "other"
)

// FailureKinds is the CLOSED set of values Summary.FailureKind can return.
// The wrapper sets it as the invocation's error.type (together with the
// generic "nonzero_exit" when nothing more specific is known) and as the
// bounded failure_kind metric label.
var FailureKinds = []string{
	ErrBuildFailed, ErrDependencyFailed, ErrHashMismatch, ErrEval, ErrSignal, ErrOther,
}

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
	// Bytes is the last "done" bytes progress seen for a substitution;
	// HasBytes is false when none was seen (nothing to report).
	Bytes    int64
	HasBytes bool
	// Placeholder is true on a hash_mismatch build whose specified hash is
	// the all-A placeholder (a deliberately wrong hash, e.g. a test fixture
	// or a freshly added fetch). It still counts as a failed derivation;
	// dashboards filter on this attribute.
	Placeholder bool
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
	// Builds / Fetch are the final aggregate progress, summed over the
	// type-104 (builds) and type-103 (fetches) activities of the run that are
	// not nested under another aggregate activity; HasBuilds / HasFetch are
	// false when no such progress line was seen. This is what nix actually
	// scheduled and finished, unlike the plan messages above.
	Builds, Fetch       Progress
	HasBuilds, HasFetch bool
	// Lines counts complete lines offered; Dropped counts those skipped
	// without a JSON decode (build log lines, and progress of activities
	// whose progress is not kept); Folded counts progress lines of kept
	// activities; Malformed counts undecodable lines.
	Lines, Dropped, Folded, Malformed int

	// FirstError is the first level-0 `error:` message, cleaned by
	// CleanError (empty when none). FailedDrvs holds the names of up to
	// MaxFailedDrvs failing derivations in the order their messages arrived.
	FirstError string
	FailedDrvs []string
	// EvalCacheBusy counts `error (ignored)` events about a busy eval-cache
	// SQLite database; DBBusy counts the busy warnings about any SQLite
	// database; Waits counts the build-wait activities emitted.
	EvalCacheBusy, DBBusy, Waits int

	// kinds is the set of failure kinds seen in messages (ErrBuildFailed,
	// ErrDependencyFailed, ErrHashMismatch, ErrEval, ErrOther) plus
	// ErrSignal for an "interrupted by the user" message.
	kinds map[string]bool
}

// MaxFailedDrvs caps Summary.FailedDrvs.
const MaxFailedDrvs = 5

// MaxErrorLen caps Summary.FirstError (in characters).
const MaxErrorLen = 200

// FailureKind classifies a run that ended with a non-zero exit code. It
// returns one of FailureKinds, or "" when nothing more specific than the
// exit code is known. Precedence: signal (an exit of 128+n, or an
// "interrupted by the user" message, which wins over everything), then
// hash_mismatch, build_failed, dependency_failed, eval_error, other.
func (s Summary) FailureKind(exit int) string {
	if exit > 128 && exit <= 128+64 || s.kinds[ErrSignal] {
		return ErrSignal
	}
	for _, k := range []string{ErrHashMismatch, ErrBuildFailed, ErrDependencyFailed, ErrEval, ErrOther} {
		if s.kinds[k] {
			return k
		}
	}
	return ""
}

// Progress is the [done, expected, running, failed] result of nix's progress
// type, minus "running" (it is zero in a finished run).
type Progress struct {
	Done, Expected, Failed int64
}

// agg is an aggregate (type 103 or 104) activity, remembered for the whole
// run so its last progress survives its stop. Memory is O(aggregates).
type agg struct {
	typ    int
	nested bool // its parent is itself an aggregate activity: counted only via the outer one
	seen   bool
	last   Progress
}

// failure is what a message said about one drv.
type failure struct {
	reason, errType string
	placeholder     bool
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
	failed map[string]failure
	// aggs holds every aggregate activity seen; copies maps an open type-100
	// CopyPath activity to the open substitution that is its parent (nix
	// reports a substitution's bytes on that child).
	aggs   map[uint64]*agg
	copies map[uint64]uint64
	seq    uint64
	sum    Summary
}

// NewProcessor returns a Processor feeding sink.
func NewProcessor(sink Sink, opts Options) *Processor {
	return &Processor{
		sink:   sink,
		opts:   opts,
		active: make(map[uint64]*open),
		failed: make(map[string]failure),
		aggs:   make(map[uint64]*agg),
		copies: make(map[uint64]uint64),
	}
}

// emit forwards a finished activity to the sink, counting waits.
func (p *Processor) emit(a Activity) {
	if a.Kind == KindWait {
		p.sum.Waits++
	}
	p.sink.Emit(a)
}

// Summary returns the counters, plan counts and aggregate progress gathered
// so far.
func (p *Processor) Summary() Summary {
	s := p.sum
	s.FailedDrvs = append([]string(nil), p.sum.FailedDrvs...)
	for _, a := range p.aggs {
		if !a.seen || a.nested {
			continue
		}
		t, has := &s.Builds, &s.HasBuilds
		if a.typ == typeCopyPaths {
			t, has = &s.Fetch, &s.HasFetch
		}
		*has = true
		t.Done += a.last.Done
		t.Expected += a.last.Expected
		t.Failed += a.last.Failed
	}
	return s
}

type event struct {
	Action string            `json:"action"`
	ID     uint64            `json:"id"`
	Type   int               `json:"type"`
	Level  int               `json:"level"`
	Parent uint64            `json:"parent"`
	Text   string            `json:"text"`
	Fields []json.RawMessage `json:"fields"`
	Msg    string            `json:"msg"`
	RawMsg string            `json:"raw_msg"`
}

var (
	prefixResult = []byte(`{"action":"result"`)
	keyID        = []byte(`"id":`)
	keyFields    = []byte(`"fields":[`)
	suffix101    = []byte(`"type":101}`)
	suffix105    = []byte(`"type":105}`)
	suffix106    = []byte(`"type":106}`)
)

// RetainDrop reports whether line is certainly a type 105 (progress) or 106
// (set-phase) result, which the RETAINED log omits (operator ruling
// 2026-10-26: progress lines are about 99% of the bytes and not useful there).
// Type 101 (build log lines) is kept in the retained log for debugging. The
// check is a byte prefix/suffix test (nix emits keys alphabetically, so "type"
// is last); a line it does not recognise
// is KEPT, so an unexpected key order costs disk, never evidence. The
// real-nix test in cmd/pg-nix-log-wrapped pins that key order.
func RetainDrop(line []byte) bool {
	return bytes.HasPrefix(line, prefixResult) &&
		(bytes.HasSuffix(line, suffix105) || bytes.HasSuffix(line, suffix106))
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes SGR colour sequences (nix colours text even when stdout is
// not a terminal).
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

var (
	reCannotBuild = regexp.MustCompile(`Cannot build '(/[^']+\.drv)'`)
	reReason      = regexp.MustCompile(`Reason: ([^\n]*?)\.?(?:\n|$)`)
	reQuoted      = regexp.MustCompile(`'(/[^']+)'`)
	// reHashMismatch matches the fixed-output hash failure; nix prints no
	// "Cannot build" for that derivation. specified: is read separately.
	reHashMismatch = regexp.MustCompile(`hash mismatch in fixed-output derivation '(/[^']+\.drv)'`)
	reSpecified    = regexp.MustCompile(`specified:\s+(\S+)`)
	reInterrupted  = regexp.MustCompile(`^error:\s+interrupted by the user`)
	reSQLiteBusy   = regexp.MustCompile(`^(warning|error \(ignored\)):\s+SQLite database '([^']*)' is busy`)
	reDrvPath      = regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^\s']*\.drv`)
	rePlanBuild    = regexp.MustCompile(`^(?:this derivation|these (\d+) derivations) will be built`)
	rePlanFetch    = regexp.MustCompile(`^(?:this path|these (\d+) paths) will be fetched`)
)

// Line feeds one complete line (no trailing newline), stamped at read time.
func (p *Processor) Line(line []byte, at time.Time) {
	p.sum.Lines++
	if bytes.HasPrefix(line, prefixResult) {
		if bytes.HasSuffix(line, suffix101) {
			p.sum.Dropped++
			return
		}
		if bytes.HasSuffix(line, suffix105) {
			if id, ok := suffixID(line); ok {
				if !p.tracked(id) {
					p.sum.Dropped++
					return
				}
				if f, ok := fastFields(line); ok {
					p.fold(id, f)
					p.sum.Folded++
					return
				}
				// Odd shape for a kept activity: decode below.
			}
			// A line whose id cannot be read from the suffix falls back to a
			// full decode; key order is never relied on for correctness.
		}
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
		// Only progress (105) of a kept activity matters; build log lines
		// (101) and everything else carry nothing we need.
		if ev.Type == 105 && p.tracked(ev.ID) {
			if f, ok := decodeFields(ev.Fields); ok {
				p.fold(ev.ID, f)
				p.sum.Folded++
			}
		}
	}
}

func (p *Processor) start(ev *event, at time.Time) {
	a := Activity{Start: at}
	switch ev.Type {
	case typeBuilds, typeCopyPaths:
		_, nested := p.aggs[ev.Parent]
		p.aggs[ev.ID] = &agg{typ: ev.Type, nested: nested}
		return
	case typeCopyPath:
		if o, ok := p.active[ev.Parent]; ok && o.act.Kind == KindSubstitute {
			p.copies[ev.ID] = ev.Parent
		}
		return
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
	delete(p.copies, id)
	o, ok := p.active[id]
	if !ok {
		return
	}
	delete(p.active, id)
	a := o.act
	a.End = at
	switch a.Kind {
	case KindBuild:
		if f, bad := p.failed[a.Path]; bad {
			markFailed(&a, f)
			p.emit(a)
			return
		}
		p.held = append(p.held, held{a, o.order})
	case KindSubstitute:
		a.NoSpan = p.opts.MinSubstituteSpan > 0 && a.End.Sub(a.Start) < p.opts.MinSubstituteSpan
		p.emit(a)
	default:
		p.emit(a)
	}
}

func (p *Processor) msg(ev *event, at time.Time) {
	text := ev.RawMsg
	if text == "" {
		text = ev.Msg
	}
	text = StripANSI(text)
	// The "error:" prefix exists only on msg, never on raw_msg.
	clean := strings.TrimSpace(StripANSI(ev.Msg))
	if m := reSQLiteBusy.FindStringSubmatch(clean); m != nil {
		if m[1] == "warning" {
			p.sum.DBBusy++
		} else if strings.Contains(m[2], "eval-cache") {
			p.sum.EvalCacheBusy++
		}
		return
	}
	if ev.Level == 0 {
		p.errorMsg(text, clean, at)
		return
	}
	if m := rePlanBuild.FindStringSubmatch(text); m != nil {
		p.sum.PlanBuild += countOrOne(m[1])
	} else if m := rePlanFetch.FindStringSubmatch(text); m != nil {
		p.sum.PlanFetch += countOrOne(m[1])
	}
}

// errorMsg handles a level-0 message: text is raw_msg-or-msg, clean is msg.
func (p *Processor) errorMsg(text, clean string, at time.Time) {
	isError := strings.HasPrefix(clean, "error:")
	if isError && p.sum.FirstError == "" {
		p.sum.FirstError = CleanError(clean)
	}
	if m := reCannotBuild.FindStringSubmatch(text); m != nil {
		reason := "build failed"
		if r := reReason.FindStringSubmatch(text); r != nil {
			reason = r[1]
		}
		p.buildFailed(m[1], failure{reason: reason, errType: reasonType(reason)}, at)
		return
	}
	if m := reHashMismatch.FindStringSubmatch(text); m != nil {
		f := failure{reason: "hash mismatch in fixed-output derivation", errType: ErrHashMismatch}
		if sp := reSpecified.FindStringSubmatch(text); sp != nil {
			f.placeholder = isPlaceholderHash(sp[1])
		}
		p.buildFailed(m[1], f, at)
		return
	}
	if !isError {
		return
	}
	switch {
	case reInterrupted.MatchString(clean):
		p.noteKind(ErrSignal)
	case reDrvPath.MatchString(clean):
		p.noteKind(ErrOther) // an error about a derivation we cannot classify
	default:
		p.noteKind(ErrEval)
	}
}

func (p *Processor) noteKind(k string) {
	if p.sum.kinds == nil {
		p.sum.kinds = make(map[string]bool)
	}
	p.sum.kinds[k] = true
}

// isPlaceholderHash reports whether an SRI hash is the all-A placeholder
// (lib.fakeHash: "sha256-AAAA...=").
func isPlaceholderHash(h string) bool {
	i := strings.LastIndexByte(h, '-')
	body := strings.TrimRight(h[i+1:], "=")
	return body != "" && strings.Trim(body, "A") == ""
}

var (
	reCredentials = regexp.MustCompile(`(://)[^/@\s:]*(?::[^/@\s]*)?@`)
	reStoreHash   = regexp.MustCompile(`/nix/store/[0-9a-z]{32}-`)
)

// CleanError makes an error message safe to record on a span: URL
// credentials are redacted, store hashes dropped, whitespace collapsed and
// the result cut to MaxErrorLen characters.
func CleanError(s string) string {
	s = reCredentials.ReplaceAllString(s, "${1}<redacted>@")
	s = reStoreHash.ReplaceAllString(s, "/nix/store/")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxErrorLen {
		s = string(r[:MaxErrorLen])
	}
	return s
}

func countOrOne(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return 1
}

// buildFailed applies a failure message to a drv in whatever state it is in.
func (p *Processor) buildFailed(drv string, f failure, at time.Time) {
	if _, seen := p.failed[drv]; seen {
		return
	}
	p.failed[drv] = f
	p.noteKind(f.errType)
	if len(p.sum.FailedDrvs) < MaxFailedDrvs {
		p.sum.FailedDrvs = append(p.sum.FailedDrvs, StorePathName(drv))
	}
	for i, h := range p.held {
		if h.act.Path == drv {
			markFailed(&h.act, f)
			p.held = append(p.held[:i], p.held[i+1:]...)
			p.emit(h.act)
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
	markFailed(&a, f)
	p.emit(a)
}

// reasonType maps a "Cannot build" reason to an activity error type.
func reasonType(reason string) string {
	if strings.Contains(reason, "dependency failed") || strings.Contains(reason, "dependencies failed") {
		return ErrDependencyFailed
	}
	return ErrBuildFailed
}

func markFailed(a *Activity, f failure) {
	a.Failed = true
	a.Reason = f.reason
	a.ErrorType = f.errType
	a.Placeholder = f.placeholder
}

// Finish ends the run at end (W-9 c): held builds are released as successful,
// and every still-open activity is closed with an error status.
func (p *Processor) Finish(end time.Time) {
	for _, h := range p.held {
		p.emit(h.act)
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
		p.emit(a)
	}
	p.active = make(map[uint64]*open)
}

// tracked reports whether progress of id is kept: an aggregate activity, an
// open substitution, or the CopyPath child of one.
func (p *Processor) tracked(id uint64) bool {
	if _, ok := p.aggs[id]; ok {
		return true
	}
	if o, ok := p.active[id]; ok && o.act.Kind == KindSubstitute {
		return true
	}
	_, ok := p.copies[id]
	return ok
}

// fold keeps the last progress values of a tracked activity.
func (p *Processor) fold(id uint64, f [4]int64) {
	if a, ok := p.aggs[id]; ok {
		a.seen = true
		a.last = Progress{Done: f[0], Expected: f[1], Failed: f[3]}
		return
	}
	if sid, ok := p.copies[id]; ok {
		id = sid
	}
	if o, ok := p.active[id]; ok && o.act.Kind == KindSubstitute {
		o.act.Bytes, o.act.HasBytes = f[0], true
	}
}

// suffixID reads the activity id of a type-105 result line from its byte
// suffix (`..."id":N,"type":105}`, nix emits keys alphabetically). It reports
// false for any line that does not have exactly that shape.
func suffixID(line []byte) (uint64, bool) {
	head := line[:len(line)-len(suffix105)]
	if len(head) == 0 || head[len(head)-1] != ',' {
		return 0, false
	}
	head = head[:len(head)-1]
	i := bytes.LastIndex(head, keyID)
	if i < 0 {
		return 0, false
	}
	digits := head[i+len(keyID):]
	if len(digits) == 0 || len(digits) > 19 {
		return 0, false
	}
	var id uint64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		id = id*10 + uint64(c-'0')
	}
	return id, true
}

// fastFields reads the numbers of the `"fields":[a,b,c,d]` array without a
// JSON decode. It reports false for anything but 1 to 4 plain non-negative
// integers, and the caller then decodes the line normally.
func fastFields(line []byte) ([4]int64, bool) {
	var f [4]int64
	i := bytes.Index(line, keyFields)
	if i < 0 {
		return f, false
	}
	rest := line[i+len(keyFields):]
	n, cur, digits := 0, int64(0), 0
	for _, c := range rest {
		switch {
		case c >= '0' && c <= '9':
			if digits >= 18 {
				return f, false
			}
			cur, digits = cur*10+int64(c-'0'), digits+1
		case c == ',' || c == ']':
			if digits == 0 || n >= 4 {
				return f, false
			}
			f[n], n, cur, digits = cur, n+1, 0, 0
			if c == ']' {
				return f, true
			}
		default:
			return f, false
		}
	}
	return f, false
}

func decodeFields(raw []json.RawMessage) ([4]int64, bool) {
	var f [4]int64
	if len(raw) == 0 || len(raw) > 4 {
		return f, false
	}
	for i, r := range raw {
		if err := json.Unmarshal(r, &f[i]); err != nil {
			return f, false
		}
	}
	return f, true
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
