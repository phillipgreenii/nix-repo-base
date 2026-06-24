# Architecture Decision Records

Index of ADRs for `phillipg-nix-repo-base`. See `0000-use-architecture-decision-records.md` for the format and process.

| ADR                                                    | Title                                                                   | Status                                         |
| ------------------------------------------------------ | ----------------------------------------------------------------------- | ---------------------------------------------- |
| [0000](0000-use-architecture-decision-records.md)      | Use Architecture Decision Records at repository root                    | Accepted                                       |
| [0001](0001-purpose-of-this-repo.md)                   | Purpose: shared Nix infrastructure repository                           | Accepted                                       |
| [0002](0002-pn-workspace-toml-schema.md)               | `pn-workspace.toml` schema for multi-repo workspace management          | Accepted                                       |
| [0003](0003-claude-marketplace-convention.md)          | Claude Code marketplace convention for `nix-*` repos                    | Accepted                                       |
| [0004](0004-pn-workspace-init-scope.md)                | `pn-workspace-init` scope: clone, lock, reconcile                       | Accepted                                       |
| [0005](0005-mkGoBuilders-factory.md)                   | `mkGoBuilders` factory for Go applications                              | Accepted (version contract superseded by 0006) |
| [0006](0006-source-content-digest-versioning.md)       | Per-source content-digest versioning for custom artifacts               | Accepted                                       |
| [0007](0007-local-replace-go-modules-overlay.md)       | Keep first-party local-replace Go modules "live" via `mkGoApp` overlay  | Superseded by 0008                             |
| [0008](0008-adopt-gomod2nix-for-go-packages.md)        | Adopt `gomod2nix` for Go packages (`mkGoApp`/`mkGoBinary`)              | Accepted                                       |
| [0009](0009-pn-workspace-update-worktree-isolation.md) | `pn workspace update` isolates per-repo work in ephemeral git worktrees | Accepted                                       |
