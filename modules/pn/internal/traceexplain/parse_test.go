package traceexplain

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) []Span {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	spans, err := ParseTempoV2(data)
	if err != nil {
		t.Fatalf("ParseTempoV2(%s): %v", name, err)
	}
	return spans
}

func TestParseTempoV2_Fixture(t *testing.T) {
	spans := loadFixture(t, "update-two-repos.json")
	if len(spans) != 18 {
		t.Fatalf("span count = %d, want 18 (pn and nix spans come from two resourceSpans)", len(spans))
	}
	names := map[string]int{}
	for _, s := range spans {
		names[s.Name]++
	}
	want := map[string]int{
		"pn.verb": 1, "pn.repo": 2, "pn.exec": 5, "pn.ul.step": 2, "pn.relock": 1,
		"pn.hook": 1, "nix.invocation": 2, "nix.build": 2, "nix.substitute": 1, "nix.build.wait": 1,
	}
	for n, c := range want {
		if names[n] != c {
			t.Errorf("spans named %s = %d, want %d", n, names[n], c)
		}
	}
}

const typedSpans = `{"trace":{"resourceSpans":[
 {"scopeSpans":[{"spans":[
  {"spanId":"cm9vdA==","name":"root","startTimeUnixNano":"1763467200000000000","endTimeUnixNano":"1763467260500000000",
   "status":{"code":"STATUS_CODE_ERROR"},
   "attributes":[
     {"key":"i","value":{"intValue":"42"}},
     {"key":"d","value":{"doubleValue":0.25}},
     {"key":"t","value":{"boolValue":true}},
     {"key":"f","value":{"boolValue":false}},
     {"key":"s","value":{"stringValue":"x"}},
     {"key":"arr","value":{"arrayValue":{"values":[{"stringValue":"a"},{"stringValue":"b"},{"intValue":"3"}]}}}]}]}]},
 {"scopeSpans":[{"spans":[
  {"spanId":"a2lk","parentSpanId":"cm9vdA==","name":"child","startTimeUnixNano":"1763467201000000000","endTimeUnixNano":"1763467202000000000",
   "status":{"code":"STATUS_CODE_UNSET"}}]}]}]}}`

func TestParseTempoV2(t *testing.T) {
	spans, err := ParseTempoV2([]byte(typedSpans))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[0].Name != "root" || spans[1].Name != "child" {
		t.Fatalf("spans = %+v", spans)
	}
	root, child := spans[0], spans[1]
	if !root.Start.Equal(time.Unix(1763467200, 0)) || !root.End.Equal(time.Unix(1763467260, 500_000_000)) {
		t.Errorf("start/end = %v / %v", root.Start, root.End)
	}
	for k, want := range map[string]string{"i": "42", "d": "0.25", "t": "true", "f": "false", "s": "x", "arr": "a,b,3"} {
		if got := root.Attrs[k]; got != want {
			t.Errorf("attr %s = %q, want %q", k, got, want)
		}
	}
	if root.ParentID != "" {
		t.Errorf("root ParentID = %q, want empty", root.ParentID)
	}
	if child.ParentID != "cm9vdA==" || child.SpanID != "a2lk" {
		t.Errorf("child ids = %q / %q", child.SpanID, child.ParentID)
	}
	if !root.Error || child.Error {
		t.Errorf("Error root/child = %v/%v, want true/false", root.Error, child.Error)
	}
}

func TestParseTempoV2_Malformed(t *testing.T) {
	if _, err := ParseTempoV2([]byte(`{"trace":`)); err == nil {
		t.Error("malformed JSON: want error")
	}
	bad := `{"trace":{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"x","startTimeUnixNano":"nope","endTimeUnixNano":"1"}]}]}]}}`
	if _, err := ParseTempoV2([]byte(bad)); err == nil {
		t.Error("bad timestamp: want error")
	}
}
