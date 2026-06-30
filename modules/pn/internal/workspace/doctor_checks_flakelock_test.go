// internal/workspace/doctor_checks_flakelock_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckFlakeLockFresh_StaleIsError(t *testing.T) {
	root := t.TempDir()
	consumer := filepath.Join(root, "consumer")
	initRealRepo(t, consumer)
	// consumer's flake.lock pins input "dep" at an OLD rev.
	old := "1111111111111111111111111111111111111111"
	lock := `{"nodes":{"root":{"inputs":{"dep":"dep"}},"dep":{"locked":{"rev":"` + old + `"}}}}`
	if err := os.WriteFile(filepath.Join(consumer, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "2222222222222222222222222222222222222222"
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"consumer": {URL: "u1", Branch: "main"}, "dep": {URL: "u2", Branch: "main"}}},
		lock: &Lock{
			Repos: map[string]LockRepoEntry{"consumer": {FlakePath: "flake.nix"}, "dep": {FlakePath: "flake.nix"}},
			Edges: []LockEdge{{Consumer: "consumer", Alias: "dep", Target: "dep"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", lock: ws.lock,
		refRev: map[string]string{"dep": want, "consumer": "x"}, skipped: map[string]bool{}}
	fs := ws.checkFlakeLockFresh(context.Background(), env)
	if !hasFindingForRepo(fs, "flake-lock-fresh", "consumer", SevError) {
		t.Fatalf("stale flake.lock should be error: %+v", fs)
	}
}
