package workspace

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// readGitRemotes runs `git -C <repoDir> remote -v` and returns a map
// remote-name -> URL (first fetch entry per name wins; fetch and push URLs
// are assumed to agree, matching git's normal configuration). Returns an
// empty map (no error) when git fails — caller may surface a warning.
func readGitRemotes(ctx context.Context, runner exec.Runner, repoDir string) (map[string]string, error) {
	res, err := runner.Run(ctx, "git",
		[]string{"-C", repoDir, "remote", "-v"},
		exec.RunOptions{})
	if err != nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// Format: "<name>\t<url> (fetch|push)"
		nameTab := strings.IndexByte(line, '\t')
		if nameTab < 0 {
			continue
		}
		name := line[:nameTab]
		rest := line[nameTab+1:]
		// Strip the trailing " (fetch)" or " (push)".
		sp := strings.LastIndexByte(rest, ' ')
		if sp < 0 {
			continue
		}
		url := rest[:sp]
		// First fetch entry per name wins.
		if _, exists := out[name]; !exists {
			out[name] = url
		}
	}
	return out, nil
}
