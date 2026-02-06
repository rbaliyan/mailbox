# Mailbox build commands

# Default recipe
default:
    @just --list

# Build the project
build:
    go build ./...

# Run tests
test:
    go test ./...

# Run tests with coverage
test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

# Tidy go modules
tidy:
    go mod tidy

# Generate go code
generate:
    go generate ./...

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
