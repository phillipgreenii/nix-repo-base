package app

// Shim mode: the same binary, entered under the name nix or darwin-rebuild
// (a symlink on PATH), decides per call whether to wrap the real tool.
//
//	disabled -> exec real
//	PG_NIX_LOG_ACTIVE set -> exec real (an outer wrapped run already covers us)
//	verb not on the allowlist (or a completion call) -> exec real
//	otherwise -> wrap the real tool like the explicit wrapper does
//
// A passthrough is silent and leaves the environment untouched;
// PG_NIX_LOG_DEBUG=1 names the branch taken. RunShim never calls
// config.ParseArgs: nix's own flags (nix --version, nix -L build) are not the
// wrapper's.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/resolve"
	"go.opentelemetry.io/otel"
)

// shimName reports the tool a program name selects shim mode for.
func shimName(argv0 string) (string, bool) {
	switch base := filepath.Base(argv0); base {
	case ToolNix, ToolDarwinRebuild:
		return base, true
	}
	return "", false
}

// RunShim runs shim mode for tool (nix or darwin-rebuild) and returns the exit
// code; a passthrough replaces the process via Deps.Exec.
func RunShim(d Deps, tool string) int {
	log.SetOutput(io.Discard)
	r := &runner{Deps: d, getenv: envLookup(d.Environ), startedBy: StartedByShim}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { r.debugf("otel: %v", err) }))

	var self os.FileInfo
	if d.Executable != "" {
		self, _ = os.Stat(d.Executable)
	}
	cmdPath, err := resolve.LookPath(tool, r.getenv("PATH"), self)
	if err != nil {
		fmt.Fprintln(d.Stderr, err)
		var perm *resolve.PermissionError
		if errors.As(err, &perm) {
			return 126
		}
		return 127
	}
	// The real binary gets the PATH-found absolute path as argv[0], so
	// darwin-rebuild's $0 keeps its /sw/bin/darwin-rebuild suffix.
	cmd := append([]string{cmdPath}, d.Args...)

	direct := func(why string) int {
		r.debugf("shim: passthrough (%s)", why)
		if err := d.Exec(cmdPath, cmd, d.Environ); err != nil {
			fmt.Fprintf(d.Stderr, "fork/exec %s: %v\n", cmdPath, err)
			return 126
		}
		return 0 // only a test double returns nil
	}

	opts, why := r.shimDecide(tool, cmd)
	if why != "" {
		return direct(why)
	}
	r.debugf("shim: wrapping %s %s", tool, Subcommand(cmd))
	return r.observe(opts, cmdPath)
}

var _ = time.Second

// shimDecide is the dispatch table. A non-empty why means "exec the real
// binary" and names the reason; otherwise opts describe the wrapped run
// (OTLPEndpoint is set only for root, from the root configuration).
func (r *runner) shimDecide(tool string, cmd []string) (opts config.Options, why string) {
	if off, w := config.Disabled(r.getenv); off {
		return opts, w
	}
	if r.getenv(ActiveEnv) != "" {
		return opts, ActiveEnv + " is set"
	}
	// nix's shell completion re-runs the command line with this variable set;
	// it is never worth wrapping.
	if r.getenv("NIX_GET_COMPLETIONS") != "" {
		return opts, "completion call"
	}
	opts = config.Options{MinSubstituteSpan: config.DefaultMinSubstituteSpan, Cmd: cmd}
	allow := DefaultAllowlist()
	if r.Euid == 0 {
		rc, w := r.loadRootConfig()
		if w != "" {
			return opts, w
		}
		allow.Add(rc.Verbs)
		if rc.RootEndpoint == "" {
			return opts, "root configuration has no rootEndpoint"
		}
		opts.OTLPEndpoint = rc.RootEndpoint
	} else {
		allow.Add(r.userVerbs())
	}
	if !allow.Wraps(tool, Subcommand(cmd)) {
		return opts, "verb not on the allowlist"
	}
	return opts, ""
}
