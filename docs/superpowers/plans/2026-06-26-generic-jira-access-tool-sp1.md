# SP1 — Generic Jira Access Tool (Foundation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a generic, tenant-agnostic Jira library (`pkg/jira`) + CLI (`cmd/jira`) in `phillipg-nix-repo-base/modules/jira/`, lifting and de-ZR'ing the existing `pg-pr-issues-jira-zr` core.

**Architecture:** A pure Go library does all Jira work (basic-auth `net/http` client, normalized model, ADF flattening, pluggable credential resolution) with **zero** ZR strings, OS-specific commands, or `pg-pr` imports. A thin cobra CLI exposes `issue`/`search`/`auth-status` over the library. Config is layered (defaults → file → env → flags) via cobra + `pelletier/go-toml/v2`. Secrets resolve from `env`/`file`/`command` sources; OS-keychain access is reached only via a config-supplied `command` argv (no CGO, no build tags).

**Tech Stack:** Go 1.25, `spf13/cobra`, `pelletier/go-toml/v2`, `net/http`, `net/http/httptest`; built with repo-base `mkGoBinary` (gomod2nix Pattern A).

**Spec:** `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` (Bead `pg2-2x2d.1`).

## Global Constraints

- **Location**: `phillipg-nix-repo-base/modules/jira/`, mirroring `modules/pn/`. Go module path `github.com/phillipgreenii/nix-repo-base/modules/jira`.
- **No viper**: config via `spf13/cobra` (flags) + `pelletier/go-toml/v2` (file) + a small precedence merger.
- **Generic core invariant**: `pkg/jira` and `cmd/jira` MUST contain no ZR string (`ziprecruiter`, `zr-jira`), no OS-specific command name (`security`, `secret-tool`), and MUST import no `pg-pr` package. Enforced by a test gate (Task 10).
- **Auth**: HTTP basic auth only (`Authorization: Basic base64(email:token)`). No Bearer/PAT.
- **Endpoints** (owned in one place): `GET /rest/api/3/issue/<key>`; `POST /rest/api/3/search/jql` (never the 410'd `/search`); `GET /rest/api/3/myself`.
- **Truncation** is authoritative via `nextPageToken`/`isLast`, never a count.
- **Build**: `mkGoBinary` + a committed git-tracked `gomod2nix.toml` (Pattern A: `src = ./.`, no `modRoot`). Regenerate the toml with `go mod tidy && nix run github:nix-community/gomod2nix -- generate` when deps change.
- **Reuse-first**: lift the client logic from `phillipg-nix-ziprecruiter/modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go` (already implements the envelope, `/search/jql`, truncation, basic auth, `GET /issue/<id>`); lift the ADF walker from `phillipgreenii-nix-support-apps/packages/activity-collector/internal/collector/jira/jira.go`.
- **SP1 acceptance is unit/contract-level only** (no live tenant); the tool ships only generic defaults (`secret.source = env`).
- **Validation before "done"**: `cd modules/jira && go test ./...` green; `gofmt -l modules/jira` clean (repo-base `treefmt` has NO Go formatter — Go formatting is not gate-enforced, so run `gofmt -l` manually); repo-base pre-commit (`prek run --all-files`) green; `nix flake check` and `pn workspace build` green.
- **Commits**: this work is on branch `generic-jira-access-tool` in repo-base. Do not push.

## File Structure

```text
modules/jira/
  go.mod                     # module github.com/phillipgreenii/nix-repo-base/modules/jira; cobra + go-toml/v2
  go.sum                     # generated
  gomod2nix.toml             # git-tracked; generated via gomod2nix
  default.nix                # mkGoBinary build (mirrors modules/pn/default.nix)
  README.md                  # docs + "TAG: extract to dedicated repo" note
  pkg/jira/
    model.go                 # Issue, SearchResult, User, ChangelogEntry, Comment, AuthState
    model_test.go
    adf.go                   # FlattenADF (lifted+extended from activity-collector)
    adf_test.go
    client.go                # Client: NewClient, GetIssue, Search, AuthStatus
    client_test.go
    secret.go                # SecretSource (env/file/command) + Runner + NewSecretSource
    secret_test.go
    config.go                # Config, SecretConfig, DefaultConfig, LoadFile, Merge
    config_test.go
    guardrails_test.go       # generic-core invariant gate
  cmd/jira/
    main.go                  # cobra root + issue/search/auth-status subcommands
    main_test.go
home/jira/default.nix        # home-manager module (installs pkgs.jira)
```

Flake registration in `flake.nix` (Task 1): a `jira` package, a `jira-go-tests` check, `homeModules.jira`, and the overlay surface — each mirroring the existing `pn` lines (94, 161, 227, ~242).

---

### Task 1: Buildable module skeleton

**Files:**

- Create: `modules/jira/go.mod`, `modules/jira/cmd/jira/main.go`, `modules/jira/cmd/jira/main_test.go`, `modules/jira/default.nix`, `modules/jira/README.md`, `home/jira/default.nix`
- Generate: `modules/jira/go.sum`, `modules/jira/gomod2nix.toml`
- Modify: `flake.nix` (add package, check, homeModule, overlay surface)

**Interfaces:**

- Produces: a `jira` cobra root command (`cmd/jira.NewRootCmd() *cobra.Command`) that later tasks attach subcommands to; a buildable `nix build .#jira`.

- [ ] **Step 1: Write the failing test**

Create `modules/jira/cmd/jira/main_test.go`:

```go
package main

import (
	"bytes"
	"testing"
)

func TestRootCmd_Help(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("root --help: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("jira")) {
		t.Errorf("help output missing tool name; got:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./cmd/jira/`
Expected: FAIL — `undefined: NewRootCmd` (and no go.mod yet).

- [ ] **Step 3: Create go.mod**

Create `modules/jira/go.mod`:

```text
module github.com/phillipgreenii/nix-repo-base/modules/jira

go 1.25.9

require (
	github.com/pelletier/go-toml/v2 v2.3.1
	github.com/spf13/cobra v1.10.2
)
```

- [ ] **Step 4: Write minimal root command**

Create `modules/jira/cmd/jira/main.go`:

```go
// Command jira is a generic, tenant-agnostic Atlassian Jira access tool.
// It hard-codes no tenant, credential location, or OS-specific behavior;
// all of those are supplied as configuration (see modules/jira/README.md).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the jira CLI root. Subcommands are attached here.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "jira",
		Short:         "Generic Atlassian Jira access tool (issue / search / auth-status)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config TOML (default: $XDG_CONFIG_HOME/jira/config.toml)")
	return root
}

func main() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Tidy modules and generate gomod2nix.toml**

Run:

```bash
cd modules/jira && go mod tidy && nix run github:nix-community/gomod2nix -- generate
```

Expected: `go.sum` + `gomod2nix.toml` created (the toml will contain `cobra`, `go-toml/v2`, `pflag`, `mousetrap` — the same set as `modules/pn/gomod2nix.toml`).

- [ ] **Step 6: Run test to verify it passes**

Run: `cd modules/jira && go test ./cmd/jira/`
Expected: PASS.

- [ ] **Step 7: Create the nix build (mirror modules/pn/default.nix)**

Create `modules/jira/default.nix`:

```nix
# Build the jira binary via mkGoBuilders.
{
  pkgs,
  self,
}:

let
  goBuilders = (import ../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "jira";
  src = ./.;
  description = "Generic Atlassian Jira access tool (library + CLI)";
  gomod2nixToml = ./gomod2nix.toml;
}
```

- [ ] **Step 8: Create the home-manager module**

Create `home/jira/default.nix`:

```nix
# jira home-manager module — installs the generic Jira CLI (Go).
# The package is sourced from pkgs.jira via this flake's overlays.default.
{
  config,
  lib,
  pkgs,
  ...
}:
with lib;
let
  cfg = config.phillipgreenii.jira;
in
{
  options.phillipgreenii.jira = {
    enable = mkEnableOption "generic jira access CLI";
    package = mkPackageOption pkgs "jira" { };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
```

- [ ] **Step 9: Register in flake.nix**

In `flake.nix`, beside the existing `pn` lines, add:

- next to `pn = pkgs.callPackage ./modules/pn { inherit self; };` (~line 94):
  ```nix
  jira = pkgs.callPackage ./modules/jira { inherit self; };
  ```
- next to `pn-go-tests = pkgs.callPackage ./modules/pn { inherit self; };` (~line 161):
  ```nix
  jira-go-tests = pkgs.callPackage ./modules/jira { inherit self; };
  ```
- next to `homeModules.pn = import ./home/pn/default.nix;` (~line 227):
  ```nix
  homeModules.jira = import ./home/jira/default.nix;
  ```
- in the overlay block that does `inherit (self.packages.${final.stdenv.hostPlatform.system}) pn;` (~line 242), add `jira` to that `inherit` list.

- [ ] **Step 10: Create README with the extraction tag**

Create `modules/jira/README.md`:

```markdown
# jira — generic Atlassian Jira access tool

A tenant-agnostic Jira library (`pkg/jira`) + CLI (`cmd/jira`). It hard-codes no
tenant, credential location, or OS-specific behavior; ZR (and any other tenant)
specifics are injected as configuration at the edge.

> **TAG — future extraction:** this lives in `repo-base` only to satisfy the
> "no new flake input / no dependency cycle" constraint (see
> `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §5.2).
> It is intended to move to a dedicated repo. Keep it free of repo-base-specific
> coupling so the lift-and-shift stays cheap. Tracking bead: pg2-2x2d.

## Operations

- `jira issue <KEY>` — one issue as JSON.
- `jira search --jql "<JQL>" [--limit N] [--expand changelog[,comments]]` — `{items,truncated}`.
- `jira auth-status` — credential check.
```

- [ ] **Step 11: Verify the build**

Run: `nix build .#jira && ./result/bin/jira --help`
Expected: prints usage including `jira`.

- [ ] **Step 12: Commit**

```bash
git add modules/jira home/jira/default.nix flake.nix
git commit -m "feat(jira): buildable module skeleton (cobra root + nix wiring) [pg2-2x2d.1]"
```

---

### Task 2: Normalized model

**Files:**

- Create: `modules/jira/pkg/jira/model.go`, `modules/jira/pkg/jira/model_test.go`

**Interfaces:**

- Produces: `jira.Issue`, `jira.SearchResult`, `jira.User`, `jira.ChangelogEntry`, `jira.Comment`, and `jira.AuthState` (string consts `AuthOK`/`AuthMissing`/`AuthUnauthenticated`/`AuthForbidden`/`AuthError`). Consumed by `client.go` (Tasks 4-6) and `cmd/jira` (Task 9).

- [ ] **Step 1: Write the failing test**

Create `modules/jira/pkg/jira/model_test.go`:

```go
package jira

import (
	"encoding/json"
	"testing"
)

func TestIssue_JSONRoundTrip_OmitsEmptyOptionals(t *testing.T) {
	in := Issue{Key: "ENG-1", Summary: "s", Status: "Open", IssueType: "Bug", Labels: []string{}, URL: "u"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Optional fields must be omitted when empty.
	for _, k := range []string{"priority", "project", "created", "updated", "reporter", "assignee", "changelog", "comments"} {
		if got := string(b); contains(got, `"`+k+`"`) {
			t.Errorf("expected %q omitted, got %s", k, got)
		}
	}
	var out Issue
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Key != "ENG-1" || out.IssueType != "Bug" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (func() bool { for i := 0; i+len(sub) <= len(s); i++ { if s[i:i+len(sub)] == sub { return true } }; return false })() }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/`
Expected: FAIL — `undefined: Issue`.

- [ ] **Step 3: Write the model**

Create `modules/jira/pkg/jira/model.go`:

```go
// Package jira is a generic, tenant-agnostic Atlassian Jira client + model.
// It MUST NOT import any pg-pr package, hard-code any tenant string, or run
// any OS-specific command (see the package guardrails test).
package jira

// AuthState is the result of an auth-status check.
type AuthState string

const (
	AuthOK              AuthState = "OK"
	AuthMissing         AuthState = "MISSING"
	AuthUnauthenticated AuthState = "UNAUTHENTICATED"
	AuthForbidden       AuthState = "FORBIDDEN"
	AuthError           AuthState = "ERROR"
)

// User is a normalized Atlassian user (reporter, assignee, comment/changelog author).
type User struct {
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// ChangelogEntry is one status transition extracted from a Jira changelog.
type ChangelogEntry struct {
	Field  string `json:"field"`
	From   string `json:"from"`
	To     string `json:"to"`
	Author User   `json:"author"`
	At     string `json:"at"` // RFC3339
}

// Comment is one issue comment with its body flattened from ADF to plain text.
type Comment struct {
	Author  User   `json:"author"`
	Body    string `json:"body"`
	Created string `json:"created"` // RFC3339
}

// Issue is the unified normalized issue returned by GetIssue and Search.
type Issue struct {
	Key       string           `json:"key"`
	Summary   string           `json:"summary"`
	Status    string           `json:"status"`
	IssueType string           `json:"issuetype"`
	Labels    []string         `json:"labels"`
	URL       string           `json:"url"`
	Priority  string           `json:"priority,omitempty"`
	Project   string           `json:"project,omitempty"`
	Created   string           `json:"created,omitempty"`
	Updated   string           `json:"updated,omitempty"`
	Reporter  *User            `json:"reporter,omitempty"`
	Assignee  *User            `json:"assignee,omitempty"`
	Changelog []ChangelogEntry `json:"changelog,omitempty"`
	Comments  []Comment        `json:"comments,omitempty"`
}

// SearchResult is the search envelope: mapped items + authoritative truncation.
type SearchResult struct {
	Items     []Issue `json:"items"`
	Truncated bool    `json:"truncated"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/model.go modules/jira/pkg/jira/model_test.go
git commit -m "feat(jira): normalized issue model [pg2-2x2d.1]"
```

---

### Task 3: ADF flattening

**Files:**

- Create: `modules/jira/pkg/jira/adf.go`, `modules/jira/pkg/jira/adf_test.go`
- Reference: `phillipgreenii-nix-support-apps/packages/activity-collector/internal/collector/jira/jira.go:310-345` (the walker to lift + extend)

**Interfaces:**

- Produces: `func FlattenADF(raw json.RawMessage) string`. Consumed by `client.go` (Task 5, comment bodies).

- [ ] **Step 1: Write the failing test**

Create `modules/jira/pkg/jira/adf_test.go`:

```go
package jira

import (
	"encoding/json"
	"testing"
)

func TestFlattenADF(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string fallback", `"hello"`, "hello"},
		{"paragraph text", `{"content":[{"type":"paragraph","content":[{"type":"text","text":"hi there"}]}]}`, "hi there"},
		{"mention -> display name", `{"content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"text":"@Jane"}}]}]}`, "@Jane"},
		{"link -> href", `{"content":[{"type":"paragraph","content":[{"type":"text","text":"see ","marks":[{"type":"link","attrs":{"href":"http://x"}}]}]}]}`, "see"},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FlattenADF(json.RawMessage(c.raw))
			if got != c.want {
				t.Errorf("FlattenADF(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestFlattenADF`
Expected: FAIL — `undefined: FlattenADF`.

- [ ] **Step 3: Write the flattener (lift + extend the activity-collector walker)**

Create `modules/jira/pkg/jira/adf.go`:

```go
package jira

import (
	"encoding/json"
	"strings"
)

// FlattenADF flattens an Atlassian Document Format body to best-effort plain
// text. A plain JSON string is returned as-is. Unknown nodes recurse into
// children; mention -> attrs.text (display name); link mark/inlineCard -> href.
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

func walkADF(nodes []json.RawMessage, sb *strings.Builder) {
	for _, n := range nodes {
		var node struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Attrs   struct {
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
```

Note on the `link` case: `link` is a _mark_ on a `text` node, not a node type; the `text` node's text is emitted by the `text` case above, which is why the test asserts `"see"` (the visible text), not the href. `inlineCard`/`mention` carry their value in `attrs` and are emitted directly.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestFlattenADF`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/adf.go modules/jira/pkg/jira/adf_test.go
git commit -m "feat(jira): ADF-to-text flattening [pg2-2x2d.1]"
```

---

### Task 4: Client — GetIssue

**Files:**

- Create: `modules/jira/pkg/jira/client.go`, `modules/jira/pkg/jira/client_test.go`
- Reference: `phillipg-nix-ziprecruiter/modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go:73-125` (basicAuth + GetIssue to lift and widen)

**Interfaces:**

- Produces: `func NewClient(baseURL, email, token string) *Client`; `func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error)`. Consumed by Tasks 5, 6, 9.

- [ ] **Step 1: Write the failing test**

Create `modules/jira/pkg/jira/client_test.go`:

```go
package jira

import (
	"context"
	"encoding/base64"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestGetIssue`
Expected: FAIL — `undefined: NewClient`.

- [ ] **Step 3: Write the client (lift + widen from pg-pr-issues-jira-zr)**

Create `modules/jira/pkg/jira/client.go`:

```go
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client talks to an Atlassian Jira tenant via basic auth. It holds no tenant
// default: BaseURL/Email/Token are supplied by the caller (config).
type Client struct {
	BaseURL string
	Email   string
	Token   string
	HTTP    *http.Client
}

// NewClient constructs a Client. BaseURL trailing slashes are trimmed.
func NewClient(baseURL, email, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Email:   email,
		Token:   token,
		HTTP:    &http.Client{Timeout: defaultTimeout},
	}
}

func (c *Client) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
}

func (c *Client) browseURL(key string) string { return c.BaseURL + "/browse/" + url.PathEscape(key) }

// rawUser is the Atlassian user shape; nil-safe mapping to *User.
type rawUser struct {
	EmailAddress string `json:"emailAddress"`
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
}

func (u *rawUser) toUser() *User {
	if u == nil || (u.EmailAddress == "" && u.AccountID == "" && u.DisplayName == "") {
		return nil
	}
	return &User{Email: u.EmailAddress, AccountID: u.AccountID, DisplayName: u.DisplayName}
}

// rawFields is the subset of Atlassian issue fields we map.
type rawFields struct {
	Summary   string   `json:"summary"`
	Labels    []string `json:"labels"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
	Status    struct{ Name string } `json:"status"`
	IssueType struct{ Name string } `json:"issuetype"`
	Priority  struct{ Name string } `json:"priority"`
	Project   struct{ Key string } `json:"project"`
	Reporter  *rawUser `json:"reporter"`
	Assignee  *rawUser `json:"assignee"`
}

func (c *Client) mapIssue(key string, f rawFields) Issue {
	labels := f.Labels
	if labels == nil {
		labels = []string{}
	}
	return Issue{
		Key:       key,
		Summary:   f.Summary,
		Status:    f.Status.Name,
		IssueType: f.IssueType.Name,
		Labels:    labels,
		URL:       c.browseURL(key),
		Priority:  f.Priority.Name,
		Project:   f.Project.Key,
		Created:   f.Created,
		Updated:   f.Updated,
		Reporter:  f.Reporter.toUser(),
		Assignee:  f.Assignee.toUser(),
	}
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", c.basicAuth())
	req.Header.Set("Accept", "application/json")
	return c.HTTP.Do(req)
}

// GetIssue fetches one issue via GET /rest/api/3/issue/<key>.
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("jira: empty issue key")
	}
	endpoint := c.BaseURL + "/rest/api/3/issue/" + url.PathEscape(key) +
		"?fields=summary,status,issuetype,labels,priority,project,created,updated,reporter,assignee"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("jira: get issue %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("jira: issue %s not found", key)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jira: get issue %s: status %s", key, resp.Status)
	}
	var raw struct {
		Key    string    `json:"key"`
		Fields rawFields `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("jira: decode issue %s: %w", key, err)
	}
	iss := c.mapIssue(raw.Key, raw.Fields)
	return &iss, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestGetIssue`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/client.go modules/jira/pkg/jira/client_test.go
git commit -m "feat(jira): client GetIssue with widened field mapping [pg2-2x2d.1]"
```

---

### Task 5: Client — Search (+ expand changelog, comments-as-field, truncation)

**Files:**

- Modify: `modules/jira/pkg/jira/client.go`, `modules/jira/pkg/jira/client_test.go`
- Reference: `pg-pr-issues-jira-zr/main.go:150-226` (search + truncation) and `activity-collector/internal/collector/jira/jira.go:243-300` (changelog/comment structs)

**Interfaces:**

- Consumes: `Issue`, `ChangelogEntry`, `Comment`, `FlattenADF`, `Client`.
- Produces: `type ExpandOpts struct { Changelog, Comments bool }`; `func (c *Client) Search(ctx context.Context, jql string, limit int, exp ExpandOpts) (*SearchResult, error)`. Consumed by Task 9.

- [ ] **Step 1: Write the failing test**

Append to `modules/jira/pkg/jira/client_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestSearch`
Expected: FAIL — `undefined: ExpandOpts`.

- [ ] **Step 3: Implement Search**

Append to `modules/jira/pkg/jira/client.go`:

```go
// ExpandOpts selects optional per-item enrichment for Search. Changelog maps to
// Atlassian expand=changelog; Comments adds the `comment` FIELD (not a Jira
// expand — sending expand=comments returns nothing).
type ExpandOpts struct {
	Changelog bool
	Comments  bool
}

type rawChangeItem struct {
	Field      string `json:"field"`
	FromString string `json:"fromString"`
	ToString   string `json:"toString"`
}
type rawHistory struct {
	Author  rawUser         `json:"author"`
	Created string          `json:"created"`
	Items   []rawChangeItem `json:"items"`
}
type rawComment struct {
	Author  rawUser         `json:"author"`
	Created string          `json:"created"`
	Body    json.RawMessage `json:"body"`
}

// searchFields embeds the shared rawFields and adds the comment list, which is
// present only when Search requests the `comment` field. GetIssue never
// requests it, so its embedded rawFields stays comment-free.
type searchFields struct {
	rawFields
	Comment struct {
		Comments []rawComment `json:"comments"`
	} `json:"comment"`
}

// toUserOrEmpty maps a changelog/comment author, returning a non-nil *User even
// when the source fields are empty (changelog/comment authors are values).
func (u *rawUser) toUserOrEmpty() *User {
	if u == nil {
		return &User{}
	}
	if su := u.toUser(); su != nil {
		return su
	}
	return &User{}
}

func (c *Client) Search(ctx context.Context, jql string, limit int, exp ExpandOpts) (*SearchResult, error) {
	if strings.TrimSpace(jql) == "" {
		return nil, fmt.Errorf("jira: empty jql")
	}
	fields := []string{"summary", "status", "issuetype", "labels", "priority", "project", "created", "updated", "reporter", "assignee"}
	if exp.Comments {
		fields = append(fields, "comment")
	}
	body := map[string]any{"jql": jql, "maxResults": limit, "fields": fields}
	if exp.Changelog {
		body["expand"] = "changelog"
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/rest/api/3/search/jql", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("jira: search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jira: search: status %s", resp.Status)
	}
	var raw struct {
		Issues []struct {
			Key       string       `json:"key"`
			Fields    searchFields `json:"fields"`
			Changelog struct {
				Histories []rawHistory `json:"histories"`
			} `json:"changelog"`
		} `json:"issues"`
		NextPageToken string `json:"nextPageToken"`
		IsLast        *bool  `json:"isLast"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("jira: search: decode: %w", err)
	}
	items := make([]Issue, 0, len(raw.Issues))
	for _, is := range raw.Issues {
		if is.Key == "" {
			return nil, fmt.Errorf("jira: search: issue missing key")
		}
		iss := c.mapIssue(is.Key, is.Fields.rawFields)
		if exp.Changelog {
			for _, h := range is.Changelog.Histories {
				for _, it := range h.Items {
					if it.Field != "status" {
						continue
					}
					iss.Changelog = append(iss.Changelog, ChangelogEntry{
						Field: it.Field, From: it.FromString, To: it.ToString,
						Author: *h.Author.toUserOrEmpty(), At: h.Created,
					})
				}
			}
		}
		if exp.Comments {
			for _, cm := range is.Fields.Comment.Comments {
				iss.Comments = append(iss.Comments, Comment{
					Author: *cm.Author.toUserOrEmpty(), Body: FlattenADF(cm.Body), Created: cm.Created,
				})
			}
		}
		items = append(items, iss)
	}
	truncated := raw.NextPageToken != "" || (raw.IsLast != nil && !*raw.IsLast)
	return &SearchResult{Items: items, Truncated: truncated}, nil
}
```

(Note: `searchFields` embeds `rawFields`, so the `summary`/`status`/etc. fields and `comment` all decode at the `fields` level; `c.mapIssue` receives the embedded `is.Fields.rawFields`. No second decode and no `io` import are needed.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestSearch`
Expected: PASS.

- [ ] **Step 5: Run the whole package + vet**

Run: `cd modules/jira && go vet ./... && go test ./pkg/jira/`
Expected: PASS (catches the placeholder/import fixes above).

- [ ] **Step 6: Commit**

```bash
git add modules/jira/pkg/jira/client.go modules/jira/pkg/jira/client_test.go
git commit -m "feat(jira): client Search with changelog/comments expand + truncation [pg2-2x2d.1]"
```

---

### Task 6: Client — AuthStatus

**Files:**

- Modify: `modules/jira/pkg/jira/client.go`, `modules/jira/pkg/jira/client_test.go`

**Interfaces:**

- Produces: `func (c *Client) AuthStatus(ctx context.Context) (AuthState, error)`. Returns `AuthOK`/`AuthUnauthenticated`/`AuthForbidden`/`AuthError` (the client never returns `AuthMissing` — that is decided pre-flight by the CLI when no token resolves). Consumed by Task 9.

- [ ] **Step 1: Write the failing test**

Append to `modules/jira/pkg/jira/client_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestAuthStatus`
Expected: FAIL — `undefined: (*Client).AuthStatus`.

- [ ] **Step 3: Implement AuthStatus**

Append to `modules/jira/pkg/jira/client.go`:

```go
// AuthStatus performs a live credential check via GET /rest/api/3/myself.
// 401 -> Unauthenticated (Atlassian returns 401 for both invalid and expired
// tokens, so there is deliberately no EXPIRED state), 403 -> Forbidden,
// 2xx -> OK, anything else (incl. transport error) -> Error.
func (c *Client) AuthStatus(ctx context.Context) (AuthState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return AuthError, err
	}
	resp, err := c.do(req)
	if err != nil {
		return AuthError, nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode/100 == 2:
		return AuthOK, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return AuthUnauthenticated, nil
	case resp.StatusCode == http.StatusForbidden:
		return AuthForbidden, nil
	default:
		return AuthError, nil
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestAuthStatus`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/client.go modules/jira/pkg/jira/client_test.go
git commit -m "feat(jira): client AuthStatus via /myself [pg2-2x2d.1]"
```

---

### Task 7: Secret sources (env / file / command)

**Files:**

- Create: `modules/jira/pkg/jira/secret.go`, `modules/jira/pkg/jira/secret_test.go`
- Reference: `pn`'s `modules/pn/internal/exec` Runner pattern; `activity-collector/.../jira/jira.go:359-371` (the command/stdout discipline)

**Interfaces:**

- Consumes: `SecretConfig` (defined here; also used by Task 8's `Config`).
- Produces: `type Runner interface { Run(ctx context.Context, argv []string) (stdout []byte, err error) }`; `type SecretSource interface { Token(ctx context.Context) (string, error) }`; `func NewSecretSource(cfg SecretConfig, runner Runner) (SecretSource, error)`; `type SecretConfig struct { Source, EnvVar, Path string; Command []string }`. Consumed by Task 9.

- [ ] **Step 1: Write the failing test**

Create `modules/jira/pkg/jira/secret_test.go`:

```go
package jira

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeRunner struct {
	out []byte
	err error
}

func (f fakeRunner) Run(context.Context, []string) ([]byte, error) { return f.out, f.err }

func tok(t *testing.T, cfg SecretConfig, r Runner) (string, error) {
	t.Helper()
	src, err := NewSecretSource(cfg, r)
	if err != nil {
		return "", err
	}
	return src.Token(context.Background())
}

func TestSecret_Env(t *testing.T) {
	t.Setenv("MY_TOK", "abc")
	got, err := tok(t, SecretConfig{Source: "env", EnvVar: "MY_TOK"}, nil)
	if err != nil || got != "abc" {
		t.Fatalf("env: got %q err %v", got, err)
	}
}

func TestSecret_File_TrimsNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(p, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tok(t, SecretConfig{Source: "file", Path: p}, nil)
	if err != nil || got != "secret" {
		t.Fatalf("file: got %q err %v", got, err)
	}
}

func TestSecret_Command_TrimsAndErrors(t *testing.T) {
	got, err := tok(t, SecretConfig{Source: "command", Command: []string{"x"}}, fakeRunner{out: []byte("tk\n")})
	if err != nil || got != "tk" {
		t.Fatalf("command ok: got %q err %v", got, err)
	}
	if _, err := tok(t, SecretConfig{Source: "command", Command: []string{"x"}}, fakeRunner{err: errors.New("exit 1")}); err == nil {
		t.Fatal("command non-zero exit must error")
	}
}

func TestSecret_UnknownSource(t *testing.T) {
	if _, err := NewSecretSource(SecretConfig{Source: "smtp"}, nil); err == nil {
		t.Fatal("unknown source must error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestSecret`
Expected: FAIL — `undefined: SecretConfig`.

- [ ] **Step 3: Implement secret sources**

Create `modules/jira/pkg/jira/secret.go`:

```go
package jira

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Runner executes a command argv and returns its stdout. It is injectable so
// the command secret source is testable without spawning processes.
type Runner interface {
	Run(ctx context.Context, argv []string) (stdout []byte, err error)
}

// SecretConfig selects how the API token is resolved at runtime. The token
// value itself never lives in config.
type SecretConfig struct {
	Source  string   `toml:"source"`  // env | file | command
	EnvVar  string   `toml:"env_var"` // for source=env (default JIRA_API_TOKEN)
	Path    string   `toml:"path"`    // for source=file
	Command []string `toml:"command"` // for source=command (argv, exec'd directly — no shell)
}

// SecretSource resolves the API token at runtime.
type SecretSource interface {
	Token(ctx context.Context) (string, error)
}

type envSecret struct{ varName string }

func (e envSecret) Token(context.Context) (string, error) {
	v := os.Getenv(e.varName)
	if v == "" {
		return "", fmt.Errorf("jira: env %s is empty", e.varName)
	}
	return v, nil
}

type fileSecret struct{ path string }

func (f fileSecret) Token(context.Context) (string, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return "", fmt.Errorf("jira: read token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

type commandSecret struct {
	argv   []string
	runner Runner
}

func (c commandSecret) Token(ctx context.Context) (string, error) {
	out, err := c.runner.Run(ctx, c.argv)
	if err != nil {
		return "", fmt.Errorf("jira: secret command failed: %w", err)
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", fmt.Errorf("jira: secret command produced an empty token")
	}
	return t, nil
}

// NewSecretSource builds the configured source. runner is required only for
// source=command (the env/file sources ignore it).
func NewSecretSource(cfg SecretConfig, runner Runner) (SecretSource, error) {
	switch cfg.Source {
	case "", "env":
		v := cfg.EnvVar
		if v == "" {
			v = "JIRA_API_TOKEN"
		}
		return envSecret{varName: v}, nil
	case "file":
		if cfg.Path == "" {
			return nil, fmt.Errorf("jira: secret source=file requires path")
		}
		return fileSecret{path: cfg.Path}, nil
	case "command":
		if len(cfg.Command) == 0 {
			return nil, fmt.Errorf("jira: secret source=command requires a non-empty command argv")
		}
		if runner == nil {
			return nil, fmt.Errorf("jira: secret source=command requires a Runner")
		}
		return commandSecret{argv: cfg.Command, runner: runner}, nil
	default:
		return nil, fmt.Errorf("jira: unknown secret source %q", cfg.Source)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestSecret`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/secret.go modules/jira/pkg/jira/secret_test.go
git commit -m "feat(jira): pluggable secret sources (env/file/command) [pg2-2x2d.1]"
```

---

### Task 8: Config schema + precedence

**Files:**

- Create: `modules/jira/pkg/jira/config.go`, `modules/jira/pkg/jira/config_test.go`

**Interfaces:**

- Consumes: `SecretConfig`.
- Produces: `type Config struct { BaseURL, Email string; DefaultLimit int; Secret SecretConfig }`; `func DefaultConfig() Config`; `func LoadFile(path string) (Config, error)` (missing file = zero Config, no error); `func (base Config) Merge(over Config) Config` (non-zero fields in `over` win). The CLI composes precedence as `DefaultConfig().Merge(file).Merge(env).Merge(flags)`. Consumed by Task 9.

- [ ] **Step 1: Write the failing test**

Create `modules/jira/pkg/jira/config_test.go`:

```go
package jira

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile_parsesTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("base_url=\"https://x.atlassian.net\"\nemail=\"e@x\"\ndefault_limit=50\n[secret]\nsource=\"command\"\ncommand=[\"sec\",\"-w\"]\n"), 0o600)
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://x.atlassian.net" || c.Email != "e@x" || c.DefaultLimit != 50 {
		t.Errorf("bad parse: %+v", c)
	}
	if c.Secret.Source != "command" || len(c.Secret.Command) != 2 {
		t.Errorf("bad secret parse: %+v", c.Secret)
	}
}

func TestLoadFile_missingIsZero(t *testing.T) {
	c, err := LoadFile(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if c.BaseURL != "" {
		t.Errorf("expected zero config, got %+v", c)
	}
}

func TestMerge_overWins_defaultLimitFallsBack(t *testing.T) {
	base := DefaultConfig() // DefaultLimit = 100
	over := Config{BaseURL: "u", Email: "e"}
	got := base.Merge(over)
	if got.BaseURL != "u" || got.Email != "e" || got.DefaultLimit != 100 {
		t.Errorf("merge precedence wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./pkg/jira/ -run 'TestLoadFile|TestMerge'`
Expected: FAIL — `undefined: LoadFile`.

- [ ] **Step 3: Implement config**

Create `modules/jira/pkg/jira/config.go`:

```go
package jira

import (
	"errors"
	"io/fs"
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the non-secret tool configuration. No tenant default lives here.
type Config struct {
	BaseURL      string       `toml:"base_url"`
	Email        string       `toml:"email"`
	DefaultLimit int          `toml:"default_limit"`
	Secret       SecretConfig `toml:"secret"`
}

// DefaultConfig is the generic, tenant-free baseline.
func DefaultConfig() Config {
	return Config{DefaultLimit: 100, Secret: SecretConfig{Source: "env", EnvVar: "JIRA_API_TOKEN"}}
}

// LoadFile parses a TOML config. A missing file yields a zero Config and no error.
func LoadFile(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := toml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Merge returns base with every non-zero field of over applied on top.
func (base Config) Merge(over Config) Config {
	out := base
	if over.BaseURL != "" {
		out.BaseURL = over.BaseURL
	}
	if over.Email != "" {
		out.Email = over.Email
	}
	if over.DefaultLimit != 0 {
		out.DefaultLimit = over.DefaultLimit
	}
	if over.Secret.Source != "" {
		out.Secret.Source = over.Secret.Source
	}
	if over.Secret.EnvVar != "" {
		out.Secret.EnvVar = over.Secret.EnvVar
	}
	if over.Secret.Path != "" {
		out.Secret.Path = over.Secret.Path
	}
	if len(over.Secret.Command) != 0 {
		out.Secret.Command = over.Secret.Command
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/jira && go test ./pkg/jira/ -run 'TestLoadFile|TestMerge'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/config.go modules/jira/pkg/jira/config_test.go
git commit -m "feat(jira): config schema, TOML load, precedence merge [pg2-2x2d.1]"
```

---

### Task 9: CLI subcommands (issue / search / auth-status)

**Files:**

- Modify: `modules/jira/cmd/jira/main.go`, `modules/jira/cmd/jira/main_test.go`

**Interfaces:**

- Consumes: `jira.Config`, `jira.DefaultConfig/LoadFile/Merge`, `jira.NewSecretSource`, `jira.NewClient`, `jira.ExpandOpts`, `jira.Auth*`.
- Produces: the wired CLI. `issue`/`search` write one JSON envelope to stdout; `auth-status` prints the state and exits with the §8.5 code. An OS `exec.Runner` (real) backs the `command` secret source in production.

- [ ] **Step 1: Write the failing test**

Replace `modules/jira/cmd/jira/main_test.go` with:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/jira && go test ./cmd/jira/ -run 'TestCLI_Issue|TestCLI_Search'`
Expected: FAIL — unknown command `issue`.

- [ ] **Step 3: Implement the CLI wiring**

Replace `modules/jira/cmd/jira/main.go` with:

```go
// Command jira is a generic, tenant-agnostic Atlassian Jira access tool.
// It hard-codes no tenant, credential location, or OS-specific behavior;
// all of those are supplied as configuration (see modules/jira/README.md).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/jira/pkg/jira"
	"github.com/spf13/cobra"
)

// osRunner backs the command secret source in production (exec'd directly, no shell).
type osRunner struct{}

func (osRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}

func defaultConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "jira", "config.toml")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "jira", "config.toml")
	}
	return ""
}

// resolveConfig composes precedence defaults -> file -> env -> flags.
func resolveConfig(cmd *cobra.Command) (jira.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = defaultConfigPath()
	}
	fileCfg, err := jira.LoadFile(path)
	if err != nil {
		return jira.Config{}, err
	}
	envCfg := jira.Config{
		BaseURL: os.Getenv("JIRA_BASE_URL"),
		Email:   os.Getenv("JIRA_EMAIL"),
	}
	cfg := jira.DefaultConfig().Merge(fileCfg).Merge(envCfg)
	if cfg.BaseURL == "" {
		return jira.Config{}, fmt.Errorf("jira: base_url not configured (set JIRA_BASE_URL, --config, or config file)")
	}
	if cfg.Email == "" {
		return jira.Config{}, fmt.Errorf("jira: email not configured (set JIRA_EMAIL, --config, or config file)")
	}
	return cfg, nil
}

func newClient(cmd *cobra.Command) (*jira.Client, jira.Config, error) {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return nil, cfg, err
	}
	src, err := jira.NewSecretSource(cfg.Secret, osRunner{})
	if err != nil {
		return nil, cfg, err
	}
	token, err := src.Token(cmd.Context())
	if err != nil {
		return nil, cfg, err
	}
	return jira.NewClient(cfg.BaseURL, cfg.Email, token), cfg, nil
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newIssueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "issue <KEY>",
		Short: "Fetch one issue as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := newClient(cmd)
			if err != nil {
				return err
			}
			iss, err := c.GetIssue(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd, iss)
		},
	}
}

func newSearchCmd() *cobra.Command {
	var jql, expand string
	var limit int
	c := &cobra.Command{
		Use:   "search",
		Short: "JQL search; writes {items,truncated} JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(jql) == "" {
				return fmt.Errorf("jira search: --jql is required")
			}
			cl, cfg, err := newClient(cmd)
			if err != nil {
				return err
			}
			if limit == 0 {
				limit = cfg.DefaultLimit
			}
			var exp jira.ExpandOpts
			for _, e := range strings.Split(expand, ",") {
				switch strings.TrimSpace(e) {
				case "changelog":
					exp.Changelog = true
				case "comments":
					exp.Comments = true
				}
			}
			res, err := cl.Search(cmd.Context(), jql, limit, exp)
			if err != nil {
				return err
			}
			return writeJSON(cmd, res)
		},
	}
	c.Flags().StringVar(&jql, "jql", "", "JQL query (required)")
	c.Flags().IntVar(&limit, "limit", 0, "max results (0 = config default)")
	c.Flags().StringVar(&expand, "expand", "", "comma-separated: changelog,comments")
	return c
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "auth-status",
		Short: "Check credential validity",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			src, err := jira.NewSecretSource(cfg.Secret, osRunner{})
			if err != nil {
				return err
			}
			token, terr := src.Token(cmd.Context())
			if terr != nil || token == "" {
				fmt.Fprintln(cmd.OutOrStdout(), jira.AuthMissing)
				os.Exit(3)
			}
			state, _ := jira.NewClient(cfg.BaseURL, cfg.Email, token).AuthStatus(cmd.Context())
			fmt.Fprintln(cmd.OutOrStdout(), state)
			switch state {
			case jira.AuthOK:
				return nil
			case jira.AuthForbidden:
				os.Exit(4)
			case jira.AuthUnauthenticated:
				os.Exit(5)
			default:
				os.Exit(1)
			}
			return nil
		},
	}
}

// NewRootCmd builds the jira CLI root with all subcommands attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "jira",
		Short:         "Generic Atlassian Jira access tool (issue / search / auth-status)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config TOML (default: $XDG_CONFIG_HOME/jira/config.toml)")
	root.AddCommand(newIssueCmd(), newSearchCmd(), newAuthStatusCmd())
	return root
}

func main() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd modules/jira && go test ./cmd/jira/`
Expected: PASS. (The `auth-status` `os.Exit` paths are covered by SP2's live wiring + manual check; the unit tests cover `issue`/`search`.)

- [ ] **Step 5: Build and smoke-test the binary**

Run: `nix build .#jira && ./result/bin/jira search --jql "" ; echo "exit=$?"`
Expected: prints `jira search: --jql is required` to stderr, `exit=1`.

- [ ] **Step 6: Commit**

```bash
git add modules/jira/cmd/jira/main.go modules/jira/cmd/jira/main_test.go
git commit -m "feat(jira): wire issue/search/auth-status CLI subcommands [pg2-2x2d.1]"
```

---

### Task 10: Generic-core guardrail gate + full validation

**Files:**

- Create: `modules/jira/pkg/jira/guardrails_test.go`

**Interfaces:**

- Produces: a test that fails if the core regains a ZR string, an OS-specific command name, or a `pg-pr` import. No production code.

- [ ] **Step 1: Write the failing test**

Create `modules/jira/pkg/jira/guardrails_test.go`:

```go
package jira

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoForbiddenStrings asserts the generic core stays generic: no ZR strings,
// no OS-specific command names, no pg-pr import. Scans pkg/jira and cmd/jira.
func TestNoForbiddenStrings(t *testing.T) {
	forbidden := []string{"ziprecruiter", "zr-jira", "security find-generic-password", "secret-tool", "/pg-pr/", "provider/issues"}
	roots := []string{".", "../../cmd/jira"}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			if strings.HasSuffix(path, "guardrails_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			low := strings.ToLower(string(b))
			for _, f := range forbidden {
				if strings.Contains(low, strings.ToLower(f)) {
					t.Errorf("%s contains forbidden token %q (the generic core must stay tenant/OS/pg-pr agnostic)", path, f)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
```

- [ ] **Step 2: Run the test**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestNoForbiddenStrings`
Expected: PASS (if it FAILS, a forbidden token leaked in — remove it; do not weaken the gate).

- [ ] **Step 3: Full package + vet + build**

Run:

```bash
cd modules/jira && go vet ./... && go test ./... && cd ../.. && nix build .#jira-go-tests
```

Expected: all PASS; `jira-go-tests` check builds (runs `go test` via `mkGoBinary` doCheck).

- [ ] **Step 4: Repo-wide gates**

Run:

```bash
prek run --all-files
nix flake check
pn workspace build
```

Expected: all green. (If `treefmt` rewrites `*.go`/`*.nix`, re-stage and re-run.)

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/guardrails_test.go
git commit -m "test(jira): generic-core guardrail gate (no ZR/OS/pg-pr) [pg2-2x2d.1]"
```

---

## Self-Review

**Spec coverage** (against `2026-06-26-generic-jira-access-tool-design.md`):

- §6 capabilities — `get_issue` (Task 4/9), `search`+expand (Task 5/9), `auth_status` (Task 6/9), `transition` reserved (not built — correct per spec). ✓
- §7 unified model + ADF + resolution-excluded — Tasks 2, 3. ✓
- §8.2 config (cobra + go-toml/v2, precedence) — Task 8/9. ✓
- §8.3 secret sources (env/file/command, no-shell, trim, non-zero-exit→error) — Task 7. ✓
- §8.5 auth-status taxonomy + exit codes — Task 6 (states) + Task 9 (MISSING pre-flight + exit codes). ✓
- §9.1 layout / mkGoBinary / gomod2nix Pattern A — Task 1. ✓
- §9.2 endpoints + fields — Tasks 4, 5, 6. ✓
- §9.3 validation + grep/import gate (no ZR/OS/pg-pr) — Task 10. ✓
- §5.2 extraction tag — Task 1 (README). ✓

**Placeholder scan:** none. Task 5 Step 3 presents complete single-pass code (embedded `searchFields`); no `import_marker`, `TBD`, `add error handling`, or `similar to Task N` anywhere.

**Type consistency:** `Config`/`SecretConfig`/`Issue`/`SearchResult`/`ExpandOpts`/`AuthState`/`Runner`/`SecretSource` and the functions `NewClient`/`GetIssue`/`Search`/`AuthStatus`/`NewSecretSource`/`DefaultConfig`/`LoadFile`/`Merge`/`NewRootCmd` are used consistently across Tasks 2-10.

## Execution Handoff

Two execution options:

1. **Subagent-Driven (recommended)** — a fresh subagent implements each task, with a two-stage review between tasks (`superpowers:subagent-driven-development`).
2. **Inline Execution** — execute the tasks in this session with checkpoints (`superpowers:executing-plans`).

Either way, work continues on branch `generic-jira-access-tool` in `phillipg-nix-repo-base`; nothing is pushed.
