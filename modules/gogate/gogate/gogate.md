# gogate

> Sequential Go validation gate: gofmt -l, go build, go vet, go test — stops at the first failure and prints only a fixed tail of its output.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Run the full gate against the current module:

`gogate`

- Scope build/vet/test to one package (gofmt always checks the whole working directory):

`gogate --pkg {{./internal/collect}}`

- Skip the test stage (fmt/build/vet only):

`gogate --quick`

- Target specific tests, passed through verbatim to `go test`:

`gogate -- -run {{TestFoo}} -count={{1}}`

- Show usage:

`gogate --help`
