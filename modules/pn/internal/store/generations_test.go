package store

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func gens() []generation {
	return []generation{
		{Num: 1, Date: "2024-01-01", Current: false}, // ancient
		{Num: 2, Date: "2099-01-01", Current: true},  // current
	}
}

func TestGenerationsToPrune_CountProtectsAll(t *testing.T) {
	// keepCount=3 over 2 gens → top 3 protects both → nothing pruned
	got := generationsToPrune(gens(), 14, 3, time.Now())
	if len(got) != 0 {
		t.Fatalf("expected none pruned, got %v", got)
	}
}

func TestGenerationsToPrune_AggressivePrunesGen1(t *testing.T) {
	// keepDays=0 (time off), keepCount=1 → only current protected → gen1 pruned
	got := generationsToPrune(gens(), 0, 1, time.Now())
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected [1], got %v", got)
	}
}

func TestGenerationsToPrune_KeepCountZeroDisablesCount(t *testing.T) {
	got := generationsToPrune(gens(), 0, 0, time.Now())
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected [1], got %v", got)
	}
}

func TestGenerationsToPrune_TimeProtects(t *testing.T) {
	now := time.Now()
	g := []generation{
		{Num: 1, Date: now.Add(-3 * 24 * time.Hour).Format("2006-01-02"), Current: false},
		{Num: 2, Date: now.Format("2006-01-02"), Current: true},
	}
	// keepDays=7 protects gen1 (3d old); keepCount=0 → gen1 kept by time
	if got := generationsToPrune(g, 7, 0, now); len(got) != 0 {
		t.Fatalf("expected none (time-protected), got %v", got)
	}
}

func TestGenerationsToPrune_TimeBoundaryLocal(t *testing.T) {
	// DETERMINISTIC now at local noon (never use time.Now() here — flaky at
	// midnight). bash's cutoff carries the current time-of-day:
	//   cutoff = now - keepDays*24h = midnight(today-keepDays) + 12h.
	// A gen dated EXACTLY keepDays ago is at LOCAL midnight(today-keepDays),
	// which is < cutoff → PRUNED (matches bash + the impl). A gen one day newer
	// is > cutoff → protected. This guards the ParseInLocation(..., Local) fix:
	// a UTC parse would shift the gen timestamp by the TZ offset and flip the
	// boundary result.
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.Local)
	cur := generation{Num: 2, Date: now.Format("2006-01-02"), Current: true}

	boundary := []generation{{Num: 1, Date: now.AddDate(0, 0, -7).Format("2006-01-02")}, cur}
	if got := generationsToPrune(boundary, 7, 0, now); len(got) != 1 || got[0] != 1 {
		t.Fatalf("gen at exactly keepDays boundary should be pruned (matches bash), got %v", got)
	}
	inside := []generation{{Num: 1, Date: now.AddDate(0, 0, -6).Format("2006-01-02")}, cur}
	if got := generationsToPrune(inside, 7, 0, now); len(got) != 0 {
		t.Fatalf("gen within keepDays window should be protected, got %v", got)
	}
}

func TestGenerationsToPrune_UnparseableDate(t *testing.T) {
	// A generation with unparseable date, keepDays > 0, not count-protected,
	// not current → it IS pruned (unparseable date is NOT time-protected).
	g := []generation{
		{Num: 1, Date: "garbage", Current: false},
		{Num: 2, Date: "2099-01-01", Current: true},
	}
	got := generationsToPrune(g, 7, 0, time.Now())
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected [1] (unparseable not time-protected), got %v", got)
	}
}

func TestListGenerations_ParsesCurrent(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix-env", []string{"--profile", "/p", "--list-generations"}, exec.Result{Stdout: []byte(
		"   1   2024-01-01 12:00:00\n   2   2099-01-01 12:00:00   (current)\n")}, nil)
	g, err := listGenerations(context.Background(), f, "/p", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 2 || g[0].Num != 1 || g[0].Date != "2024-01-01" || !g[1].Current {
		t.Fatalf("parsed wrong: %+v", g)
	}
}

func TestListGenerations_Sudo(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sudo", []string{"nix-env", "--profile", "/p", "--list-generations"}, exec.Result{Stdout: []byte(
		"   1   2024-01-01 12:00:00   (current)\n")}, nil)
	g, err := listGenerations(context.Background(), f, "/p", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 1 || g[0].Num != 1 || g[0].Date != "2024-01-01" || !g[0].Current {
		t.Fatalf("parsed wrong with sudo=true: %+v", g)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "sudo" {
		t.Fatalf("expected call recorded with name 'sudo', got %+v", calls)
	}
}

func TestListGenerations_SkipsMalformed(t *testing.T) {
	// blank line, single-token line, non-integer-first-field → all skipped
	// one valid line → returned
	stdout := "\n" +
		"   onlyonetoken\n" +
		"   notanint 2024-01-01 12:00:00\n" +
		"   1   2024-06-01 12:00:00   (current)\n"
	f := exec.NewFakeRunner()
	f.AddResponse("nix-env", []string{"--profile", "/p", "--list-generations"},
		exec.Result{Stdout: []byte(stdout)}, nil)
	g, err := listGenerations(context.Background(), f, "/p", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 1 || g[0].Num != 1 || g[0].Date != "2024-06-01" || !g[0].Current {
		t.Fatalf("expected only the valid generation, got %+v", g)
	}
}

func TestPruneGenerations_Sudo(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sudo", []string{"nix-env", "--profile", "/p", "--delete-generations", "1", "3"}, exec.Result{}, nil)
	if err := pruneGenerations(context.Background(), f, "/p", []int{1, 3}, true); err != nil {
		t.Fatal(err)
	}
}
