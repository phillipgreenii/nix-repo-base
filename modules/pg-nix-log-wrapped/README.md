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

| Signal | Name                      | Notes                                                                                                                                        |
| ------ | ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| span   | `nix.invocation`          | `INTERNAL`; child of `--traceparent` / `TRACEPARENT`, else a new root                                                                        |
| span   | `nix.build`               | one per derivation; `nix.drv.name`; error status and `error.type` on failure; `nix.hash_mismatch.placeholder` on a placeholder-hash mismatch |
| span   | `nix.substitute`          | `server.address`; only when at least 250 ms (always counted)                                                                                 |
| span   | `nix.build.wait`          | waiting for a lock held by another client                                                                                                    |
| metric | `nix.derivations`         | counter, `outcome` = `built` / `substituted` / `failed`                                                                                      |
| metric | `nix.build.duration`      | histogram, seconds, `status`                                                                                                                 |
| metric | `nix.invocation.duration` | histogram, seconds, `command`, `subcommand`, `status`, `failure_kind` (only when `status=error`)                                             |

Metrics use **delta** temporality (the process is short-lived and exports once at exit); query with
`increase()` / `rate()`. Derivation names are never metric labels. The `built` count includes
trivial always-local derivations. The `subcommand` label (span attribute `nix.subcommand`) names the
operation: for `nix` one of `build`, `eval`, `fmt`, `run`, `develop`, `shell`, `path-info`, `store`,
`copy`, `flake check`, `flake update`, `flake lock`, `flake show`, `flake metadata`; for
`darwin-rebuild` one of `switch`, `activate`, `build`, `check`, `edit`, `changelog`; everything else
(unknown verb, no verb, another program) is `other`, so the label is closed and never a raw user string.
It is found by skipping flags and the values of known value-taking flags (`--option`, `--override-input`,
`--flake`, ...) and taking the first allowlisted word; a value-taking flag missing from the table can
still mislead it. Split error rates with
`sum by (command, subcommand) (increase(nix_invocation_duration_count{status="error"}[1d]))`.
The invocation span carries `nix.subcommand`, `process.exit.code`, `nix.plan.build`,
`nix.plan.fetch` (the plan MESSAGES, "these N derivations will be built", summed), `nix.builds.done`,
`nix.builds.expected`, `nix.builds.failed`, `nix.fetch.done`, `nix.fetch.expected` (the final aggregate
progress of nix's own "N of M" counters, summed over the run's aggregate activities; set only when a
progress line was seen; they differ from the plan when nix stops early, e.g. after the first failure),
`nix.tailer.lag` (seconds), `nix.eval.duration` (seconds; see below), `nix.eval.only` (`true` only
when no build or fetch activity ever occurred), `nix.log.file` (the retained `.jsonl`),
`nix.log.truncated` when the parse cap was hit, and `nix.log.retained.truncated` when the retained
cap was hit. A `nix.substitute` span carries `nix.substitute.bytes` (the last "done" bytes progress seen
for that substitution; absent when none was seen), so slow caches can be identified. All of these are
span attributes only, never metric labels.

**Failure diagnostics (a failed or noisy run).** A non-zero exit sets `error.type` on the invocation
span to the first match of this CLOSED set, else the generic `nonzero_exit`:

| `error.type` / `failure_kind` | When                                                                                                                                |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `signal`                      | exit `128+n` (129 to 192), OR a run that printed `error: interrupted by the user` (a real nix exits 1 on SIGTERM/SIGINT); beats all |
| `hash_mismatch`               | `error: hash mismatch in fixed-output derivation '<drv>'`                                                                           |
| `build_failed`                | `Cannot build '<drv>'` with `builder failed ...`                                                                                    |
| `dependency_failed`           | `Cannot build '<drv>'` with `N dependency failed` (and no root-cause message was seen)                                              |
| `eval_error`                  | an `error:` message with no derivation (`.drv`) path in it                                                                          |
| `other`                       | an `error:` message that names a derivation but matches none of the above; as `failure_kind` also "non-zero exit, nothing known"    |

The metric label `failure_kind` on `nix.invocation.duration` uses the same set for `status=error`
only; where the span says `nonzero_exit` the label says `other`, and a value outside the set is
recorded as `other`, so the label cannot grow. Precedence is the table order. The span also carries
`nix.error.message` (the run's FIRST `error:` message, read from `msg` because `raw_msg` has no
`error:` prefix; whitespace collapsed, store hashes removed as `/nix/store/<name>`, URL
credentials redacted as `://<redacted>@`, cut to 200 characters), `nix.failed.drvs` (names of up
to 5 failing derivations, in message order), `nix.ignored.eval_cache_busy` (count of
`error (ignored): SQLite database '.../eval-cache-...' is busy`), `nix.db_busy` (count of
`warning: SQLite database '...' is busy`) and `nix.build.wait.count` (number of `nix.build.wait`
spans); the last three are set only when above zero. `nix.error.message` and `nix.failed.drvs`
are free text, so they are span attributes only, NEVER metric labels. A fixed-output
derivation that fails its hash check gets a failed `nix.build` span with `error.type =
hash_mismatch` and counts in `nix.derivations{outcome="failed"}` (as do its dependents); when its
`specified:` hash is the all-A placeholder (`sha256-AAAA...=`, a deliberately wrong hash such as a
test fixture) the span also carries `nix.hash_mismatch.placeholder = true`, so dashboards can
filter those out without the metric hiding real failures.
**`nix.eval.duration` is "time to first build/fetch activity", not evaluation CPU.** It is the
read-time stamp of the first `start` event whose activity type is not 0 (type 0 is nix's "unknown",
which is how evaluation is reported, e.g. `evaluating derivation '...'`) minus the invocation start.
If no such activity ever occurs (everything cached) it is the whole invocation duration and
`nix.eval.only` is `true`. Limits: lines are stamped when the wrapper reads them (20 ms poll; the lag
is exported as `nix.tailer.lag`), and `nix flake check` interleaves evaluation and builds, so the value
sizes only the lead-in before the first non-evaluation activity. Any other typed activity (a flake input
download, a path-info query) also ends the phase. It is a span attribute only, never a metric.

**Progress is read only for a few activities.** The parser skips `type 101` (build log) lines and
progress (`type 105` result) lines without decoding them. The exception is the progress of the aggregate
activities (type 104 Builds, type 103 CopyPaths; the last value is kept in memory) and of substitutions
(type 108, plus the type-100 CopyPath child that nix reports a substitution's bytes on). The id is read
from the line's byte suffix, so progress of every other activity (downloads emit thousands of lines) is
still never decoded. Aggregate activities are summed when they are siblings; one nested under another
aggregate activity is ignored, so only the outer one counts.

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

| Control                                 | Effect                                                                                         |
| --------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `PG_NIX_LOG_DISABLE=1`                  | exec CMD unmodified                                                                            |
| `OTEL_SDK_DISABLED=true`                | exec CMD unmodified                                                                            |
| no endpoint resolved                    | exec CMD unmodified                                                                            |
| `PG_NIX_LOG_DEBUG=1`                    | print why the wrapper failed open (and OTel export errors) to stderr                           |
| `PG_NIX_LOG_FLUSH_TIMEOUT=2s`           | bound on the end-of-run export; a dead collector never costs more                              |
| `--check`                               | print endpoint, reachability, log-dir writability and the shim state; exit 0 only when healthy |
| `--min-substitute-span=250ms`           | shortest substitution that still gets a span (they are always counted)                         |
| `PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE=1` | turn the unmatched `--override-input` guard off (below)                                        |

## Shim mode (pg-nix-shim)

The `pg-nix-shim` package holds two symlinks, `bin/nix` and `bin/darwin-rebuild`, onto this one
binary. When `basename(argv[0])` is `nix` or `darwin-rebuild` the binary decides per call whether to
wrap the real tool, so callers need no `pg-nix-log-wrapped` prefix. The home-manager module puts the
package on the user's `PATH`; the nix-darwin module puts its directory first on the system path and
writes `/etc/pg-nix-shim.json` for root runs. Design:
[`docs/superpowers/specs/2026-11-04-pg-nix-shim-design.md`](../../docs/superpowers/specs/2026-11-04-pg-nix-shim-design.md);
the decisions: [ADR 0030](../../docs/adr/0030-pg-nix-log-wrapped.md), "Amendment 2026-11-05".

Dispatch, on every call: `PG_NIX_LOG_DISABLE` set, then exec the real binary; `PG_NIX_LOG_ACTIVE`
set (an outer wrapped run already covers this call), then exec the real binary; verb not on the
allowlist, then exec the real binary; otherwise wrap it. A passthrough is silent and leaves the
environment untouched.

### Which verbs are wrapped

Default: `nix build`, `nix flake check`, `darwin-rebuild build`, `switch` and `check`. Opt-in verbs,
added to the defaults and never removing one: `nix run`, `nix fmt`, `nix eval`, `nix flake lock`,
`nix flake update`. `nix develop`, `nix shell`, `nix repl`, `darwin-rebuild activate` and every other
verb are never wrapped (an opt-in naming one is ignored, and `--check` says so).

| Run as   | Opt-in source                                                                                                              |
| -------- | -------------------------------------------------------------------------------------------------------------------------- |
| non-root | `verbs = ["run", "fmt"]` in `~/.config/pn/shim.toml` (home-manager `extraVerbs`), and `PG_NIX_SHIM_VERBS=run,fmt` (comma)  |
| root     | only the `verbs` array of `/etc/pg-nix-shim.json` (nix-darwin `extraVerbs`); `sudo` drops the variable, root reads no home |

### Bypassing, inspecting and debugging the shim

- One command: `PG_NIX_LOG_DISABLE=1 nix build .#x`. Under `sudo` the variable is dropped, so use
  `sudo env PG_NIX_LOG_DISABLE=1 nix build .#x`.
- Permanently: call the real binary by its absolute path, for example
  `/run/current-system/sw/bin/nix`.
- `type -a nix` lists the shim first and the real `nix` after it; `nix --version` and completion
  calls pass through and print the real tool's output.
- `PG_NIX_LOG_DEBUG=1` makes the shim print, on stderr, which branch it took and why (`shim:
passthrough (...)` or `shim: wrapping ...`). It prints nothing otherwise.
- `pg-nix-log-wrapped --check` additionally resolves `nix` and `darwin-rebuild` on the caller's
  `PATH`. A `PATH` entry is a shim when its basename is `nix` or `darwin-rebuild` and its fully
  resolved final component is `pg-nix-log-wrapped`. Output, after the unchanged telemetry lines:

  ```text
  shim: nix: shim at /etc/profiles/per-user/me/bin/nix
  shim: darwin-rebuild: NOT a shim: /run/current-system/sw/bin/darwin-rebuild is a real binary shadowing it
  shim: allowlist: darwin-rebuild build, ..., nix build, nix flake check
  shim: allowlist-ignored: develop (never wrapped or unknown)
  shim: root-config: /etc/pg-nix-shim.json -> /nix/store/...-pg-nix-shim.json (store regular file)
  shim: root-run: would wrap (rootEndpoint http://127.0.0.1:4318)
  status: telemetry=ok shim=fail
  ```

  It exits 1 when either name does not resolve to a shim (or telemetry is unhealthy), and the final
  `status:` line says which. `shim: root-config:` is `absent`, or `present but not usable: ... (not a
store symlink)` for a copy or a file outside `/nix/store/`; a root run passes through in both
  cases, and `shim: root-run:` says so. The effective allowlist is the defaults plus `shim.toml` and
  `PG_NIX_SHIM_VERBS` (non-root) or the root file's `verbs` (root).

- `pn workspace doctor` runs the same `--check` once and turns the shim lines into warnings (check
  id `shim`): a name that is not a shim, "root config not a store symlink", no root configuration
  while a user shim is installed, and a home-manager `telemetry.toml` endpoint that differs from
  `rootEndpoint`. Root runs write their logs mode 0600 under `/var/log/pg-nix-log-wrapped`, which a
  non-root doctor cannot read, so the doctor cannot tell whether root runs were wrapped. With
  telemetry off or no shim module in use it reports nothing new.

A silent fail-open is NOT visible: a passthrough execs the real binary before any telemetry
exists, so it cannot be counted. `--check`, `PG_NIX_LOG_DEBUG=1` and finding one real wrapped build
in the trace backend are how a broken shim is found. There is no daemon, launchd job or Grafana rule
for it.

### Behaviour that is NOT unchanged

- Allowlisted verbs get the unmatched `--override-input` guard (exit 3, next sections), as in
  explicit use; `PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE=1` turns it off. Verbs off the allowlist have
  no guard.
- Every command the wrapper wraps sees one new variable, `PG_NIX_LOG_ACTIVE=1`, and so does
  everything it spawns: a daemon started from a wrapped `nix run` is never wrapped again, and under
  `sudo` the variable is dropped, so a root shim inside a wrapped user run starts its own run.
- Signals follow the "Signals" guarantee below: SIGINT is not forwarded (it reaches nix through the
  shared process group), SIGTERM and SIGHUP are, other signals are not, and a signalled child
  surfaces as `128+n`. A harness that signals only the shim's pid leaves nix running on SIGKILL.
  This applies to allowlisted verbs only.
- Latency: allowlisted verbs pay initialisation, the log-directory sweep, the flush and the tailer
  drain; every other call pays one extra process start plus a `PATH` walk. Measured below.
- The explicit wrapper now also skips any `PATH` candidate that resolves to a file named
  `pg-nix-log-wrapped`, not only its own inode.

### Measured cost

One-off measurement, not a regression guard. Date 2026-11-05; Apple M3 Pro (11 cores, arm64
darwin), host load average about 16 during the run (a busy workstation, so the p95 values are
noisy). The built `pg-nix-shim` package on a temporary `PATH`, nothing applied. Each case runs 100
interleaved pairs: the real `nix` by absolute path, then the same command through the shim's
`nix`; added latency is the per-pair difference. The workload is a no-op `nix build --impure
--no-link --offline` of a trivial local derivation that is already realised (so it includes nix's
own evaluation, which is the same on both sides). Harness:
`cmd/pg-nix-log-wrapped/latency_measure_test.go`, opt-in behind the `latency` build tag and
`PG_MEASURE_SHIM_DIR` (never in the unit gate):
`PG_MEASURE_SHIM_DIR=<pg-nix-shim>/bin go test -tags latency -run TestMeasureShim -v ./cmd/pg-nix-log-wrapped`.

| Case                                                 | Real nix p50 / p95 | Through the shim p50 / p95 | Added p50 / p95 |
| ---------------------------------------------------- | ------------------ | -------------------------- | --------------- |
| no-op `nix build`, live collector (local stub OTLP)  | 67.4 / 101 ms      | 81.7 / 116 ms              | 13.5 / 45.6 ms  |
| no-op `nix build`, dead collector (closed port)      | 63.1 / 103.9 ms    | 78.0 / 126.8 ms            | 13.6 / 43.9 ms  |
| `nix --version` (off the allowlist, one process hop) | 75.6 / 209.7 ms    | 87.9 / 158.3 ms            | 11.3 / 42.7 ms  |

- The "live" collector is a local stub OTLP/HTTP receiver that answers 200 to everything, not a real
  collector; "dead" is a closed loopback port, which refuses at once. A collector that blackholes
  packets can cost up to the 2 s flush bound instead; it was not measured.
- A no-op build emits almost no activity, so these runs pay initialisation, the sweep and the
  export, but not a long tailer drain. A real build adds the tailing cost it always had.
- Sweep cost: the log directory is swept on every wrapped run (retention is by age, 7 days, and
  total size, 2 GiB; never by file count). A loop of 300 short allowlisted runs against the live
  stub took p50 113.7 ms for the first 30 runs and 113.8 ms for the last 30 (p95 237.7 and 141.6 ms)
  while the directory grew to 502 files, 334,834 bytes: no visible growth with directory size at
  that scale. The 7-day and 2 GiB limits were not reached, so the cost of sweeping a directory at
  its cap is unmeasured. The loop's absolute time is higher than the paired runs because it ran
  without a paired baseline and under the same host load.

## Guarantees

- **Fail open.** Any setup failure (unwritable log directory, bad endpoint, exporter error, garbage
  config) runs CMD exactly as if the wrapper were absent, with **empty wrapper stderr**; CMD's
  stdout and stderr are inherited and never copied.
- **Exit status.** CMD's exit code, or `128+n` when it dies of signal `n` (137 for SIGKILL). A missing
  CMD gives 127 with Go's `exec: "x": executable file not found in $PATH`; a non-executable one 126.
- **Unmatched `--override-input` fails the run (exit 3).** See the next section; this is the only
  case where the wrapper's exit status differs from CMD's.
- **Signals.** SIGTERM and SIGHUP are forwarded once. SIGINT is never forwarded: a terminal Ctrl-C
  reaches CMD through the shared process group. After a signal the wrapper waits for CMD, then exports
  as for any run: every build nix had started and not stopped becomes a `nix.build` span with
  `error.type=aborted` and its `nix.drv.name`, and `nix.invocation` carries `process.exit.code` 128+n
  (143 for SIGTERM, 130 for SIGINT) and `error.type=signal`, all inside the flush budget. This is the
  only record of which derivations were running in a killed run (pinned by
  `TestSignalledRunExportsAbortedBuildSpans`).
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
(`nixlog.RetainDrop`; the parser separately skips `type 101` and progress unread, see below). `nix.log.file` on `nix.invocation`
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
  still drops the whole batch, including `nix.invocation` and, after a signal, every aborted
  `nix.build` span, and the exit code is unaffected and the wrapper still exits inside the budget; the
  retained log is then the only record of the run. `PG_NIX_LOG_DEBUG=1` prints the export error.
  What is retried: a refused (503) `/v1/traces` POST, repeatedly, from a 100 ms first wait until the
  flush budget ends. What is dropped: the trace batch of the run, whole, once the budget ends (the
  exporter keeps no spool and nothing is re-sent by a later run). Pinned by
  `TestTransient503OnTracesIsRetriedInsideTheFlushBudget` and `TestCollector503OnASignalledRun`.

Root-run logs in `/var/log/pg-nix-log-wrapped` (directory 0750, files 0600, owned by root) are read
with `sudo`. Membership of the `admin` group is not enough on its own: the directory's group is
`wheel` and the files grant nothing to a group. A non-root observer sees root runs only as spans in
Tempo (`nix.log.file` names the file).

## Querying the traces in Tempo

> **WARNING: a Tempo `/api/search` call without `start` and `end` silently searches only the
> ingesters' recent data (about the last 15 minutes).** It returns a near-empty answer with no error
> (2 traces instead of about 2,000 per 7 days, observed 2026-10-26). Every search MUST carry explicit
> `start` and `end` (epoch seconds, at most 168 h apart), and a search stops at its `limit`.

Use `pg-tempo-search` (`modules/pg-tempo-search`), which always sets them, slices the range into
windows of at most 24 h, splits a window that hits the limit, and prints one merged JSON array:

```sh
pg-tempo-search --lookback 7d '{ resource.service.name = "pg-nix-log-wrapped" && name = "nix.invocation" }'
```

A raw call looks like this:

```sh
END=$(date +%s); START=$((END - 86400))
curl -sG "http://127.0.0.1:3200/api/search?start=$START&end=$END&limit=1000&spss=100" \
  --data-urlencode 'q={ resource.service.name = "pg-nix-log-wrapped" && name = "nix.invocation" }'
```

In a response the span duration is `durationNanos` (a string; not `durationNs`), and Tempo 2.10.5
rejects `| select(span:startTime, span:duration)`: the span already carries `startTimeUnixNano` and
`durationNanos`. More recipes: the `pg-nix-log-wrapped` skill in `pn-workspace-rules`.

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
