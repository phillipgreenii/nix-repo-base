package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runCLI(t *testing.T, baseURL string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", baseURL)
	t.Setenv("JIRA_EMAIL", "u@x")
	t.Setenv("JIRA_API_TOKEN", "tok")
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func runCLIOutErr(t *testing.T, baseURL string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", baseURL)
	t.Setenv("JIRA_EMAIL", "u@x")
	t.Setenv("JIRA_API_TOKEN", "tok")
	cmd := NewRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func paginatedSearchServerCLI(t *testing.T) *httptest.Server {
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
		}
	}))
}

func TestCLI_Issue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}`))
	}))
	defer srv.Close()
	out, err := runCLI(t, srv.URL, "issue", "ENG-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got["key"] != "ENG-1" {
		t.Errorf("key = %v", got["key"])
	}
}

func TestCLI_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"isLast":true}`))
	}))
	defer srv.Close()
	out, err := runCLI(t, srv.URL, "search", "--jql", "project = ENG")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Items) != 1 || got.Truncated {
		t.Errorf("bad envelope: %+v", got)
	}
}

func TestCLI_SearchAll_aggregatesPages(t *testing.T) {
	srv := paginatedSearchServerCLI(t)
	defer srv.Close()
	out, err := runCLI(t, srv.URL, "search", "--jql", "project = ENG", "--all")
	if err != nil {
		t.Fatalf("search --all: %v", err)
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
		Next      string           `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Items) != 3 || got.Truncated || got.Next != "" {
		t.Errorf("bad aggregated envelope: %+v", got)
	}
}

func TestCLI_SearchCursorEmitsNextToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p2"}`))
	}))
	defer srv.Close()
	out, err := runCLI(t, srv.URL, "search", "--jql", "project = ENG", "--cursor", "p1")
	if err != nil {
		t.Fatalf("search --cursor: %v", err)
	}
	var got struct {
		Truncated bool   `json:"truncated"`
		Next      string `json:"next_page_token"`
	}
	_ = json.Unmarshal([]byte(out), &got)
	if !got.Truncated || got.Next != "p2" {
		t.Errorf("cursor page must surface next_page_token=p2, truncated=true: %+v", got)
	}
}

func TestCLI_SearchCursorAndAllConflict(t *testing.T) {
	out, err := runCLI(t, "http://unused", "search", "--jql", "project = ENG", "--cursor", "p1", "--all")
	if err == nil {
		t.Fatal("want error when --cursor and --all are combined")
	}
	if out != "" {
		t.Errorf("no envelope must be written on usage error, got: %s", out)
	}
}

func TestCLI_SearchAll_capHitWarnsAndSucceeds(t *testing.T) {
	srv := paginatedSearchServerCLI(t)
	defer srv.Close()
	out, errOut, err := runCLIOutErr(t, srv.URL, "search", "--jql", "project = ENG", "--all", "--max-pages", "2")
	if err != nil {
		t.Fatalf("cap-hit must exit 0, got err: %v", err)
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
	}
	_ = json.Unmarshal([]byte(out), &got)
	if len(got.Items) != 2 || !got.Truncated {
		t.Errorf("cap-hit envelope must be partial+truncated: %+v", got)
	}
	if !strings.Contains(errOut, "truncated") {
		t.Errorf("expected a stderr truncation warning, got: %q", errOut)
	}
}
