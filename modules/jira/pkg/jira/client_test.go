package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testClient(srv *httptest.Server) *Client {
	c := NewClient(srv.URL, "user@example.com", "tok")
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c
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
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Done"},"issuetype":{"name":"Task"},"labels":[],"comment":{"comments":[{"author":{"displayName":"C"},"created":"2026-01-03T00:00:00.000+0000","body":"a note"}]}},"changelog":{"histories":[{"author":{"displayName":"H"},"created":"2026-01-02T00:00:00.000+0000","items":[{"field":"status","fromString":"Open","toString":"Done"}]}]}}],"nextPageToken":"more"}`))
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
		_ = json.NewDecoder(r.Body).Decode(&body)
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
