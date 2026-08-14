# pg-go-mutate

> Report which assertions a Go package's tests are missing.
> Every surviving mutant is an assertion you do not make.

- Analyze the current package:

`pg-go-mutate`

- Analyze a specific package:

`pg-go-mutate ./internal/collect`

- Include tests behind build tags:

`pg-go-mutate --tags {{contract,smoke}} ./internal/collect`

- Emit machine-readable output:

`pg-go-mutate --json ./internal/collect`

- Widen parallelism on an idle machine:

`pg-go-mutate --workers {{4}} ./internal/collect`
