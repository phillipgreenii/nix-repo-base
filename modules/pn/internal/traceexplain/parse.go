// Package traceexplain reads one trace from Tempo's HTTP API v2 and reports
// where a "pn workspace update" spent its time. It has three layers: parse.go
// turns Tempo's JSON into flat Spans, analyze.go turns Spans into a Report,
// and render.go turns a Report into text.
package traceexplain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Span is one span of a trace, with attributes flattened to strings. SpanID
// and ParentID are Tempo's opaque base64 ids: they are only compared, never
// decoded.
type Span struct {
	SpanID, ParentID, Name string
	Start, End             time.Time
	Attrs                  map[string]string
	Error                  bool
}

// ErrIncompleteTrace is returned by Analyze when the trace has no pn.verb
// span (the trace was cut off or is still being written).
var ErrIncompleteTrace = errors.New("trace has no pn.verb span")

type tempoValue struct {
	StringValue *string         `json:"stringValue"`
	IntValue    json.RawMessage `json:"intValue"`
	BoolValue   *bool           `json:"boolValue"`
	DoubleValue *float64        `json:"doubleValue"`
	ArrayValue  *struct {
		Values []tempoValue `json:"values"`
	} `json:"arrayValue"`
}

type tempoSpan struct {
	SpanID            string `json:"spanId"`
	ParentSpanID      string `json:"parentSpanId"`
	Name              string `json:"name"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	EndTimeUnixNano   string `json:"endTimeUnixNano"`
	Status            struct {
		Code json.RawMessage `json:"code"`
	} `json:"status"`
	Attributes []struct {
		Key   string     `json:"key"`
		Value tempoValue `json:"value"`
	} `json:"attributes"`
}

type tempoResponse struct {
	Trace struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []tempoSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	} `json:"trace"`
}

// ParseTempoV2 parses the body of GET /api/v2/traces/<id>. The pn and
// pg-nix-log-wrapped services arrive as separate resourceSpans; their spans
// are returned together in document order.
func ParseTempoV2(data []byte) ([]Span, error) {
	var resp tempoResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse tempo trace: %w", err)
	}
	var out []Span
	for _, rs := range resp.Trace.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, ts := range ss.Spans {
				sp, err := convertSpan(ts)
				if err != nil {
					return nil, err
				}
				out = append(out, sp)
			}
		}
	}
	return out, nil
}

func convertSpan(ts tempoSpan) (Span, error) {
	start, err := parseNano(ts.StartTimeUnixNano)
	if err != nil {
		return Span{}, fmt.Errorf("span %q start: %w", ts.Name, err)
	}
	end, err := parseNano(ts.EndTimeUnixNano)
	if err != nil {
		return Span{}, fmt.Errorf("span %q end: %w", ts.Name, err)
	}
	sp := Span{
		SpanID:   ts.SpanID,
		ParentID: ts.ParentSpanID,
		Name:     ts.Name,
		Start:    start,
		End:      end,
		Attrs:    make(map[string]string, len(ts.Attributes)),
		Error:    isErrorCode(ts.Status.Code),
	}
	for _, a := range ts.Attributes {
		sp.Attrs[a.Key] = a.Value.text()
	}
	return sp, nil
}

func parseNano(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad unix-nano timestamp %q: %w", s, err)
	}
	return time.Unix(0, n).UTC(), nil
}

// isErrorCode accepts both spellings of OTLP's error status: the enum name
// and the number 2.
func isErrorCode(raw json.RawMessage) bool {
	s := strings.Trim(string(raw), `"`)
	return s == "STATUS_CODE_ERROR" || s == "2"
}

func (v tempoValue) text() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case len(v.IntValue) > 0:
		return strings.Trim(string(v.IntValue), `"`)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	case v.ArrayValue != nil:
		parts := make([]string, 0, len(v.ArrayValue.Values))
		for _, e := range v.ArrayValue.Values {
			parts = append(parts, e.text())
		}
		return strings.Join(parts, ",")
	}
	return ""
}
