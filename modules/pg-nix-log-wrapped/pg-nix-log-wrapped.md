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

- Explain why the wrapper silently ran a command unobserved:

`PG_NIX_LOG_DEBUG=1 pg-nix-log-wrapped nix build {{.#package}}`
