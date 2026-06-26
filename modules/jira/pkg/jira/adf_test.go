package jira

import (
	"encoding/json"
	"testing"
)

func TestFlattenADF(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string fallback", `"hello"`, "hello"},
		{"paragraph text", `{"content":[{"type":"paragraph","content":[{"type":"text","text":"hi there"}]}]}`, "hi there"},
		{"mention -> display name", `{"content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"text":"@Jane"}}]}]}`, "@Jane"},
		{"link -> href", `{"content":[{"type":"paragraph","content":[{"type":"text","text":"see ","marks":[{"type":"link","attrs":{"href":"http://x"}}]}]}]}`, "see"},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FlattenADF(json.RawMessage(c.raw))
			if got != c.want {
				t.Errorf("FlattenADF(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}
