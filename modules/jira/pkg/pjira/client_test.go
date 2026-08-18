package pjira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(srv *httptest.Server) *Client {
	c := NewClient(srv.URL, "user@example.com", "tok")
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c
}

// TestRawUser_toUser_nilVsEmpty pins the nil-vs-empty collapse. Atlassian returns
// a present-but-empty user object in places (an unassigned issue), and toUser is
// what folds that back to a nil *User. Because the guard is an && chain over all
// three fields, each SINGLE-field-populated row is separately load-bearing: flip
// the chain to || and only those rows notice.
func TestRawUser_toUser_nilVsEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   *rawUser
		want *User // nil means "must collapse to a nil *User"
	}{
		{"nil pointer", nil, nil},
		{"present but every field empty", &rawUser{}, nil},
		{"email only", &rawUser{EmailAddress: "e@x"}, &User{Email: "e@x"}},
		{"accountID only", &rawUser{AccountID: "a1"}, &User{AccountID: "a1"}},
		{"displayName only", &rawUser{DisplayName: "D"}, &User{DisplayName: "D"}},
		{
			"all three",
			&rawUser{EmailAddress: "e@x", AccountID: "a1", DisplayName: "D"},
			&User{Email: "e@x", AccountID: "a1", DisplayName: "D"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.toUser()
			if c.want == nil {
				if got != nil {
					t.Fatalf("toUser() = %+v, want a nil *User", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("toUser() = nil, want %+v", *c.want)
			}
			if *got != *c.want {
				t.Errorf("toUser() = %+v, want %+v", *got, *c.want)
			}
		})
	}
}

// TestRawUser_toUserOrEmpty_alwaysNonNil pins the opposite convention for
// changelog/comment authors, which are VALUES: every input — nil, empty, or
// populated — must yield a non-nil *User, and a populated one must map through
// rather than be flattened to the empty User.
func TestRawUser_toUserOrEmpty_alwaysNonNil(t *testing.T) {
	if got := (*rawUser)(nil).toUserOrEmpty(); got == nil || *got != (User{}) {
		t.Errorf("nil author must map to an empty non-nil User, got %+v", got)
	}
	if got := (&rawUser{}).toUserOrEmpty(); got == nil || *got != (User{}) {
		t.Errorf("empty author must map to an empty non-nil User, got %+v", got)
	}
	got := (&rawUser{DisplayName: "H"}).toUserOrEmpty()
	if got == nil {
		t.Fatal("populated author must not map to nil")
	}
	if got.DisplayName != "H" {
		t.Errorf("populated author must map through, got %+v", *got)
	}
}

func TestGetIssue_mapsFieldsAndAuth(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:tok"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/ENG-1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Errorf("auth = %q want %q", r.Header.Get("Authorization"), wantAuth)
		}
		_, _ = w.Write([]byte(`{"key":"ENG-1","fields":{"summary":"Fix","status":{"name":"In Progress"},"issuetype":{"name":"Bug"},"labels":["x"],"priority":{"name":"High"},"project":{"key":"ENG"},"created":"2026-01-01T00:00:00.000+0000","updated":"2026-01-02T00:00:00.000+0000","reporter":{"emailAddress":"r@x","accountId":"a1","displayName":"R"},"assignee":{"emailAddress":"a@x","accountId":"a2","displayName":"A"}}}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).GetIssue(context.Background(), "ENG-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Key != "ENG-1" || got.Summary != "Fix" || got.Status != "In Progress" || got.IssueType != "Bug" || got.Priority != "High" || got.Project != "ENG" {
		t.Errorf("bad mapping: %+v", got)
	}
	if got.Reporter == nil || got.Reporter.DisplayName != "R" || got.Assignee == nil || got.Assignee.Email != "a@x" {
		t.Errorf("bad people mapping: %+v", got)
	}
	if got.URL != srv.URL+"/browse/ENG-1" {
		t.Errorf("url = %s", got.URL)
	}
}

func TestGetIssue_notFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer srv.Close()
	if _, err := testClient(srv).GetIssue(context.Background(), "NOPE-1"); err == nil {
		t.Fatal("want error on 404")
	}
}

func TestSearch_mapsItemsExpandAndTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Done"},"issuetype":{"name":"Task"},"labels":[],"comment":{"comments":[{"id":"c-501","author":{"displayName":"C"},"created":"2026-01-03T00:00:00.000+0000","body":"a note"}]}},"changelog":{"histories":[{"id":"h-900","author":{"displayName":"H"},"created":"2026-01-02T00:00:00.000+0000","items":[{"field":"status","fromString":"Open","toString":"Done"}]}]}}],"nextPageToken":"more"}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).Search(context.Background(), "project = ENG", 100, ExpandOpts{Changelog: true, Comments: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !got.Truncated {
		t.Error("nextPageToken present => truncated must be true")
	}
	if len(got.Items) != 1 || got.Items[0].Key != "ENG-1" {
		t.Fatalf("items: %+v", got.Items)
	}
	if len(got.Items[0].Changelog) != 1 || got.Items[0].Changelog[0].To != "Done" {
		t.Errorf("changelog: %+v", got.Items[0].Changelog)
	}
	if len(got.Items[0].Comments) != 1 || got.Items[0].Comments[0].Body != "a note" {
		t.Errorf("comments: %+v", got.Items[0].Comments)
	}
	// Stable IDs (Jira changelog-history id / comment id) are carried through so
	// downstream consumers (activity-collector SP6) can build collision-free
	// per-event ExternalIDs.
	if got.Items[0].Changelog[0].ID != "h-900" {
		t.Errorf("changelog id not mapped: %+v", got.Items[0].Changelog)
	}
	if got.Items[0].Comments[0].ID != "c-501" {
		t.Errorf("comment id not mapped: %+v", got.Items[0].Comments)
	}
}

func TestSearch_emptyJQLErrors(t *testing.T) {
	if _, err := NewClient("http://x", "e", "t").Search(context.Background(), "  ", 100, ExpandOpts{}); err == nil {
		t.Fatal("want error on empty jql")
	}
}

func TestAuthStatus_mapsHTTP(t *testing.T) {
	cases := []struct {
		code int
		want AuthState
	}{
		{200, AuthOK}, {401, AuthUnauthenticated}, {403, AuthForbidden}, {500, AuthError},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/rest/api/3/myself" {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.WriteHeader(c.code)
		}))
		got, err := testClient(srv).AuthStatus(context.Background())
		srv.Close()
		if err != nil {
			t.Fatalf("AuthStatus(%d): %v", c.code, err)
		}
		if got != c.want {
			t.Errorf("AuthStatus(%d) = %s, want %s", c.code, got, c.want)
		}
	}
}

// TestAuthStatus_transportErrorReturned proves a transport failure yields
// AuthError AND a non-nil error, rather than the error being discarded (bead
// pg2-yfjm7).
func TestAuthStatus_transportErrorReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // make the endpoint unreachable (connection refused)
	c := NewClient(url, "user@example.com", "tok")
	c.HTTP = &http.Client{Timeout: 2 * time.Second}
	got, err := c.AuthStatus(context.Background())
	if got != AuthError {
		t.Errorf("state = %s, want %s", got, AuthError)
	}
	if err == nil {
		t.Error("want a non-nil transport error, got nil")
	}
}

func TestSearchPage_sendsTokenAndSurfacesNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["nextPageToken"] != "PAGE2" {
			t.Errorf("request nextPageToken = %v, want PAGE2", body["nextPageToken"])
		}
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-9","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"PAGE3"}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).SearchPage(context.Background(), "project = ENG", 100, ExpandOpts{}, "PAGE2")
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if got.NextPageToken != "PAGE3" {
		t.Errorf("NextPageToken = %q, want PAGE3", got.NextPageToken)
	}
	if !got.Truncated || len(got.Items) != 1 || got.Items[0].Key != "ENG-9" {
		t.Errorf("bad result: %+v", got)
	}
}

func TestSearch_firstPageOmitsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, present := body["nextPageToken"]; present {
			t.Errorf("Search() must NOT send nextPageToken on the first page; body=%v", body)
		}
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).Search(context.Background(), "project = ENG", 100, ExpandOpts{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Truncated || got.NextPageToken != "" {
		t.Errorf("complete page must be untruncated with empty token: %+v", got)
	}
}

func paginatedSearchServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["nextPageToken"] {
		case nil, "":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-2","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p3"}`))
		case "p3":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-3","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"isLast":true}`))
		default:
			t.Errorf("unexpected token %v", body["nextPageToken"])
			http.Error(w, "unexpected token", http.StatusBadRequest)
		}
	}))
}

func TestSearchAll_concatenatesAllPages(t *testing.T) {
	srv := paginatedSearchServer(t)
	defer srv.Close()
	got, err := testClient(srv).SearchAll(context.Background(), "project = ENG", 100, ExpandOpts{}, DefaultMaxSearchPages)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(got.Items) != 3 || got.Items[0].Key != "ENG-1" || got.Items[1].Key != "ENG-2" || got.Items[2].Key != "ENG-3" {
		t.Fatalf("items: %+v", got.Items)
	}
	if got.Truncated || got.NextPageToken != "" {
		t.Errorf("complete run must be untruncated with empty token: %+v", got)
	}
}

func TestSearchAll_respectsMaxPages(t *testing.T) {
	srv := paginatedSearchServer(t)
	defer srv.Close()
	got, err := testClient(srv).SearchAll(context.Background(), "project = ENG", 100, ExpandOpts{}, 2)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(got.Items) != 2 {
		t.Errorf("want 2 items at maxPages=2, got %d", len(got.Items))
	}
	if !got.Truncated || got.NextPageToken != "p3" {
		t.Errorf("cap-hit must be truncated with the next token p3: %+v", got)
	}
}

// TestSearchAll_propagatesPageError pins the per-page error propagation: a failing
// page must ABORT the loop and surface the error, never be swallowed into a
// silently short result set (which a caller would read as "that's all there is").
func TestSearchAll_propagatesPageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	got, err := testClient(srv).SearchAll(context.Background(), "project = ENG", 100, ExpandOpts{}, 3)
	if err == nil {
		t.Fatal("a failing page must abort SearchAll with an error")
	}
	if got != nil {
		t.Errorf("no partial result may accompany the error: %+v", got)
	}
}

// TestClient_requestConstructionErrorsSurface pins the request-construction error
// paths of all three endpoints. An unparseable BaseURL fails inside
// http.NewRequestWithContext, before any transport work, and that error must be
// RETURNED — swallowing it hands the caller a nil result with a nil error.
func TestClient_requestConstructionErrorsSurface(t *testing.T) {
	// U+007F is a control character, which net/url rejects while parsing.
	c := NewClient("http://bad\x7fhost.invalid", "u@x", "dummy-token")
	const wantMsg = "invalid control character in URL"

	if got, err := c.GetIssue(context.Background(), "ENG-1"); err == nil {
		t.Errorf("GetIssue: want a request-construction error, got issue %+v", got)
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("GetIssue error = %v, want it to mention %q", err, wantMsg)
	}

	if got, err := c.SearchPage(context.Background(), "project = ENG", 10, ExpandOpts{}, ""); err == nil {
		t.Errorf("SearchPage: want a request-construction error, got result %+v", got)
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("SearchPage error = %v, want it to mention %q", err, wantMsg)
	}

	state, err := c.AuthStatus(context.Background())
	if err == nil {
		t.Error("AuthStatus: want a request-construction error, got nil")
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("AuthStatus error = %v, want it to mention %q", err, wantMsg)
	}
	if state != AuthError {
		t.Errorf("AuthStatus state = %s, want %s", state, AuthError)
	}
}
