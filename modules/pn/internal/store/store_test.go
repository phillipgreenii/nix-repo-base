package store

import (
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestEnv_ConfigHomeFallsBackToHome(t *testing.T) {
	e := Env{Home: "/h", XDGConfigHome: ""}
	if got := e.configHome(); got != "/h/.config" {
		t.Fatalf("configHome = %q, want /h/.config", got)
	}
	e2 := Env{Home: "/h", XDGConfigHome: "/x"}
	if got := e2.configHome(); got != "/x" {
		t.Fatalf("configHome = %q, want /x", got)
	}
}

func TestNew_ExposesRunner(t *testing.T) {
	f := exec.NewFakeRunner()
	s := New(f)
	if s.Runner() != f {
		t.Fatalf("Runner() did not return the runner passed to New")
	}
}
