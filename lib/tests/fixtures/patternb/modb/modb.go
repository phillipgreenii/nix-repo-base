// Package modb is a dependency-free fixture module. It exists only to be
// consumed by the sibling `moda` module through a local `replace`, exercising
// the Go builders' Pattern-B (local-replace) path from base's own flake checks.
// It is never shipped.
package modb

// Greeting returns a constant string. Trivial and lint-clean; moda imports it
// and moda's test asserts on the value.
func Greeting() string {
	return "hello from modb"
}
