package workspace

import (
	"os"
	"strings"
)

// substituteCommand expands {terminal_flake} and {hostname} in a command
// template and splits the result into argv on whitespace.
func substituteCommand(tmpl, terminalFlake, hostname string) []string {
	r := strings.NewReplacer("{terminal_flake}", terminalFlake, "{hostname}", hostname)
	return strings.Fields(r.Replace(tmpl))
}

// shortenHostname truncates a hostname at the first dot (mimics `hostname -s`).
func shortenHostname(h string) string {
	if i := strings.IndexByte(h, '.'); i >= 0 {
		return h[:i]
	}
	return h
}

// shortHostname returns the current host's short name.
func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return shortenHostname(h)
}
