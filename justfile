# nix-repo-base justfile — light dev recipes.
# Heavy logic goes in scripts/* or Nix derivations, not here.

default:
    @just --list

# Run Go test suite with race detector + coverage
test:
    cd modules/pn && go test -race -coverprofile=coverage.out ./...

# Run golangci-lint
lint:
    cd modules/pn && golangci-lint run

# Run go vet
vet:
    cd modules/pn && go vet ./...

# Build the pn binary via Nix
build:
    nix build .#pn

# Run pn from source (for dev/CI when binary may be stale)
run *args:
    ./modules/pn/run-from-source.sh {{ args }}

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
