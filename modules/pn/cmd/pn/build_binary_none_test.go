//go:build !realnix && !realbin

package main

import "testing"

// runIntegrationTests is the default-gate form: no real binary is built, so the
// suite is just the unit tests. The tagged form (build_binary_test.go) builds pn.
func runIntegrationTests(m *testing.M) int { return m.Run() }
