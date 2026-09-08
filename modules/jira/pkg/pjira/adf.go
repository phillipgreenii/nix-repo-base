package pjira

import (
	"encoding/json"
	"strings"
)

// FlattenADF flattens an Atlassian Document Format body to best-effort plain
// text. A plain JSON string is returned as-is. Unknown nodes recurse into
// children; mention -> attrs.text (display name); inlineCard -> attrs.url; link marks emit visible text only.
func FlattenADF(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var doc struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return string(raw)
	}
	var sb strings.Builder
	walkADF(doc.Content, &sb)
	return strings.TrimSpace(sb.String())
}

// EncodeADFText encodes plain text into a minimal Atlassian Document Format
// document: one paragraph per input line (split on "\n"), each paragraph
// holding a single text node (an empty line becomes an empty paragraph, never
// dropped, so line structure round-trips). It is the encode-side counterpart
// to FlattenADF and round-trips through it exactly.
//
// Kept deliberately separable from any particular write endpoint (create,
// comment, ...) so it can be reused wherever a plain-text field needs to
// become an ADF document, rather than each call site re-deriving its own
// envelope.
func EncodeADFText(text string) map[string]any {
	lines := strings.Split(text, "\n")
	content := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		inline := []map[string]any{}
		if line != "" {
			inline = append(inline, map[string]any{"type": "text", "text": line})
		}
		content = append(content, map[string]any{"type": "paragraph", "content": inline})
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func walkADF(nodes []json.RawMessage, sb *strings.Builder) {
	for _, n := range nodes {
		var node struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Attrs struct {
				Text string `json:"text"`
				Href string `json:"href"`
				URL  string `json:"url"`
			} `json:"attrs"`
			Content []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(n, &node); err != nil {
			continue
		}
		switch node.Type {
		case "text":
			sb.WriteString(node.Text)
		case "mention":
			sb.WriteString(node.Attrs.Text)
		case "inlineCard":
			sb.WriteString(node.Attrs.URL)
		}
		walkADF(node.Content, sb)
		switch node.Type {
		case "paragraph", "heading", "bulletList", "orderedList":
			sb.WriteString("\n")
		}
	}
}
