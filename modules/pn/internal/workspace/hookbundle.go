// internal/workspace/hookbundle.go
package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// HookBundleState is the state of a checkout's per-clone hook bundle (design:
// per-clone hook bundle, sections 4.2 pointer rules and 4.5 staleness). The
// values and their precedence mirror `pg-hooks status` (modules/pg-hooks/
// pg-hooks/pg-hooks.sh cmd_status), but this reader works from the files
// directly and never needs `pg-hooks` on PATH.
type HookBundleState string

const (
	// HookBundlePresent: a usable bundle whose stamp and recorded overrides
	// still match the checkout.
	HookBundlePresent HookBundleState = "present"
	// HookBundleStale: a usable bundle, but the stamp inputs or a recorded
	// override (HEAD or dirty state) changed since it was built.
	HookBundleStale HookBundleState = "stale"
	// HookBundleMissing: no bundle and no usable legacy config.
	HookBundleMissing HookBundleState = "missing"
	// HookBundleBroken: a bundle exists but the pointer is invalid or bin/prek
	// or bin/pg-hooks-run is not executable.
	HookBundleBroken HookBundleState = "broken"
	// HookBundleUnreachable: git does not run hooks from <common-dir>/hooks
	// (a core.hooksPath bypasses them).
	HookBundleUnreachable HookBundleState = "unreachable"
	// HookBundleRelocated: the clone moved since the bundle was rooted.
	HookBundleRelocated HookBundleState = "relocated"
	// HookBundleLegacy: no bundle, but a usable .pre-commit-config.yaml (dual
	// mode, decision D1).
	HookBundleLegacy HookBundleState = "legacy"
)

// hookPointerRe is the only legal content of a pointer (`current`) file.
var hookPointerRe = regexp.MustCompile(`^gen-[0-9]+$`)

// HookOverride is one flake-input override recorded in a generation's
// source.json: the input name, the local path nix was pointed at, and that
// path's HEAD and dirty state at install time.
type HookOverride struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Head  string `json:"head"`
	Dirty bool   `json:"dirty"`
}

// HookBundleInfo is the detail behind a HookBundleState.
type HookBundleInfo struct {
	// Dir is the selected pg-hooks directory: the checkout's private one when
	// it holds a `current` pointer, else the shared one. Empty outside a repo.
	Dir string
	// Gen is the generation the pointer names (gen-N), empty when none.
	Gen string
	// Bundle is <Dir>/<Gen>/bundle, empty when there is no valid pointer.
	Bundle string
	// Private is true when Dir is a worktree-only directory: a linked worktree
	// that holds its own pointer. A canonical clone's single directory is the
	// shared one.
	Private bool
	// Linked is true when the checkout is a linked worktree.
	Linked bool
	// BrokenReason says why the state is broken.
	BrokenReason string
	// ClonePath and BuiltAt come from source.json.
	ClonePath string
	BuiltAt   string
	// StampDiffers is true when the stamp inputs changed since install.
	StampDiffers bool
	// Overrides are the recorded overrides; ChangedOverrides names those whose
	// HEAD or dirty state differs from now.
	Overrides        []HookOverride
	ChangedOverrides []string
	// UnreachableValue / UnreachableOrigin describe the core.hooksPath that
	// bypasses the hooks directory.
	UnreachableValue  string
	UnreachableOrigin string
}

// hookGitContext is the absolute git context of a checkout.
type hookGitContext struct {
	Common string // absolute common dir
	GitDir string // absolute git dir (equals Common in the canonical clone)
	Top    string // work tree root, empty for a bare repository
}

func (c hookGitContext) linked() bool { return c.Common != c.GitDir }

// canonical is the canonical clone path: the common dir's parent.
func (c hookGitContext) canonical() string { return filepath.Dir(c.Common) }

// runGitIn runs `git <args>` in dir and returns stdout (untrimmed). A non-zero
// exit is an error.
func runGitIn(dir string, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// readHookGitContext resolves the absolute git context of dir with one git
// process. Every path is requested with --path-format=absolute: the canonical
// clone otherwise yields a relative ".git".
func readHookGitContext(dir string) (hookGitContext, error) {
	out, err := runGitIn(dir, "", "rev-parse", "--path-format=absolute",
		"--git-common-dir", "--absolute-git-dir", "--show-toplevel")
	if err != nil {
		return hookGitContext{}, fmt.Errorf("hook bundle: %s is not a git work tree: %w", dir, err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		return hookGitContext{}, fmt.Errorf("hook bundle: unexpected git rev-parse output for %s: %q", dir, out)
	}
	c := hookGitContext{Common: lines[0], GitDir: lines[1]}
	if len(lines) >= 3 {
		c.Top = lines[2]
	}
	return c, nil
}

// isLinkedWorktree reports whether dir is a linked git worktree. A directory
// that does not exist, or is not a work tree, is not.
func isLinkedWorktree(dir string) bool {
	c, err := readHookGitContext(dir)
	return err == nil && c.linked()
}

// physicalDir resolves symlinks of an existing directory; any other path is
// returned unchanged (the same rule the shell side uses to compare directories
// across a symlinked prefix such as /tmp -> /private/tmp).
func physicalDir(p string) string {
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
	}
	return p
}

// Pointer read results (the same numbers the shell library returns).
const (
	hookPtrOK      = 0
	hookPtrInvalid = 12
	hookPtrNone    = 13
)

// readHookPointer reads <dir>/current. `current` must be a regular file (never
// a symlink) whose first line matches ^gen-[0-9]+$.
func readHookPointer(dir string) (string, int) {
	cur := filepath.Join(dir, "current")
	fi, err := os.Lstat(cur)
	if err != nil {
		return "", hookPtrNone
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return "", hookPtrInvalid
	}
	data, err := os.ReadFile(cur)
	if err != nil {
		return "", hookPtrInvalid
	}
	line, _, _ := strings.Cut(string(data), "\n")
	if !hookPointerRe.MatchString(line) {
		return "", hookPtrInvalid
	}
	return line, hookPtrOK
}

// executableFile reports whether p is a regular file with an execute bit.
func executableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// checkHookBundle classifies <dir>/<gen>/bundle: none when it has no
// bin/pg-hooks-run (also a dangling GC root), broken when bin/prek or
// bin/pg-hooks-run is not executable.
func checkHookBundle(bundle string) int {
	if _, err := os.Stat(filepath.Join(bundle, "bin", "pg-hooks-run")); err != nil {
		return hookPtrNone
	}
	if !executableFile(filepath.Join(bundle, "bin", "prek")) ||
		!executableFile(filepath.Join(bundle, "bin", "pg-hooks-run")) {
		return hookPtrInvalid
	}
	return hookPtrOK
}

// hookSourceJSON is a generation's source.json.
type hookSourceJSON struct {
	Stamp     string         `json:"stamp"`
	Overrides []HookOverride `json:"overrides"`
	ClonePath string         `json:"clone_path"`
	BuiltAt   string         `json:"built_at"`
}

func readHookSource(path string) (hookSourceJSON, bool) {
	var s hookSourceJSON
	data, err := os.ReadFile(path)
	if err != nil {
		return s, false
	}
	if json.Unmarshal(data, &s) != nil {
		return s, false
	}
	return s, true
}

// hookStampPaths reads meta.json's stampPaths (nil when absent).
func hookStampPaths(bundle string) []string {
	data, err := os.ReadFile(filepath.Join(bundle, "meta.json"))
	if err != nil {
		return nil
	}
	var m struct {
		StampPaths []string `json:"stampPaths"`
	}
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m.StampPaths
}

// hookStamp computes the staleness stamp (spec 4.5):
// `git ls-files -s -- <inputs> | git hash-object --stdin`, run in the work tree
// root. It reads the index, so a staged edit counts. "unknown" when none of the
// inputs is tracked.
func hookStamp(top string, inputs []string) (string, error) {
	args := append([]string{"ls-files", "-s", "--"}, inputs...)
	listing, err := runGitIn(top, "", args...)
	if err != nil {
		return "unknown", nil
	}
	listing = strings.TrimRight(listing, "\n")
	if listing == "" {
		return "unknown", nil
	}
	out, err := runGitIn(top, listing+"\n", "hash-object", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// overrideNow reads an override path's current HEAD and dirty state.
func overrideNow(path string) (head string, dirty bool) {
	if out, err := runGitIn(path, "", "rev-parse", "HEAD"); err == nil {
		head = strings.TrimSpace(out)
	}
	if out, err := runGitIn(path, "", "status", "--porcelain"); err == nil {
		dirty = strings.TrimSpace(out) != ""
	}
	return head, dirty
}

// changedHookOverrides names every recorded override whose HEAD differs from
// now or whose dirty state differs from the recorded one.
func changedHookOverrides(ovs []HookOverride) []string {
	var out []string
	for _, o := range ovs {
		head, dirty := overrideNow(o.Path)
		if head != o.Head || dirty != o.Dirty {
			out = append(out, o.Name)
		}
	}
	return out
}

// legacyHookConfig reports whether a usable legacy .pre-commit-config.yaml
// exists: the work tree's, else the canonical clone's.
func legacyHookConfig(c hookGitContext) bool {
	usable := func(p string) bool {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return false
		}
		f, err := os.Open(p)
		if err != nil {
			return false
		}
		_ = f.Close()
		return true
	}
	if c.Top != "" && usable(filepath.Join(c.Top, preCommitConfigName)) {
		return true
	}
	return usable(filepath.Join(c.canonical(), preCommitConfigName))
}

// hooksUnreachable reports whether git would NOT run hooks from
// <common-dir>/hooks because a core.hooksPath points elsewhere. A value that
// resolves to the common hooks dir is fine; a global or system one is fine too
// when it names a dispatcher (a directory holding an executable pre-commit that
// chains to <common-dir>/hooks).
func hooksUnreachable(dir string, c hookGitContext) (bool, string, string) {
	out, err := runGitIn(dir, "", "config", "--show-scope", "--show-origin", "--get", "core.hooksPath")
	if err != nil {
		return false, "", ""
	}
	parts := strings.SplitN(strings.TrimRight(out, "\n"), "\t", 3)
	if len(parts) != 3 || parts[2] == "" {
		return false, "", ""
	}
	scope, origin, value := parts[0], strings.TrimPrefix(parts[1], "file:"), parts[2]
	// git reports a repository config file relative (".git/config"); name it by
	// its absolute path.
	if !filepath.IsAbs(origin) {
		switch scope {
		case "local":
			origin = filepath.Join(c.Common, "config")
		case "worktree":
			origin = filepath.Join(c.GitDir, "config.worktree")
		}
	}
	var resolved string
	switch {
	case filepath.IsAbs(value):
		resolved = value
	case strings.HasPrefix(value, "~/"):
		resolved = filepath.Join(os.Getenv("HOME"), value[2:])
	default:
		base := c.Top
		if base == "" {
			base = dir
		}
		resolved = filepath.Join(base, value)
	}
	if physicalDir(resolved) == physicalDir(filepath.Join(c.Common, "hooks")) {
		return false, "", ""
	}
	if scope == "global" || scope == "system" {
		if executableFile(filepath.Join(resolved, "pre-commit")) {
			return false, "", ""
		}
	}
	return true, value, origin
}

// ReadHookBundleState reads the hook bundle state of the checkout at repoDir
// directly from <git-dir>/pg-hooks (`current`, `<gen>/source.json`,
// `<gen>/bundle/meta.json`), with no dependency on `pg-hooks` on PATH. The
// private bundle (a linked worktree's own) wins over the shared one, like the
// stub. Precedence: relocated > unreachable > broken > stale > present; with no
// usable bundle: unreachable, else legacy, else missing. An error means repoDir
// is not a git work tree (the state is empty).
func ReadHookBundleState(repoDir string) (HookBundleState, HookBundleInfo, error) {
	var info HookBundleInfo
	c, err := readHookGitContext(repoDir)
	if err != nil {
		return "", info, err
	}
	info.Linked = c.linked()
	shared := filepath.Join(c.Common, "pg-hooks")
	private := filepath.Join(c.GitDir, "pg-hooks")
	info.Dir = shared
	if _, err := os.Lstat(filepath.Join(private, "current")); err == nil {
		info.Dir = private
		info.Private = c.linked()
	}

	gen, ptr := readHookPointer(info.Dir)
	bundleRC := ptr
	if ptr == hookPtrOK {
		info.Gen = gen
		info.Bundle = filepath.Join(info.Dir, gen, "bundle")
		bundleRC = checkHookBundle(info.Bundle)
		if bundleRC == hookPtrInvalid {
			info.BrokenReason = "bin/prek or bin/pg-hooks-run is not executable"
		} else if bundleRC != hookPtrOK {
			info.Bundle = ""
		}
	} else if ptr == hookPtrInvalid {
		info.BrokenReason = fmt.Sprintf("pointer %s/current is invalid", info.Dir)
	}

	unreachable, urValue, urOrigin := hooksUnreachable(repoDir, c)
	info.UnreachableValue, info.UnreachableOrigin = urValue, urOrigin

	var src hookSourceJSON
	haveSrc := false
	if info.Gen != "" {
		src, haveSrc = readHookSource(filepath.Join(info.Dir, info.Gen, "source.json"))
		if haveSrc {
			info.ClonePath = src.ClonePath
			info.BuiltAt = src.BuiltAt
			info.Overrides = src.Overrides
		}
	}
	relocated := false
	if haveSrc && (bundleRC == hookPtrOK || bundleRC == hookPtrInvalid) &&
		src.ClonePath != "" && physicalDir(src.ClonePath) != physicalDir(c.Common) {
		relocated = true
	}

	switch {
	case relocated:
		return HookBundleRelocated, info, nil
	case unreachable:
		return HookBundleUnreachable, info, nil
	case bundleRC == hookPtrInvalid:
		return HookBundleBroken, info, nil
	case bundleRC == hookPtrOK:
		if hookBundleStale(&info, c, src, haveSrc) {
			return HookBundleStale, info, nil
		}
		return HookBundlePresent, info, nil
	case legacyHookConfig(c):
		return HookBundleLegacy, info, nil
	}
	return HookBundleMissing, info, nil
}

// hookBundleStale fills info.StampDiffers / info.ChangedOverrides and reports
// whether the bundle is stale: its stamp inputs changed, or a recorded
// override's HEAD or dirty state differs from now. A bundle with no readable
// source.json, or in a bare repository, is not reported stale.
func hookBundleStale(info *HookBundleInfo, c hookGitContext, src hookSourceJSON, haveSrc bool) bool {
	if !haveSrc || c.Top == "" {
		return false
	}
	inputs := append([]string{"flake.lock", "flake.nix"}, hookStampPaths(info.Bundle)...)
	cur, err := hookStamp(c.Top, inputs)
	if err == nil && cur != "" && cur != "unknown" && cur != src.Stamp {
		info.StampDiffers = true
	}
	info.ChangedOverrides = changedHookOverrides(src.Overrides)
	return info.StampDiffers || len(info.ChangedOverrides) > 0
}
