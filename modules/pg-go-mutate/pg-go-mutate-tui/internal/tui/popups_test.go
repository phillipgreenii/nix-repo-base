package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// escKey is a sentinel rune value passed to keyMsg to request an "esc" key
// message rather than a plain-letter rune message. It cannot collide with a
// real letter key.
const escKey rune = 0

// keyMsg builds a tea.KeyMsg for use in tests. For a plain letter rune r, the
// resulting message's String() matches what primary.go's Update switches on
// (e.g. keyMsg('Q').String() == "Q"). For the escKey sentinel, it produces the
// "esc" key message instead.
func keyMsg(r rune) tea.KeyMsg {
	if r == escKey {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestRenderHistoryShowsKillSurvivedCounts(t *testing.T) {
	s := HistoryState{Rows: []HistoryRow{{Status: "done", File: "pb/auth.go", Killed: 9, Survived: 2}}}
	out := RenderHistory(s)
	if !strings.Contains(out, "killed 9") || !strings.Contains(out, "survived 2") {
		t.Fatalf("History popup must show per-run kill/survived counts (design's exempted case), got:\n%s", out)
	}
}

func TestRenderQueueGroupsByPackage(t *testing.T) {
	s := QueueState{Packages: []QueuePackageRow{{PkgPath: "pb/internal/gate", Files: []string{"session.go", "config.go"}}}}
	out := RenderQueue(s)
	if !strings.Contains(out, "pb/internal/gate") || !strings.Contains(out, "session.go") {
		t.Fatalf("expected package grouping, got:\n%s", out)
	}
}

func TestQKeySwitchesToQueueScreenAndEscReturnsToPrimary(t *testing.T) {
	m := NewModel(PrimaryState{})
	m2, _ := m.Update(keyMsg('Q'))
	updated, ok := m2.(Model)
	if !ok || updated.Screen != ScreenQueue {
		t.Fatal("Q must switch to the queue screen")
	}
	m3, _ := updated.Update(keyMsg(escKey))
	returned, ok := m3.(Model)
	if !ok || returned.Screen != ScreenPrimary {
		t.Fatal("esc must return to the primary screen")
	}
}

func TestHAndBKeysSwitchToHistoryAndBeadsScreensRespectively(t *testing.T) {
	m := NewModel(PrimaryState{})
	m2, _ := m.Update(keyMsg('H'))
	if updated, ok := m2.(Model); !ok || updated.Screen != ScreenHistory {
		t.Fatal("H must switch to the history screen")
	}
	m3, _ := m.Update(keyMsg('B'))
	if updated, ok := m3.(Model); !ok || updated.Screen != ScreenBeads {
		t.Fatal("B must switch to the beads screen")
	}
}
