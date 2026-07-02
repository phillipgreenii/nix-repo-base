package workspace

import "testing"

func TestWsidRegistry_DuplicateFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := checkWsidUnique("ws1", "/ws/a"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := checkWsidUnique("ws1", "/ws/a"); err != nil {
		t.Fatalf("same root re-claim must pass: %v", err)
	}
	if err := checkWsidUnique("ws1", "/ws/b"); err == nil {
		t.Fatal("a different root claiming the same wsid MUST fail")
	}
}
