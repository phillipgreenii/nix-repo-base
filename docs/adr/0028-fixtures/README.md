# ADR-0028 golden files

Raw nix `--json-log-path` output captured on 2026-10-01 with **nix 2.34.8+1** (daemon mode, macOS
aarch64). Events carry no timestamps (F4). Store hashes are real and machine-specific; they are
fixtures, not references to be resolved.

| File                            | Scenario                                                       |
| ------------------------------- | -------------------------------------------------------------- |
| `cached-fetch.jsonl`            | `nix build nixpkgs#ffmpeg --no-link` (4 paths fetched)         |
| `local-build.jsonl`             | one local derivation with a 1 s builder                        |
| `failed-build-keep-going.jsonl` | two failing and two good builds, `--keep-going -j4`            |
| `failed-build-dependency.jsonl` | failing dependency; dependent reports `1 dependency failed`    |
| `wait-for-lock.jsonl`           | second client waiting for the same output (type 111)           |
| `flake-check.jsonl`             | `nix flake check` on a trivial two-check flake                 |
| `killed-mid-build.jsonl`        | SIGTERM sent to nix mid-build; ends `interrupted by the user`  |
| `chatty-build.trimmed.jsonl`    | 20,000 build-log lines trimmed to the first 300 type 101 lines |

## Refresh procedure

Use a scratch directory outside the repo. Re-capture with the same `nix` version or note the new one
here and in ADR 0028.

1. Record the version: `nix --version`.
2. Write a scratch `b.nix` whose derivations use `builder = "/bin/sh"` and a unique name per run
   (for example `${toString builtins.currentTime}`) so nix cannot reuse a previous result.
3. Capture each scenario with `nix build ... --json-log-path <abs file>`:
   - cached fetch: `nix build nixpkgs#ffmpeg --no-link --json-log-path <file>` (pick a small path set
     that is not yet in the store);
   - local build: a derivation with `/bin/sleep 1; echo > $out`;
   - failed build: `nix build -f b.nix fail1 fail2 slow ok --no-link --keep-going -j4`, where the
     fail derivations `exit 3` and `exit 4`;
   - dependency failure: a derivation that interpolates `${fail1}` in its script;
   - wait: start the same `sleep 20` derivation in two terminals, keep the log of the SECOND;
   - flake check: `nix flake check` in a scratch flake with two trivial checks;
   - killed mid-build: send SIGTERM to the nix process about 9 s after start (after the `building`
     activity appears) and keep the log;
   - chatty: a builder echoing 20,000 lines, then keep all non-101 lines plus the first 300 type 101
     lines.
4. Copy the files here, keeping the names above.
5. Update the nix version in this README and the Findings in ADR 0028 if the shapes changed.
