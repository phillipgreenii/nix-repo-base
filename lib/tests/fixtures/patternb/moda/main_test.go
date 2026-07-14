package main

import "testing"

// TestGreeting exercises the Pattern-B path end to end: it can only compile if
// buildGoApplication resolved the local `replace => ../modb` and ran the test
// from moda's module root (modRoot). Asserts moda sees modb's exported value.
func TestGreeting(t *testing.T) {
	got := greeting()
	want := "hello from modb"
	if got != want {
		t.Errorf("greeting() = %q, want %q", got, want)
	}
}
