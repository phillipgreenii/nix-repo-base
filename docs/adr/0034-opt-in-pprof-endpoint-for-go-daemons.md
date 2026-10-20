# ADR-0034: Opt-in pprof endpoint for Go daemons

**Date:** 2026-10-20
**Status:** Accepted
**Deciders:** phillipgreenii (implemented by Claude)

## Context

Long-running Go daemons in this workspace (launchd services managed through Home-Manager and darwin
modules) have no uniform way to be profiled while they run. A pilot daemon exposed `net/http/pprof`
behind an environment variable, and the pilot showed which choices matter: the listener must be off
by default, must not be reachable from the network, and must not share a mux with anything else.

Each daemon repo would otherwise re-derive these rules, so they are recorded once here and the shared
implementation lives in one Go package, `github.com/phillipgreenii/x/pprofserver`.

## Decision

Go daemons adopt the following opt-in pprof endpoint standard.

### Daemon-side requirements

1. A daemon MUST open NO profiling listener unless `<DAEMON>_PPROF_ADDR` is set (for example
   `PA_MONITOR_PPROF_ADDR`).
2. The value MUST be an absolute unix-socket path or a loopback `host:port` (`127.0.0.1`,
   `localhost`, `::1`). Wildcard, remote and relative values MUST be refused with one stderr line,
   and the daemon MUST keep running.
3. The socket MUST be created mode 0600, MUST replace a stale socket, and MUST NOT remove a regular
   file.
4. The server MUST use its own mux holding only the pprof routes, never `http.DefaultServeMux`, and
   MUST NOT set a write timeout, because `/debug/pprof/profile?seconds=N` holds the response open.

### Module and documentation requirements

- The Home-Manager (or darwin) module requirements (`pprof.enable` and the environment variable that
  points at the socket) are owned by the convention page [docs/go-daemons.md](../go-daemons.md).
- The capture recipe and the warning about goroutine dumps are owned by the runbook
  [docs/go-daemon-profiling-runbook.md](../go-daemon-profiling-runbook.md). Docs MUST link it rather
  than restate either.

Other pages cite the standard by these repo paths, not by ADR number, so each repo's edit stands
alone.

## Consequences

### Positive

- A daemon is not profilable, and exposes no new listener, until an operator opts in.
- One shared package and one convention page replace per-daemon variations.
- A unix socket at mode 0600 limits access to the owning user.

### Negative

- Each daemon repo MUST wire its module to the convention page's requirements; the standard does not
  enforce that by itself.
- `go tool pprof` cannot dial a unix socket directly, so profiling needs the two-step recipe in the
  runbook.

## Related Decisions

- Convention page: [docs/go-daemons.md](../go-daemons.md).
- Runbook: [docs/go-daemon-profiling-runbook.md](../go-daemon-profiling-runbook.md).
