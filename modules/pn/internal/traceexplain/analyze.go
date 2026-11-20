package traceexplain

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// Span names and attribute keys the analysis reads. They mirror
// internal/telemetry and pg-nix-log-wrapped; the producer-consumer test
// builds spans with the real starters so a rename shows up there.
const (
	spanVerb       = "pn.verb"
	spanRepo       = "pn.repo"
	spanExec       = "pn.exec"
	spanHook       = "pn.hook"
	spanRelock     = "pn.relock"
	spanULStep     = "pn.ul.step"
	spanNixInvoke  = "nix.invocation"
	spanNixBuild   = "nix.build"
	spanSubstitute = "nix.substitute"
	spanLockWait   = "nix.build.wait"

	attrRepo        = "pn.repo"
	attrVerb        = "pn.verb"
	attrScope       = "pn.update.scope"
	attrExecutable  = "process.executable.name"
	attrSubcommand  = "nix.subcommand"
	attrPlanBuild   = "nix.plan.build"
	attrPlanFetch   = "nix.plan.fetch"
	attrDrvName     = "nix.drv.name"
	attrHookWhen    = "pn.hook.when"
	attrHookName    = "pn.hook.name"
	attrStepName    = "pn.ul.step.name"
	attrStepOutcome = "pn.ul.step.outcome"

	updateLocksScript = "./update-locks.sh"

	// anomalyTolerance is how far a child may end after its parent before
	// that is worth reporting.
	anomalyTolerance = time.Second
)

// Report is the analysis of one trace. Seconds are unrounded here; JSON
// marshalling rounds them to 0.1 s.
type Report struct {
	TraceID                 string       `json:"trace_id"`
	Verb                    string       `json:"verb"`
	Scope                   string       `json:"scope"`
	Start                   time.Time    `json:"start"`
	Incomplete              bool         `json:"incomplete"`
	WallSeconds             float64      `json:"wall_seconds"`
	OutsideReposSeconds     float64      `json:"outside_repos_seconds"`
	Hooks                   []Hook       `json:"hooks"`
	Repos                   []RepoReport `json:"repos"`
	TopBuilds               []Build      `json:"top_builds"`
	LockWaitSecondsSummed   float64      `json:"lock_wait_seconds_summed"`
	SubstituteSecondsSummed float64      `json:"substitute_seconds_summed"`
	Anomalies               []string     `json:"anomalies"`
}

// Hook is one pn.hook span.
type Hook struct {
	When    string  `json:"when"`
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
}

// RepoReport is where one pn.repo span spent its time.
type RepoReport struct {
	Name                string    `json:"name"`
	WallSeconds         float64   `json:"wall_seconds"`
	GitSeconds          float64   `json:"git_seconds"`
	RelockSeconds       float64   `json:"relock_seconds"`
	UpdateLocks         *ULReport `json:"update_locks,omitempty"`
	Nix                 []NixCall `json:"nix"`
	UnattributedSeconds float64   `json:"unattributed_seconds"`
}

// ULReport describes the update-locks.sh run inside a repo.
type ULReport struct {
	Seconds             float64 `json:"seconds"`
	SetupSeconds        float64 `json:"setup_seconds"`
	Steps               []Step  `json:"steps"`
	BetweenStepsSeconds float64 `json:"between_steps_seconds"`
	FinalizeSeconds     float64 `json:"finalize_seconds"`
	StepsMissing        bool    `json:"steps_missing"`
}

// Step is one pn.ul.step span.
type Step struct {
	Name    string  `json:"name"`
	Outcome string  `json:"outcome"`
	Seconds float64 `json:"seconds"`
}

// NixCall is one nix.invocation span.
type NixCall struct {
	Subcommand  string  `json:"subcommand"`
	Seconds     float64 `json:"seconds"`
	PlanBuilds  int     `json:"plan_builds"`
	PlanFetches int     `json:"plan_fetches"`
}

// Build is one local nix.build span. Step is the pn.ul.step whose interval
// contains the build's start, or "" when none does.
type Build struct {
	Drv     string  `json:"drv"`
	Repo    string  `json:"repo"`
	Step    string  `json:"step"`
	Seconds float64 `json:"seconds"`
}

func round1(s float64) float64 { return math.Round(s*10) / 10 }

// The MarshalJSON methods round every "...seconds..." field to 0.1 s. They
// work on a copy through a method-less alias type, so the Report itself keeps
// unrounded values.

func (r Report) MarshalJSON() ([]byte, error) {
	type alias Report
	r.WallSeconds = round1(r.WallSeconds)
	r.OutsideReposSeconds = round1(r.OutsideReposSeconds)
	r.LockWaitSecondsSummed = round1(r.LockWaitSecondsSummed)
	r.SubstituteSecondsSummed = round1(r.SubstituteSecondsSummed)
	r.Start = r.Start.UTC()
	return json.Marshal(alias(r))
}

func (h Hook) MarshalJSON() ([]byte, error) {
	type alias Hook
	h.Seconds = round1(h.Seconds)
	return json.Marshal(alias(h))
}

func (r RepoReport) MarshalJSON() ([]byte, error) {
	type alias RepoReport
	r.WallSeconds = round1(r.WallSeconds)
	r.GitSeconds = round1(r.GitSeconds)
	r.RelockSeconds = round1(r.RelockSeconds)
	r.UnattributedSeconds = round1(r.UnattributedSeconds)
	return json.Marshal(alias(r))
}

func (u ULReport) MarshalJSON() ([]byte, error) {
	type alias ULReport
	u.Seconds = round1(u.Seconds)
	u.SetupSeconds = round1(u.SetupSeconds)
	u.BetweenStepsSeconds = round1(u.BetweenStepsSeconds)
	u.FinalizeSeconds = round1(u.FinalizeSeconds)
	return json.Marshal(alias(u))
}

func (s Step) MarshalJSON() ([]byte, error) {
	type alias Step
	s.Seconds = round1(s.Seconds)
	return json.Marshal(alias(s))
}

func (n NixCall) MarshalJSON() ([]byte, error) {
	type alias NixCall
	n.Seconds = round1(n.Seconds)
	return json.Marshal(alias(n))
}

func (b Build) MarshalJSON() ([]byte, error) {
	type alias Build
	b.Seconds = round1(b.Seconds)
	return json.Marshal(alias(b))
}

// node is a Span plus its place in the tree. cs/ce are the span's interval
// clamped to its parent's already-clamped interval.
type node struct {
	Span
	parent   *node
	kids     []*node
	cs, ce   time.Time
	repo     *node // nearest pn.repo ancestor (itself for a pn.repo), or nil
	orphaned bool  // under a span whose parent is missing from the trace
}

func (n *node) secs() float64 { return n.ce.Sub(n.cs).Seconds() }

func (n *node) label() string {
	var l string
	switch n.Name {
	case spanNixBuild:
		l = n.Attrs[attrDrvName]
	case spanNixInvoke:
		l = n.Attrs[attrSubcommand]
	}
	if l == "" {
		return n.Name
	}
	return n.Name + " " + l
}

type interval struct{ s, e time.Time }

// unionSeconds is the length of the union of the intervals.
func unionSeconds(iv []interval) float64 {
	sort.Slice(iv, func(i, j int) bool { return iv[i].s.Before(iv[j].s) })
	var total time.Duration
	var cur interval
	open := false
	for _, x := range iv {
		switch {
		case !open:
			cur, open = x, true
		case x.s.After(cur.e):
			total += cur.e.Sub(cur.s)
			cur = x
		case x.e.After(cur.e):
			cur.e = x.e
		}
	}
	if open {
		total += cur.e.Sub(cur.s)
	}
	return total.Seconds()
}

type anomaly struct {
	start time.Time
	msg   string
}

// Analyze builds the Report for one trace. top limits Report.TopBuilds (top
// <= 0 keeps every build). Without a pn.verb span it returns
// ErrIncompleteTrace, unless partial is set: then it analyzes the pn.repo
// spans and marks the report Incomplete.
func Analyze(traceID string, spans []Span, top int, partial bool) (Report, error) {
	nodes, roots := link(spans)

	var (
		anoms   []anomaly
		orphans int
	)
	var visit func(n, parent *node, orphaned bool)
	visit = func(n, parent *node, orphaned bool) {
		n.parent, n.orphaned = parent, orphaned
		n.cs, n.ce = n.Start, n.End
		n.repo = nil
		if parent != nil {
			n.repo = parent.repo
			if n.cs.Before(parent.cs) {
				n.cs = parent.cs
			}
			if n.ce.After(parent.ce) {
				n.ce = parent.ce
			}
			if n.ce.Before(n.cs) {
				n.ce = n.cs
			}
		}
		if n.Name == spanRepo {
			if n.repo != nil && !orphaned {
				anoms = append(anoms, anomaly{n.Start, fmt.Sprintf("pn.repo %s is nested inside pn.repo %s",
					n.Attrs[attrRepo], n.repo.Attrs[attrRepo])})
			}
			n.repo = n
		}
		if parent != nil && !orphaned && n.End.After(parent.End.Add(anomalyTolerance)) {
			msg := n.label()
			repoName := ""
			if n.repo != nil {
				repoName = n.repo.Attrs[attrRepo]
				msg += " in " + repoName
			}
			msg += fmt.Sprintf(" ends %ds after its parent %s", int(math.Round(n.End.Sub(parent.End).Seconds())), parent.Name)
			if repoName != "" {
				msg += fmt.Sprintf(" (%s's figures clamp it to its parent's end)", repoName)
			} else {
				msg += " (the figures clamp it to its parent's end)"
			}
			anoms = append(anoms, anomaly{n.Start, msg})
		}
		for _, k := range n.kids {
			visit(k, n, orphaned)
		}
	}
	for _, r := range roots {
		orphaned := false
		if r.missingParent && r.Name != spanRepo {
			orphaned = true
			orphans++
		}
		visit(r.node, nil, orphaned)
	}

	var verb *node
	for _, n := range nodes {
		if n.Name == spanVerb && !n.orphaned {
			verb = n
			break
		}
	}
	if verb == nil && !partial {
		return Report{}, ErrIncompleteTrace
	}

	rep := Report{
		TraceID:   traceID,
		Hooks:     []Hook{},
		Repos:     []RepoReport{},
		TopBuilds: []Build{},
		Anomalies: []string{},
	}

	var repoNodes []*node
	for _, n := range nodes {
		if n.Name == spanRepo && !n.orphaned {
			repoNodes = append(repoNodes, n)
		}
	}

	var repoIv []interval
	for _, r := range repoNodes {
		repoIv = append(repoIv, interval{r.cs, r.ce})
	}
	if verb != nil {
		rep.Verb = verb.Attrs[attrVerb]
		rep.Scope = verb.Attrs[attrScope]
		rep.Start = verb.Start.UTC()
		rep.WallSeconds = verb.End.Sub(verb.Start).Seconds()
		rep.OutsideReposSeconds = math.Max(0, verb.End.Sub(verb.Start).Seconds()-unionSeconds(repoIv))
	} else {
		rep.Incomplete = true
		if len(repoNodes) > 0 {
			lo, hi := repoNodes[0].cs, repoNodes[0].ce
			for _, r := range repoNodes {
				if r.cs.Before(lo) {
					lo = r.cs
				}
				if r.ce.After(hi) {
					hi = r.ce
				}
			}
			rep.Start = lo.UTC()
			rep.WallSeconds = hi.Sub(lo).Seconds()
		}
	}

	stepsByRepo := map[*node][]*node{}
	for _, r := range repoNodes {
		rr, steps := repoReport(r)
		stepsByRepo[r] = steps
		rep.Repos = append(rep.Repos, rr)
	}

	var builds []*node
	for _, n := range nodes {
		if n.orphaned {
			continue
		}
		switch n.Name {
		case spanHook:
			rep.Hooks = append(rep.Hooks, Hook{When: n.Attrs[attrHookWhen], Name: n.Attrs[attrHookName], Seconds: n.secs()})
		case spanSubstitute:
			rep.SubstituteSecondsSummed += n.secs()
		case spanLockWait:
			rep.LockWaitSecondsSummed += n.secs()
		case spanNixBuild:
			builds = append(builds, n)
		}
	}
	sort.SliceStable(builds, func(i, j int) bool { return builds[i].secs() > builds[j].secs() })
	if top > 0 && len(builds) > top {
		builds = builds[:top]
	}
	for _, b := range builds {
		bd := Build{Drv: b.Attrs[attrDrvName], Seconds: b.secs()}
		if b.repo != nil {
			bd.Repo = b.repo.Attrs[attrRepo]
			for _, s := range stepsByRepo[b.repo] {
				if !b.cs.Before(s.cs) && b.cs.Before(s.ce) {
					bd.Step = s.Attrs[attrStepName]
					break
				}
			}
		}
		rep.TopBuilds = append(rep.TopBuilds, bd)
	}

	sort.SliceStable(anoms, func(i, j int) bool { return anoms[i].start.Before(anoms[j].start) })
	for _, a := range anoms {
		rep.Anomalies = append(rep.Anomalies, a.msg)
	}
	if orphans > 0 {
		rep.Anomalies = append(rep.Anomalies, fmt.Sprintf(
			"%d span(s) reference a parent missing from the trace (Tempo may have truncated it; those spans are left out of every figure)", orphans,
		))
	}
	return rep, nil
}

type root struct {
	*node
	missingParent bool
}

// link builds the span tree. nodes is in start order (ties keep input order);
// roots are the spans with no parent or a parent absent from the trace.
func link(spans []Span) (nodes []*node, roots []root) {
	nodes = make([]*node, len(spans))
	byID := make(map[string]*node, len(spans))
	for i := range spans {
		nodes[i] = &node{Span: spans[i]}
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Start.Before(nodes[j].Start) })
	for _, n := range nodes {
		byID[n.SpanID] = n
	}
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, root{node: n})
			continue
		}
		p, ok := byID[n.ParentID]
		if !ok {
			roots = append(roots, root{node: n, missingParent: true})
			continue
		}
		if p != n {
			p.kids = append(p.kids, n)
		}
	}
	return nodes, roots
}

// repoReport accounts for one pn.repo node. It also returns the repo's
// pn.ul.step nodes in start order, for tagging builds.
func repoReport(r *node) (RepoReport, []*node) {
	rr := RepoReport{Name: r.Attrs[attrRepo], WallSeconds: r.secs(), Nix: []NixCall{}}

	var counted []interval
	var script *node
	for _, k := range r.kids {
		switch k.Name {
		case spanExec:
			counted = append(counted, interval{k.cs, k.ce})
			switch k.Attrs[attrExecutable] {
			case "git":
				rr.GitSeconds += k.secs()
			case updateLocksScript:
				if script == nil {
					script = k
				}
			}
		case spanRelock:
			counted = append(counted, interval{k.cs, k.ce})
			rr.RelockSeconds += k.secs()
		}
	}
	rr.UnattributedSeconds = math.Max(0, rr.WallSeconds-unionSeconds(counted))

	// Everything below the repo, except spans of a nested pn.repo.
	var steps, nixNodes []*node
	var walk func(n *node)
	walk = func(n *node) {
		for _, k := range n.kids {
			if k.Name == spanRepo {
				continue
			}
			switch k.Name {
			case spanULStep:
				steps = append(steps, k)
			case spanNixInvoke:
				nixNodes = append(nixNodes, k)
			}
			walk(k)
		}
	}
	walk(r)
	// The walk is depth first; steps and nix calls are reported in start order.
	byStart := func(ns []*node) {
		sort.SliceStable(ns, func(i, j int) bool { return ns[i].Start.Before(ns[j].Start) })
	}
	byStart(steps)
	byStart(nixNodes)
	for _, k := range nixNodes {
		rr.Nix = append(rr.Nix, NixCall{
			Subcommand:  k.Attrs[attrSubcommand],
			Seconds:     k.secs(),
			PlanBuilds:  atoi(k.Attrs[attrPlanBuild]),
			PlanFetches: atoi(k.Attrs[attrPlanFetch]),
		})
	}

	if script != nil {
		ul := &ULReport{Seconds: script.secs(), Steps: []Step{}}
		if len(steps) == 0 {
			ul.StepsMissing = true
		} else {
			var iv []interval
			first, last := steps[0].cs, steps[0].ce
			for _, s := range steps {
				ul.Steps = append(ul.Steps, Step{Name: s.Attrs[attrStepName], Outcome: s.Attrs[attrStepOutcome], Seconds: s.secs()})
				iv = append(iv, interval{s.cs, s.ce})
				if s.cs.Before(first) {
					first = s.cs
				}
				if s.ce.After(last) {
					last = s.ce
				}
			}
			ul.SetupSeconds = math.Max(0, first.Sub(script.cs).Seconds())
			ul.BetweenStepsSeconds = math.Max(0, last.Sub(first).Seconds()-unionSeconds(iv))
			ul.FinalizeSeconds = math.Max(0, script.ce.Sub(last).Seconds())
		}
		rr.UpdateLocks = ul
	}
	return rr, steps
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
