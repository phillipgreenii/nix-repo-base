package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// InputSpec records the URL and "is-flake" status for one declared flake input.
type InputSpec struct {
	// URL is the remote URL declared for this input (may be "" for follows-only inputs).
	URL string
	// Flake reports whether this input is a flake (true) or a non-flake source (false).
	// Non-flake inputs (flake=false) are skipped during edge discovery.
	Flake bool
}

// gatherInputURLs returns, for each workspace repo present on disk, a map of
// alias → InputSpec for all declared flake inputs.
//
// The evaluation tries three approaches in order:
//  1. Full: nix eval --json --file <flake> inputs --apply
//     'is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is'
//  2. Fallback: same but without relying on v.flake (assumes true for all).
//  3. Last resort: attrNames only (all aliases, no URLs — no edges generated for this repo).
//
// Returns the map keyed by repoKey. Repos without a resolvable flake_path
// or whose eval fails entirely contribute an empty inner map (no edges).
//
// The second return value lists repos whose flake was present on disk but
// failed EVERY eval tier (case 3 below). It distinguishes an eval failure —
// which must be fatal for the persisted lock (bead pg2-cqcex), because pn
// cannot prove the un-evaluable repo has no workspace inputs — from the
// legitimately-empty cases (no flake_path, or not cloned), which are excluded.
func (ws *Workspace) gatherInputURLs(ctx context.Context) (map[string]map[string]InputSpec, []string, error) {
	result := make(map[string]map[string]InputSpec)
	var evalFailed []string
	// Alpha (not topoAlpha): gatherInputURLs feeds buildEdges which feeds the
	// lock — using topoAlpha would be circular.
	names := orderedRepoNames(ws.config.Repos)

	for _, key := range names {
		fp := ws.resolveFlakePath(key)
		if fp == "" {
			result[key] = nil
			continue
		}
		absFlake := filepath.Join(ws.root, key, fp)
		if _, err := os.Stat(absFlake); err != nil {
			result[key] = nil
			continue
		}

		specs, err := evalInputSpecs(ctx, ws.runner, absFlake)
		if err != nil {
			// Log and continue; this repo contributes no edges. Record it as an
			// eval failure (flake present on disk, all tiers exhausted) so the
			// caller can gate the persisted lock rather than silently omit its
			// override edges. Cases 1/2 above (no flake_path, not cloned) are
			// legitimately empty and MUST NOT be recorded here.
			fmt.Fprintf(os.Stderr, "pn: warn: gatherInputURLs %s: %v\n", key, err)
			result[key] = nil
			evalFailed = append(evalFailed, key)
			continue
		}
		result[key] = specs
	}
	sort.Strings(evalFailed)
	return result, evalFailed, nil
}

// evalInputSpecs evaluates a flake's inputs and returns alias → InputSpec.
// It tries the full expression first, then the fallback.
func evalInputSpecs(ctx context.Context, runner exec.Runner, absFlakePath string) (map[string]InputSpec, error) {
	// Attempt 1: full evaluation with url and flake fields.
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	res, err := runner.Run(ctx, "nix", []string{
		"eval", "--json", "--file", absFlakePath, "inputs", "--apply", fullExpr,
	}, exec.RunOptions{})
	if err == nil {
		specs, parseErr := parseInputSpecJSON(res.Stdout)
		if parseErr == nil {
			return specs, nil
		}
	}

	// Attempt 2: fallback — assume flake=true for all inputs.
	fallbackExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = true; }) is`
	res, err = runner.Run(ctx, "nix", []string{
		"eval", "--json", "--file", absFlakePath, "inputs", "--apply", fallbackExpr,
	}, exec.RunOptions{})
	if err == nil {
		specs, parseErr := parseInputSpecJSON(res.Stdout)
		if parseErr == nil {
			fmt.Fprintf(os.Stderr, "pn: info: evalInputSpecs %s: used fallback expression\n", absFlakePath)
			return specs, nil
		}
	}

	// Last resort: attrNames only; no URLs — no edges for this flake.
	attrNamesExpr := "builtins.attrNames"
	res, err = runner.Run(ctx, "nix", []string{
		"eval", "--json", "--file", absFlakePath, "inputs", "--apply", attrNamesExpr,
	}, exec.RunOptions{})
	if err == nil {
		var names []string
		if json.Unmarshal(res.Stdout, &names) == nil {
			fmt.Fprintf(os.Stderr, "pn: warn: evalInputSpecs %s: fell back to attrNames-only; no URLs available, no edges generated\n", absFlakePath)
			specs := make(map[string]InputSpec, len(names))
			for _, n := range names {
				specs[n] = InputSpec{Flake: true}
			}
			return specs, nil
		}
	}

	return nil, fmt.Errorf("all eval attempts failed for %s", absFlakePath)
}

// parseInputSpecJSON parses the JSON output of the full/fallback nix eval expression.
// Expected format: {"alias": {"url": "...", "flake": true/false}, ...}
func parseInputSpecJSON(data []byte) (map[string]InputSpec, error) {
	var raw map[string]struct {
		URL   interface{} `json:"url"`
		Flake bool        `json:"flake"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse input specs JSON: %w", err)
	}
	out := make(map[string]InputSpec, len(raw))
	for alias, v := range raw {
		var url string
		if v.URL != nil {
			switch u := v.URL.(type) {
			case string:
				url = u
			}
		}
		out[alias] = InputSpec{URL: url, Flake: v.Flake}
	}
	return out, nil
}

// buildEdges constructs the edge list from gathered input URL specs.
// For each (consumer, alias, InputSpec) where Flake==true and URL != "" and
// canonicalURL(URL) matches some workspace repo's canonical remote URL, emit
// a LockEdge.
//
// The remoteURLs parameter maps repoKey → canonical URL (pre-computed from
// the config's url/remotes fields).
//
// Also returns a topological order over the resulting edge set (Kahn's algorithm,
// same as topoSortByDeps).
//
// Errors: if two workspace repos share the same canonical URL (duplicate_remote_url),
// returns a non-nil error naming both repos.
func buildEdges(
	repos map[string]RepoConfig,
	inputURLs map[string]map[string]InputSpec,
) ([]LockEdge, []string, error) {
	// Alpha (not topoAlpha): buildEdges constructs the lock — cannot depend
	// on the lock-derived topological order.
	repoKeys := orderedRepoNames(repos)

	// Build canonical URL → repo key index, detecting duplicates.
	canonByKey := make(map[string]string, len(repoKeys)) // repoKey → canonical
	for _, key := range repoKeys {
		r := repos[key]
		raw := displayURL(r)
		if raw == "" {
			continue
		}
		canonByKey[key] = canonicalURL(raw)
	}

	// Check for duplicate canonical URLs.
	seen := make(map[string]string) // canonical → first repoKey
	for key, canon := range canonByKey {
		if canon == "" {
			continue
		}
		if existing, dup := seen[canon]; dup {
			return nil, nil, fmt.Errorf("duplicate_remote_url: repos %q and %q both resolve to %q", existing, key, canon)
		}
		seen[canon] = key
	}

	// Invert: canonical URL → repoKey.
	keyByCanon := make(map[string]string, len(seen))
	for canon, key := range seen {
		keyByCanon[canon] = key
	}

	var edges []LockEdge

	for _, consumer := range repoKeys {
		specs := inputURLs[consumer]
		if specs == nil {
			continue
		}

		// Collect aliases in sorted order for deterministic output.
		aliases := make([]string, 0, len(specs))
		for alias := range specs {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)

		for _, alias := range aliases {
			spec := specs[alias]
			if !spec.Flake || spec.URL == "" {
				continue // skip non-flake or follow-only inputs
			}
			canon := canonicalURL(spec.URL)
			if canon == "" {
				continue
			}
			targetKey, ok := keyByCanon[canon]
			if !ok || targetKey == consumer {
				continue // no workspace match, or self-edge
			}
			edges = append(edges, LockEdge{
				Consumer: consumer,
				Alias:    alias,
				Target:   targetKey,
			})
		}
	}

	// Compute topological order over the edge set.
	dependsOn := edgesToDependsOn(edges, repoKeys)
	order := topoSortByDeps(repoKeys, dependsOn)

	return edges, order, nil
}

// edgesToDependsOn converts a []LockEdge into the dependsOn map used by
// topoSortByDeps. For each edge (consumer, alias, target), consumer depends on target.
func edgesToDependsOn(edges []LockEdge, repoKeys []string) map[string][]string {
	// Use a set to avoid duplicates.
	depSet := make(map[string]map[string]struct{})
	for _, key := range repoKeys {
		depSet[key] = make(map[string]struct{})
	}
	for _, e := range edges {
		depSet[e.Consumer][e.Target] = struct{}{}
	}
	dependsOn := make(map[string][]string)
	for key, targets := range depSet {
		if len(targets) == 0 {
			continue
		}
		deps := make([]string, 0, len(targets))
		for t := range targets {
			deps = append(deps, t)
		}
		sort.Strings(deps)
		dependsOn[key] = deps
	}
	return dependsOn
}
