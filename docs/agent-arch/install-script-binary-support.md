# Install Script Binary Support Implementation Plan

## Overview

Update `install.sh` to default to installing prebuilt binaries from GitHub releases, with configurable fallback to building from source.

## Design Decisions

Based on user requirements:
- **Environment variable**: `INSTALL_FROM_SOURCE=1` to force source build
- **Version selection**: Support `VERSION` env var for specific versions (e.g., `VERSION=v0.0.1`)
- **Missing binary handling**: Auto-fallback to source build if prebuilt binary unavailable
- **Rate limiting**: Handle GitHub API rate limits gracefully with fallback to source

## Current State Analysis

### Existing install.sh Structure
1. Platform detection (`detect_platform()`)
2. Dependency checking (`check_dependencies()`)
3. Go version checking and installation (`check_go_version()`, `install_go()`)
4. Repository cloning (`clone_repository()`)
5. Binary building (`build_binary()`)
6. Binary installation (`install_binary()`)
7. Installation verification (`verify_installation()`)

### Release Artifact Naming Convention
From `.github/workflows/release.yml`:
- Format: `remote-control-{os}-{arch}`
- Examples:
  - `remote-control-darwin-arm64`
  - `remote-control-linux-arm64`
  - `remote-control-android-arm64`

### Current Platform Detection
Returns format: `{os}-{arch}` (e.g., `darwin-arm64`, `linux-amd64`)
- Matches release artifact naming convention ✓

## Implementation Plan

### 1. Add New Configuration Variables

Add at the top of the script after existing configuration:

```bash
# Installation mode configuration
INSTALL_FROM_SOURCE="${INSTALL_FROM_SOURCE:-0}"
VERSION="${VERSION:-latest}"
GITHUB_API_URL="https://api.github.com/repos/IBM/remote-control"
GITHUB_RELEASE_URL="https://github.com/IBM/remote-control/releases/download"
```

### 2. Create GitHub API Functions

#### 2.1 Fetch Latest Release Info

```bash
# Fetch latest release information from GitHub API
# Returns: release tag (e.g., "v0.0.1") or empty string on failure
fetch_latest_release() {
    local api_url="${GITHUB_API_URL}/releases/latest"
    local response=""
    
    log_info "Fetching latest release information..."
    
    if check_command curl; then
        response=$(curl -fsSL -H "Accept: application/vnd.github.v3+json" "$api_url" 2>/dev/null || echo "")
    elif check_command wget; then
        response=$(wget -qO- --header="Accept: application/vnd.github.v3+json" "$api_url" 2>/dev/null || echo "")
    fi
    
    if [ -z "$response" ]; then
        log_warning "Failed to fetch release information from GitHub API"
        return 1
    fi
    
    # Extract tag_name from JSON response (simple grep/sed approach)
    local tag
    tag=$(echo "$response" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')
    
    if [ -z "$tag" ]; then
        log_warning "Failed to parse release tag from API response"
        return 1
    fi
    
    echo "$tag"
    return 0
}
```

#### 2.2 Get Release Version

```bash
# Determine which version to install
# Returns: version tag or empty string
get_release_version() {
    if [ "$VERSION" = "latest" ]; then
        fetch_latest_release
    else
        # Validate version format (should start with 'v')
        case "$VERSION" in
            v*)
                echo "$VERSION"
                return 0
                ;;
            *)
                log_error "Invalid version format: $VERSION (should start with 'v', e.g., v0.0.1)"
                return 1
                ;;
        esac
    fi
}
```

### 3. Create Binary Download Functions

#### 3.1 Download Prebuilt Binary

```bash
# Download prebuilt binary from GitHub release
# Args: $1 = version tag, $2 = platform (e.g., "darwin-arm64")
# Returns: path to downloaded binary or empty string on failure
download_prebuilt_binary() {
    local version="$1"
    local platform="$2"
    local binary_name="remote-control-${platform}"
    local download_url="${GITHUB_RELEASE_URL}/${version}/${binary_name}"
    local output_path="${TEMP_DIR}/${binary_name}"
    
    log_info "Downloading prebuilt binary for ${platform}..."
    log_info "URL: ${download_url}"
    
    if check_command curl; then
        if ! curl -fsSL "$download_url" -o "$output_path" 2>/dev/null; then
            log_warning "Failed to download prebuilt binary from ${download_url}"
            return 1
        fi
    elif check_command wget; then
        if ! wget -q "$download_url" -O "$output_path" 2>/dev/null; then
            log_warning "Failed to download prebuilt binary from ${download_url}"
            return 1
        fi
    else
        log_error "Neither curl nor wget is available"
        return 1
    fi
    
    # Verify the file was downloaded and is not empty
    if [ ! -f "$output_path" ] || [ ! -s "$output_path" ]; then
        log_warning "Downloaded file is missing or empty"
        return 1
    fi
    
    # Make binary executable
    chmod +x "$output_path"
    
    log_success "Prebuilt binary downloaded successfully"
    echo "$output_path"
    return 0
}
```

#### 3.2 Try Binary Installation

```bash
# Attempt to install from prebuilt binary
# Args: $1 = platform
# Returns: 0 on success, 1 on failure (triggers fallback)
try_binary_install() {
    local platform="$1"
    local version=""
    local binary_path=""
    
    # Get version to install
    if ! version=$(get_release_version); then
        log_warning "Could not determine release version"
        return 1
    fi
    
    log_info "Target version: ${version}"
    
    # Download prebuilt binary
    if ! binary_path=$(download_prebuilt_binary "$version" "$platform"); then
        log_warning "Prebuilt binary not available for ${platform}"
        return 1
    fi
    
    # Install binary
    local install_path
    if ! install_path=$(install_binary "$binary_path"); then
        log_error "Failed to install binary"
        return 1
    fi
    
    # Verify installation
    if ! verify_installation "$install_path"; then
        log_error "Installation verification failed"
        return 1
    fi
    
    return 0
}
```

### 4. Create Source Build Function

Refactor existing build logic into a dedicated function:

```bash
# Build and install from source
# Args: $1 = platform
# Returns: 0 on success, 1 on failure
build_from_source() {
    local platform="$1"
    
    log_info "Building from source..."
    
    # Check Go version
    if ! check_go_version; then
        install_go "$platform"
    fi
    
    # Clone repository
    local repo_dir
    if ! repo_dir=$(clone_repository); then
        log_error "Failed to clone repository"
        return 1
    fi
    
    # Build binary
    local binary_path
    if ! binary_path=$(build_binary "$repo_dir"); then
        log_error "Failed to build binary"
        return 1
    fi
    
    # Install binary
    local install_path
    if ! install_path=$(install_binary "$binary_path"); then
        log_error "Failed to install binary"
        return 1
    fi
    
    # Verify installation
    if ! verify_installation "$install_path"; then
        log_error "Installation verification failed"
        return 1
    fi
    
    return 0
}
```

### 5. Update Main Installation Flow

Replace the existing main() function logic with:

```bash
main() {
    echo ""
    echo "${BOLD}🚀 Remote Control Installer${RESET}"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    # Detect platform
    log_info "Detecting platform..."
    local platform
    platform=$(detect_platform)
    log_success "Platform: $platform"

    # Check dependencies
    log_info "Checking dependencies..."
    check_dependencies
    log_success "All dependencies found"

    # Create temporary directory
    log_info "Creating temporary directory..."
    if [ "$(uname -s)" = "Darwin" ]; then
        TEMP_DIR=$(mktemp -d -t remote-control-install)
    else
        TEMP_DIR=$(mktemp -d -t remote-control-install.XXXXXX)
    fi
    log_success "Temporary directory: $TEMP_DIR"

    # Determine installation method
    if [ "$INSTALL_FROM_SOURCE" = "1" ]; then
        log_info "INSTALL_FROM_SOURCE=1 detected, building from source..."
        if ! build_from_source "$platform"; then
            log_error "Source build failed"
            exit 1
        fi
    else
        log_info "Attempting to install prebuilt binary..."
        if ! try_binary_install "$platform"; then
            log_warning "Prebuilt binary installation failed, falling back to source build..."
            if ! build_from_source "$platform"; then
                log_error "Source build failed"
                exit 1
            fi
        fi
    fi

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "${GREEN}✓${RESET} ${BOLD}Installation complete!${RESET}"
    echo ""
    log_info "Get started with:"
    echo "  ${BOLD}remote-control init${RESET}     # Initialize mTLS certificates"
    echo "  ${BOLD}remote-control server${RESET}   # Start the server"
    echo "  ${BOLD}remote-control --help${RESET}   # View all commands"
    echo ""
}
```

## Error Handling Strategy

### GitHub API Rate Limiting
- Anonymous requests: 60/hour
- If rate limited (HTTP 403 with specific headers), fall back to source build
- Log clear message about rate limiting

### Missing Prebuilt Binary
- If 404 on binary download, automatically fall back to source build
- Log informative message about platform support

### Network Failures
- Timeout on API calls after 10 seconds
- Fall back to source build on any network error
- Provide clear error messages

## Testing Strategy

### Manual Testing Scenarios

1. **Default installation (latest binary)**
   ```bash
   curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
   ```

2. **Specific version installation**
   ```bash
   VERSION=v0.0.1 curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
   ```

3. **Force source build**
   ```bash
   INSTALL_FROM_SOURCE=1 curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
   ```

4. **Unsupported platform (should auto-fallback)**
   ```bash
   # On linux-amd64 (not in current release matrix)
   curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
   ```

5. **Rate limiting simulation**
   ```bash
   # After 60 API calls in an hour
   curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
   ```

### Platform Coverage

Test on:
- ✓ macOS ARM64 (darwin-arm64) - has prebuilt binary
- ✓ Linux ARM64 (linux-arm64) - has prebuilt binary
- ✓ Linux AMD64 (linux-amd64) - no prebuilt binary, should fallback
- ✓ macOS Intel (darwin-amd64) - no prebuilt binary, should fallback

## Documentation Updates

### README.md Updates

Add installation options section:

```markdown
## Installation

### Quick Install (Recommended)

Install the latest prebuilt binary:

```bash
curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
```

### Install Specific Version

```bash
VERSION=v0.0.1 curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
```

### Build from Source

```bash
INSTALL_FROM_SOURCE=1 curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh
```

### Installation Options

- `VERSION`: Specify a release version (default: `latest`)
- `INSTALL_FROM_SOURCE`: Set to `1` to build from source instead of using prebuilt binaries
- `REPO_URL`: Custom repository URL (default: `https://github.com/IBM/remote-control.git`)
- `NO_CLEANUP`: Set to `1` to keep temporary files after installation

**Note**: The installer automatically falls back to building from source if:
- No prebuilt binary exists for your platform
- GitHub API rate limits are exceeded
- Network issues prevent binary download
```

## Implementation Checklist

- [ ] Add new configuration variables
- [ ] Implement `fetch_latest_release()` function
- [ ] Implement `get_release_version()` function
- [ ] Implement `download_prebuilt_binary()` function
- [ ] Implement `try_binary_install()` function
- [ ] Refactor existing build logic into `build_from_source()` function
- [ ] Update `main()` function with new installation flow
- [ ] Add error handling for rate limiting
- [ ] Add error handling for missing binaries
- [ ] Test on all supported platforms
- [ ] Test fallback scenarios
- [ ] Update README.md with new installation options
- [ ] Test with specific version installation
- [ ] Verify backward compatibility

## Backward Compatibility

The changes maintain full backward compatibility:
- Existing environment variables (`REPO_URL`, `NO_CLEANUP`) continue to work
- Default behavior changes from "always build from source" to "try binary first, fallback to source"
- Users can opt into old behavior with `INSTALL_FROM_SOURCE=1`
- All existing functionality remains intact

## Security Considerations

1. **Binary Verification**: Currently no checksum verification
   - Future enhancement: Download and verify SHA256 checksums
   - GitHub releases are served over HTTPS (transport security)

2. **API Authentication**: Using anonymous GitHub API access
   - Rate limited to 60 requests/hour
   - Acceptable for installation script usage pattern

3. **Source Fallback**: Maintains security of building from source
   - Clones from official repository
   - Uses verified Go toolchain

## Future Enhancements

1. **Checksum Verification**
   - Generate SHA256 checksums in release workflow
   - Verify checksums in install script

2. **Progress Indicators**
   - Show download progress for large binaries
   - Improve user experience for slow connections

3. **Caching**
   - Cache downloaded binaries in `~/.remote-control/cache`
   - Skip re-download if version already cached

4. **Platform Detection Improvements**
   - Support more architectures (386, ppc64le, etc.)
   - Better error messages for unsupported platforms

5. **Authenticated API Access**
   - Support `GITHUB_TOKEN` env var for higher rate limits
   - Useful for CI/CD environments
