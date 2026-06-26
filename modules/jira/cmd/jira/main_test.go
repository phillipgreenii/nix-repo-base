package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
