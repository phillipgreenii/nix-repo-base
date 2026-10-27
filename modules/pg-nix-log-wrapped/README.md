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
`nix.plan.fetch`, `nix.tailer.lag` (seconds), `nix.log.file` (the retained `.jsonl`),
`nix.log.truncated` when the parse cap was hit, and `nix.log.retained.truncated` when the retained
cap was hit.

## Endpoint and configuration

For a **non-root** wrapper, in order: `--otlp-endpoint`; then `enabled = false` in
`~/.config/pn/telemetry.toml` (off, even if an endpoint is set elsewhere); then
`OTEL_EXPORTER_OTLP_ENDPOINT`; then `endpoint` in the file:

```toml
enabled = true
endpoint = "http://127.0.0.1:4318"
```

`enabled` is the per-system runtime switch (absent means "on iff an endpoint resolves"; ADR 0028,
"Amendment 2026-10-08"). The wrapper is installed on every system; this file decides whether it
emits. `PG_NIX_LOG_DISABLE=1` and `OTEL_SDK_DISABLED=true` still force it off first.

OTLP over HTTP only. `--traceparent` falls back to `TRACEPARENT` the same way. A **root** wrapper
(under `sudo`, which drops the environment) reads **flags only**, never a user file, and always
logs to `/var/log/pg-nix-log-wrapped`.

## Escape hatches

| Control                                 | Effect                                                                      |
| --------------------------------------- | --------------------------------------------------------------------------- |
| `PG_NIX_LOG_DISABLE=1`                  | exec CMD unmodified                                                         |
| `OTEL_SDK_DISABLED=true`                | exec CMD unmodified                                                         |
| no endpoint resolved                    | exec CMD unmodified                                                         |
| `PG_NIX_LOG_DEBUG=1`                    | print why the wrapper failed open (and OTel export errors) to stderr        |
| `PG_NIX_LOG_FLUSH_TIMEOUT=2s`           | bound on the end-of-run export; a dead collector never costs more           |
| `--check`                               | print endpoint, reachability, log-dir writability; exit 0 only when healthy |
| `--min-substitute-span=250ms`           | shortest substitution that still gets a span (they are always counted)      |
| `PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE=1` | turn the unmatched `--override-input` guard off (below)                     |

## Guarantees

- **Fail open.** Any setup failure (unwritable log directory, bad endpoint, exporter error, garbage
  config) runs CMD exactly as if the wrapper were absent, with **empty wrapper stderr**; CMD's
  stdout and stderr are inherited and never copied.
- **Exit status.** CMD's exit code, or `128+n` when it dies of signal `n` (137 for SIGKILL). A missing
  CMD gives 127 with Go's `exec: "x": executable file not found in $PATH`; a non-executable one 126.
- **Unmatched `--override-input` fails the run (exit 3).** See the next section; this is the only
  case where the wrapper's exit status differs from CMD's.
- **Signals.** SIGTERM and SIGHUP are forwarded once. SIGINT is never forwarded: a terminal Ctrl-C
  reaches CMD through the shared process group.
- **No blocking.** Export is asynchronous with a 16384-span queue; a dead collector drops data.

## Unmatched `--override-input` guard

nix only **warns** when `--override-input NAME URL` names an input the flake does not have
(`the flag '--override-input NAME URL' does not match any input`, and `input 'X' has an override for
a non-existent input 'NAME'`). It then builds against the **locked** input and exits 0, so a wrongly
named override silently validates the wrong code. **Override names are the flake input names
(`phillipgreenii-*`), not repo directory names.** `pn workspace` derives them from the workspace lock
itself; the risk is a hand-built nix command.

The wrapper guards against it (bead `pg2-jvzmgf`, option (a) of the three considered: a `pn`
subcommand validating names would not cover hand-built runs, and documentation alone does not catch
the mistake). It reads the same `json-log-path` stream it already tails (no stderr capture, so nix's
tty behaviour is unchanged) and, when CMD carries `--override-input` before any `--`:

- if nix finished with status 0 **and** logged either warning above, the wrapper prints the warning
  text (naming the override) and a hint to stderr and exits **3** (`app.ExitUnmatchedOverride`);
- a run that fails on its own keeps CMD's exit code; a run with a valid override, or with no
  `--override-input` at all, is untouched, so existing callers see no exit-code change;
- the guard works with telemetry off too: a run carrying `--override-input` with no endpoint (or a
  failed telemetry setup) is observed without export instead of `exec`'d. Runs without the flag keep
  the plain `exec` fail-open, and `PG_NIX_LOG_DISABLE=1` / `OTEL_SDK_DISABLED=true` stay pure
  passthrough, guard included;
- `PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE=1` restores the old exit 0.

Tests: `internal/app/overrideguard_test.go` (fake nix) and
`cmd/pg-nix-log-wrapped/override_guard_realnix_test.go` (real `nix`, hermetic local path flake,
`--offline`).

## Retained logs

nix writes its JSONL to a scratch file, and the wrapper keeps a **filtered copy**. A run creates
`<log-dir>/<utc-ts>-<pid>-<rand>.raw` (the scratch file nix writes through `json-log-path`) and
`<utc-ts>-<pid>-<rand>.jsonl` (the retained log), both mode 0600 and created `O_EXCL|O_NOFOLLOW`, in
`$XDG_STATE_HOME/pn/nix-logs` (or `~/.local/state/pn/nix-logs`), or `--log-dir`. The retained log is
NOT a verbatim copy of nix's output: it omits `type 105` (progress) and `type 106` results, which are
about 99% of the bytes of a big run, and keeps everything else, including `type 101` build-log lines
(`nixlog.RetainDrop`, deliberately not the parser's `FastDrop`). `nix.log.file` on `nix.invocation`
names the retained `.jsonl`, never the scratch file.

- **Two caps, two meanings.** The _retained cap_ (64 MiB) stops writing the `.jsonl` when hit and sets
  `nix.log.retained.truncated=true`; parsing carries on, so a failure message after the cap still
  yields its failed `nix.build` span. The _parse cap_ (256 MiB, W-12) counts RETAINED bytes (the
  lines the retained log keeps), not the raw file offset, so progress cannot exhaust it; when it is
  hit parsing stops, nix keeps writing the scratch file, and `nix.log.truncated=true` is set.
  Deps `RetainedCap` and `LogSizeCap` inject both for tests.
- **A live run is the `.raw` file.** To inspect an in-progress run, read its `.raw` (unfiltered, live).
  The `.jsonl` is written as lines are read and is complete only when the run ends. On a normal exit,
  and after a forwarded SIGTERM/SIGHUP, the wrapper flushes and closes the `.jsonl` and then deletes the
  `.raw`.
- **A SIGKILLed wrapper leaves its `.raw`**, which is then the only evidence of that run. The next
  sweep FINALIZES it: a `.raw` whose pid (part of its name) is no longer alive is filtered into its
  `.jsonl` with the same rule and the retained cap, then removed (never deleted unfiltered). A `.raw`
  whose pid is alive is left alone, except that one older than the max age is finalized regardless
  (pid reuse). A `.raw` whose name carries no parseable pid is left until it is over age.
- **Retention.** Each start sweeps **its own directory only**: orphan `.raw` files are finalized
  first, then `.jsonl` files older than 7 days go, then oldest first until the directory is under
  2 GiB. Live `.raw` files count toward the 2 GiB total but are never evicted, and neither is the
  `.jsonl` of a run that still holds a `.raw`; only finalized `.jsonl` files are evicted.
- **The sweep is oldest-first by mtime, so the 7-day window is a ceiling, not a floor.** Before
  filtering, a few oversized logs (1.07 GB and 0.50 GB, 12.6M of 12.7M lines progress) reached
  2 GiB and pushed out everything older, including logs younger than 7 days (the observation week
  kept 3 of 7 days). The 64 MiB retained cap bounds each file now. Nothing records what a sweep
  removed.
- **Spans can be fewer than log files.** A log file exists once CMD has started (setup failures
  remove it), but the run's spans and metrics leave in one bounded end-of-run flush
  (`PG_NIX_LOG_FLUSH_TIMEOUT`, 2 s). The wrapper asks the exporters for a short retry (first wait
  100 ms, total bounded by the flush budget), so one transient refusal (for example the collector's
  memory limiter answering 503) is retried inside the budget. A refusal that outlasts the budget
  still drops the whole batch, including `nix.invocation`, and the exit code is unaffected; the
  retained log is then the only record of the run. `PG_NIX_LOG_DEBUG=1` prints the export error.

Root-run logs in `/var/log/pg-nix-log-wrapped` (directory 0750, files 0600, owned by root) are read
with `sudo`. Membership of the `admin` group is not enough on its own: the directory's group is
`wheel` and the files grant nothing to a group. A non-root observer sees root runs only as spans in
Tempo (`nix.log.file` names the file).

## Layout

| Path                     | Responsibility                                                 |
| ------------------------ | -------------------------------------------------------------- |
| `cmd/pg-nix-log-wrapped` | `main`, plus the process-level tests                           |
| `internal/app`           | control flow: fail-open decision, spawn, signals, W-9 ordering |
| `internal/config`        | flags and endpoint / traceparent precedence                    |
| `internal/logdir`        | directory choice, safe file creation, retention sweep          |
| `internal/retain`        | filtered retained log: live handler and offline finalize       |
| `internal/resolve`       | PATH lookup that skips the wrapper itself                      |
| `internal/tailer`        | polling reader, partial lines, read-time stamps, lag, size cap |
| `internal/nixlog`        | activity state machine (tested against the ADR 0028 goldens)   |
| `internal/recorder`      | Adapter from activities to OTel spans and metrics              |
| `internal/fakecmd`       | test double for nix (imported by tests only)                   |

## Testing

`go test -race ./...` from this directory, or `nix build .#checks.<system>.pg-nix-log-wrapped-go-tests`.
No test uses a real network or a real OTel stack. One canary builds a unique derivation through the
wrapper with the real `nix` and asserts a `nix.build` span, and the override-guard test drives a
real `nix eval` over a local path-flake fixture; both skip when `nix` is absent or when running
inside a nix builder. `internal/nixlog/testdata` is a copy of `docs/adr/0028-fixtures`; a
test fails if they drift (refresh with `cp docs/adr/0028-fixtures/*.jsonl
modules/pg-nix-log-wrapped/internal/nixlog/testdata/`).
