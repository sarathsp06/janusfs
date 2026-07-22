# Development Guide for JanusFS

This guide covers building, testing, linting, and contributing to JanusFS.

## Quick Reference Commands

| Action | Command | Notes |
|--------|---------|-------|
| Build | `make build` | Output: `build/janusfs-$(GOOS)-$(GOARCH)` |
| Run all tests | `make test` | `go test ./...` |
| Race tests | `make test-race` | `go test -race ./...` |
| Mounted integration tests | `make integration` | `-tags fuseintegration`; mounts for real |
| Leak oracle | `make leak-oracle` | Sentinel-secret scan over every mounted read |
| Benchmarks | `make bench` | Compares against `bench/BASELINE.md` |
| Lint | `make lint` | `golangci-lint` |
| Format | `make fmt` | `gofmt` + `goimports` |

## Code Conventions & Architecture

Refer to **[`SPEC.md`](../SPEC.md)** for the detailed engineering contract and **[`AGENTS.md`](../AGENTS.md)** for coding agent conventions.

### Key Packages

- `cmd/janusfs/`: CLI entrypoints and command dispatch.
- `internal/config/`: Single-source configuration and validation.
- `internal/rules/`: Policy compiler and `.janusignore`/`.janusmask` parsing.
- `internal/engine/`: Pure decision resolution logic.
- `internal/redact/`: Size-preserving `*` redaction pipeline.
- `internal/provider/`: Content cache and memory-management layers.
- `internal/mount/`: Go-FUSE filesystems adapter for macOS (macFUSE) and Linux.

## Build Requirements

- Go 1.26 or higher
- FUSE runtimes installed locally:
  - **macOS:** macFUSE system extension approved
  - **Linux:** `fuse3` and `libfuse3-dev` libraries installed

### Building Locally

```bash
# Compile the binary for your host OS/architecture
make build

# Install the binary into your $GOPATH/bin (or $GOBIN)
make install
```

### Running Tests

We practice proactive testing to prevent security regressions and memory leaks.

```bash
# Run unit tests
make test

# Run unit tests with race detection
make test-race

# Run tests with HTML coverage report (writes coverageprofile to coverage.html)
make coverage

# Run FUSE mounted integration tests (requires FUSE installed)
make integration

# Run the leak-oracle sentinel scan through a live mount
make leak-oracle
```

### Formatting & Linting

Before submitting your pull request, ensure the codebase is clean:

```bash
# Format your Go files
make fmt

# Run Go vet
make vet

# Run golangci-lint
make lint
```

### Simulating Release Snapshots

To sanity-check the release configuration:

```bash
# Check if GoReleaser config is valid
make release-check

# Run GoReleaser snapshot build locally
make release-snapshot
```
