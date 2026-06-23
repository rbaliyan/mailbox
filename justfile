# Mailbox build commands

# Container engine for integration services: prefer docker, fall back to podman.
# Both expose a compatible `compose` subcommand.
container-engine := `command -v docker >/dev/null 2>&1 && echo docker || echo podman`

# Default recipe
default:
    @just --list

# Build the project
build:
    go build ./...

# Run tests
test:
    go test ./...

# Run tests with verbose output
test-v:
    go test -v ./...

# Run tests with race detector
test-race:
    go test -race ./...

# Run tests with coverage
test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

# Run the fast, dependency-free smoke suite (and runnable examples)
smoke:
    go test -race -run '^(TestSmoke|Example)' -count=1 .

# Spin up backing services, run all integration suites, then tear down
# (uses docker if installed, otherwise podman)
test-integration:
    {{container-engine}} compose -f docker-compose.test.yml up -d --wait
    MONGO_URI=mongodb://localhost:27019/?directConnection=true \
    POSTGRES_DSN=postgres://mailbox_test:mailbox_test@localhost:5433/mailbox_test?sslmode=disable \
        go test -tags integration -race ./store/... ; \
        status=$? ; \
        {{container-engine}} compose -f docker-compose.test.yml down -v ; \
        exit $status

# Run MongoDB integration tests only (expects services from docker-compose.test.yml)
test-mongo:
    MONGO_URI=mongodb://localhost:27019/?directConnection=true \
        go test -tags integration -race ./store/mongo/...

# Run PostgreSQL integration tests only (expects services from docker-compose.test.yml)
test-pg:
    POSTGRES_DSN=postgres://mailbox_test:mailbox_test@localhost:5433/mailbox_test?sslmode=disable \
        go test -tags integration -race ./store/postgres/...

# Run benchmarks (pass extra flags via ARGS, e.g. just bench BenchmarkSendMessage)
bench *ARGS:
    go test -run='^$' -bench='{{ARGS:-\.}}' -benchmem -benchtime=3s .

# Tidy go modules
tidy:
    go mod tidy

# Format code
fmt:
    go fmt ./...

# Lint code
lint:
    golangci-lint run ./...

# Run vulnerability check
vulncheck:
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Check for outdated dependencies
depcheck:
    go list -m -u all | grep '\[' || echo "All dependencies are up to date"

# Generate go code
generate:
    go generate ./...

# Generate proto stubs (requires buf)
proto:
    buf generate

# Clean generated files
clean:
    rm -f coverage.out coverage.html

# Install mise tools
tools:
    mise install

# Full rebuild: clean, build
rebuild: clean build

# Create and push a new release tag (bumps patch version)
release:
    ./scripts/release.sh
