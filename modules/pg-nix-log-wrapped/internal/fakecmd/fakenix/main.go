// Command fakenix is the stand-in nix child used by tests. NewDir builds it
// once per test process WITHOUT -race, so launching it is cheap (the
// race-instrumented test binary costs about 1.4 s per exec).
package main

import "github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"

func main() {
	if !fakecmd.Run() {
		// Started under a name that does not begin with "fake-".
		panic("fakenix must be executed through a fake-* name")
	}
}
