package pjira

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimit is the Atlassian rate-limit reading carried by ONE HTTP response.
// Every field is optional: a pointer or empty string means the response did not
// carry that header (or carried a value that could not be parsed), so a caller
// can tell "absent" from a real zero.
//
// Limit, Remaining and NearLimit ride on every response; RetryAfterSeconds,
// Reset and Reason ride on a 429 (Retry-After may also appear on a transient
// 5xx). Only these headers are copied -- never the request's Authorization or
// any other header -- so a RateLimit holds no secret.
type RateLimit struct {
	// Status is the HTTP status code of the response the reading came from.
	Status int `json:"status"`
	// Limit is X-RateLimit-Limit.
	Limit *int `json:"limit,omitempty"`
	// Remaining is X-RateLimit-Remaining.
	Remaining *int `json:"remaining,omitempty"`
	// NearLimit is X-RateLimit-NearLimit (true below 20% capacity).
	NearLimit *bool `json:"near_limit,omitempty"`
	// RetryAfterSeconds is Retry-After as whole seconds, rounded up. Atlassian
	// documents it as a MINIMUM delay, not a hint. An HTTP-date value is
	// converted to seconds from now (never negative).
	RetryAfterSeconds *int `json:"retry_after_seconds,omitempty"`
	// Reset is X-RateLimit-Reset verbatim (an ISO 8601 timestamp).
	Reset string `json:"reset,omitempty"`
	// Reason is RateLimit-Reason verbatim (for example jira-burst-based).
	Reason string `json:"reason,omitempty"`
}

// Empty reports whether the response carried none of the rate-limit headers.
func (r RateLimit) Empty() bool {
	return r.Limit == nil && r.Remaining == nil && r.NearLimit == nil &&
		r.RetryAfterSeconds == nil && r.Reset == "" && r.Reason == ""
}

func intHeader(h http.Header, name string) *int {
	v := strings.TrimSpace(h.Get(name))
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func retryAfterSeconds(v string, now time.Time) *int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil
		}
		n := int(math.Ceil(f))
		return &n
	}
	if when, err := http.ParseTime(v); err == nil {
		n := max(int(math.Ceil(when.Sub(now).Seconds())), 0)
		return &n
	}
	return nil
}

// ParseRateLimit extracts the rate-limit reading from a response's status and
// headers. now anchors an HTTP-date Retry-After.
func ParseRateLimit(status int, h http.Header, now time.Time) RateLimit {
	rl := RateLimit{
		Status:            status,
		Limit:             intHeader(h, "X-RateLimit-Limit"),
		Remaining:         intHeader(h, "X-RateLimit-Remaining"),
		RetryAfterSeconds: retryAfterSeconds(h.Get("Retry-After"), now),
		Reset:             strings.TrimSpace(h.Get("X-RateLimit-Reset")),
		Reason:            strings.TrimSpace(h.Get("RateLimit-Reason")),
	}
	if b, err := strconv.ParseBool(strings.TrimSpace(h.Get("X-RateLimit-NearLimit"))); err == nil {
		rl.NearLimit = &b
	}
	return rl
}
