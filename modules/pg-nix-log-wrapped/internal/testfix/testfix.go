// Package testfix builds synthetic nix --json-log-path streams in memory for
// tests of the failure diagnostics (pg2-sdbhh4.4). The message shapes are the
// ones observed in real nix logs (ANSI colours on msg, no "error:" prefix on
// raw_msg, no raw_msg on a hash mismatch); every store hash and name here is
// invented, so no real hostname, user name or store path is committed.
package testfix

import (
	"encoding/json"
	"fmt"
)

const (
	red     = "\x1b[31;1m"
	magenta = "\x1b[35;1m"
	reset   = "\x1b[0m"

	// PlaceholderHash is lib.fakeHash, the all-A "specified" hash.
	PlaceholderHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	// RealHash is a made-up non-placeholder hash.
	RealHash = "sha256-6LwWPILu4YczKIx9SsY22zpt6wE+8tN7aDIr4g7cRcw="
)

// Drv returns a drv store path with a 32-character fake hash derived from n.
func Drv(n int, name string) string {
	return "/nix/store/" + hash32(n) + "-" + name + ".drv"
}

func hash32(n int) string {
	const alphabet = "0123456789abcdfghijklmnpqrsvwxyz"
	b := make([]byte, 32)
	for i := range b {
		b[i] = alphabet[(n+i*7)%len(alphabet)]
	}
	return string(b)
}

// Out returns an output store path for the same fake hash scheme.
func Out(n int, name string) string { return "/nix/store/" + hash32(n+1) + "-" + name }

func enc(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Msg is a level-0 message with msg only (no raw_msg).
func Msg(level int, msg string) string {
	return enc(map[string]any{"action": "msg", "level": level, "msg": msg})
}

// MsgRaw is a message with both msg and raw_msg, as nix prints most errors.
func MsgRaw(level int, msg, raw string) string {
	return enc(map[string]any{"action": "msg", "level": level, "msg": msg, "raw_msg": raw})
}

// StartBuild starts a type-105 build activity for drv.
func StartBuild(id int, drv string) string {
	return enc(map[string]any{
		"action": "start", "id": id, "level": 3, "parent": 0, "type": 105,
		"text": "building '" + drv + "'", "fields": []any{drv, "", 1, 1},
	})
}

// Stop stops an activity.
func Stop(id int) string { return enc(map[string]any{"action": "stop", "id": id}) }

// StartWait starts a type-111 build-wait activity.
func StartWait(id int, out string) string {
	return enc(map[string]any{
		"action": "start", "id": id, "level": 3, "parent": 0, "type": 111,
		"text": "waiting for a machine to build '" + out + "'", "fields": []any{},
	})
}

// Plan is the "will be built" message.
func Plan(n int) string {
	return Msg(3, fmt.Sprintf("these %d derivations will be built:", n))
}

// HashMismatchMsg is the fixed-output hash failure: msg only, no raw_msg.
func HashMismatchMsg(drv, specified, got string) string {
	return Msg(0, red+"error:"+reset+" hash mismatch in fixed-output derivation '"+magenta+drv+reset+"':\n"+
		"         specified: "+magenta+specified+reset+"\n"+
		"            got:    "+magenta+got+reset)
}

// CannotBuild is the "Cannot build" failure for drv with reason, in the shape
// where the colour reset closes the reason.
func CannotBuild(drv, out, reason string) string {
	msg := red + "error:" + reset + " Cannot build '" + magenta + drv + reset + "'.\n       Reason: " +
		red + reason + reset + ".\n       Output paths:\n         " + magenta + out + reset
	raw := "Cannot build '" + magenta + drv + reset + "'.\nReason: " + red + reason + reset +
		".\nOutput paths:\n  " + magenta + out + reset
	return MsgRaw(0, msg, raw)
}

// HashMismatchRun is a fixed-output derivation failing its hash check and
// the dependent that never starts. Its stream, in order: plan, the
// fixed-output drv builds and stops, its hash-mismatch message, the
// dependent's "1 dependency failed" message.
func HashMismatchRun(specified string) []string {
	deps, top := Drv(10, "demo-fixture-1-deps"), Drv(20, "demo-fixture-1")
	return []string{
		Plan(2),
		StartBuild(1, deps),
		Stop(1),
		HashMismatchMsg(deps, specified, RealHash),
		CannotBuild(top, Out(20, "demo-fixture-1"), "1 dependency failed"),
	}
}

// EvalCacheBusyRun is a successful run that printed ignored eval-cache busy
// errors (three, over two distinct databases and one repeat) and two
// database-busy warnings. The user's name appears only in the invented path.
func EvalCacheBusyRun() []string {
	busy := func(h string) string {
		return Msg(0, red+"error (ignored):"+reset+" SQLite database '"+magenta+
			"/home/someone/.cache/nix/eval-cache-v6/"+h+".sqlite"+reset+"' is busy")
	}
	warn := Msg(0, "\x1b[35;1mwarning:"+reset+" SQLite database '"+magenta+"/nix/var/nix/db/db.sqlite"+reset+"' is busy")
	return []string{
		busy("aa11"), warn, busy("bb22"), busy("aa11"), warn,
	}
}

// EvalErrorRun is a flake check that fails to evaluate: an error with a
// nested "error:" and a store path, and no derivation involved.
func EvalErrorRun() []string {
	src := "/nix/store/" + hash32(30) + "-source/pin.nix"
	return []string{
		Msg(0, red+"error:"+reset+"\n       … while calling the '"+magenta+"import"+reset+"' builtin\n"+
			"         at «string»:5:5:\n\n       "+red+"error:"+reset+" path '"+magenta+src+reset+"' does not exist"),
	}
}

// InterruptedRun is what a real nix prints on SIGTERM or SIGINT before it
// exits 1: an eval-looking error first, then "interrupted by the user".
func InterruptedRun() []string {
	return []string{
		StartBuild(1, Drv(40, "slow-1")),
		Msg(0, red+"error:"+reset+" attribute 'missing' missing"),
		MsgRaw(0, red+"error:"+reset+" interrupted by the user", "interrupted by the user"),
	}
}

// KilledRun is a build still open when the process dies of a signal; nothing
// is said about why.
func KilledRun() []string {
	return []string{Plan(1), StartBuild(1, Drv(50, "slow-2"))}
}
