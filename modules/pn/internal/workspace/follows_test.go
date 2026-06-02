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
