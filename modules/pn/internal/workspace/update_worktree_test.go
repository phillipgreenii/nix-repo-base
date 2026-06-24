package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestPrimaryMainState(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:o/foo\"\n")
	foo := filepath.Join(root, "foo")

	cases := []struct {
		name   string
		branch string
		exit   int // exit code of diff --quiet (0 clean, 1 dirty)
		want   primaryState
	}{
		{"clean main", "main", 0, primaryOnCleanMain},
		{"other branch", "feature-x", 0, primaryOnOtherBranch},
		{"dirty main", "main", 1, primaryOnDirtyMain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := exec.NewFakeRunner()
			f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"},
				exec.Result{Stdout: []byte(tc.branch + "\n")}, nil)
			if tc.branch == "main" {
				var derr error
				if tc.exit != 0 {
					derr = &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: tc.exit}}
				}
				f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: tc.exit}, derr)
				if tc.exit == 0 {
					f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
				}
			}
			w, _ := Open(root, f)
			if got := w.primaryMainState(context.Background(), foo); got != tc.want {
				t.Errorf("primaryMainState = %v, want %v", got, tc.want)
			}
		})
	}
}
