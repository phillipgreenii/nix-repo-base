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
					name, other, name, other, other,
				))
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
