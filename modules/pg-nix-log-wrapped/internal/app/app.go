// Package app is the wrapper's control flow: parse flags, decide whether to
// observe at all, run CMD with a json-log-path injected, tail that log into
// OpenTelemetry, and exit with CMD's own status.
//
// Design (Proxy + Observer + Adapter): the wrapper is a process-level Proxy
// for CMD. It never touches CMD's stdio. A tailer goroutine (Observer) feeds
// the nixlog state machine, whose finished activities the recorder (Adapter)
// turns into spans and delta metrics. Every failure before CMD starts falls
// back to exec'ing CMD unmodified and silently (W-2), so telemetry can never
// change what a command does.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/logdir"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/nixlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/recorder"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/resolve"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/tailer"
	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	// ServiceName is set in code, never from OTEL_SERVICE_NAME.
	ServiceName = "pg-nix-log-wrapped"
	// SpanQueueSize is the span batch queue (W-8), far above the SDK's 2048.
	SpanQueueSize = 16384
	// DefaultFlushTimeout bounds the end-of-run export (W-9 e).
	DefaultFlushTimeout = 2 * time.Second
	// drainFor bounds the tailer's final drain after CMD exits.
	drainFor = 2 * time.Second
)

// Telemetry is the slice of an OTel setup the wrapper needs. The default
// comes from otelsetup.Init; tests inject in-memory providers.
type Telemetry struct {
	Tracer   trace.Tracer
	Meter    metric.Meter
	Shutdown func(context.Context) error
}

// Deps is everything Run touches in the outside world.
type Deps struct {
	Args    []string // without argv[0]
	Environ []string
	Euid    int
	Pid     int
	Home    string
	// Executable is the wrapper's own path, used to skip itself on PATH.
	Executable string
	Version    string

	Stdout, Stderr io.Writer
	// Child stdio is inherited from these (the real process streams).
	ChildStdin  io.Reader
	ChildStdout io.Writer
	ChildStderr io.Writer

	Now func() time.Time
	// Exec replaces the process (fail-open path). It returns only on failure.
	Exec         func(path string, argv, env []string) error
	NewTelemetry func(ctx context.Context, cfg otelsetup.Config, opts ...otelsetup.Option) (Telemetry, error)
	ReadFile     func(string) ([]byte, error)
	Dial         func(addr string, timeout time.Duration) error

	// RootLogDir overrides logdir.RootDir (tests only).
	RootLogDir string
	// LogSizeCap overrides the W-12 cap; zero selects the default.
	LogSizeCap int64
	// Sweep limits override the W-10 defaults when non-zero.
	SweepMaxAge   time.Duration
	SweepTotalCap int64
	PollInterval  time.Duration
	FlushTimeout  time.Duration
}

// DefaultDeps wires the real process environment.
func DefaultDeps(version string) Deps {
	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	return Deps{
		Args:        os.Args[1:],
		Environ:     os.Environ(),
		Euid:        os.Geteuid(),
		Pid:         os.Getpid(),
		Home:        home,
		Executable:  exe,
		Version:     version,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		ChildStdin:  os.Stdin,
		ChildStdout: os.Stdout,
		ChildStderr: os.Stderr,
		Now:         time.Now,
		Exec:        syscall.Exec,
		NewTelemetry: func(ctx context.Context, cfg otelsetup.Config, opts ...otelsetup.Option) (Telemetry, error) {
			t, err := otelsetup.Init(ctx, cfg, opts...)
			if err != nil {
				return Telemetry{}, err
			}
			return Telemetry{Tracer: t.Tracer, Meter: t.Meter, Shutdown: t.Shutdown}, nil
		},
		ReadFile: os.ReadFile,
		Dial: func(addr string, timeout time.Duration) error {
			c, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				return err
			}
			return c.Close()
		},
	}
}

type runner struct {
	Deps
	getenv func(string) string
}

func (r *runner) debugf(format string, args ...any) {
	if r.getenv("PG_NIX_LOG_DEBUG") == "1" {
		fmt.Fprintf(r.Stderr, "pg-nix-log-wrapped: "+format+"\n", args...)
	}
}

// Run executes the wrapper and returns the process exit code. It returns only
// on the observed path; the fail-open path replaces the process via Deps.Exec.
func Run(d Deps) int {
	// The OTel SDK reports export errors through a global handler that logs
	// to stderr by default. The wrapper MUST be silent on stderr (W-2).
	log.SetOutput(io.Discard)
	r := &runner{Deps: d, getenv: envLookup(d.Environ)}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { r.debugf("otel: %v", err) }))

	opts, err := config.ParseArgs(d.Args)
	if err != nil {
		fmt.Fprintf(d.Stderr, "pg-nix-log-wrapped: %v\n\n%s", err, config.Usage)
		return 2
	}
	if opts.Help {
		fmt.Fprint(d.Stdout, config.Usage)
		return 0
	}
	if opts.Check {
		return r.check(opts)
	}
	if len(opts.Cmd) == 0 {
		fmt.Fprint(d.Stderr, "pg-nix-log-wrapped: no command given\n\n"+config.Usage)
		return 2
	}

	var self os.FileInfo
	if d.Executable != "" {
		self, _ = os.Stat(d.Executable)
	}
	cmdPath, err := resolve.LookPath(opts.Cmd[0], r.getenv("PATH"), self)
	if err != nil {
		fmt.Fprintln(d.Stderr, err)
		var perm *resolve.PermissionError
		if errors.As(err, &perm) {
			return 126
		}
		return 127
	}

	passthrough := func(why string) int {
		r.debugf("fail open: %s", why)
		err := d.Exec(cmdPath, opts.Cmd, d.Environ)
		if err == nil {
			return 0 // only a test double returns nil; the real Exec never returns on success
		}
		fmt.Fprintf(d.Stderr, "fork/exec %s: %v\n", cmdPath, err)
		return 126
	}

	if off, why := config.Disabled(r.getenv); off {
		return passthrough(why)
	}
	res := config.Resolve(config.Inputs{Opts: opts, Getenv: r.getenv, Euid: d.Euid, Home: d.Home, ReadFile: d.ReadFile})
	if res.Endpoint == "" {
		return passthrough("no OTLP endpoint resolved")
	}

	s, err := r.setup(opts, res, cmdPath)
	if err != nil {
		return passthrough(err.Error())
	}
	return s.run()
}

func envLookup(environ []string) func(string) string {
	return func(key string) string {
		for _, kv := range environ {
			if k, v, ok := strings.Cut(kv, "="); ok && k == key {
				return v
			}
		}
		return ""
	}
}

// session is everything set up before CMD starts.
type session struct {
	r       *runner
	opts    config.Options
	cmdPath string
	env     []string

	logFile *os.File
	logPath string
	tel     Telemetry
	inv     trace.Span
	rec     *recorder.Recorder
	proc    *nixlog.Processor
	tail    *tailer.Tailer
	start   time.Time
}

func (r *runner) flushTimeout() time.Duration {
	if v := r.getenv("PG_NIX_LOG_FLUSH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if r.FlushTimeout > 0 {
		return r.FlushTimeout
	}
	return DefaultFlushTimeout
}

// setup prepares the log file and telemetry. Any error (or panic) means CMD
// has not started and the caller should fail open.
func (r *runner) setup(opts config.Options, res config.Resolved, cmdPath string) (s *session, err error) {
	defer func() {
		if p := recover(); p != nil {
			s, err = nil, fmt.Errorf("setup panic: %v", p)
		}
	}()

	dir, err := logdir.Select(logdir.Params{
		Euid: r.Euid, Flag: opts.LogDir, XDGState: r.getenv("XDG_STATE_HOME"),
		Home: r.Home, RootDir: r.RootLogDir,
	})
	if err != nil {
		return nil, err
	}
	if err := logdir.Prepare(dir, r.Euid); err != nil {
		return nil, err
	}
	maxAge, totalCap := logdir.DefaultMaxAge, logdir.DefaultTotalCap
	if r.SweepMaxAge > 0 {
		maxAge = r.SweepMaxAge
	}
	if r.SweepTotalCap > 0 {
		totalCap = r.SweepTotalCap
	}
	logdir.Sweep(dir, r.Now(), maxAge, totalCap)
	f, logPath, err := logdir.Create(dir, r.Now(), r.Pid)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*session, error) {
		_ = f.Close()
		_ = os.Remove(logPath)
		return nil, err
	}
	if strings.ContainsAny(logPath, "\r\n") {
		return fail(errors.New("log path contains a newline"))
	}

	ctx := context.Background()
	if tp := res.Traceparent; tp != "" {
		// A malformed traceparent yields no span context: a fresh root trace.
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": tp})
	}
	tel, err := r.NewTelemetry(ctx, otelsetup.Config{
		Endpoint: res.Endpoint, ServiceName: ServiceName, Version: r.Version,
	}, otelsetup.WithSpanQueueSize(SpanQueueSize), otelsetup.WithTimeout(r.flushTimeout()))
	if err != nil {
		return fail(err)
	}

	start := r.Now()
	ctx, inv := tel.Tracer.Start(ctx, "nix.invocation",
		trace.WithSpanKind(trace.SpanKindInternal), trace.WithTimestamp(start),
		trace.WithAttributes(
			attribute.String("command", filepath.Base(opts.Cmd[0])),
			attribute.String("nix.log.file", logPath),
		))
	rec, err := recorder.New(ctx, tel.Tracer, tel.Meter)
	if err != nil {
		inv.End()
		return fail(err)
	}
	proc := nixlog.NewProcessor(rec, nixlog.Options{MinSubstituteSpan: opts.MinSubstituteSpan})
	return &session{
		r: r, opts: opts, cmdPath: cmdPath,
		env:     ChildEnv(r.Environ, logPath),
		logFile: f, logPath: logPath, tel: tel, inv: inv, rec: rec, proc: proc,
		tail:  tailer.New(f, proc, r.Now, r.LogSizeCap),
		start: start,
	}, nil
}

// ChildEnv returns environ with "json-log-path = <logPath>" appended to any
// ambient NIX_CONFIG (W-5). A later json-log-path line wins in nix, so no
// argument rewriting is needed.
func ChildEnv(environ []string, logPath string) []string {
	ambient, seen := "", false
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "NIX_CONFIG="); ok {
			if !seen {
				ambient, seen = v, true
			}
			continue
		}
		out = append(out, kv)
	}
	if ambient != "" && !strings.HasSuffix(ambient, "\n") {
		ambient += "\n"
	}
	return append(out, "NIX_CONFIG="+ambient+"json-log-path = "+logPath)
}

// run is the observed path: start CMD, tail, finish in the W-9 order.
func (s *session) run() int {
	r := s.r
	cmd := exec.Command(s.cmdPath)
	cmd.Args = s.opts.Cmd
	cmd.Env = s.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.ChildStdin, r.ChildStdout, r.ChildStderr

	// SIGINT is caught (never ignored: an ignored disposition would be
	// inherited by CMD) and deliberately not forwarded: a terminal Ctrl-C
	// already reaches CMD through the shared process group.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	endSignals := func() { signal.Stop(sigs); close(sigs) }

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }() // a tailer bug MUST NOT touch CMD
		s.tail.Run(stop, r.PollInterval, drainFor)
	}()

	if err := cmd.Start(); err != nil {
		endSignals()
		close(stop)
		<-done
		s.abandon()
		fmt.Fprintf(r.Stderr, "%v\n", err)
		if errors.Is(err, fs.ErrNotExist) {
			return 127
		}
		return 126
	}
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGINT {
				continue
			}
			_ = cmd.Process.Signal(sig)
		}
	}()

	werr := cmd.Wait()
	endSignals()
	code := exitCode(cmd.ProcessState, werr)
	close(stop) // (b) drain the file to EOF
	<-done
	s.finish(code)
	return code
}

// abandon releases everything when CMD never started.
func (s *session) abandon() {
	s.inv.End()
	_ = s.logFile.Close()
	_ = os.Remove(s.logPath)
	s.shutdown()
}

func (s *session) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), s.r.flushTimeout()+500*time.Millisecond)
	defer cancel()
	if err := s.tel.Shutdown(ctx); err != nil {
		s.r.debugf("telemetry shutdown: %v", err)
	}
}

// finish performs W-9 (c) to (e); (a), (b) and (f) belong to run.
func (s *session) finish(code int) {
	defer func() {
		if p := recover(); p != nil {
			s.r.debugf("finish panic: %v", p)
		}
	}()
	end := s.r.Now()
	s.proc.Finish(end) // (c) close open activities with error status
	sum := s.proc.Summary()
	s.inv.SetAttributes(
		attribute.Int("process.exit.code", code),
		attribute.Float64("nix.tailer.lag", s.tail.MaxLag().Seconds()),
		attribute.Int("nix.plan.build", sum.PlanBuild),
		attribute.Int("nix.plan.fetch", sum.PlanFetch),
	)
	if s.tail.Truncated() {
		s.inv.SetAttributes(attribute.Bool("nix.log.truncated", true))
	}
	if code != 0 {
		s.inv.SetAttributes(attribute.String("error.type", "nonzero_exit"))
		s.inv.SetStatus(codes.Error, fmt.Sprintf("exit code %d", code))
	}
	s.inv.End(trace.WithTimestamp(end)) // (d)
	s.rec.Invocation(filepath.Base(s.opts.Cmd[0]), end.Sub(s.start), code != 0)
	_ = s.logFile.Close()
	s.shutdown() // (e) bounded flush
}

// exitCode maps a finished process to a shell-style code: the exit status, or
// 128+n when it died of signal n (Go's ExitCode() is -1 then).
func exitCode(ps *os.ProcessState, waitErr error) int {
	if ps == nil {
		_ = waitErr
		return 126
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ws.ExitStatus()
	}
	return ps.ExitCode()
}

// check implements --check (W-11): print the resolved endpoint, whether it is
// reachable, and whether the log directory is writable. Exit 0 only when
// telemetry would actually run.
func (r *runner) check(opts config.Options) int {
	healthy := true
	if off, why := config.Disabled(r.getenv); off {
		fmt.Fprintf(r.Stdout, "telemetry: disabled (%s)\n", why)
		healthy = false
	}
	res := config.Resolve(config.Inputs{Opts: opts, Getenv: r.getenv, Euid: r.Euid, Home: r.Home, ReadFile: r.ReadFile})
	if res.Endpoint == "" {
		fmt.Fprintln(r.Stdout, "endpoint: none resolved (telemetry is off)")
		healthy = false
	} else {
		fmt.Fprintf(r.Stdout, "endpoint: %s (from %s)\n", res.Endpoint, res.EndpointSource)
		if err := r.Dial(hostPort(res.Endpoint), 2*time.Second); err != nil {
			fmt.Fprintf(r.Stdout, "reachable: no (%v)\n", err)
			healthy = false
		} else {
			fmt.Fprintln(r.Stdout, "reachable: yes")
		}
	}
	dir, err := logdir.Select(logdir.Params{
		Euid: r.Euid, Flag: opts.LogDir, XDGState: r.getenv("XDG_STATE_HOME"),
		Home: r.Home, RootDir: r.RootLogDir,
	})
	if err == nil {
		err = writable(dir, r.Euid)
	}
	if err != nil {
		fmt.Fprintf(r.Stdout, "log-dir: %s (writable: no: %v)\n", dir, err)
		healthy = false
	} else {
		fmt.Fprintf(r.Stdout, "log-dir: %s (writable: yes)\n", dir)
	}
	if healthy {
		return 0
	}
	return 1
}

func writable(dir string, euid int) error {
	if err := logdir.Prepare(dir, euid); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// hostPort extracts "host:port" from an OTLP endpoint (URL or host:port).
func hostPort(ep string) string {
	if !strings.Contains(ep, "://") {
		return ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return ep
	}
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}
