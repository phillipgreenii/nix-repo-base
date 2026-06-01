package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// readFlakeInputs runs `nix eval --json --file <repoDir>/flake.nix inputs` and
// returns a map of top-level input name -> input URL (the url field on each
// input). Returns an empty map (and no error) when:
//   - flake.nix is missing — the repo isn't a flake host (e.g. a vendored
//     non-flake repo in the workspace).
//   - nix eval fails or returns malformed JSON — the repo contributes no
//     out-edges to the dep graph. Errors are not logged.
//
// Higher layers turn the input URLs into github slugs via ExtractGithubSlug
// and match them against workspace repos' SlugSets.
func readFlakeInputs(ctx context.Context, runner exec.Runner, repoDir string) (map[string]string, error) {
	flakePath := filepath.Join(repoDir, "flake.nix")
	if _, err := os.Stat(flakePath); err != nil {
		return map[string]string{}, nil
	}
	res, err := runner.Run(ctx, "nix",
		[]string{"eval", "--json", "--file", flakePath, "inputs"},
		exec.RunOptions{})
	if err != nil {
		return map[string]string{}, nil
	}
	// Unmarshal as a top-level object whose values may have a "url" field.
	// We tolerate values that are themselves objects (some inputs are
	// followers-only and may have no url field).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(raw))
	for k, rv := range raw {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rv, &obj); err != nil {
			continue
		}
		var url string
		if rawURL, ok := obj["url"]; ok {
			_ = json.Unmarshal(rawURL, &url)
		}
		if url != "" {
			out[k] = url
		}
	}
	return out, nil
}
