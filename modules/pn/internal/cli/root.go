// Package cli wires the pn binary's cobra command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

func init() {
	// Run the root command's PersistentPreRunE even for subcommands that
	// define their own persistent hooks (cobra >= 1.9), so a later child-level
	// hook can never silently replace the root pn.verb span setup.
	cobra.EnableTraverseRunHooks = true
}

// addExtraCmdHook is a test seam: tests set it to graft extra verbs onto the
// root command built by newRootCmd. Always nil in production.
var addExtraCmdHook func(*cobra.Command)

// Execute builds the root command tree and runs it against os.Args[1:].
// version must be a real version string from mkVersion; "dev" is rejected.
//
// Execute owns the telemetry lifecycle: telemetrycfg.Prepare decides whether
// and where to export, telemetry.New builds the exporters only when an endpoint
// resolved (Null Object otherwise), and Shutdown runs after the command tree
// returns. cmd/pn/main.go calls os.Exit only AFTER Execute returns, so the
// flush covers both the success return and the non-zero exit path.
func Execute(version string) error {
	args := os.Args[1:]
	res := telemetrycfg.Prepare(context.Background(), args, os.Getenv, os.Geteuid() == 0, os.Stderr)
	tel := newTelemetry(version, res, os.Stderr)
	return executeAndShutdown(version, args, os.Stdout, os.Stderr, res, tel)
}

// executeAndShutdown runs the command tree and then shuts tel down exactly once,
// whatever the outcome (success, failing verb, signal-interrupted run).
func executeAndShutdown(version string, args []string, stdout, stderr io.Writer, res telemetrycfg.Resolution, tel *telemetry.Telemetry) error {
	err := executeWith(version, args, stdout, stderr, res, tel)
	// The signal-aware command context is gone by now; Shutdown uses a fresh
	// bounded context (telemetry.ShutdownTimeout) so a SIGINT/SIGTERM-ended run
	// still flushes. A Shutdown failure is deliberately dropped: telemetry MUST
	// NOT change pn's exit code.
	_ = tel.Shutdown(context.Background())
	return err
}

// newTelemetry builds the process telemetry for a resolved configuration. A
// disabled resolution yields the no-op Telemetry without touching the
// network. An exporter-setup failure degrades to no-op (noted under -v only).
func newTelemetry(version string, res telemetrycfg.Resolution, stderr io.Writer) *telemetry.Telemetry {
	tel, err := telemetry.New(context.Background(), version, res.Endpoint)
	if err != nil && res.Verbose {
		_, _ = fmt.Fprintf(stderr, "telemetry disabled: %v\n", err)
	}
	return tel
}

// executeWithVersion is the test seam — caller passes args, stdout, stderr writers.
// Telemetry is resolved from the process environment, exactly as Execute does;
// tests that must not see a developer's real configuration set HOME/XDG_* and
// unset OTEL_* first. The exporter itself is NOT built here (the seam always
// runs with the no-op Telemetry); tests that need spans use executeWith with
// an in-memory Telemetry.
func executeWithVersion(version string, args []string, stdout, stderr io.Writer) error {
	res := telemetrycfg.Prepare(context.Background(), args, os.Getenv, os.Geteuid() == 0, stderr)
	return executeWith(version, args, stdout, stderr, res, nil)
}

// verbState carries the root pn.verb span from the root PersistentPreRunE
// (which starts it) to the defer in executeWith (which ends it).
type verbState struct {
	span *telemetry.VerbSpan
}

type verbStateKey struct{}

// executeWith runs the command tree with an already-resolved telemetry
// configuration (res) and the Telemetry built from it (tel; nil means off).
func executeWith(version string, args []string, stdout, stderr io.Writer, res telemetrycfg.Resolution, tel *telemetry.Telemetry) (err error) {
	if version == "dev" {
		return errors.New("pn: built with version=\"dev\"; this binary was built outside the Nix derivation. Use `nix build .#pn` for a real binary")
	}
	if tel == nil {
		tel = telemetry.Disabled()
	}

	// Cancel the command's context on SIGINT/SIGTERM so a Ctrl-C during a
	// fan-out (e.g. `pn workspace update`) propagates cancellation to the
	// in-flight subprocesses via cmd.Context(), instead of being ignored until
	// each child exits on its own (bead pg2-x3r0a). A second signal restores the
	// default behavior (immediate termination).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	state := telemetrycfg.NewRunState(res)
	ctx = telemetrycfg.WithRunState(ctx, state)
	ctx = telemetry.WithTelemetry(ctx, tel)
	verb := &verbState{}
	ctx = context.WithValue(ctx, verbStateKey{}, verb)

	root := newRootCmd(version)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true

	// End the root pn.verb span HERE, after ExecuteContext returns, and not in a
	// PersistentPostRunE: cobra skips the post hook when RunE fails, which is
	// exactly when the span most needs closing (with an error status).
	defer func() { verb.span.End(ExitCode(err), err) }()
	err = root.ExecuteContext(ctx)

	// `trace: <id>` goes to stderr only (never stdout), and only under -v or
	// PN_TRACE_HINT=1; it needs a trace id, which exists only when the root
	// span recorded one via RunState.SetTraceID.
	telemetrycfg.WriteHint(stderr, state, res.Verbose, os.Getenv)
	return err
}

// addTelemetryFlags registers the global telemetry flags. The VALUES are read
// by telemetrycfg.ScanArgs before cobra runs (telemetry must exist before the
// command tree does); registering them here makes cobra accept them and list
// them in --help.
func addTelemetryFlags(root *cobra.Command) {
	pf := root.PersistentFlags()
	pf.Bool("no-telemetry", false, "disable OpenTelemetry export for this run (also: PG_NIX_LOG_DISABLE=1, OTEL_SDK_DISABLED=true)")
	pf.String("otlp-endpoint", "", "OTLP/HTTP collector endpoint for this run (overrides OTEL_EXPORTER_OTLP_ENDPOINT and ~/.config/pn/telemetry.toml)")
	pf.BoolP("verbose", "v", false, "verbose diagnostics: print the trace id hint and telemetry reachability notes on stderr")
}

// rootLong is the `pn --help` text. Its escape-hatch table is the single
// authoritative list of telemetry controls; modules/pn/README.md carries the
// same table (ADR 0028). Keep the two in sync.
const rootLong = `pn-workspace multi-repo Nix workflow tool.

Telemetry (OpenTelemetry over OTLP/HTTP to a collector) is always built in;
whether it runs is a per-system RUNTIME setting. It is ON only when an endpoint
resolves: --otlp-endpoint, then (unless ~/.config/pn/telemetry.toml says
enabled = false) OTEL_EXPORTER_OTLP_ENDPOINT, then the file's endpoint (the file
is generated by the home-manager module phillipgreenii.pn.telemetry). When off,
pn creates no exporter, opens no connection and behaves exactly as without
telemetry.

Telemetry escape hatches:
  telemetry.toml enabled = false               Off for the system, no rebuild (phillipgreenii.pn.telemetry.enable = false)
  pn --no-telemetry                            Off for one pn run
  PG_NIX_LOG_DISABLE=1                         Off for pn; the wrapper execs CMD unmodified
  pg-nix-log-wrapped --check                   Prints the resolved endpoint, reachability and log-dir writability
  OTEL_SDK_DISABLED=true                       Both binaries off
  PG_NIX_LOG_DEBUG=1                           The wrapper prints diagnostics to stderr`

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "pn",
		Short:   "pn-workspace multi-repo Nix workflow tool",
		Long:    rootLong,
		Version: version,
		// Start the root pn.verb span and publish it via cmd.SetContext so every
		// RunE (and the WorkerPool, which is handed cmd.Context()) sees it.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			vs, ok := ctx.Value(verbStateKey{}).(*verbState)
			if !ok {
				return nil
			}
			name := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
			ctx, vs.span = telemetry.From(ctx).StartVerb(ctx, name)
			telemetrycfg.RunStateFrom(ctx).SetTraceID(vs.span.TraceID())
			cmd.SetContext(ctx)
			return nil
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	addTelemetryFlags(root)
	addWorkspaceCmd(root)
	addStoreCmd(root)
	if addExtraCmdHook != nil {
		addExtraCmdHook(root)
	}
	return root
}
