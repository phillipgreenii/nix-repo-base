# pg-tempo-search

> Search Tempo with an explicit time window, always: slices the range into at most 24 h windows, splits slices that hit the limit, merges and flattens the traces into one JSON array.
> Without start and end Tempo silently searches only the last ~15 minutes; this helper never omits them.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Search the last 7 days (the default lookback):

`pg-tempo-search '{{ resource.service.name = "pg-nix-log-wrapped" }}'`

- Search a shorter window (at most 168h):

`pg-tempo-search --lookback {{12h}} '{{ name = "pn.verb" }}'`

- Return chosen span attributes, flattened, then summarize with jq:

`pg-tempo-search --select '{{span.pn.verb, span.pn.exit_code}}' '{{ name = "pn.verb" }}' | jq '[.[].spans[].durationNanos]'`

- Point at another Tempo:

`PG_TEMPO_URL={{http://127.0.0.1:3200}} pg-tempo-search '{{query}}'`

- Exit codes: `2` usage error (lookback over 168h), `3` a 1 h slice hit the limit so results may be truncated (the array is still printed):

`pg-tempo-search --help`
