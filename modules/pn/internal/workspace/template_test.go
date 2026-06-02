package workspace

import (
	"reflect"
	"testing"
)

func TestSubstituteCommand(t *testing.T) {
	got := substituteCommand("sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}", "/ws/leaf", "host01")
	want := []string{"sudo", "darwin-rebuild", "switch", "--flake", "/ws/leaf#host01"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestSubstituteCommand_NoPlaceholders(t *testing.T) {
	got := substituteCommand("echo hello", "/ws/leaf", "host01")
	want := []string{"echo", "hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestShortenHostname(t *testing.T) {
	if got := shortenHostname("phillipg-mbp-02.local"); got != "phillipg-mbp-02" {
		t.Errorf("got %q", got)
	}
	if got := shortenHostname("plainhost"); got != "plainhost" {
		t.Errorf("got %q", got)
	}
}
