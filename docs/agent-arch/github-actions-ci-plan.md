# GitHub Actions CI Configuration Plan

## Overview

This document outlines the GitHub Actions CI configuration for the remote-control project. We'll create two workflows:

1. **PR Testing Workflow** (`test.yml`) - Runs on every pull request
2. **Release Build Workflow** (`release.yml`) - Runs on release creation and builds multi-platform binaries

## Workflow 1: PR Testing (`test.yml`)

### Trigger
- On pull request events (opened, synchronized, reopened)
- On push to main branch (optional, for continuous validation)

### Jobs

#### Job: `test`
**Purpose**: Build and test the codebase

**Steps**:
1. **Checkout code** - Use `actions/checkout@v4`
2. **Setup Go** - Use `actions/setup-go@v5` with Go version from `go.mod` (1.24.0)
3. **Cache Go modules** - Use built-in caching from `actions/setup-go@v5`
4. **Build** - Run `make build` (equivalent to `go build .`)
5. **Run tests** - Run `make test` (runs `go test ./... -race -count=1 -timeout 120s`)

**Environment**:
- Runner: `ubuntu-latest` (standard for Go projects)
- Go version: Read from `go.mod` or explicitly set to `1.24.0`

### Workflow File Structure

```yaml
name: Test

on:
  pull_request:
    branches: [ main ]
  push:
    branches: [ main ]

jobs:
  test:
    name: Build and Test
    runs-on: ubuntu-latest
    
    steps:
      - name: Checkout code
        uses: actions/checkout@v4
      
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.24.0'
          cache: true
      
      - name: Build
        run: make build
      
      - name: Run tests
        run: make test
```

## Workflow 2: Release Build (`release.yml`)

### Trigger
- On release published events

### Jobs

#### Job: `build-release`
**Purpose**: Build binaries for multiple platforms and attach to release

**Strategy**: Use matrix build for different platforms

**Platforms**:
1. **macOS ARM64** (Apple Silicon)
   - GOOS: `darwin`
   - GOARCH: `arm64`
   - Binary name: `remote-control-darwin-arm64`

2. **Linux ARM64** (aarch64)
   - GOOS: `linux`
   - GOARCH: `arm64`
   - Binary name: `remote-control-linux-arm64`

3. **Android ARM64**
   - Use `make build.android` command
   - Binary name: `remote-control-android-arm64`

**Steps**:
1. **Checkout code** - Use `actions/checkout@v4`
2. **Setup Go** - Use `actions/setup-go@v5` with Go version 1.24.0
3. **Build binary** - Build for specific platform using GOOS/GOARCH or make command
4. **Upload release asset** - Use `actions/upload-release-asset@v1` or `softprops/action-gh-release@v1`

### Workflow File Structure

```yaml
name: Release

on:
  release:
    types: [published]

jobs:
  build-release:
    name: Build Release Binaries
    runs-on: ubuntu-latest
    
    strategy:
      matrix:
        include:
          - goos: darwin
            goarch: arm64
            output: remote-control-darwin-arm64
          - goos: linux
            goarch: arm64
            output: remote-control-linux-arm64
          - goos: android
            goarch: arm64
            output: remote-control-android-arm64
    
    steps:
      - name: Checkout code
        uses: actions/checkout@v4
      
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.24.0'
          cache: true
      
      - name: Build binary
        env:
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
        run: |
          if [ "${{ matrix.goos }}" = "android" ]; then
            make build.android
            mv remote-control-android ${{ matrix.output }}
          else
            go build -o ${{ matrix.output }} .
          fi
      
      - name: Upload Release Asset
        uses: softprops/action-gh-release@v1
        with:
          files: ${{ matrix.output }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

## Implementation Steps

1. **Create directory structure**:
   ```bash
   mkdir -p .github/workflows
   ```

2. **Create `test.yml`**:
   - Place in `.github/workflows/test.yml`
   - Configure PR and push triggers
   - Add build and test steps

3. **Create `release.yml`**:
   - Place in `.github/workflows/release.yml`
   - Configure release trigger
   - Add matrix build strategy for multiple platforms
   - Configure asset upload

4. **Test workflows**:
   - Create a test PR to verify `test.yml`
   - Create a test release to verify `release.yml`

## Best Practices Applied

1. **Use latest stable action versions**: `@v4` for checkout, `@v5` for setup-go
2. **Enable Go module caching**: Built into `actions/setup-go@v5` with `cache: true`
3. **Explicit Go version**: Pin to `1.24.0` from `go.mod`
4. **Matrix builds**: Efficient parallel building for multiple platforms
5. **Standard runners**: Use `ubuntu-latest` for consistency and speed
6. **Minimal permissions**: Use default `GITHUB_TOKEN` with minimal required permissions
7. **Clear naming**: Descriptive job and step names for easy debugging

## Notes

- The `test.yml` workflow runs on both PRs and pushes to main for continuous validation
- The `release.yml` workflow only runs when a release is published
- All binaries are built with the same Go version for consistency
- The Android build uses the existing `make build.android` target for consistency with local development
- Binary names include platform identifiers for clarity (e.g., `remote-control-darwin-arm64`)

## Future Enhancements

Potential improvements for future consideration:
- Add code coverage reporting
- Add linting (golangci-lint)
- Add security scanning (gosec)
- Add dependency vulnerability scanning
- Add build caching for faster builds
- Add checksums for release binaries
- Add signing for release binaries
