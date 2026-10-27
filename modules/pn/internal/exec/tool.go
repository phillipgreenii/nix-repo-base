package exec

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
)

// SystemProfileBinDirs are the system-profile directories searched, after PATH,
// for a bare tool name (darwin-rebuild, nixos-rebuild, nix) that PATH lacks. Both
// exist on a nix-darwin machine: /run/current-system is a symlink to the active
// generation and /nix/var/nix/profiles/system is the profile it resolves to, so
// the second is the fallback when the first symlink is absent (early boot, a
// non-activated system).
var SystemProfileBinDirs = []string{
	"/run/current-system/sw/bin",
	"/nix/var/nix/profiles/system/sw/bin",
}

// ToolNotFoundExitCode is the process exit status for a ToolNotFoundError. It is
// deliberately NOT 127 (what a shell reports for "command not found" and what the
// failure this guards against used to exit with), so a pre-flight refusal is
// countable apart from the old instant-failure signature. 69 is sysexits.h
// EX_UNAVAILABLE ("a service is unavailable").
const ToolNotFoundExitCode = 69

// ToolNotFoundError is returned by ResolveTool when a bare tool name is on
// neither PATH nor any of the fallback directories.
type ToolNotFoundError struct {
	Tool  string
	Tried []string // the fallback directories searched after PATH
}

func (e *ToolNotFoundError) Error() string {
	return fmt.Sprintf("%s not found on PATH (also tried %s); run from a shell with the system profile on PATH",
		e.Tool, strings.Join(e.Tried, ", "))
}

// ResolveTool makes sure the bare command name is runnable. It returns:
//
//   - (nil, nil) when name is already found on PATH, or is not a bare name (it
//     contains a path separator): nothing to change.
//   - (env, nil) when PATH lacks it but one of dirs has an executable of that
//     name: env is a RunOptions.Env overlay whose PATH is the current PATH with
//     that directory appended. The caller passes it to the Run of the SAME bare
//     name. argv is deliberately not rewritten to an absolute path: the nix
//     wrapper decorator recognises only bare names, so an absolute path would run
//     unwrapped and lose its telemetry (ADR 0028).
//   - (nil, *ToolNotFoundError) when it is nowhere.
//
// The only side effects are the stat calls of the lookup.
func ResolveTool(name string, dirs []string) (map[string]string, error) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return nil, nil
	}
	if _, err := osexec.LookPath(name); err == nil {
		return nil, nil
	}
	for _, dir := range dirs {
		if isExecutableFile(filepath.Join(dir, name)) {
			path := os.Getenv("PATH")
			if path == "" {
				path = dir
			} else {
				path += string(os.PathListSeparator) + dir
			}
			return map[string]string{"PATH": path}, nil
		}
	}
	return nil, &ToolNotFoundError{Tool: name, Tried: append([]string{}, dirs...)}
}

// IsToolNotFound reports whether err is (or wraps) a ToolNotFoundError.
func IsToolNotFound(err error) bool {
	var tnf *ToolNotFoundError
	return errors.As(err, &tnf)
}

// lookPathIn resolves a bare name against an explicit PATH value, like
// exec.LookPath but without consulting the process environment.
func lookPathIn(name, pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, name); isExecutableFile(p) {
			return p, nil
		}
	}
	return "", &osexec.Error{Name: name, Err: osexec.ErrNotFound}
}
