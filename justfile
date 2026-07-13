# nix-repo-base justfile — light dev recipes.
# Heavy logic goes in scripts/* or Nix derivations, not here.

# Pass recipe args to the shell as $1, $2, … / "$@" so `run` can forward them
# with quoting preserved instead of splitting an unquoted {{ args }} (pg2-gfgxu).
set positional-arguments

default:
    @just --list

# Run Go test suite with race detector + coverage (both Go modules; pg2-gfgxu)
test:
    cd modules/pn && go test -race -coverprofile=coverage.out ./...
    cd modules/jira && go test -race -coverprofile=coverage.out ./...

# Run golangci-lint (both Go modules; pg2-gfgxu)
lint:
    cd modules/pn && golangci-lint run
    cd modules/jira && golangci-lint run

# Run go vet (both Go modules; pg2-gfgxu)
vet:
    cd modules/pn && go vet ./...
    cd modules/jira && go vet ./...

# Build the pn binary via Nix
build:
    nix build .#pn

# Run pn from source, forwarding args with quoting preserved via "$@" (pg2-gfgxu)
run *args:
    ./bin/pn "$@"

# Run nix flake check (all repo validation)
check:
    nix flake check

# Format code with treefmt
fmt:
    nix fmt

# Show pn coverage report
coverage:
    cd modules/pn && go tool cover -func=coverage.out

# Show pn coverage in browser
coverage-html:
    cd modules/pn && go tool cover -html=coverage.out

# Run pn end-to-end smoke tests (scenario-based, temp-dir, repeatable)
pn-smoke:
    cd modules/pn && CGO_ENABLED=0 go test -tags=smoke ./internal/workspace/smoke/... -v
