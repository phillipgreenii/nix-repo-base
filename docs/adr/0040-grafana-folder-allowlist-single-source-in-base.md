# 0040. The Grafana folder allowlist has one source, owned by nix-repo-base

Date: 2026-11-02
Status: Accepted (completes nix-support-apps ADR 0055; bead pg2-dkacab.6, epic pg2-dkacab)

## Context

The local Grafana is limited to five folders: `Host`, `Daemon Health`, `Claude Agents`, `Work`,
`Build & Nix`. nix-support-apps ADR 0055 enforces this at BUILD time (an enum over
`darwin/modules/observability/folders.nix` and a `folders` mode of its alerting validator). A sixth
folder therefore fails at `pn workspace build`, after commit and often after landing in the repo that
defines the alert. The commit-time hook `check-grafana-alert-uids` (this repo, runs in every
workspace repo) is the right place to fail earlier, but nix-agent-support, nix-ziprecruiter and
nix-repo-base cannot import a file from nix-support-apps, while all of them input nix-repo-base.

## Decision

Option (a): the list is ONE plain text file in this repo,
`modules/pg-check-grafana-alert-uids/pg-check-grafana-alert-uids/grafana-folders.txt`, one title per
line. The flake exports it as `lib.grafanaFoldersFile` (the path) and `lib.grafanaFolders` (the
parsed list of strings). The hook reads it through `GRAFANA_ALLOWED_FOLDERS_FILE`, which the hook
entry sets; the script holds no copy of the titles. Rejected: (b) keep it in support-apps and pass it
to the hook per repo (a second hand-maintained copy); (c) leave the build-time gate as the only gate
(the failure surfaces late).

The hook fails a file whose group `folder:` is not in the list, naming the file and the folder. A
group with no `folder:` key is not checked (the build-time gate and Grafana own that case).

## Consequences

- Changing the list is still an ADR-level decision (ADR 0055): edit `grafana-folders.txt` here.
- nix-support-apps MUST later replace its hand-written list in
  `darwin/modules/observability/folders.nix` with
  `inputs.nix-repo-base.lib.grafanaFolders` (a list of strings, same shape as today), so the
  dashboard-provider enum and the alerting `folders` mode read the same source. Until its locked base
  input is relocked (operator-run) support-apps keeps its own list and still evaluates; the two
  lists are identical, and drift in that window is covered by the relock bead.
- The hook reaches each workspace repo through that repo's locked nix-repo-base input, so a changed
  list takes effect per repo after the usual lock bump.
