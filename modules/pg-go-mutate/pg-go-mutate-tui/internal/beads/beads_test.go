package beads

import "testing"

type fakeBd struct {
	created []struct {
		title, body string
		labels      []string
		priority    int
	}
}

func (f *fakeBd) Create(title, body string, labels []string, priority int) (string, error) {
	f.created = append(f.created, struct {
		title, body string
		labels      []string
		priority    int
	}{title, body, labels, priority})
	return "pg2-fake1", nil
}

func TestFileTriageBeadUsesCanonicalLabelNotPathDerived(t *testing.T) {
	bd := &fakeBd{}
	_, err := FileTriageBead(bd, "pkg/internal/gate", map[string]int{"done": 3, "failed": 1}, "base")
	if err != nil {
		t.Fatal(err)
	}
	labels := bd.created[0].labels
	found := false
	for _, l := range labels {
		if l == "base" {
			found = true
		}
		if l == "phillipg-nix-repo-base" {
			t.Fatal("must not use the raw project-key path component as the label")
		}
	}
	if !found {
		t.Fatal("must carry the canonical repo label")
	}
}

func TestFileTriageBeadIsPriorityThreeAndNeverAnEpic(t *testing.T) {
	bd := &fakeBd{}
	FileTriageBead(bd, "pkg", map[string]int{"done": 1}, "base")
	if bd.created[0].priority != 3 {
		t.Fatalf("expected P3, got P%d", bd.created[0].priority)
	}
}

func TestFileTriageBeadBodyCarriesStatusTallyNeverASurvivorCount(t *testing.T) {
	bd := &fakeBd{}
	FileTriageBead(bd, "pkg", map[string]int{"done": 3, "timeout": 1}, "base")
	body := bd.created[0].body
	if !contains(body, "done: 3") || !contains(body, "timeout: 1") {
		t.Fatal("body must carry the status tally")
	}
	if contains(body, "survived") || contains(body, "killed") {
		t.Fatal("body must never carry a survivor/kill count")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
