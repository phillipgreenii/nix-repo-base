package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestTree_PrintsWorkspaceAndRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
name = "phillipg"

[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, "pn-workspace.lock"), `{"repos":{"foo":{"url":"github:owner/foo","rev":"abc1234"}}}`)

	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var buf bytes.Buffer
	if err := w.Tree(context.Background(), &buf, TreeOptions{}); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "phillipg\n") {
		t.Errorf("expected workspace name as root; got:\n%s", out)
	}
	if !strings.Contains(out, "bar") {
		t.Errorf("expected bar in output; got:\n%s", out)
	}
	if !strings.Contains(out, "foo") {
		t.Errorf("expected foo in output; got:\n%s", out)
	}
	if !strings.Contains(out, "abc1234") {
		t.Errorf("expected locked rev abc1234 to appear; got:\n%s", out)
	}
	// last line uses └── connector
	if !strings.Contains(out, "└── ") {
		t.Errorf("expected └── connector for last entry; got:\n%s", out)
	}
}
