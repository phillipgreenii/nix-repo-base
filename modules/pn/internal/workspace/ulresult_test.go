package workspace

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

func TestParseULSteps(t *testing.T) {
	a := telemetry.ULStep{Name: "a", Outcome: "ok", StartMS: 1000, MS: 5}
	b := telemetry.ULStep{Name: "b.c-d_1", Outcome: "failed", StartMS: 2000, MS: 7}
	tests := []struct {
		name  string
		input string
		want  []telemetry.ULStep
	}{
		{"no lines", "hello\nworld\n", nil},
		{"two valid lines", "UL_STEP name=a outcome=ok start_ms=1000 ms=5\nUL_STEP name=b.c-d_1 outcome=failed start_ms=2000 ms=7\n", []telemetry.ULStep{a, b}},
		{"interleaved with other output", "building\nUL_STEP name=a outcome=ok start_ms=1000 ms=5\nUL_RESULT transient=0\nUL_STEP name=b.c-d_1 outcome=failed start_ms=2000 ms=7\nUL_RESULT transient=0\n", []telemetry.ULStep{a, b}},
		{"partial output before the marker", "downloading...UL_STEP name=a outcome=ok start_ms=1000 ms=5\n", []telemetry.ULStep{a}},
		{"unknown outcome", "UL_STEP name=a outcome=bogus start_ms=1000 ms=5\n", nil},
		{"missing field", "UL_STEP name=a outcome=ok ms=5\n", nil},
		{"non-numeric ms", "UL_STEP name=a outcome=ok start_ms=1000 ms=abc\n", nil},
		{"negative ms", "UL_STEP name=a outcome=ok start_ms=1000 ms=-1\n", nil},
		{"empty name", "UL_STEP name= outcome=ok start_ms=1000 ms=5\n", nil},
		{"name with a space", "UL_STEP name=a b outcome=ok start_ms=1000 ms=5\n", nil},
		{"trailing carriage return", "UL_STEP name=a outcome=ok start_ms=1000 ms=5\r\n", []telemetry.ULStep{a}},
		{"same name twice kept in order", "UL_STEP name=a outcome=ok start_ms=1000 ms=5\nUL_STEP name=a outcome=skipped start_ms=3000 ms=0\n", []telemetry.ULStep{a, {Name: "a", Outcome: "skipped", StartMS: 3000, MS: 0}}},
		{"unknown fifth field", "UL_STEP name=a outcome=ok start_ms=1000 ms=5 extra=1\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseULSteps([]byte(tc.input))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseULSteps = %+v, want %+v", got, tc.want)
			}
		})
	}
}
