# pg-nix-log-wrapped

> Run a nix command unchanged and turn its --json-log-path activity stream into OpenTelemetry spans and metrics.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Run a nix command and export spans and metrics to the endpoint in `OTEL_EXPORTER_OTLP_ENDPOINT` or `~/.config/pn/telemetry.toml`:

`pg-nix-log-wrapped nix build {{.#package}}`

- Pick the endpoint explicitly and join an existing trace:

`pg-nix-log-wrapped --otlp-endpoint {{http://127.0.0.1:4318}} --traceparent {{00-<trace-id>-<span-id>-01}} -- nix build {{.#package}}`

- Check the resolved endpoint, whether it is reachable, and whether the log directory is writable:

`pg-nix-log-wrapped --check`

- Run the command with no telemetry at all (the wrapper execs it unmodified):

`PG_NIX_LOG_DISABLE=1 pg-nix-log-wrapped nix build {{.#package}}`

- Fail (exit 3) instead of silently using the locked input when an override names no flake input (use the flake input name, `phillipgreenii-*`, not the repo directory; `PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE=1` turns this off):

`pg-nix-log-wrapped nix build {{.#package}} --override-input {{phillipgreenii-nix-base}} {{git+file:///path/to/repo}}`

- Explain why the wrapper silently ran a command unobserved:

`PG_NIX_LOG_DEBUG=1 pg-nix-log-wrapped nix build {{.#package}}`

- With the pg-nix-shim installed, `nix` and `darwin-rebuild` on `PATH` are the wrapper itself; show the shim and the real binary, and check both resolve to a shim (a shim failure exits 1 with `shim:` lines):

`type -a nix; pg-nix-log-wrapped --check`

- Bypass the shim for one command (a root run drops the variable, so pass it through `env`):

`PG_NIX_LOG_DISABLE=1 nix build {{.#package}}; sudo env PG_NIX_LOG_DISABLE=1 nix build {{.#package}}`

- Bypass the shim permanently by calling the real binary by its absolute path:

`{{/run/current-system/sw/bin/nix}} build {{.#package}}`

- Wrap more verbs than the default (`nix build`, `nix flake check`, `darwin-rebuild build|switch|check`); opt-in verbs are `run`, `fmt`, `eval`, `flake lock`, `flake update`, from `PG_NIX_SHIM_VERBS` (comma list) or `verbs = [...]` in `~/.config/pn/shim.toml`; root reads only `/etc/pg-nix-shim.json`:

`PG_NIX_SHIM_VERBS={{eval,fmt}} nix eval {{.#attr}}`

- Say which branch the shim took (wrap or passthrough and why); the shim prints nothing otherwise:

`PG_NIX_LOG_DEBUG=1 nix build {{.#package}}`
