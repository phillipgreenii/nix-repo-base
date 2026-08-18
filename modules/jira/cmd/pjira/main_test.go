package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRootCommandIsPjira pins the renamed tool identity: the cobra root's Use
// is "pjira" (not the upstream go-jira "jira"), so `pjira --help` shows pjira.
func TestRootCommandIsPjira(t *testing.T) {
	if use := NewRootCmd().Use; use != "pjira" {
		t.Errorf("root Use = %q, want %q", use, "pjira")
	}
}

// TestDefaultConfigPathUnderPjira pins the config-dir rename: the default config
// resolves under a "pjira" directory (BREAKING move from the old "jira" dir).
func TestDefaultConfigPathUnderPjira(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := defaultConfigPath(), filepath.Join("/xdg", "pjira", "config.toml"); got != want {
		t.Errorf("defaultConfigPath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := defaultConfigPath(), filepath.Join("/home/u", ".config", "pjira", "config.toml"); got != want {
		t.Errorf("defaultConfigPath() home fallback = %q, want %q", got, want)
	}
}

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

// runCLIWithConfig runs the CLI against an EXPLICIT --config path, so a test can
// never read — or accidentally depend on — the operator's real
// $XDG_CONFIG_HOME/pjira/config.toml. Point cfgPath at an absent file to exercise
// the "no config file" case hermetically.
func runCLIWithConfig(t *testing.T, baseURL, cfgPath string, args ...string) (string, error) {
	t.Helper()
	return runCLI(t, baseURL, append(args, "--config", cfgPath)...)
}

// writeConfig writes a config TOML into a fresh temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
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
			http.Error(w, "unexpected token", http.StatusBadRequest)
		}
	}))
}

// TestCLI_AuthStatus_ExitCodes proves auth-status now maps credential states to
// exit codes via a returned exitCodeError (testable, cobra teardown runs)
// instead of an in-RunE os.Exit (bead pg2-yfjm7).
func TestCLI_AuthStatus_ExitCodes(t *testing.T) {
	cases := []struct {
		status    int
		wantCode  int // 0 == expect a nil error (exit 0)
		wantState string
	}{
		{http.StatusOK, 0, "OK"},
		{http.StatusForbidden, 4, "FORBIDDEN"},
		{http.StatusUnauthorized, 5, "UNAUTHENTICATED"},
		{http.StatusInternalServerError, 1, "ERROR"},
	}
	for _, c := range cases {
		t.Run(c.wantState, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
			}))
			defer srv.Close()
			out, err := runCLI(t, srv.URL, "auth-status")
			if !strings.Contains(out, c.wantState) {
				t.Errorf("stdout = %q, want state %q", out, c.wantState)
			}
			if c.wantCode == 0 {
				if err != nil {
					t.Errorf("want nil error (exit 0), got %v", err)
				}
				return
			}
			var ec exitCodeError
			if !errors.As(err, &ec) {
				t.Fatalf("want exitCodeError, got %T: %v", err, err)
			}
			if ec.code != c.wantCode {
				t.Errorf("exit code = %d, want %d", ec.code, c.wantCode)
			}
		})
	}
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
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
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

// TestCLI_AuthStatus_UnresolvableSecretReportsMissing pins the MISSING state and
// its exit code 3. Both rows matter, because the guard is
// `terr != nil || token == ""` and each row satisfies a DIFFERENT half: an absent
// token file fails with an error, whereas a blank one succeeds with an empty
// string. Either way the tenant must not be contacted — the fake server fails the
// test if it is.
func TestCLI_AuthStatus_UnresolvableSecretReportsMissing(t *testing.T) {
	cases := []struct {
		name string
		// write prepares any token file inside dir and returns the path of a
		// config TOML referencing it.
		write func(t *testing.T, dir string) string
	}{
		{
			name: "token file absent (Token returns an error)",
			write: func(t *testing.T, dir string) string {
				return writeConfig(t, "[secret]\nsource=\"file\"\npath=\""+filepath.Join(dir, "absent")+"\"\n")
			},
		},
		{
			name: "token file blank (Token succeeds with an empty token)",
			write: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "blank")
				if err := os.WriteFile(p, []byte("  \n\t\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return writeConfig(t, "[secret]\nsource=\"file\"\npath=\""+p+"\"\n")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfgPath := c.write(t, t.TempDir())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("auth-status must not contact the tenant when the secret is unresolvable")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			out, err := runCLIWithConfig(t, srv.URL, cfgPath, "auth-status")
			if !strings.Contains(out, "MISSING") {
				t.Errorf("stdout = %q, want MISSING", out)
			}
			var ec exitCodeError
			if !errors.As(err, &ec) {
				t.Fatalf("want exitCodeError, got %T: %v", err, err)
			}
			if ec.code != 3 {
				t.Errorf("exit code = %d, want 3", ec.code)
			}
		})
	}
}

type subcommand struct {
	name string
	args []string
}

// subcommands are the three user-facing commands. Each resolves config and a
// secret through the SAME helpers, but each carries its OWN copy of the
// error-propagation plumbing, so a startup failure has to be pinned per command
// rather than once.
func subcommands() []subcommand {
	return []subcommand{
		{"issue", []string{"issue", "ENG-1"}},
		{"search", []string{"search", "--jql", "project = ENG"}},
		{"auth-status", []string{"auth-status"}},
	}
}

// assertStartupFailure requires each named subcommand (all of them when none are
// named) to abort with an error mentioning wantMsg and to write nothing to
// stdout. Asserting the MESSAGE, not merely a non-nil error, is what stops a
// swallowed diagnosis from being masked by a later unrelated failure such as an
// unreachable host.
func assertStartupFailure(t *testing.T, baseURL, cfgPath, wantMsg string, only ...string) {
	t.Helper()
	for _, sc := range subcommands() {
		if len(only) > 0 && !slices.Contains(only, sc.name) {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			out, err := runCLIWithConfig(t, baseURL, cfgPath, sc.args...)
			if err == nil {
				t.Fatalf("want a startup failure mentioning %q, got success with stdout %q", wantMsg, out)
			}
			if !strings.Contains(err.Error(), wantMsg) {
				t.Errorf("error = %v, want it to mention %q", err, wantMsg)
			}
			if out != "" {
				t.Errorf("nothing may be written to stdout on a startup failure, got: %s", out)
			}
		})
	}
}

// TestCLI_SecretGuardErrorPropagates pins that a secret-source misconfiguration
// aborts the command and reaches the user, rather than being swallowed into a
// silent success or a request sent with an empty credential.
func TestCLI_SecretGuardErrorPropagates(t *testing.T) {
	cfgPath := writeConfig(t, "[secret]\nsource=\"file\"\n") // source=file, no path
	assertStartupFailure(t, "http://unused.invalid", cfgPath, "source=file requires path")
}

// TestCLI_MalformedConfigFilePropagates pins the LoadFile failure path: a config
// file that does not parse must abort the command, not degrade to a zero Config.
func TestCLI_MalformedConfigFilePropagates(t *testing.T) {
	cfgPath := writeConfig(t, "base_url = \"unterminated\n")
	assertStartupFailure(t, "http://unused.invalid", cfgPath, "toml")
}

// TestCLI_MissingBaseURLPropagates pins the other resolveConfig guard: with no
// config file and no JIRA_BASE_URL, every subcommand must say so.
func TestCLI_MissingBaseURLPropagates(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.toml")
	assertStartupFailure(t, "", absent, "base_url not configured")
}

// TestCLI_UnreadableTokenFilePropagates pins the Token()-STAGE failure, a
// different return site from the construction-stage one above: source=file with a
// path accepts the config and only fails when the token is actually read. issue
// and search must abort rather than proceed with no credential; auth-status is
// excluded because its contract for an unresolvable secret is MISSING + exit 3,
// pinned separately.
func TestCLI_UnreadableTokenFilePropagates(t *testing.T) {
	absentToken := filepath.Join(t.TempDir(), "absent")
	cfgPath := writeConfig(t, "[secret]\nsource=\"file\"\npath=\""+absentToken+"\"\n")
	assertStartupFailure(t, "http://unused.invalid", cfgPath, "read token file", "issue", "search")
}

// TestCLI_TenantErrorPropagates pins the fetch-time error paths, which are
// separate return sites from the startup ones above: an upstream failure must
// abort the command rather than emit an empty-but-successful envelope that a
// downstream consumer would read as "no results".
func TestCLI_TenantErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	absent := filepath.Join(t.TempDir(), "absent.toml")
	cases := []struct {
		name string
		args []string
	}{
		{"issue", []string{"issue", "ENG-1"}},
		{"search single page", []string{"search", "--jql", "project = ENG"}},
		{"search --all", []string{"search", "--jql", "project = ENG", "--all"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLIWithConfig(t, srv.URL, absent, c.args...)
			if err == nil {
				t.Fatalf("an upstream failure must fail the command, got stdout %q", out)
			}
			if !strings.Contains(err.Error(), "status 500") {
				t.Errorf("error = %v, want it to mention the upstream status", err)
			}
			if out != "" {
				t.Errorf("nothing may be written to stdout when the fetch failed: %s", out)
			}
		})
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
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Items) != 2 || !got.Truncated {
		t.Errorf("cap-hit envelope must be partial+truncated: %+v", got)
	}
	if !strings.Contains(errOut, "truncated") {
		t.Errorf("expected a stderr truncation warning, got: %q", errOut)
	}
}
