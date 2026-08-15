# pg-go-mutate

> Report which assertions a Go package's tests are missing.
> Every surviving mutant is an assertion you do not make.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Analyze the current package:

`pg-go-mutate`

- Analyze a specific package (a directory, walked recursively):

`pg-go-mutate ./internal/collect`

- Include tests behind build tags:

`pg-go-mutate --tags {{contract,smoke}} ./internal/collect`

- Emit machine-readable output:

`pg-go-mutate --json ./internal/collect`

- Widen parallelism on an idle machine:

`pg-go-mutate --workers {{4}} ./internal/collect`

- Raise the per-mutant test timeout for a slow suite (does NOT bound the compile phase):

`pg-go-mutate --timeout {{180}} ./internal/collect`

- Show usage:

`pg-go-mutate --help`

## Reading the output

Real output from `pg-go-mutate modules/jira`, **truncated** — the full worklist ran to 202 entries,
and the repository root is abridged to `…`:

```
pg-go-mutate: 202 surviving mutants in …/modules/jira

cmd/pjira/main.go
    L40   Replace != with >   [conditional_binary]
    L52   Replace branch condition "path == \"\"" with true   [branch_condition]
    L52   Replace branch condition "path == \"\"" with false   [branch_condition]
    L52   Replace == with <   [conditional_binary]
    L56   Replace branch condition "err != nil" with false   [branch_condition]
    L57   Replace return err with return nil   [error_nilify]
    L64   Replace branch condition "cfg.BaseURL == \"\"" with false   [branch_condition]

pkg/pjira/adf.go
    L12   Replace branch condition "len(raw) == 0" with false   [branch_condition]
    L12   Replace == with <   [conditional_binary]

[… 193 further entries omitted, including 3 more files …]

Each surviving mutant is an assertion your tests do not make.

  killed 214  survived 208 (202 actionable, 6 no-op)  not-viable 21  timed-out 0  errors 0
```

Counts move by a mutant or two between runs on identical source — including which mutants are
viable — so verify a fix by re-running and checking that **that specific mutant** is now killed,
never by comparing totals.

- Each entry is a **file**, a **line**, the mutation, and the operator that produced it. Write an
  assertion that would fail under that mutation, then re-run and confirm that specific mutant —
  matched on `file:line:type` — is now killed.
- `survived 211 (205 actionable, 6 no-op)` reconciles the summary with the first line: the raw
  bucket counts every survivor so the five statuses sum to the mutant total, while **no-op** mutants
  (where the mutated code is identical to the original, so no assertion can ever kill them) are
  excluded from the worklist and from the headline count.
- The exit status is **0** whenever an analysis completed, however many mutants survived. This is a
  diagnostic and gates nothing; a non-zero exit means the run itself failed.
