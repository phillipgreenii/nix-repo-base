# pg-nix-log-wrapped

Runs a nix command (`nix`, `darwin-rebuild`, ...) **unchanged** and turns nix's `--json-log-path`
activity stream into OpenTelemetry spans and metrics. With no OTLP endpoint resolved it does
nothing at all: it `exec`s the command. Design and decisions:
[ADR 0030](../../docs/adr/0030-pg-nix-log-wrapped.md); the facts it rests on:
[ADR 0028](../../docs/adr/0028-pn-nix-telemetry.md).

```text
pg-nix-log-wrapped [--traceparent TP] [--otlp-endpoint URL] [--log-dir DIR] [--] CMD ARGS...
pg-nix-log-wrapped --check [--otlp-endpoint URL] [--log-dir DIR]
```

Wrapper flags stop at the first `--` or the first non-flag argument; everything after is CMD's.
CMD is resolved from `PATH` skipping the wrapper's own file, so a `PATH` shadow cannot recurse.

## What you get

| Signal | Name                      | Notes                                                                 |
| ------ | ------------------------- | --------------------------------------------------------------------- |
| span   | `nix.invocation`          | `INTERNAL`; child of `--traceparent` / `TRACEPARENT`, else a new root |
| span   | `nix.build`               | one per derivation; `nix.drv.name`; error status on failure           |
| span   | `nix.substitute`          | `server.address`; only when at least 250 ms (always counted)          |
| span   | `nix.build.wait`          | waiting for a lock held by another client                             |
| metric | `nix.derivations`         | counter, `outcome` = `built` / `substituted` / `failed`               |
| metric | `nix.build.duration`      | histogram, seconds, `status`                                          |
| metric | `nix.invocation.duration` | histogram, seconds, `command`, `status`                               |

Metrics use **delta** temporality (the process is short-lived and exports once at exit); query with
`increase()` / `rate()`. Derivation names are never metric labels. The `built` count includes
trivial always-local derivations. The invocation span carries `process.exit.code`, `nix.plan.build`,
`nix.plan.fetch`, `nix.tailer.lag` (seconds), `nix.log.file`, and `nix.log.truncated` when the log
size cap was hit.

## Endpoint and configuration

For a **non-root** wrapper, in order: `--otlp-endpoint`, `OTEL_EXPORTER_OTLP_ENDPOINT`, then
`endpoint` in `~/.config/pn/telemetry.toml`:

```toml
endpoint = "http://127.0.0.1:4318"
```

OTLP over HTTP only. `--traceparent` falls back to `TRACEPARENT` the same way. A **root** wrapper
(under `sudo`, which drops the environment) reads **flags only**, never a user file, and always
logs to `/var/log/pg-nix-log-wrapped`.

## Escape hatches

| Control                       | Effect                                                                      |
| ----------------------------- | --------------------------------------------------------------------------- |
| `PG_NIX_LOG_DISABLE=1`        | exec CMD unmodified                                                         |
| `OTEL_SDK_DISABLED=true`      | exec CMD unmodified                                                         |
| no endpoint resolved          | exec CMD unmodified                                                         |
| `PG_NIX_LOG_DEBUG=1`          | print why the wrapper failed open (and OTel export errors) to stderr        |
| `PG_NIX_LOG_FLUSH_TIMEOUT=2s` | bound on the end-of-run export; a dead collector never costs more           |
| `--check`                     | print endpoint, reachability, log-dir writability; exit 0 only when healthy |
| `--min-substitute-span=250ms` | shortest substitution that still gets a span (they are always counted)      |

## Guarantees

- **Fail open.** Any setup failure (unwritable log directory, bad endpoint, exporter error, garbage
  config) runs CMD exactly as if the wrapper were absent, with **empty wrapper stderr**; CMD's
  stdout and stderr are inherited and never copied.
- **Exit status.** CMD's exit code, or `128+n` when it dies of signal `n` (137 for SIGKILL). A missing
  CMD gives 127 with Go's `exec: "x": executable file not found in $PATH`; a non-executable one 126.
- **Signals.** SIGTERM and SIGHUP are forwarded once. SIGINT is never forwarded: a terminal Ctrl-C
  reaches CMD through the shared process group.
- **No blocking.** Export is asynchronous with a 16384-span queue; a dead collector drops data.

## Raw logs

Every observed run keeps its raw JSONL at `<log-dir>/<utc-ts>-<pid>-<rand>.jsonl` (mode 0600, created
`O_EXCL|O_NOFOLLOW`): `$XDG_STATE_HOME/pn/nix-logs` (or `~/.local/state/pn/nix-logs`), or
`--log-dir`. Each start sweeps **its own directory only**: files older than 7 days, then oldest first
until the directory is under 2 GiB. Ingest stops at 256 MiB per file (`nix.log.truncated`).

Root-run logs in `/var/log/pg-nix-log-wrapped` (0750) are read with `sudo` or as an admin-group
member.

## Layout

| Path                     | Responsibility                                                 |
| ------------------------ | -------------------------------------------------------------- |
| `cmd/pg-nix-log-wrapped` | `main`, plus the process-level tests                           |
| `internal/app`           | control flow: fail-open decision, spawn, signals, W-9 ordering |
| `internal/config`        | flags and endpoint / traceparent precedence                    |
| `internal/logdir`        | directory choice, safe file creation, retention sweep          |
| `internal/resolve`       | PATH lookup that skips the wrapper itself                      |
| `internal/tailer`        | polling reader, partial lines, read-time stamps, lag, size cap |
| `internal/nixlog`        | activity state machine (tested against the ADR 0028 goldens)   |
| `internal/recorder`      | Adapter from activities to OTel spans and metrics              |
| `internal/fakecmd`       | test double for nix (imported by tests only)                   |

## Testing

`go test -race ./...` from this directory, or `nix build .#checks.<system>.pg-nix-log-wrapped-go-tests`.
No test uses a real network or a real OTel stack. One canary builds a unique derivation through the
wrapper with the real `nix` and asserts a `nix.build` span; it skips when `nix` is absent or when
running inside a nix builder. `internal/nixlog/testdata` is a copy of `docs/adr/0028-fixtures`; a
test fails if they drift (refresh with `cp docs/adr/0028-fixtures/*.jsonl
modules/pg-nix-log-wrapped/internal/nixlog/testdata/`).
