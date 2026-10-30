# lint-grafana-dashboards

> Lint Grafana dashboard JSON: fixed query windows, per-process labels, pushed-gauge sums, duplicate or empty panel titles, range queries on stat panels.
> Run it on source JSON for static dashboards and on the rendered file for generated ones.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Lint dashboards (L1 is only a warning):

`lint-grafana-dashboards {{dashboard.json}} {{other.json}}`

- Fail on fixed windows too:

`lint-grafana-dashboards --error L1 {{dashboard.json}}`

- List every unmarked literal window (file, panel id, title, expr):

`lint-grafana-dashboards --list L1 {{dashboard.json}}`

- Opt a panel out of L1: put `window: fixed` in the panel description.
