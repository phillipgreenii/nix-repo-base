package workspace

import (
	"strconv"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// ulResultPrefix is the machine-readable line update-locks.sh prints from
// ul_finalize on every exit path: "UL_RESULT transient=<N>". It is pn's only
// window into a green update-locks run — the exit code is 0 whether or not
// steps were classified transient, so an automated update that checks only the
// exit code never sees a permanently-skipped ("silently transient") update
// (ADR 0020). The token is a stable key=value contract, extensible with more
// fields later; only `transient` is consumed today.
const ulResultPrefix = "UL_RESULT "

// parseULTransient extracts the transient-step count from update-locks.sh's
// captured stdout (see ulResultPrefix / ul_finalize). The LAST UL_RESULT line
// wins, since a wrapped step could itself echo the token earlier in the log.
// Returns 0 when no parseable line is present — an older update-locks.sh, a
// skipped/absent script, or a resource-abort that exits before ul_finalize.
func parseULTransient(stdout []byte) int {
	transient := 0
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ulResultPrefix) {
			continue
		}
		for _, field := range strings.Fields(line[len(ulResultPrefix):]) {
			if v, ok := strings.CutPrefix(field, "transient="); ok {
				if n, err := strconv.Atoi(v); err == nil {
					transient = n
				}
			}
		}
	}
	return transient
}

// ulStepPrefix marks the per-step line the shared update-locks lib prints on
// stdout: "UL_STEP name=<step> outcome=<outcome> start_ms=<epoch ms> ms=<ms>".
const ulStepPrefix = "UL_STEP "

// ulStepOutcomes are the outcomes a UL_STEP line may carry.
var ulStepOutcomes = map[string]bool{
	"ok": true, "skipped": true, "deferred": true, "transient": true, "failed": true, "aborted": true,
}

// parseULSteps extracts the UL_STEP lines from update-locks.sh's captured
// stdout, in order. The marker is searched for anywhere in a line, so a step's
// unterminated partial output cannot hide it; the four fields must then match
// exactly (name, outcome, start_ms, ms, nothing else) or the line is ignored.
func parseULSteps(stdout []byte) []telemetry.ULStep {
	var steps []telemetry.ULStep
	for _, line := range strings.Split(string(stdout), "\n") {
		_, rest, ok := strings.Cut(line, ulStepPrefix)
		if !ok {
			continue
		}
		if st, ok := parseULStepFields(strings.TrimRight(rest, "\r")); ok {
			steps = append(steps, st)
		}
	}
	return steps
}

func parseULStepFields(rest string) (telemetry.ULStep, bool) {
	var st telemetry.ULStep
	fields := strings.Split(rest, " ")
	if len(fields) != 4 {
		return st, false
	}
	keys := [4]string{"name=", "outcome=", "start_ms=", "ms="}
	var vals [4]string
	for i, f := range fields {
		v, ok := strings.CutPrefix(f, keys[i])
		if !ok || v == "" {
			return st, false
		}
		vals[i] = v
	}
	if !validULStepName(vals[0]) || !ulStepOutcomes[vals[1]] {
		return st, false
	}
	start, err1 := strconv.ParseInt(vals[2], 10, 64)
	ms, err2 := strconv.ParseInt(vals[3], 10, 64)
	if err1 != nil || err2 != nil || start < 0 || ms < 0 || vals[2][0] == '+' || vals[3][0] == '+' {
		return st, false
	}
	return telemetry.ULStep{Name: vals[0], Outcome: vals[1], StartMS: start, MS: ms}, true
}

// validULStepName reports whether s matches ^[A-Za-z0-9._-]+$.
func validULStepName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return s != ""
}

// ulStepFields is the project_result fields for steps: a steps list of
// {name, outcome, ms} and their summed steps_ms. It is nil for no steps.
func ulStepFields(steps []telemetry.ULStep) map[string]any {
	if len(steps) == 0 {
		return nil
	}
	list := make([]map[string]any, 0, len(steps))
	var sum int64
	for _, st := range steps {
		list = append(list, map[string]any{"name": st.Name, "outcome": st.Outcome, "ms": st.MS})
		sum += st.MS
	}
	return map[string]any{"steps": list, "steps_ms": sum}
}

// withFields copies extra into base and returns base.
func withFields(base, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}
