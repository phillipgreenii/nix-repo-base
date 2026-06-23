# nix-fast-build evaluation

**Date:** 2026-06-23
**Question:** Would [`nix-fast-build`](https://github.com/Mic92/nix-fast-build) (Mic92) improve how we build the pn-workspace nix projects?
**Verdict:** Not worth adopting based on anything measurable locally. The only scenario where it could win (cold builds) does not reproduce on a warm dev machine — it only matters on fresh CI. Revisit via a CI A/B on the next cold-cache run if we want a definitive number.

## What nix-fast-build is

Speeds up building many flake outputs by combining three things `nix flake check` / `nix build` don't:

1. **Parallel evaluation** via `nix-eval-jobs` (attributes evaluated concurrently, not sequentially).
2. **Eager building** — starts building each derivation the moment _that_ attribute finishes evaluating, instead of waiting for the whole flake to evaluate. This is the headline feature.
3. **CI-friendly output** — GitHub/Gitea Actions job summaries, `--result-file` (JSON/JUnit), `--skip-cached`, retry/stall detection.

Default target is `.#checks.$currentSystem`, so it is nearly a drop-in for `nix flake check`. No install needed — runs via `nix run github:Mic92/nix-fast-build`.

## Fit with our setup

Looked like a strong fit on paper: 6 flake-parts flakes, each with a CI job running `nix flake check --show-trace -L` on an ubuntu + macOS matrix, with `cache-nix-action` for the store cache, plus a "build every package as a check" pattern.

In practice the `.#checks` sets are modest (9–23 attrs per repo); the ~120–190 derivation definitions per repo are mostly _packages_, not all wired into checks. So there is less for parallelism to chew on than the raw counts suggested.

## Measurements

Environment: `aarch64-darwin`, Nix 2.34.7, `max-jobs=11`, `cores=0`, **fully warm store**. nix-fast-build invoked as `nix run github:Mic92/nix-fast-build -- --flake .#checks.$SYS --no-nom`.

| Repo                             | Scenario                       | `nix flake check` | `nix-fast-build`       |
| -------------------------------- | ------------------------------ | ----------------- | ---------------------- |
| overlay (1 check attr)           | warm                           | 6s                | 5s                     |
| support-apps (23 checks, 9 pkgs) | warm                           | 11s               | 9s                     |
| support-apps                     | flake-check ran first, cold    | 124s ⚠️           | —                      |
| repo-base (13 checks)            | reversed order, nfb cold-first | 16s (warm)        | 16s (only 3 drvs cold) |

⚠️ The 124s figure is **contaminated** — `nix flake check` ran first and did all the actual building; by the time nix-fast-build ran the store was warm, so it coasted on cache. That 124s→fast is cache-warming order, **not** a speedup. Recorded here only so the number isn't misread later.

### Why we couldn't get a fair cold number

- Probed every repo: **all `.#checks` and `.#packages` outputs are already built** (warm). There is no uncompiled project on this machine.
- Reversed the order on repo-base to give nix-fast-build the cold run — only 3 derivations were actually cold, so 16s ≈ flake-check's 16s warm. No signal.
- `nix flake check --rebuild` no-op'd (0s); `nix-fast-build` does not accept `--rebuild` (only `--option name value` passthrough). So `--rebuild` can't force a fair cold build.
- A true cold number would require GC'ing the store (declined — destructive) or a fresh CI runner.

## Findings

- **Warm / incremental (our day-to-day + cached CI):** nix-fast-build is consistently ~10–20% faster (9s vs 11s, 5s vs 6s), from parallel evaluation. Real but marginal.
- **Cold (eager building, the headline win):** not measurable locally because the store is fully warm. It only occurs on a fresh CI runner — or right after `update-flakes.yml` bumps inputs and invalidates the `hashFiles('**/flake.lock')` cache key. That auto-update PR is the run worth optimizing and the one we can't reproduce here.
- It ran clean as a true drop-in on every repo — no install, no errors.
- **Not a 1:1 replacement for `nix flake check`'s validation breadth:** it builds the derivation attrs you point it at (`checks`/`packages`); it does not evaluate `overlays`, `devShells`, `formatter`, configs, or validate the output schema the way flake check does. `flake-checker-action` (input hygiene) is independent and still needed either way.

## Recommendation / next step (if revisited)

Don't adopt based on local warm numbers. The decisive, cheap test: add nix-fast-build as a **parallel CI job** on one repo (e.g. nix-personal) targeting `.#checks.$system`, keep `nix flake check` as the authoritative gate, and compare wall-clock on the **next `update-flakes` PR** (guaranteed cold cache). If it doesn't clearly beat flake check there, drop it.
