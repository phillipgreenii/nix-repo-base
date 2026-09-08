package pjira

import (
	"encoding/json"
	"testing"
)

// TestEncodeADFText pins the envelope shape (doc/version 1) and the
// one-paragraph-per-line splitting, including that a blank line becomes an
// empty paragraph rather than being dropped.
func TestEncodeADFText(t *testing.T) {
	cases := []struct {
		name           string
		in             string
		wantParagraphs int
	}{
		{"single line", "hello", 1},
		{"multi line", "a\nb", 2},
		{"blank line preserved", "a\n\nb", 3},
		{"empty string", "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := EncodeADFText(c.in)
			if doc["type"] != "doc" || doc["version"] != 1 {
				t.Fatalf("doc envelope = %+v, want type=doc version=1", doc)
			}
			content, ok := doc["content"].([]map[string]any)
			if !ok {
				t.Fatalf("content type = %T, want []map[string]any", doc["content"])
			}
			if len(content) != c.wantParagraphs {
				t.Errorf("paragraphs = %d, want %d: %+v", len(content), c.wantParagraphs, content)
			}
			for _, p := range content {
				if p["type"] != "paragraph" {
					t.Errorf("node type = %v, want paragraph", p["type"])
				}
			}
		})
	}
}

// TestEncodeADFText_roundTripsThroughFlattenADF pins that EncodeADFText is the
// exact encode-side counterpart of FlattenADF: marshaling the encoded doc to
// JSON and flattening it back must reproduce the original text.
func TestEncodeADFText_roundTripsThroughFlattenADF(t *testing.T) {
	cases := []string{"hello", "hello\nworld", "a\n\nb", ""}
	for _, want := range cases {
		t.Run(want, func(t *testing.T) {
			raw, err := json.Marshal(EncodeADFText(want))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := FlattenADF(raw)
			if got != want {
				t.Errorf("FlattenADF(EncodeADFText(%q)) = %q, want %q", want, got, want)
			}
		})
	}
}

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
