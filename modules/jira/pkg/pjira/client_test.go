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

func strPtr(s string) *string { return &s }

// assertDuedate compares a nullable duedate against the expected pointer,
// treating nil/nil as equal without dereferencing either side.
func assertDuedate(t *testing.T, got, want *string) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("Duedate = %q, want nil", *got)
	case want != nil && got == nil:
		t.Errorf("Duedate = nil, want %q", *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("Duedate = %q, want %q", *got, *want)
	}
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

// TestGetIssue_duedate pins the nullable duedate mapping for GetIssue: present
// when Jira sets it, and null/absent (never an error) when Jira has no due
// date on the issue. Daily Focus v2 ranking key 1 ("due urgency") depends on
// this field having a stable shape across both duedate-set and duedate-unset
// issues.
func TestGetIssue_duedate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *string
	}{
		{
			name: "due date set",
			body: `{"key":"ENG-1","fields":{"summary":"Fix","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[],"duedate":"2026-09-15"}}`,
			want: strPtr("2026-09-15"),
		},
		{
			name: "due date explicitly null",
			body: `{"key":"ENG-1","fields":{"summary":"Fix","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[],"duedate":null}}`,
			want: nil,
		},
		{
			name: "due date field absent entirely",
			body: `{"key":"ENG-1","fields":{"summary":"Fix","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}`,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.RawQuery, "duedate") {
					t.Errorf("GetIssue fields query must include duedate, got %q", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			got, err := testClient(srv).GetIssue(context.Background(), "ENG-1")
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			assertDuedate(t, got.Duedate, c.want)
		})
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

// TestSearchPage_duedate is SearchPage's counterpart to TestGetIssue_duedate:
// the same nullable-duedate contract must hold through the search field list
// and item mapping, not just GetIssue.
func TestSearchPage_duedate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *string
	}{
		{
			name: "due date set",
			body: `{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[],"duedate":"2026-09-15"}}],"isLast":true}`,
			want: strPtr("2026-09-15"),
		},
		{
			name: "due date explicitly null",
			body: `{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[],"duedate":null}}],"isLast":true}`,
			want: nil,
		},
		{
			name: "due date field absent entirely",
			body: `{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"isLast":true}`,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody map[string]any
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				fields, _ := reqBody["fields"].([]any)
				found := false
				for _, f := range fields {
					if f == "duedate" {
						found = true
					}
				}
				if !found {
					t.Errorf("SearchPage fields list must include duedate, got %v", fields)
				}
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			got, err := testClient(srv).SearchPage(context.Background(), "project = ENG", 100, ExpandOpts{}, "")
			if err != nil {
				t.Fatalf("SearchPage: %v", err)
			}
			if len(got.Items) != 1 {
				t.Fatalf("items: %+v", got.Items)
			}
			assertDuedate(t, got.Items[0].Duedate, c.want)
		})
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

// TestCreateIssue_success pins the request shape (POST /rest/api/3/issue with
// fields.project.key/fields.issuetype.name/fields.summary, plus a description
// encoded to ADF only when non-empty) and the mapped result (key + browse URL,
// mirroring Issue.URL's derivation).
func TestCreateIssue_success(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:tok"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Errorf("auth = %q want %q", r.Header.Get("Authorization"), wantAuth)
		}
		var body struct {
			Fields struct {
				Project     struct{ Key string }
				IssueType   struct{ Name string } `json:"issuetype"`
				Summary     string
				Description map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Fields.Project.Key != "ENG" || body.Fields.IssueType.Name != "Bug" || body.Fields.Summary != "Fix it" {
			t.Errorf("bad request fields: %+v", body.Fields)
		}
		if body.Fields.Description == nil || body.Fields.Description["type"] != "doc" {
			t.Errorf("description not encoded as ADF: %+v", body.Fields.Description)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10000","key":"ENG-100","self":"https://example.atlassian.net/rest/api/3/issue/10000"}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).CreateIssue(context.Background(), CreateIssueRequest{
		Project: "ENG", IssueType: "Bug", Summary: "Fix it", Description: "some detail",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if got.Key != "ENG-100" {
		t.Errorf("Key = %q, want ENG-100", got.Key)
	}
	if got.URL != srv.URL+"/browse/ENG-100" {
		t.Errorf("URL = %q, want %q", got.URL, srv.URL+"/browse/ENG-100")
	}
}

// TestCreateIssue_omitsEmptyDescription pins that a blank description is
// dropped from the request entirely rather than sent as an empty ADF doc.
func TestCreateIssue_omitsEmptyDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		fields, _ := body["fields"].(map[string]any)
		if _, present := fields["description"]; present {
			t.Errorf("description must be omitted when blank, got fields=%v", fields)
		}
		_, _ = w.Write([]byte(`{"key":"ENG-101"}`))
	}))
	defer srv.Close()
	if _, err := testClient(srv).CreateIssue(context.Background(), CreateIssueRequest{
		Project: "ENG", IssueType: "Bug", Summary: "Fix it",
	}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
}

// TestCreateIssue_emptyRequiredFields mirrors GetIssue's/Search's empty-input
// guard: a blank project/issue-type/summary must fail locally, without
// reaching the tenant.
func TestCreateIssue_emptyRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		req  CreateIssueRequest
	}{
		{"empty project", CreateIssueRequest{IssueType: "Bug", Summary: "S"}},
		{"empty issue type", CreateIssueRequest{Project: "ENG", Summary: "S"}},
		{"empty summary", CreateIssueRequest{Project: "ENG", IssueType: "Bug"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not contact the tenant with a blank required field")
			}))
			defer srv.Close()
			if _, err := testClient(srv).CreateIssue(context.Background(), c.req); err == nil {
				t.Fatal("want an error on a blank required field")
			}
		})
	}
}

// TestCreateIssue_validationErrorSurfaced pins that Jira's own validation
// error body (an invalid project/issue-type) is decoded and surfaced in the
// returned error, not swallowed behind a bare status code.
func TestCreateIssue_validationErrorSurfaced(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{
			name:    "errorMessages",
			body:    `{"errorMessages":["project key is invalid"],"errors":{}}`,
			wantMsg: "project key is invalid",
		},
		{
			name:    "field errors map",
			body:    `{"errorMessages":[],"errors":{"issuetype":"valid issue type is required"}}`,
			wantMsg: "valid issue type is required",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			_, err := testClient(srv).CreateIssue(context.Background(), CreateIssueRequest{
				Project: "NOPE", IssueType: "Bogus", Summary: "S",
			})
			if err == nil {
				t.Fatal("want an error on a validation failure")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error = %v, want it to mention %q", err, c.wantMsg)
			}
		})
	}
}

// TestCreateIssue_unauthenticated pins the 401 classification, distinct from
// the generic non-2xx path (mirroring GetIssue's 404 special-case).
func TestCreateIssue_unauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := testClient(srv).CreateIssue(context.Background(), CreateIssueRequest{
		Project: "ENG", IssueType: "Bug", Summary: "S",
	})
	if err == nil {
		t.Fatal("want an error on 401")
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error = %v, want it to mention unauthenticated", err)
	}
}

// TestCreateIssue_unavailableNetworkError pins that a transport failure (the
// tenant unreachable) is returned as an error, not swallowed into a nil
// result/nil error pair.
func TestCreateIssue_unavailableNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // make the endpoint unreachable (connection refused)
	c := NewClient(url, "user@example.com", "tok")
	c.HTTP = &http.Client{Timeout: 2 * time.Second}
	got, err := c.CreateIssue(context.Background(), CreateIssueRequest{
		Project: "ENG", IssueType: "Bug", Summary: "S",
	})
	if err == nil {
		t.Fatal("want a non-nil transport error")
	}
	if got != nil {
		t.Errorf("no partial result may accompany the error: %+v", got)
	}
}

// TestTransition_success pins the two-step request shape (GET
// .../transitions to resolve the target state to an id, then POST the chosen
// id) and the mapped result (key + the requested target state). The target
// state is deliberately passed in a different case ("done" vs the tenant's
// "Done") to pin the case-insensitive match.
func TestTransition_success(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:tok"))
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/ENG-1/transitions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Errorf("auth = %q want %q", r.Header.Get("Authorization"), wantAuth)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"transitions":[{"id":"11","to":{"name":"In Progress"}},{"id":"31","to":{"name":"Done"}}]}`))
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()
	got, err := testClient(srv).Transition(context.Background(), "ENG-1", "done")
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if got.Key != "ENG-1" || got.To != "done" {
		t.Errorf("result = %+v", got)
	}
	transition, _ := posted["transition"].(map[string]any)
	if transition["id"] != "31" {
		t.Errorf("posted transition id = %v, want 31 (the id whose to.name case-insensitively matches %q)", transition["id"], "done")
	}
}

// TestTransition_unknownTargetState pins that a target state matching no
// available transition is a DISTINCT, clearly-classified error — never the
// issue's own "not found" classification — and that the POST step is never
// reached once resolution fails.
func TestTransition_unknownTargetState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("must not POST when no transition matches, got %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"transitions":[{"id":"11","to":{"name":"In Progress"}}]}`))
	}))
	defer srv.Close()
	_, err := testClient(srv).Transition(context.Background(), "ENG-1", "Nonexistent State")
	if err == nil {
		t.Fatal("want an error when no transition matches the target state")
	}
	if !strings.Contains(err.Error(), "no transition to state") {
		t.Errorf("error = %v, want it to mention the unmatched state", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, must not be classified as issue-not-found", err)
	}
}

// TestTransition_issueNotFound pins the 404 classification on the initial
// GET .../transitions call, mirroring GetIssue's own 404 special-case.
func TestTransition_issueNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := testClient(srv).Transition(context.Background(), "NOPE-1", "Done")
	if err == nil {
		t.Fatal("want an error on 404")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to mention not found", err)
	}
}

// TestTransition_unauthenticated pins the 401 classification, distinct from
// the generic non-2xx path (mirroring CreateIssue's own 401 special-case).
func TestTransition_unauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := testClient(srv).Transition(context.Background(), "ENG-1", "Done")
	if err == nil {
		t.Fatal("want an error on 401")
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error = %v, want it to mention unauthenticated", err)
	}
}

// TestTransition_postValidationErrorSurfaced pins that Jira's own validation
// error body on the POST step (e.g. a transition rejected by the workflow) is
// decoded via decodeJiraError and surfaced in the returned error, not
// swallowed behind a bare status code — mirroring CreateIssue's reuse of the
// same helper.
func TestTransition_postValidationErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"transitions":[{"id":"31","to":{"name":"Done"}}]}`))
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorMessages":["Transition is not allowed"],"errors":{}}`))
		}
	}))
	defer srv.Close()
	_, err := testClient(srv).Transition(context.Background(), "ENG-1", "Done")
	if err == nil {
		t.Fatal("want an error on a validation failure")
	}
	if !strings.Contains(err.Error(), "Transition is not allowed") {
		t.Errorf("error = %v, want it to mention the Jira validation message", err)
	}
}

// TestTransition_unavailableNetworkError pins that a transport failure (the
// tenant unreachable) is returned as an error, not swallowed into a nil
// result/nil error pair.
func TestTransition_unavailableNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // make the endpoint unreachable (connection refused)
	c := NewClient(url, "user@example.com", "tok")
	c.HTTP = &http.Client{Timeout: 2 * time.Second}
	got, err := c.Transition(context.Background(), "ENG-1", "Done")
	if err == nil {
		t.Fatal("want a non-nil transport error")
	}
	if got != nil {
		t.Errorf("no partial result may accompany the error: %+v", got)
	}
}

// TestTransition_emptyRequiredFields mirrors CreateIssue's empty-input guard:
// a blank key/target-state must fail locally, without reaching the tenant.
func TestTransition_emptyRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		key  string
		to   string
	}{
		{"empty key", "", "Done"},
		{"empty target state", "ENG-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not contact the tenant with a blank required field")
			}))
			defer srv.Close()
			if _, err := testClient(srv).Transition(context.Background(), c.key, c.to); err == nil {
				t.Fatal("want an error on a blank required field")
			}
		})
	}
}

// TestAddComment_success pins the request shape (POST
// /rest/api/3/issue/{key}/comment with the plain-text body encoded to ADF,
// mirroring CreateIssue's description handling) and the mapped result (issue
// key + the new comment's Jira id).
func TestAddComment_success(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:tok"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/ENG-1/comment" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Errorf("auth = %q want %q", r.Header.Get("Authorization"), wantAuth)
		}
		var body struct {
			Body map[string]any `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Body == nil || body.Body["type"] != "doc" {
			t.Errorf("comment body not encoded as ADF: %+v", body.Body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10050","self":"https://example.atlassian.net/rest/api/3/issue/10000/comment/10050"}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).AddComment(context.Background(), "ENG-1", "looks good")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if got.Key != "ENG-1" || got.ID != "10050" {
		t.Errorf("result = %+v, want {Key:ENG-1 ID:10050}", got)
	}
}

// TestAddComment_issueNotFound pins the 404 classification, mirroring
// GetIssue's/Transition's own 404 special-case.
func TestAddComment_issueNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := testClient(srv).AddComment(context.Background(), "NOPE-1", "hi")
	if err == nil {
		t.Fatal("want an error on 404")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to mention not found", err)
	}
}

// TestAddComment_unauthenticated pins the 401 classification, distinct from
// the generic non-2xx path (mirroring CreateIssue's/Transition's own 401
// special-case).
func TestAddComment_unauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := testClient(srv).AddComment(context.Background(), "ENG-1", "hi")
	if err == nil {
		t.Fatal("want an error on 401")
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error = %v, want it to mention unauthenticated", err)
	}
}

// TestAddComment_validationErrorSurfaced pins that Jira's own validation
// error body is decoded via decodeJiraError and surfaced in the returned
// error, mirroring CreateIssue's/Transition's reuse of the same helper.
func TestAddComment_validationErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorMessages":["comment body is invalid"],"errors":{}}`))
	}))
	defer srv.Close()
	_, err := testClient(srv).AddComment(context.Background(), "ENG-1", "hi")
	if err == nil {
		t.Fatal("want an error on a validation failure")
	}
	if !strings.Contains(err.Error(), "comment body is invalid") {
		t.Errorf("error = %v, want it to mention the Jira validation message", err)
	}
}

// TestAddComment_unavailableNetworkError pins that a transport failure (the
// tenant unreachable) is returned as an error, not swallowed into a nil
// result/nil error pair.
func TestAddComment_unavailableNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // make the endpoint unreachable (connection refused)
	c := NewClient(url, "user@example.com", "tok")
	c.HTTP = &http.Client{Timeout: 2 * time.Second}
	got, err := c.AddComment(context.Background(), "ENG-1", "hi")
	if err == nil {
		t.Fatal("want a non-nil transport error")
	}
	if got != nil {
		t.Errorf("no partial result may accompany the error: %+v", got)
	}
}

// TestAddComment_emptyRequiredFields mirrors CreateIssue's/Transition's
// empty-input guard: a blank key/body must fail locally, without reaching
// the tenant.
func TestAddComment_emptyRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		key  string
		body string
	}{
		{"empty key", "", "hi"},
		{"empty body", "ENG-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not contact the tenant with a blank required field")
			}))
			defer srv.Close()
			if _, err := testClient(srv).AddComment(context.Background(), c.key, c.body); err == nil {
				t.Fatal("want an error on a blank required field")
			}
		})
	}
}

// TestClient_requestConstructionErrorsSurface pins the request-construction error
// paths of all six endpoints. An unparseable BaseURL fails inside
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

	if got, err := c.CreateIssue(context.Background(), CreateIssueRequest{Project: "ENG", IssueType: "Bug", Summary: "S"}); err == nil {
		t.Errorf("CreateIssue: want a request-construction error, got result %+v", got)
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("CreateIssue error = %v, want it to mention %q", err, wantMsg)
	}

	if got, err := c.SearchPage(context.Background(), "project = ENG", 10, ExpandOpts{}, ""); err == nil {
		t.Errorf("SearchPage: want a request-construction error, got result %+v", got)
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("SearchPage error = %v, want it to mention %q", err, wantMsg)
	}

	if got, err := c.Transition(context.Background(), "ENG-1", "Done"); err == nil {
		t.Errorf("Transition: want a request-construction error, got result %+v", got)
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("Transition error = %v, want it to mention %q", err, wantMsg)
	}

	if got, err := c.AddComment(context.Background(), "ENG-1", "hi"); err == nil {
		t.Errorf("AddComment: want a request-construction error, got result %+v", got)
	} else if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("AddComment error = %v, want it to mention %q", err, wantMsg)
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
