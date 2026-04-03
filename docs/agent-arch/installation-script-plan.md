# Installation Script Plan

## Overview

Create a single-command installation script for `remote-control` that can be executed via:
```bash
curl -fsSL https://raw.githubusercontent.com/gabe-l-hart/remote-control/main/install.sh | sh
```

## Requirements

### User Preferences
- Accept Go >= 1.24.0 (minimum version check, not exact match)
- Support both system-wide and user-local installation with automatic fallback
- Prioritize Linux and macOS platforms
- Clone from main GitHub repository

### Functional Requirements
1. Create OS-appropriate temporary working directory
2. Check Go version compatibility (>= 1.24.0)
3. Install Go if needed (into temp dir with custom GOROOT)
4. Clone repository to temp dir
5. Build the `remote-control` binary
6. Install binary to OS-standard location
7. Clean up temporary directory

## Architecture

### Script Structure

```
install.sh
├── Initialization & Setup
│   ├── Set strict error handling (set -e, set -u, set -o pipefail)
│   ├── Define color codes for output
│   └── Set up cleanup trap
├── Platform Detection
│   ├── Detect OS (Linux, macOS, Windows/WSL)
│   ├── Detect architecture (x86_64, arm64, aarch64)
│   └── Set platform-specific variables
├── Utility Functions
│   ├── log_info() - Informational messages
│   ├── log_error() - Error messages
│   ├── log_success() - Success messages
│   ├── cleanup() - Remove temp directory
│   └── check_command() - Verify command availability
├── Go Version Management
│   ├── check_go_version() - Check if Go >= 1.24.0 exists
│   ├── get_go_download_url() - Determine correct Go download URL
│   └── install_go() - Download and install Go to temp dir
├── Installation Process
│   ├── create_temp_dir() - Create working directory
│   ├── clone_repository() - Clone remote-control repo
│   ├── build_binary() - Build with Go
│   ├── install_binary() - Copy to installation location
│   └── verify_installation() - Test installed binary
└── Main Execution Flow
```

### Platform Detection

#### Operating Systems
- **Linux**: Detect via `uname -s` returning "Linux"
- **macOS**: Detect via `uname -s` returning "Darwin"
- **Windows/WSL**: Detect via `uname -r` containing "microsoft" or "WSL"

#### Architectures
- **x86_64/amd64**: Standard Intel/AMD 64-bit
- **arm64/aarch64**: ARM 64-bit (Apple Silicon, ARM servers)

### Go Version Management

#### Version Check Logic
```bash
# Check if go exists
if command -v go >/dev/null 2>&1; then
    # Extract version (e.g., "go1.24.0" -> "1.24.0")
    current_version=$(go version | awk '{print $3}' | sed 's/go//')
    
    # Compare versions (need to handle semantic versioning)
    # Required: 1.24.0
    # Accept: 1.24.0, 1.24.1, 1.25.0, 2.0.0, etc.
    if version_gte "$current_version" "1.24.0"; then
        use_system_go=true
    fi
fi
```

#### Version Comparison Function
```bash
version_gte() {
    # Returns 0 (true) if $1 >= $2
    # Handles semantic versioning (major.minor.patch)
    local ver1="$1"
    local ver2="$2"
    
    # Split versions into components
    local IFS='.'
    read -ra ver1_parts <<< "$ver1"
    read -ra ver2_parts <<< "$ver2"
    
    # Compare major, minor, patch
    for i in 0 1 2; do
        local v1="${ver1_parts[$i]:-0}"
        local v2="${ver2_parts[$i]:-0}"
        
        if [ "$v1" -gt "$v2" ]; then
            return 0
        elif [ "$v1" -lt "$v2" ]; then
            return 1
        fi
    done
    
    return 0  # Equal versions
}
```

#### Go Installation Strategy
If Go is not available or version is insufficient:

1. **Download URL Pattern**:
   - Linux x86_64: `https://go.dev/dl/go1.24.0.linux-amd64.tar.gz`
   - Linux arm64: `https://go.dev/dl/go1.24.0.linux-arm64.tar.gz`
   - macOS x86_64: `https://go.dev/dl/go1.24.0.darwin-amd64.tar.gz`
   - macOS arm64: `https://go.dev/dl/go1.24.0.darwin-arm64.tar.gz`

2. **Installation Location**: `$TEMP_DIR/go`

3. **Environment Configuration**:
   ```bash
   export GOROOT="$TEMP_DIR/go"
   export PATH="$GOROOT/bin:$PATH"
   export GOPATH="$TEMP_DIR/gopath"
   ```

### Binary Installation Locations

#### Priority Order (with fallback)

1. **System-wide** (requires write permission):
   - Linux/macOS: `/usr/local/bin/remote-control`
   - Check: `[ -w /usr/local/bin ]`

2. **User-local** (fallback):
   - Linux/macOS: `$HOME/.local/bin/remote-control`
   - Create directory if needed: `mkdir -p "$HOME/.local/bin"`
   - Add to PATH if not present (inform user)

#### Installation Logic
```bash
install_binary() {
    local binary_path="$1"
    local install_location=""
    
    # Try system-wide first
    if [ -w /usr/local/bin ]; then
        install_location="/usr/local/bin/remote-control"
        log_info "Installing to /usr/local/bin (system-wide)"
    else
        # Fallback to user-local
        install_location="$HOME/.local/bin/remote-control"
        mkdir -p "$HOME/.local/bin"
        log_info "Installing to $HOME/.local/bin (user-local)"
        
        # Check if in PATH
        if ! echo "$PATH" | grep -q "$HOME/.local/bin"; then
            log_info "Note: Add $HOME/.local/bin to your PATH"
            log_info "  Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
            log_info "  export PATH=\"\$HOME/.local/bin:\$PATH\""
        fi
    fi
    
    cp "$binary_path" "$install_location"
    chmod +x "$install_location"
    
    echo "$install_location"
}
```

### Temporary Directory Management

#### Creation
```bash
# OS-appropriate temp directory
if [ "$(uname -s)" = "Darwin" ]; then
    TEMP_DIR=$(mktemp -d -t remote-control-install)
else
    TEMP_DIR=$(mktemp -d -t remote-control-install.XXXXXX)
fi
```

#### Cleanup
```bash
cleanup() {
    if [ -n "${TEMP_DIR:-}" ] && [ -d "$TEMP_DIR" ]; then
        log_info "Cleaning up temporary directory..."
        rm -rf "$TEMP_DIR"
    fi
}

# Register cleanup on exit
trap cleanup EXIT INT TERM
```

### Error Handling

#### Strict Mode
```bash
set -e          # Exit on error
set -u          # Exit on undefined variable
set -o pipefail # Exit on pipe failure
```

#### Error Messages
- Clear, actionable error messages
- Include context (what failed, why, how to fix)
- Exit with non-zero status code

#### Common Error Scenarios
1. **Network failures**: Downloading Go or cloning repo
   - Error: "Failed to download Go. Please check your internet connection."
   - Retry logic or manual download instructions

2. **Build failures**: Go build errors
   - Error: "Failed to build remote-control. Build output: <output>"
   - Suggest checking Go version or reporting issue

3. **Permission issues**: Cannot write to installation location
   - Error: "Cannot write to /usr/local/bin. Try running with sudo or install to user directory."
   - Automatic fallback to user-local

4. **Missing dependencies**: git, curl/wget, tar
   - Error: "Required command 'git' not found. Please install git and try again."
   - List all missing dependencies

### Verification Steps

#### Pre-installation Checks
```bash
check_dependencies() {
    local missing_deps=()
    
    for cmd in git tar; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            missing_deps+=("$cmd")
        fi
    done
    
    # Check for curl or wget
    if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
        missing_deps+=("curl or wget")
    fi
    
    if [ ${#missing_deps[@]} -gt 0 ]; then
        log_error "Missing required dependencies: ${missing_deps[*]}"
        log_error "Please install them and try again."
        exit 1
    fi
}
```

#### Post-installation Checks
```bash
verify_installation() {
    local install_path="$1"
    
    # Check binary exists
    if [ ! -f "$install_path" ]; then
        log_error "Binary not found at $install_path"
        return 1
    fi
    
    # Check binary is executable
    if [ ! -x "$install_path" ]; then
        log_error "Binary at $install_path is not executable"
        return 1
    fi
    
    # Test binary runs
    if ! "$install_path" version >/dev/null 2>&1; then
        log_error "Binary at $install_path failed to run"
        return 1
    fi
    
    log_success "Installation verified successfully"
    return 0
}
```

### User Experience

#### Output Format
```
🚀 Remote Control Installer
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━