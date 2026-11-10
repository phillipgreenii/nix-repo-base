package workspace

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// The sudo session lets apply ask for sudo once, up front, and keeps the
// credentials warm while the (long) user build runs, so the two root steps
// afterwards need no second prompt. It is acquired once and released by Stop
// (RAII style): Stop ends the refresh goroutine and, only when pn created the
// credentials, invalidates them with `sudo -k`.
//
// The sudo calls are exactly `sudo -n -v` (probe, verify, refresh), `sudo -v`
// (the one prompt) and `sudo -k` (release). They are hard-coded, never
// templated, and stay in pn's own process group: a background group would be
// stopped by SIGTTIN when sudo reads the terminal.

var (
	// sudoRefreshInterval is how often the loop runs `sudo -n -v`. It is a
	// package variable so tests shorten it; the session copies it at start.
	sudoRefreshInterval = 60 * time.Second
	// sudoRefreshTimeout bounds one refresh call.
	sudoRefreshTimeout = 10 * time.Second
)

// sudoStopTimeout bounds the `sudo -k` of Stop and of the cancel paths.
const sudoStopTimeout = 5 * time.Second

type sudoSession struct {
	runner exec.Runner
	// stopCtx is context.WithoutCancel(ctx), derived once at start: it carries
	// the trace but not the cancellation, so `sudo -k` still runs after a cancel.
	stopCtx  context.Context
	stdin    io.Reader
	interval time.Duration
	timeout  time.Duration

	created   bool // pn made the credentials (so Stop invalidates them)
	cacheless bool // sudo caches nothing here
	lost      atomic.Bool

	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// startSudoSession acquires the session and starts the refresh goroutine. It
// returns (nil, err) only when authentication failed or ctx was cancelled.
func startSudoSession(ctx context.Context, runner exec.Runner, out io.Writer, stdin io.Reader) (*sudoSession, error) {
	s := &sudoSession{
		runner:   runner,
		stopCtx:  context.WithoutCancel(ctx),
		stdin:    stdin,
		interval: sudoRefreshInterval,
		timeout:  sudoRefreshTimeout,
		done:     make(chan struct{}),
	}

	// 1. Does the operator already hold credentials?
	if err := s.noPrompt(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 2. No: ask once. created is set BEFORE the call: a SIGTERM during the
		// prompt may still have created the ticket.
		fmt.Fprintln(out, "apply: authenticate sudo now; the build runs as you and the root steps reuse this authentication")
		s.created = true
		_, err := s.runner.Run(ctx, "sudo", []string{"-v"}, exec.RunOptions{Dir: "/", Stdin: s.stdin, Stdout: out, Stderr: out})
		if err != nil {
			if ctx.Err() != nil {
				s.invalidate()
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("sudo authentication: %w", err)
		}
		// 3. Verify that sudo cached the credentials. A cancel is checked FIRST so
		// it is never mistaken for "sudo caches nothing".
		if err := ctx.Err(); err != nil {
			s.invalidate()
			return nil, err
		}
		if err := s.noPrompt(ctx); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				s.invalidate()
				return nil, cerr
			}
			fmt.Fprintln(out, "apply: sudo does not cache credentials here; it will ask again after the build")
			s.cacheless = true
			s.cancel = func() {}
			close(s.done)
			return s, nil
		}
	}

	// 4. One refresh goroutine, derived from the apply's ctx.
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.refreshLoop(loopCtx)
	return s, nil
}

// noPrompt runs `sudo -n -v`: success means valid credentials exist. It sets no
// Stdout or Stderr (it never touches the apply log).
func (s *sudoSession) noPrompt(ctx context.Context) error {
	_, err := s.runner.Run(ctx, "sudo", []string{"-n", "-v"}, exec.RunOptions{Dir: "/", Stdin: s.stdin})
	return err
}

// invalidate runs `sudo -k` with a context that survives a cancelled apply.
func (s *sudoSession) invalidate() {
	kctx, cancel := context.WithTimeout(s.stopCtx, sudoStopTimeout)
	defer cancel()
	_, _ = s.runner.Run(kctx, "sudo", []string{"-k"}, exec.RunOptions{Dir: "/", Stdin: s.stdin})
}

// refreshLoop is the only goroutine of the session. It ends when ctx is
// cancelled or a refresh fails, and always closes done.
func (s *sudoSession) refreshLoop(ctx context.Context) {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.refreshOnce(ctx) {
				return
			}
		}
	}
}

// refreshOnce runs one `sudo -n -v` under its own deadline. A failure caused by
// the loop being cancelled is not a lost session; any other failure is.
func (s *sudoSession) refreshOnce(ctx context.Context) bool {
	rctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.noPrompt(rctx); err != nil {
		if ctx.Err() != nil {
			return false
		}
		s.lost.Store(true)
		return false
	}
	return true
}

// Lost reports that a refresh failed (the credentials are gone).
func (s *sudoSession) Lost() bool { return s != nil && s.lost.Load() }

// Cacheless reports that verification showed sudo caches nothing.
func (s *sudoSession) Cacheless() bool { return s != nil && s.cacheless }

// Stop ends the refresh goroutine, waits for it, then invalidates the
// credentials iff pn created them. It is idempotent, concurrency-safe and safe
// on a nil receiver.
func (s *sudoSession) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.cancel()
		<-s.done
		if s.created {
			s.invalidate()
		}
	})
}
