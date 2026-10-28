package pjira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func intp(n int) *int    { return &n }
func boolp(b bool) *bool { return &b }

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestParseRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		headers map[string]string
		check   func(t *testing.T, rl RateLimit)
	}{
		{"none present", nil, func(t *testing.T, rl RateLimit) {
			if !rl.Empty() {
				t.Errorf("want Empty, got %+v", rl)
			}
		}},
		{"every-response trio", map[string]string{
			"X-RateLimit-Limit": "100", "X-RateLimit-Remaining": "97", "X-RateLimit-NearLimit": "false",
		}, func(t *testing.T, rl RateLimit) {
			if rl.Empty() || !eqIntPtr(rl.Limit, intp(100)) || !eqIntPtr(rl.Remaining, intp(97)) ||
				rl.NearLimit == nil || *rl.NearLimit {
				t.Errorf("got %+v", rl)
			}
			if rl.RetryAfterSeconds != nil || rl.Reset != "" || rl.Reason != "" {
				t.Errorf("429-only fields must stay unset: %+v", rl)
			}
		}},
		{"near limit true, zero remaining is not absent", map[string]string{
			"X-RateLimit-Remaining": "0", "X-RateLimit-NearLimit": "true",
		}, func(t *testing.T, rl RateLimit) {
			if !eqIntPtr(rl.Remaining, intp(0)) || rl.NearLimit == nil || !*rl.NearLimit {
				t.Errorf("got %+v", rl)
			}
		}},
		{"429 trio", map[string]string{
			"Retry-After": "7", "X-RateLimit-Reset": "2026-10-27T12:00:07Z", "RateLimit-Reason": "jira-burst-based",
		}, func(t *testing.T, rl RateLimit) {
			if !eqIntPtr(rl.RetryAfterSeconds, intp(7)) || rl.Reset != "2026-10-27T12:00:07Z" || rl.Reason != "jira-burst-based" {
				t.Errorf("got %+v", rl)
			}
		}},
		{"Retry-After HTTP-date", map[string]string{
			"Retry-After": now.Add(30 * time.Second).Format(http.TimeFormat),
		}, func(t *testing.T, rl RateLimit) {
			if !eqIntPtr(rl.RetryAfterSeconds, intp(30)) {
				t.Errorf("got %+v", rl)
			}
		}},
		{"Retry-After HTTP-date in the past clamps to 0", map[string]string{
			"Retry-After": now.Add(-time.Minute).Format(http.TimeFormat),
		}, func(t *testing.T, rl RateLimit) {
			if !eqIntPtr(rl.RetryAfterSeconds, intp(0)) {
				t.Errorf("got %+v", rl)
			}
		}},
		{"fractional Retry-After rounds up (it is a floor)", map[string]string{"Retry-After": "1.2"}, func(t *testing.T, rl RateLimit) {
			if !eqIntPtr(rl.RetryAfterSeconds, intp(2)) {
				t.Errorf("got %+v", rl)
			}
		}},
		{"unparsable values are dropped, not zeroed", map[string]string{
			"X-RateLimit-Limit": "lots", "X-RateLimit-Remaining": "", "X-RateLimit-NearLimit": "maybe", "Retry-After": "soon",
		}, func(t *testing.T, rl RateLimit) {
			if !rl.Empty() {
				t.Errorf("want Empty, got %+v", rl)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range c.headers {
				h.Set(k, v)
			}
			rl := ParseRateLimit(200, h, now)
			if rl.Status != 200 {
				t.Errorf("Status = %d", rl.Status)
			}
			c.check(t, rl)
		})
	}
}

// TestClient_OnRateLimit drives the hook through a hermetic fake server: a 200
// with the every-response headers, a 429 with the full set, and a response with
// none (a tenant that sends no headers), which must not fire the hook.
func TestClient_OnRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/issue/ENG-200":
			w.Header().Set("X-RateLimit-Limit", "100")
			w.Header().Set("X-RateLimit-Remaining", "42")
			w.Header().Set("X-RateLimit-NearLimit", "false")
			_, _ = w.Write([]byte(`{"key":"ENG-200","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}`))
		case "/rest/api/3/issue/ENG-429":
			w.Header().Set("X-RateLimit-Limit", "100")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-NearLimit", "true")
			w.Header().Set("Retry-After", "11")
			w.Header().Set("X-RateLimit-Reset", "2026-10-27T12:00:11Z")
			w.Header().Set("RateLimit-Reason", "jira-burst-based")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			_, _ = w.Write([]byte(`{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}`))
		}
	}))
	defer srv.Close()

	var got []RateLimit
	c := testClient(srv)
	c.OnRateLimit = func(rl RateLimit) { got = append(got, rl) }

	if _, err := c.GetIssue(context.Background(), "ENG-200"); err != nil {
		t.Fatalf("200: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("200 with headers: want 1 reading, got %d", len(got))
	}
	rl := got[0]
	if rl.Status != 200 || !eqIntPtr(rl.Limit, intp(100)) || !eqIntPtr(rl.Remaining, intp(42)) || rl.NearLimit == nil || *rl.NearLimit {
		t.Errorf("200 reading = %+v", rl)
	}
	if rl.RetryAfterSeconds != nil || rl.Reset != "" || rl.Reason != "" {
		t.Errorf("200 must not carry 429-only fields: %+v", rl)
	}

	got = nil
	if _, err := c.GetIssue(context.Background(), "ENG-429"); err == nil {
		t.Fatal("429 must still be an error")
	}
	if len(got) != 1 {
		t.Fatalf("429 with headers: want 1 reading, got %d", len(got))
	}
	rl = got[0]
	if rl.Status != 429 || !eqIntPtr(rl.Remaining, intp(0)) || rl.NearLimit == nil || !*rl.NearLimit ||
		!eqIntPtr(rl.RetryAfterSeconds, intp(11)) || rl.Reset != "2026-10-27T12:00:11Z" || rl.Reason != "jira-burst-based" {
		t.Errorf("429 reading = %+v", rl)
	}

	got = nil
	if _, err := c.GetIssue(context.Background(), "ENG-1"); err != nil {
		t.Fatalf("no headers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a response with no headers must not fire the hook, got %+v", got)
	}

	// A nil hook (the default) must be harmless.
	c.OnRateLimit = nil
	if _, err := c.GetIssue(context.Background(), "ENG-200"); err != nil {
		t.Fatalf("nil hook: %v", err)
	}
}
