# pn workspace build & apply — Go port — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port `pn workspace build` and `pn workspace apply` from placeholders to full-fidelity terminal-flake build/activate with local `--override-input` wiring.

**Architecture:** Resolve workspace root → load config → resolve the explicit terminal repo → build `--override-input <input-name> git+file://<clone>` flags for every non-terminal repo → `cd` terminal → `nix fmt` → run a `build_command`/`apply_command` template with overrides appended. `apply` adds a nix-daemon health check, a dirty-aware rebuild-skip gate, a guarded `nvd` profile diff, and mark-applied state.

**Tech Stack:** Go 1.25, `spf13/cobra`, `pelletier/go-toml/v2`, the in-repo `internal/exec` Runner (FakeRunner for tests).

**Working directory for all commands:** `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base` (the `pn` module is `modules/pn`). All `go` commands use `go -C modules/pn …`.

**Prerequisite already in working tree (uncommitted):** the `input-name` fix — `RepoConfig.InputName` + `WorkspaceConfig.InputNameFor` in `config.go`, `computeOverrideArgs` using it in `helpers.go`, and tests in `config_test.go`/`nix_test.go`. Task 1 commits this together with the new `[workspace]` fields.

**Two deliberate simplifications from the old bash (full fidelity otherwise):**

1. `checkNixDaemon` probes and returns an actionable error on failure; it does **not** implement the interactive `/dev/tty` "restart daemon?" prompt (hard to test, marginal value).
2. No custom signal handling: the apply command runs synchronously in the same process group, so a terminal `Ctrl-C`/`SIGTERM` already reaches `sudo`→`darwin-rebuild` via normal group signal delivery. This is simpler and equally correct.

---

### Task 1: `[workspace]` config fields + accessors

**Files:**

- Modify: `modules/pn/internal/workspace/config.go`
- Test: `modules/pn/internal/workspace/config_test.go`

- [ ] **Step 1: Write failing tests**

Append to `config_test.go`:

```go
func TestParseConfig_WorkspaceCommands(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[workspace]
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_flake}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	term, err := cfg.TerminalRepo()
	if err != nil || term != "leaf" {
		t.Fatalf("TerminalRepo: got %q, %v", term, err)
	}
	if got := cfg.BuildCommandTemplate(); got != "darwin-rebuild build --flake {terminal_flake}" {
		t.Errorf("BuildCommandTemplate: got %q", got)
	}
	ac, err := cfg.ApplyCommandTemplate()
	if err != nil || ac != "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}" {
		t.Errorf("ApplyCommandTemplate: got %q, %v", ac, err)
	}
}

func TestParseConfig_TerminalMustNameRepo(t *testing.T) {
	_, err := ParseConfig([]byte(`
[workspace]
terminal = "nope"

[repos.leaf]
url = "github:owner/leaf"
`))
	if err == nil {
		t.Fatal("expected error for terminal not matching a repo")
	}
}

func TestParseConfig_DefaultsWhenCommandsAbsent(t *testing.T) {
	cfg, err := ParseConfig([]byte(`[repos.leaf]
url = "github:owner/leaf"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.BuildCommandTemplate(); got != "darwin-rebuild build --flake {terminal_flake}" {
		t.Errorf("default build command: got %q", got)
	}
	if _, err := cfg.TerminalRepo(); err == nil {
		t.Error("expected error when terminal unset")
	}
	if _, err := cfg.ApplyCommandTemplate(); err == nil {
		t.Error("expected error when apply_command unset")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestParseConfig_Workspace|TestParseConfig_Terminal|TestParseConfig_Defaults' 2>&1 | tail`
Expected: build failure — `cfg.TerminalRepo` / `BuildCommandTemplate` / `ApplyCommandTemplate` undefined.

- [ ] **Step 3: Implement**

In `config.go`, replace the `WorkspaceSection` struct:

```go
// WorkspaceSection is the [workspace] table.
type WorkspaceSection struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	// Terminal is the repo key of the terminal flake — the one build/apply
	// build and activate; all others are injected as local overrides.
	Terminal string `toml:"terminal,omitempty"`
	// BuildCommand / ApplyCommand are command templates expanded with
	// {terminal_flake} and {hostname}. BuildCommand defaults when empty;
	// ApplyCommand is required by `apply`.
	BuildCommand string `toml:"build_command,omitempty"`
	ApplyCommand string `toml:"apply_command,omitempty"`
}
```

In `ParseConfig`, just before `return cfg, nil`, add terminal validation:

```go
	if t := cfg.Workspace.Terminal; t != "" {
		if _, ok := cfg.Repos[t]; !ok {
			return nil, fmt.Errorf("workspace.terminal %q does not match any [repos.*] entry", t)
		}
	}
```

Add accessors at the end of `config.go`:

```go
const defaultBuildCommand = "darwin-rebuild build --flake {terminal_flake}"

// TerminalRepo returns the configured terminal repo key, or an error if unset.
func (c *WorkspaceConfig) TerminalRepo() (string, error) {
	if c == nil || c.Workspace.Terminal == "" {
		return "", fmt.Errorf("workspace.terminal is not set in pn-workspace.toml")
	}
	return c.Workspace.Terminal, nil
}

// BuildCommandTemplate returns the configured build_command, or the default.
func (c *WorkspaceConfig) BuildCommandTemplate() string {
	if c != nil && c.Workspace.BuildCommand != "" {
		return c.Workspace.BuildCommand
	}
	return defaultBuildCommand
}

// ApplyCommandTemplate returns the configured apply_command, or an error if unset.
func (c *WorkspaceConfig) ApplyCommandTemplate() (string, error) {
	if c == nil || c.Workspace.ApplyCommand == "" {
		return "", fmt.Errorf("workspace.apply_command is not set in pn-workspace.toml")
	}
	return c.Workspace.ApplyCommand, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ 2>&1 | tail`
Expected: `ok` (all workspace tests pass, including the prerequisite input-name tests).

- [ ] **Step 5: Commit (folds in the input-name prerequisite)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git add modules/pn/internal/workspace/config.go modules/pn/internal/workspace/config_test.go \
        modules/pn/internal/workspace/helpers.go modules/pn/internal/workspace/nix_test.go
git commit -m "feat(pn): per-repo input-name + workspace terminal/build/apply config"
```

---

### Task 2: override-path parser

**Files:**

- Create: `modules/pn/internal/workspace/overridepaths.go`
- Test: `modules/pn/internal/workspace/overridepaths_test.go`

- [ ] **Step 1: Write failing tests**

`overridepaths_test.go`:

```go
package workspace

import "testing"

func TestParseOverridePaths_FlagOnly(t *testing.T) {
	t.Setenv("PN_WORKSPACE_OVERRIDE_PATHS", "")
	m, err := parseOverridePaths([]string{"repo-a=/abs/a", " repo-b = /abs/b "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["repo-a"] != "/abs/a" || m["repo-b"] != "/abs/b" {
		t.Errorf("got %#v", m)
	}
}

func TestParseOverridePaths_FlagOverridesEnv(t *testing.T) {
	t.Setenv("PN_WORKSPACE_OVERRIDE_PATHS", "repo-a=/abs/env,repo-c=/abs/c")
	m, err := parseOverridePaths([]string{"repo-a=/abs/flag"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["repo-a"] != "/abs/flag" {
		t.Errorf("flag should override env: got %q", m["repo-a"])
	}
	if m["repo-c"] != "/abs/c" {
		t.Errorf("env entry lost: got %q", m["repo-c"])
	}
}

func TestParseOverridePaths_Errors(t *testing.T) {
	t.Setenv("PN_WORKSPACE_OVERRIDE_PATHS", "")
	if _, err := parseOverridePaths([]string{"noequals"}); err == nil {
		t.Error("expected error for missing =")
	}
	if _, err := parseOverridePaths([]string{"=/abs"}); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := parseOverridePaths([]string{"a=rel/path"}); err == nil {
		t.Error("expected error for non-absolute path")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run TestParseOverridePaths 2>&1 | tail`
Expected: build failure — `parseOverridePaths` undefined.

- [ ] **Step 3: Implement**

`overridepaths.go`:

```go
package workspace

import (
	"fmt"
	"os"
	"strings"
)

const overridePathsEnv = "PN_WORKSPACE_OVERRIDE_PATHS"

// parseOverridePaths builds a map of repo-key -> absolute override path. Entries
// come from the PN_WORKSPACE_OVERRIDE_PATHS env var (comma-separated, lower
// precedence) and then the given specs (higher precedence). Each entry is
// "name=path"; path must be absolute.
func parseOverridePaths(specs []string) (map[string]string, error) {
	out := map[string]string{}
	add := func(raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			return fmt.Errorf("invalid override spec (expected name=path): %s", raw)
		}
		name := strings.TrimSpace(raw[:eq])
		path := strings.TrimSpace(raw[eq+1:])
		if name == "" {
			return fmt.Errorf("invalid override spec (empty name): %s", raw)
		}
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("override path must be absolute: %s", path)
		}
		out[name] = path
		return nil
	}
	if env := os.Getenv(overridePathsEnv); env != "" {
		for _, e := range strings.Split(env, ",") {
			if err := add(e); err != nil {
				return nil, err
			}
		}
	}
	for _, s := range specs {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run TestParseOverridePaths -v 2>&1 | tail`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/overridepaths.go modules/pn/internal/workspace/overridepaths_test.go
git commit -m "feat(pn): parse --override-path / PN_WORKSPACE_OVERRIDE_PATHS"
```

---

### Task 3: command template substitution

**Files:**

- Create: `modules/pn/internal/workspace/template.go`
- Test: `modules/pn/internal/workspace/template_test.go`

- [ ] **Step 1: Write failing tests**

`template_test.go`:

```go
package workspace

import (
	"reflect"
	"testing"
)

func TestSubstituteCommand(t *testing.T) {
	got := substituteCommand("sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}", "/ws/leaf", "host01")
	want := []string{"sudo", "darwin-rebuild", "switch", "--flake", "/ws/leaf#host01"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_NoPlaceholders(t *testing.T) {
	got := substituteCommand("echo hello", "/ws/leaf", "host01")
	want := []string{"echo", "hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestShortenHostname(t *testing.T) {
	if got := shortenHostname("phillipg-mbp-02.local"); got != "phillipg-mbp-02" {
		t.Errorf("got %q", got)
	}
	if got := shortenHostname("plainhost"); got != "plainhost" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestSubstituteCommand|TestShortenHostname' 2>&1 | tail`
Expected: build failure — `substituteCommand` / `shortenHostname` undefined.

- [ ] **Step 3: Implement**

`template.go`:

```go
package workspace

import (
	"os"
	"strings"
)

// substituteCommand expands {terminal_flake} and {hostname} in a command
// template and splits the result into argv on whitespace.
func substituteCommand(tmpl, terminalFlake, hostname string) []string {
	r := strings.NewReplacer("{terminal_flake}", terminalFlake, "{hostname}", hostname)
	return strings.Fields(r.Replace(tmpl))
}

// shortenHostname truncates a hostname at the first dot (mimics `hostname -s`).
func shortenHostname(h string) string {
	if i := strings.IndexByte(h, '.'); i >= 0 {
		return h[:i]
	}
	return h
}

// shortHostname returns the current host's short name.
func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return shortenHostname(h)
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestSubstituteCommand|TestShortenHostname' -v 2>&1 | tail`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/template.go modules/pn/internal/workspace/template_test.go
git commit -m "feat(pn): build/apply command template substitution"
```

---

### Task 4: unified override builder (config-driven, git+file://)

**Files:**

- Modify: `modules/pn/internal/workspace/helpers.go`
- Modify: `modules/pn/internal/workspace/nix.go`
- Create: `modules/pn/internal/workspace/overrides_test.go`
- Modify: `modules/pn/internal/workspace/nix_test.go`

Note: leave the existing `computeOverrideArgs` in place for now — `build.go`/`apply.go` still call it and stay green until Tasks 7/8. This task adds `overrideInputArgs` and switches only `NixCommand`.

- [ ] **Step 1: Write failing tests**

`overrides_test.go`:

```go
package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func mkRepoDir(t *testing.T, root, name string) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", d, err)
	}
	return d
}

func openWS(t *testing.T, root, toml string) *Workspace {
	t.Helper()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w
}

func TestOverrideInputArgs_GitFileAndInputName(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dir-base")
	w := openWS(t, root, `
[repos.dir-base]
url = "github:owner/base"
input-name = "real-base"
`)
	got := w.overrideInputArgs(overrideOpts{})
	want := []string{"--override-input", "real-base", "git+file://" + filepath.Join(root, "dir-base")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestOverrideInputArgs_ExcludeTerminalAndMissingDir(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dep") // present
	// "leaf" dir intentionally absent.
	w := openWS(t, root, `
[workspace]
terminal = "leaf"

[repos.dep]
url = "github:owner/dep"

[repos.leaf]
url = "github:owner/leaf"
`)
	got := w.overrideInputArgs(overrideOpts{ExcludeTerminal: true})
	want := []string{"--override-input", "dep", "git+file://" + filepath.Join(root, "dep")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestOverrideInputArgs_OverridePathSwap(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dep")
	alt := t.TempDir() // stand-in worktree
	w := openWS(t, root, `
[repos.dep]
url = "github:owner/dep"
`)
	got := w.overrideInputArgs(overrideOpts{OverridePaths: map[string]string{"dep": alt}})
	want := []string{"--override-input", "dep", "git+file://" + alt}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}
```

Replace the entire body of `nix_test.go` with the config-driven, git+file:// expectations:

```go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNixCommand_AppendsGitFileOverrideForEachRepo(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "foo", "git+file://" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_NoRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "x"
`)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "show"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "show"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_MultipleOverridesAlphabetical(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	mkRepoDir(t, root, "bar")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"build", ".",
		"--override-input", "bar", "git+file://" + filepath.Join(root, "bar"),
		"--override-input", "foo", "git+file://" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"build", "."}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_UsesConfiguredInputName(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "phillipg-nix-repo-base")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.phillipg-nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"
input-name = "phillipgreenii-nix-base"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "phillipgreenii-nix-base", "git+file://" + filepath.Join(root, "phillipg-nix-repo-base"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestOverrideInputArgs|TestNixCommand' 2>&1 | tail`
Expected: build failure — `overrideInputArgs` / `overrideOpts` undefined.

- [ ] **Step 3: Implement**

Append to `helpers.go` (keep `computeOverrideArgs` for now):

```go
// overrideOpts configures overrideInputArgs.
type overrideOpts struct {
	// ExcludeTerminal omits the terminal repo (build/apply build it, so it must
	// not override itself).
	ExcludeTerminal bool
	// OverridePaths maps repo key -> absolute path, replacing the default clone
	// location for that repo.
	OverridePaths map[string]string
}

// overrideInputArgs returns --override-input flags pinning each declared,
// non-excluded workspace repo whose clone exists on disk to its local clone via
// git+file://. The override NAME is the repo's resolved input-name; the PATH is
// the repo's clone dir (or its --override-path override). Sorted by repo key.
func (ws *Workspace) overrideInputArgs(opts overrideOpts) []string {
	if ws == nil || ws.config == nil {
		return []string{}
	}
	terminal := ws.config.Workspace.Terminal
	names := orderedRepoNames(ws.config.Repos)
	out := make([]string, 0, 3*len(names))
	for _, name := range names {
		if opts.ExcludeTerminal && name == terminal {
			continue
		}
		dir := filepath.Join(ws.root, name)
		if ov, ok := opts.OverridePaths[name]; ok {
			dir = ov
		}
		if !dirExists(dir) {
			continue
		}
		out = append(out, "--override-input", ws.config.InputNameFor(name), "git+file://"+dir)
	}
	return out
}

// dirExists reports whether p exists and is a directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
```

Add `"os"` to the `helpers.go` import block (it currently imports only `path/filepath` and `sort`).

In `nix.go`, change `NixCommand` to use the new builder:

```go
func (ws *Workspace) NixCommand(ctx context.Context, args []string) error {
	overrides := ws.overrideInputArgs(overrideOpts{})
	full := append([]string{}, args...)
	full = append(full, overrides...)
	_, err := ws.runner.Run(ctx, "nix", full, exec.RunOptions{Dir: ws.root})
	return err
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestOverrideInputArgs|TestNixCommand' -v 2>&1 | tail`
Expected: PASS. (Full `go -C modules/pn test ./internal/workspace/` may still show the OLD build/apply tests passing too — that's fine, they use `computeOverrideArgs`.)

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/helpers.go modules/pn/internal/workspace/nix.go \
        modules/pn/internal/workspace/overrides_test.go modules/pn/internal/workspace/nix_test.go
git commit -m "feat(pn): config-driven git+file:// override builder; switch nix passthrough"
```

---

### Task 5: `check_follows`

**Files:**

- Create: `modules/pn/internal/workspace/follows.go`
- Test: `modules/pn/internal/workspace/follows_test.go`

- [ ] **Step 1: Write failing tests**

`follows_test.go`:

```go
package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFollows_MissingLockIsOK(t *testing.T) {
	if err := checkFollows(t.TempDir(), []string{"a", "b"}); err != nil {
		t.Errorf("missing lock should be ok, got %v", err)
	}
}

func TestCheckFollows_ProperFollowsIsOK(t *testing.T) {
	dir := t.TempDir()
	// node "a" follows "b" (array value) -> ok.
	writeFile(t, filepath.Join(dir, "flake.lock"), `{
      "nodes": {
        "root": {"inputs": {"a": "a", "b": "b"}},
        "a": {"inputs": {"b": ["b"]}},
        "b": {"inputs": {}}
      }
    }`)
	if err := checkFollows(dir, []string{"a", "b"}); err != nil {
		t.Errorf("proper follows should be ok, got %v", err)
	}
}

func TestCheckFollows_UnfollowedCopyIsError(t *testing.T) {
	dir := t.TempDir()
	// node "a" carries its own copy of "b" (string value) -> error.
	writeFile(t, filepath.Join(dir, "flake.lock"), `{
      "nodes": {
        "root": {"inputs": {"a": "a", "b": "b"}},
        "a": {"inputs": {"b": "b_2"}},
        "b": {"inputs": {}},
        "b_2": {"inputs": {}}
      }
    }`)
	err := checkFollows(dir, []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for unfollowed copy")
	}
	if !strings.Contains(err.Error(), "does not follow") ||
		!strings.Contains(err.Error(), "inputs.a.inputs.b.follows") {
		t.Errorf("error missing detail/hint: %v", err)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run TestCheckFollows 2>&1 | tail`
Expected: build failure — `checkFollows` undefined.

- [ ] **Step 3: Implement**

`follows.go`:

```go
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type lockFile struct {
	Nodes map[string]lockNode `json:"nodes"`
}

type lockNode struct {
	// Inputs maps input name -> string (node key) OR array (follows path).
	Inputs map[string]json.RawMessage `json:"inputs"`
}

// checkFollows verifies that every workspace input that is a direct dependency
// of the terminal flake `follows` every other workspace input rather than
// carrying an unfollowed copy (which makes --override-input silently
// ineffective for the shared dep). Returns nil if the lock is absent or fewer
// than two workspace inputs are present.
func checkFollows(terminalDir string, inputNames []string) error {
	lockPath := filepath.Join(terminalDir, "flake.lock")
	data, err := os.ReadFile(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", lockPath, err)
	}
	if len(inputNames) < 2 {
		return nil
	}
	var lf lockFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return fmt.Errorf("parse %s: %w", lockPath, err)
	}
	root, ok := lf.Nodes["root"]
	if !ok {
		return nil
	}

	want := append([]string(nil), inputNames...)
	sort.Strings(want)

	var problems []string
	for _, name := range want {
		raw, ok := root.Inputs[name]
		if !ok {
			continue
		}
		nodeKey, ok := asString(raw)
		if !ok {
			continue
		}
		node, ok := lf.Nodes[nodeKey]
		if !ok {
			continue
		}
		for _, other := range want {
			if other == name {
				continue
			}
			ref, ok := node.Inputs[other]
			if !ok {
				continue
			}
			if _, isString := asString(ref); isString {
				problems = append(problems, fmt.Sprintf(
					"workspace input %q does not follow %q\n  Fix: add to flake.nix: inputs.%s.inputs.%s.follows = %q",
					name, other, name, other, other))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}

// asString reports whether a raw JSON value is a string, returning its value.
func asString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return "", false
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run TestCheckFollows -v 2>&1 | tail`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/follows.go modules/pn/internal/workspace/follows_test.go
git commit -m "feat(pn): port workspace check_follows validation"
```

---

### Task 6: update-cache (rebuild gate + daemon check + mark-applied)

**Files:**

- Create: `modules/pn/internal/workspace/updatecache.go`
- Test: `modules/pn/internal/workspace/updatecache_test.go`

- [ ] **Step 1: Write failing tests**

`updatecache_test.go`:

```go
package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNeedsRebuild_Force(t *testing.T) {
	w := &Workspace{runner: exec.NewFakeRunner()}
	got, err := w.needsRebuild(context.Background(), []string{"/x"}, true, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("force should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_DirtyTree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "status", "--porcelain"}, exec.Result{Stdout: []byte(" M file\n")}, nil)
	w := &Workspace{runner: f}
	got, err := w.needsRebuild(context.Background(), []string{"/repo"}, false, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("dirty tree should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_CleanUnchangedSkips(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	hashDir := filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hashDir, "repo"), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", "/repo", "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc123\n")}, nil)
	w := &Workspace{runner: f}
	out := &bytes.Buffer{}
	got, err := w.needsRebuild(context.Background(), []string{"/repo"}, false, out)
	if err != nil || got {
		t.Fatalf("clean+unchanged should skip: %v %v", got, err)
	}
}

func TestMarkApplied_WritesHead(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("deadbeef\n")}, nil)
	w := &Workspace{runner: f}
	if err := w.markApplied(context.Background(), []string{"/repo"}); err != nil {
		t.Fatalf("markApplied: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash", "repo"))
	if err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if string(got) != "deadbeef\n" {
		t.Errorf("stored hash: got %q", got)
	}
}

func TestCheckNixDaemon_ErrorPath(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, &exec.CommandError{Name: "nix", Args: []string{"eval"}, Result: exec.Result{ExitCode: 1}})
	w := &Workspace{runner: f}
	if err := w.checkNixDaemon(context.Background()); err == nil {
		t.Fatal("expected daemon-check error")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestNeedsRebuild|TestMarkApplied|TestCheckNixDaemon' 2>&1 | tail`
Expected: build failure — `needsRebuild` / `markApplied` / `checkNixDaemon` undefined.

- [ ] **Step 3: Implement**

`updatecache.go`:

```go
package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// stateDir returns the update-cache state root, honoring XDG_STATE_HOME.
func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "zn-self-upgrade")
}

func appliedHashDir() string  { return filepath.Join(stateDir(), "apply", "applied-hash") }
func appliedHashFile(repoDir string) string {
	return filepath.Join(appliedHashDir(), filepath.Base(repoDir))
}

// needsRebuild reports whether apply must rebuild. Returns true if force is set,
// any repo's working tree is dirty, any repo's HEAD differs from the recorded
// applied hash, or any repo has no recorded hash. Returns false (with a notice)
// only when every repo is clean and unchanged.
func (ws *Workspace) needsRebuild(ctx context.Context, repoDirs []string, force bool, out io.Writer) (bool, error) {
	if force {
		return true, nil
	}
	for _, dir := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "status", "--porcelain"}, exec.RunOptions{})
		if err != nil {
			return false, fmt.Errorf("git status in %s: %w", dir, err)
		}
		if strings.TrimSpace(string(res.Stdout)) != "" {
			return true, nil
		}
		res, err = ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return false, fmt.Errorf("git rev-parse in %s: %w", dir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		stored, err := os.ReadFile(appliedHashFile(dir))
		if err != nil {
			return true, nil
		}
		if head != strings.TrimSpace(string(stored)) {
			return true, nil
		}
	}
	fmt.Fprintln(out, "Skipping rebuild: all workspace repos clean and unchanged since last apply")
	return false, nil
}

// markApplied records each repo's current HEAD as the last applied hash.
func (ws *Workspace) markApplied(ctx context.Context, repoDirs []string) error {
	if err := os.MkdirAll(appliedHashDir(), 0o755); err != nil {
		return err
	}
	for _, dir := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("git rev-parse in %s: %w", dir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		if err := os.WriteFile(appliedHashFile(dir), []byte(head+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// checkNixDaemon probes daemon responsiveness with a 10s-bounded `nix eval`. On
// failure it returns an actionable error (the interactive restart prompt from
// the bash version is intentionally omitted).
func (ws *Workspace) checkNixDaemon(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := ws.runner.Run(tctx, "nix", []string{"eval", "--expr", "true"}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("nix daemon health check failed: %w\n  Try: sudo launchctl kickstart -k system/org.nixos.nix-daemon", err)
	}
	return nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run 'TestNeedsRebuild|TestMarkApplied|TestCheckNixDaemon' -v 2>&1 | tail`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/updatecache.go modules/pn/internal/workspace/updatecache_test.go
git commit -m "feat(pn): dirty-aware rebuild gate, mark-applied, nix daemon check"
```

---

### Task 7: rewrite `build`

**Files:**

- Modify (replace contents): `modules/pn/internal/workspace/build.go`
- Modify (replace contents): `modules/pn/internal/workspace/build_test.go`
- Modify: `modules/pn/internal/cli/workspace.go` (build command call site only)

- [ ] **Step 1: Write failing tests** — replace `build_test.go` entirely:

```go
package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestBuild_TerminalOnlyWithOverrides(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
input-name = "dep-input"
`)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	// default build_command = darwin-rebuild build --flake {terminal_flake}
	f.AddResponse("darwin-rebuild", []string{
		"build", "--flake", leafDir,
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected fmt + build = 2 calls, got %d", len(calls))
	}
	if calls[0].Opts.Dir != leafDir || calls[1].Opts.Dir != leafDir {
		t.Errorf("commands must run in terminal dir; got %q,%q", calls[0].Opts.Dir, calls[1].Opts.Dir)
	}
}

func TestBuild_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Build(context.Background(), out, BuildOptions{ShowNixCommandsOnly: true}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "nix fmt") ||
		!strings.Contains(out.String(), "darwin-rebuild build --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing commands:\n%s", out.String())
	}
}

func TestBuild_ErrorsWhenTerminalUnset(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.leaf]
url = "github:owner/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{}); err == nil {
		t.Fatal("expected error when terminal unset")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run TestBuild 2>&1 | tail`
Expected: build failure — `Build` signature mismatch (`out` arg) / `BuildOptions` fields.

- [ ] **Step 3: Implement** — replace `build.go` entirely:

```go
package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// BuildOptions configures Build.
type BuildOptions struct {
	BuildCmd            string            // overrides build_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
}

// Build formats and builds the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It does not activate.
func (ws *Workspace) Build(ctx context.Context, out io.Writer, opts BuildOptions) error {
	terminal, err := ws.config.TerminalRepo()
	if err != nil {
		return err
	}
	terminalDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalDir = td
	}

	overrides := ws.overrideInputArgs(overrideOpts{ExcludeTerminal: true, OverridePaths: opts.OverridePaths})

	if err := checkFollows(terminalDir, ws.workspaceInputNames(terminal)); err != nil {
		return err
	}

	tmpl := ws.config.BuildCommandTemplate()
	if opts.BuildCmd != "" {
		tmpl = opts.BuildCmd
	}
	cmdArgs := substituteCommand(tmpl, terminalDir, shortHostname())
	if len(cmdArgs) == 0 {
		return fmt.Errorf("build_command is empty")
	}

	if opts.ShowNixCommandsOnly {
		fmt.Fprintf(out, "cd %s && nix fmt\n", terminalDir)
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	fmt.Fprintln(out, "  --== Formatting flake ==--  ")
	if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: terminalDir}); err != nil {
		return fmt.Errorf("nix fmt in %s: %w", terminalDir, err)
	}

	fmt.Fprintln(out, "  --== Building flake ==--  ")
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalDir}); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	fmt.Fprintln(out, "Build successful. To apply, run: pn workspace apply")
	return nil
}

// workspaceInputNames returns the resolved input names of all non-terminal
// repos (used for check_follows).
func (ws *Workspace) workspaceInputNames(terminal string) []string {
	var names []string
	for _, key := range orderedRepoNames(ws.config.Repos) {
		if key == terminal {
			continue
		}
		names = append(names, ws.config.InputNameFor(key))
	}
	return names
}
```

In `cli/workspace.go`, update only the build command's `RunE` body:

```go
			return w.Build(context.Background(), cmd.OutOrStdout(), workspace.BuildOptions{})
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run TestBuild -v 2>&1 | tail` then `go -C modules/pn build ./...`
Expected: PASS (3 tests); package builds.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/build.go modules/pn/internal/workspace/build_test.go modules/pn/internal/cli/workspace.go
git commit -m "feat(pn): build the terminal flake with local overrides"
```

---

### Task 8: rewrite `apply` (and remove dead `computeOverrideArgs`)

**Files:**

- Modify (replace contents): `modules/pn/internal/workspace/apply.go`
- Modify (replace contents): `modules/pn/internal/workspace/apply_test.go`
- Modify: `modules/pn/internal/workspace/helpers.go` (delete `computeOverrideArgs`)
- Modify: `modules/pn/internal/cli/workspace.go` (apply command call site only)

- [ ] **Step 1: Write failing tests** — replace `apply_test.go` entirely:

```go
package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const applyTOML = `
[workspace]
terminal = "leaf"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
input-name = "dep-input"
`

func TestApply_RunsApplyCommandWithOverrides(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	// needsRebuild gate (force=true via option below skips git probes)
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", leafDir + "#" + shortHostname(),
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)
	// markApplied rev-parse for both repos
	f.AddResponse("git", []string{"-C", depDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("d\n")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("l\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestApply_ErrorsWhenApplyCommandMissing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{}); err == nil {
		t.Fatal("expected error when apply_command unset")
	}
}

func TestApply_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{ShowNixCommandsOnly: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "sudo darwin-rebuild switch --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing apply command:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/workspace/ -run TestApply 2>&1 | tail`
Expected: build failure — `Apply` signature / `ApplyOptions` fields.

- [ ] **Step 3: Implement** — replace `apply.go` entirely:

```go
package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ApplyOptions configures Apply.
type ApplyOptions struct {
	ApplyCmd            string            // overrides apply_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
	Force               bool // always rebuild (bypass the skip gate)
}

// Apply formats and activates the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It checks daemon health, skips the rebuild
// when nothing changed, diffs the system profile via nvd when available, and
// records the applied state.
func (ws *Workspace) Apply(ctx context.Context, out io.Writer, opts ApplyOptions) error {
	terminal, err := ws.config.TerminalRepo()
	if err != nil {
		return err
	}
	terminalDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalDir = td
	}

	overrides := ws.overrideInputArgs(overrideOpts{ExcludeTerminal: true, OverridePaths: opts.OverridePaths})

	if err := checkFollows(terminalDir, ws.workspaceInputNames(terminal)); err != nil {
		return err
	}

	tmpl := opts.ApplyCmd
	if tmpl == "" {
		tmpl, err = ws.config.ApplyCommandTemplate()
		if err != nil {
			return err
		}
	}
	cmdArgs := substituteCommand(tmpl, terminalDir, shortHostname())
	if len(cmdArgs) == 0 {
		return fmt.Errorf("apply_command is empty")
	}

	if opts.ShowNixCommandsOnly {
		fmt.Fprintf(out, "cd %s && nix fmt\n", terminalDir)
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	if err := ws.checkNixDaemon(ctx); err != nil {
		return err
	}

	fmt.Fprintln(out, "  --== Formatting flake ==--  ")
	if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: terminalDir}); err != nil {
		return fmt.Errorf("nix fmt in %s: %w", terminalDir, err)
	}

	fmt.Fprintln(out, "  --== Applying flake ==--  ")
	allDirs := ws.allRepoDirs(opts.OverridePaths)
	rebuild, err := ws.needsRebuild(ctx, allDirs, opts.Force, out)
	if err != nil {
		return err
	}
	if !rebuild {
		return nil
	}

	oldProfile := readSystemProfile()
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalDir}); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	newProfile := readSystemProfile()
	if oldProfile != newProfile && newProfile != "" && commandExists("nvd") {
		fmt.Fprintln(out, "  --== Package changes ==--  ")
		_, _ = ws.runner.Run(ctx, "nvd", []string{"diff", oldProfile, newProfile}, exec.RunOptions{Dir: terminalDir})
	}

	return ws.markApplied(ctx, allDirs)
}

// allRepoDirs returns the clone dir for every declared repo, honoring overrides.
func (ws *Workspace) allRepoDirs(overrides map[string]string) []string {
	var dirs []string
	for _, key := range orderedRepoNames(ws.config.Repos) {
		dir := filepath.Join(ws.root, key)
		if ov, ok := overrides[key]; ok {
			dir = ov
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

const systemProfileLink = "/nix/var/nix/profiles/system"

// readSystemProfile resolves the current system profile to an absolute store
// path, or "" if it cannot be read.
func readSystemProfile() string {
	target, err := os.Readlink(systemProfileLink)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return target
	}
	return filepath.Join(filepath.Dir(systemProfileLink), target)
}

func commandExists(name string) bool {
	_, err := osexec.LookPath(name)
	return err == nil
}
```

In `helpers.go`, delete the now-unused `computeOverrideArgs` function entirely.

In `cli/workspace.go`, update only the apply command's `RunE` body:

```go
			return w.Apply(context.Background(), cmd.OutOrStdout(), workspace.ApplyOptions{})
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/workspace/ -run TestApply -v 2>&1 | tail` then `go -C modules/pn vet ./... && go -C modules/pn test ./... 2>&1 | tail`
Expected: apply tests PASS; vet clean; only the pre-existing `internal/exec` `/tmp` symlink test fails (unrelated).

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/apply.go modules/pn/internal/workspace/apply_test.go \
        modules/pn/internal/workspace/helpers.go modules/pn/internal/cli/workspace.go
git commit -m "feat(pn): apply terminal flake with daemon check, rebuild gate, nvd diff"
```

---

### Task 9: CLI — root resolution + build/apply flags

**Files:**

- Modify: `modules/pn/internal/cli/workspace.go`
- Test: `modules/pn/internal/cli/workspace_test.go` (create if absent)

- [ ] **Step 1: Write failing test** — `workspace_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceRoot_WalkUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	got, err := resolveWorkspaceRoot("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// macOS /tmp symlink: compare resolved forms.
	gotR, _ := filepath.EvalSymlinks(got)
	rootR, _ := filepath.EvalSymlinks(root)
	if gotR != rootR {
		t.Errorf("got %q want %q", gotR, rootR)
	}
}

func TestResolveWorkspaceRoot_FlagMissingToml(t *testing.T) {
	if _, err := resolveWorkspaceRoot(t.TempDir()); err == nil {
		t.Fatal("expected error when --root has no pn-workspace.toml")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go -C modules/pn test ./internal/cli/ -run TestResolveWorkspaceRoot 2>&1 | tail`
Expected: build failure — `resolveWorkspaceRoot` undefined.

- [ ] **Step 3: Implement**

In `cli/workspace.go`, add imports `"path/filepath"` and `"strings"` if absent, and replace `openWorkspace` plus add resolution:

```go
// openWorkspace opens the workspace by walking up from cwd (or PN_WORKSPACE_ROOT).
func openWorkspace() (*workspace.Workspace, error) { return openWorkspaceRoot("") }

// openWorkspaceRoot opens the workspace rooted via resolveWorkspaceRoot(rootFlag).
func openWorkspaceRoot(rootFlag string) (*workspace.Workspace, error) {
	root, err := resolveWorkspaceRoot(rootFlag)
	if err != nil {
		return nil, err
	}
	return workspace.Open(root, exec.NewRealRunner())
}

// resolveWorkspaceRoot resolves the workspace root: --root flag, then
// PN_WORKSPACE_ROOT, then the nearest ancestor of cwd containing pn-workspace.toml.
func resolveWorkspaceRoot(rootFlag string) (string, error) {
	check := func(dir string) (string, error) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if !fileExists(filepath.Join(abs, workspace.ConfigFileName)) {
			return "", fmt.Errorf("no %s in %s", workspace.ConfigFileName, abs)
		}
		return abs, nil
	}
	if rootFlag != "" {
		return check(rootFlag)
	}
	if env := os.Getenv("PN_WORKSPACE_ROOT"); env != "" {
		return check(env)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, workspace.ConfigFileName)) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in cwd or any ancestor", workspace.ConfigFileName)
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
```

Replace the `workspaceBuildCmd` and `workspaceApplyCmd` functions with flag-wired versions:

```go
func workspaceBuildCmd() *cobra.Command {
	var root, buildCmd string
	var overridePaths []string
	var showOnly bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the terminal flake with local workspace overrides",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspaceRoot(root)
			if err != nil {
				return err
			}
			ovr, err := workspace.ParseOverridePaths(overridePaths)
			if err != nil {
				return err
			}
			return w.Build(context.Background(), cmd.OutOrStdout(), workspace.BuildOptions{
				BuildCmd:            buildCmd,
				OverridePaths:       ovr,
				ShowNixCommandsOnly: showOnly,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "workspace root (default: PN_WORKSPACE_ROOT or walk up from cwd)")
	cmd.Flags().StringVar(&buildCmd, "build-cmd", "", "override build_command template")
	cmd.Flags().StringArrayVar(&overridePaths, "override-path", nil, "override a repo path: name=path (repeatable)")
	cmd.Flags().BoolVar(&showOnly, "show-nix-commands-only", false, "print commands without running")
	return cmd
}

func workspaceApplyCmd() *cobra.Command {
	var root, applyCmd string
	var overridePaths []string
	var showOnly, force bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply (activate) the terminal flake with local workspace overrides",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspaceRoot(root)
			if err != nil {
				return err
			}
			ovr, err := workspace.ParseOverridePaths(overridePaths)
			if err != nil {
				return err
			}
			return w.Apply(context.Background(), cmd.OutOrStdout(), workspace.ApplyOptions{
				ApplyCmd:            applyCmd,
				OverridePaths:       ovr,
				ShowNixCommandsOnly: showOnly,
				Force:               force,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "workspace root (default: PN_WORKSPACE_ROOT or walk up from cwd)")
	cmd.Flags().StringVar(&applyCmd, "apply-cmd", "", "override apply_command template")
	cmd.Flags().StringArrayVar(&overridePaths, "override-path", nil, "override a repo path: name=path (repeatable)")
	cmd.Flags().BoolVar(&showOnly, "show-nix-commands-only", false, "print commands without running")
	cmd.Flags().BoolVar(&force, "force", false, "always rebuild (bypass the unchanged-skip gate)")
	return cmd
}
```

Because the CLI calls it, export the override parser: in `overridepaths.go` add an exported wrapper:

```go
// ParseOverridePaths is the exported entry point for CLI flag parsing.
func ParseOverridePaths(specs []string) (map[string]string, error) { return parseOverridePaths(specs) }
```

- [ ] **Step 4: Run, verify pass**

Run: `go -C modules/pn test ./internal/cli/ -run TestResolveWorkspaceRoot -v 2>&1 | tail` then `go -C modules/pn test ./... 2>&1 | tail`
Expected: PASS; whole module green except the pre-existing `internal/exec` `/tmp` symlink test.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/cli/workspace.go modules/pn/internal/cli/workspace_test.go modules/pn/internal/workspace/overridepaths.go
git commit -m "feat(pn): workspace root resolution + build/apply flags"
```

---

### Task 10: workspace TOML + end-to-end smoke test

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/pn-workspace.toml`

- [ ] **Step 1: Add `[workspace]` terminal + commands**

Edit `/Users/phillipg/phillipg_mbp/pn-workspace.toml` `[workspace]` table to:

```toml
[workspace]
name = ""
description = ""
terminal = "phillipg-nix-ziprecruiter"
build_command = "darwin-rebuild build --flake {terminal_flake}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
```

(Leave the `[repos.*]` entries — including the four `input-name`s — unchanged.)

- [ ] **Step 2: Build the patched pn**

Run:

```bash
go -C /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/pn build -ldflags "-X main.Version=20260601-patched" -o /tmp/pn-patched ./cmd/pn
```

Expected: builds.

- [ ] **Step 3: Dry-run build and apply from the workspace root**

Run:

```bash
cd /Users/phillipg/phillipg_mbp
/tmp/pn-patched workspace build --show-nix-commands-only
/tmp/pn-patched workspace apply --show-nix-commands-only
```

Expected build output:

```
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter && nix fmt
darwin-rebuild build --flake /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter --override-input phillipgreenii-agent-support git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support --override-input phillipgreenii-nix-base git+file:///Users/phillipg/phillipg_mbp/phillipg-nix-repo-base --override-input phillipgreenii-nix-overlay git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-overlay --override-input phillipgreenii-personal git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-personal --override-input phillipgreenii-support-apps git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps
```

Expected apply output: same shape but `sudo darwin-rebuild switch --flake …#phillipg-mbp-02 …`.
The terminal (`phillipg-nix-ziprecruiter`) must NOT appear as an `--override-input`.

- [ ] **Step 4: Real build (optional, slow — verifies overrides resolve)**

Run: `cd /Users/phillipg/phillipg_mbp && /tmp/pn-patched workspace build`
Expected: `nix fmt` + a `darwin-rebuild build` that evaluates using the local clones. Stop here; do NOT auto-apply.

- [ ] **Step 5: No commit** (workspace is not a git repo). Report the dry-run output to the user and hand off the decision to `apply` for real.

---

## Self-review notes

- **Spec coverage:** terminal selection (Tasks 1,7,8), git+file:// override builder + `--override-path` + missing-clone skip (Task 4), templates + `{hostname}` (Task 3), `parse_overrides` (Task 2), `check_follows` (Task 5), daemon check + dirty-aware rebuild gate + `--force` + mark-applied (Task 6), `nvd` guarded diff (Task 8), root resolution + all flags (Task 9), workspace migration + verification (Task 10). The two documented simplifications (interactive daemon restart; custom signal handling) replace those spec lines.
- **Type consistency:** `BuildOptions`/`ApplyOptions` field names, `overrideOpts{ExcludeTerminal,OverridePaths}`, `overrideInputArgs`, `checkFollows(dir, names)`, `needsRebuild(ctx,dirs,force,out)`, `markApplied(ctx,dirs)`, `workspaceInputNames(terminal)` are used consistently across tasks. `Build`/`Apply` gain an `io.Writer` arg; CLI call sites updated in the same tasks (7/8) that change the signatures, then re-wired with flags in Task 9.
