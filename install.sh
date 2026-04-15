#!/usr/bin/env sh
# Remote Control Installation Script
# Usage: curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh

# Configuration
REPO_URL="${REPO_URL:-https://github.com/IBM/remote-control.git}"
MIN_GO_VERSION="1.24.0"
GO_VERSION="1.24.0"
NO_CLEANUP=${NO_CLEANUP:-"0"}

# Installation mode configuration
INSTALL_FROM_SOURCE="${INSTALL_FROM_SOURCE:-0}"
VERSION="${VERSION:-latest}"
GITHUB_API_URL="https://api.github.com/repos/IBM/remote-control"
GITHUB_RELEASE_URL="https://github.com/IBM/remote-control/releases/download"

# Color codes for output
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    RED=''
    GREEN=''
    YELLOW=''
    BLUE=''
    BOLD=''
    RESET=''
fi

# Global variables
TEMP_DIR=""
INSTALL_GO=false
USE_SYSTEM_GO=false

# Logging functions
log_info() {
    printf "${BLUE}ℹ${RESET} %s\n" "$*" >&2
}

log_success() {
    printf "${GREEN}✓${RESET} %s\n" "$*" >&2
}

log_error() {
    printf "${RED}✗${RESET} %s\n" "$*" >&2
}

log_warning() {
    printf "${YELLOW}⚠${RESET} %s\n" "$*" >&2
}

# Cleanup function
cleanup() {
    if [ "$NO_CLEANUP" != "1" ] && [ -n "${TEMP_DIR:-}" ] && [ -d "$TEMP_DIR" ]; then
        log_info "Cleaning up temporary directory..."
        rm -rf "$TEMP_DIR"
    fi
}

# Register cleanup on exit
trap cleanup EXIT INT TERM

# Check if a command exists
check_command() {
    command -v "$1" >/dev/null 2>&1
}

# Compare semantic versions
# Returns 0 if $1 >= $2, 1 otherwise
version_gte() {
    local ver1="$1"
    local ver2="$2"

    # Handle empty versions
    if [ -z "$ver1" ] || [ -z "$ver2" ]; then
        return 1
    fi

    # Split versions and compare
    local IFS='.'
    set -- $ver1
    local v1_major="${1:-0}" v1_minor="${2:-0}" v1_patch="${3:-0}"
    set -- $ver2
    local v2_major="${1:-0}" v2_minor="${2:-0}" v2_patch="${3:-0}"

    # Compare major version
    if [ "$v1_major" -gt "$v2_major" ]; then
        return 0
    elif [ "$v1_major" -lt "$v2_major" ]; then
        return 1
    fi

    # Compare minor version
    if [ "$v1_minor" -gt "$v2_minor" ]; then
        return 0
    elif [ "$v1_minor" -lt "$v2_minor" ]; then
        return 1
    fi

    # Compare patch version
    if [ "$v1_patch" -ge "$v2_patch" ]; then
        return 0
    else
        return 1
    fi
}

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

# Detect operating system and architecture
detect_platform() {
    local os=""
    local arch=""

    # Detect OS
    case "$(uname -s)" in
        Linux*)
            os="linux"
            ;;
        Darwin*)
            os="darwin"
            ;;
        *)
            log_error "Unsupported operating system: $(uname -s)"
            log_error "This script supports Linux and macOS only."
            exit 1
            ;;
    esac

    # Detect architecture
    case "$(uname -m)" in
        x86_64|amd64)
            arch="amd64"
            ;;
        arm64|aarch64)
            arch="arm64"
            ;;
        *)
            log_error "Unsupported architecture: $(uname -m)"
            log_error "This script supports x86_64/amd64 and arm64/aarch64 only."
            exit 1
            ;;
    esac

    echo "${os}-${arch}"
}

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
    chmod +x "$output_path" 2>/dev/null

    log_success "Prebuilt binary downloaded successfully"
    echo "$output_path"
    return 0
}

# Check dependencies
check_dependencies() {
    local missing_deps=""

    for cmd in git tar; do
        if ! check_command "$cmd"; then
            missing_deps="${missing_deps} $cmd"
        fi
    done

    # Check for curl or wget
    if ! check_command curl && ! check_command wget; then
        missing_deps="${missing_deps} curl-or-wget"
    fi

    if [ -n "$missing_deps" ]; then
        log_error "Missing required dependencies:${missing_deps}"
        log_error "Please install them and try again."
        exit 1
    fi
}

# Download file using curl or wget
download_file() {
    local url="$1"
    local output="$2"

    if check_command curl; then
        curl -fsSL "$url" -o "$output"
    elif check_command wget; then
        wget -q "$url" -O "$output"
    else
        log_error "Neither curl nor wget is available"
        exit 1
    fi
}

# Check Go version
check_go_version() {
    if ! check_command go; then
        log_info "Go is not installed"
        return 1
    fi

    local go_version_output
    go_version_output=$(go version 2>/dev/null || echo "")

    if [ -z "$go_version_output" ]; then
        log_info "Unable to determine Go version"
        return 1
    fi

    # Extract version (e.g., "go version go1.24.0 linux/amd64" -> "1.24.0")
    local current_version
    current_version=$(echo "$go_version_output" | awk '{print $3}' | sed 's/go//')

    if [ -z "$current_version" ]; then
        log_info "Unable to parse Go version"
        return 1
    fi

    log_info "Found Go version: $current_version"

    if version_gte "$current_version" "$MIN_GO_VERSION"; then
        log_success "Go version $current_version meets minimum requirement ($MIN_GO_VERSION)"
        USE_SYSTEM_GO=true
        return 0
    else
        log_warning "Go version $current_version is below minimum requirement ($MIN_GO_VERSION)"
        return 1
    fi
}

# Install Go to temporary directory
install_go() {
    local platform="$1"
    local go_archive="go${GO_VERSION}.${platform}.tar.gz"
    local go_url="https://go.dev/dl/${go_archive}"

    log_info "Downloading Go ${GO_VERSION} for ${platform}..."

    local go_archive_path="${TEMP_DIR}/${go_archive}"
    if ! download_file "$go_url" "$go_archive_path"; then
        log_error "Failed to download Go from $go_url"
        log_error "Please check your internet connection and try again."
        exit 1
    fi

    log_info "Extracting Go..."
    tar -C "$TEMP_DIR" -xzf "$go_archive_path"
    rm "$go_archive_path"

    # Set up Go environment
    export GOROOT="${TEMP_DIR}/go"
    export PATH="${GOROOT}/bin:${PATH}"
    export GOPATH="${TEMP_DIR}/gopath"

    log_success "Go ${GO_VERSION} installed to temporary directory"
    INSTALL_GO=true
}

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

# Clone repository
clone_repository() {
    local repo_dir="${TEMP_DIR}/remote-control"

    log_info "Cloning repository from ${REPO_URL}..."

    if ! git clone --depth 1 "$REPO_URL" "$repo_dir" >/dev/null 2>&1; then
        log_error "Failed to clone repository from ${REPO_URL}"
        log_error "Please check your internet connection and try again."
        exit 1
    fi

    log_success "Repository cloned successfully"
    echo "$repo_dir"
}

# Build binary
build_binary() {
    local repo_dir="$1"

    log_info "Building remote-control binary..."

    cd "$repo_dir"

    if ! go build -o remote-control . 1>&2; then
        log_error "Failed to build remote-control binary"
        log_error "Please check the build output above for details."
        exit 1
    fi

    log_success "Binary built successfully"
    echo "${repo_dir}/remote-control"
}

# Install binary
install_binary() {
    local binary_path="$1"
    local install_location=""

    # Try system-wide installation first
    if [ -w /usr/local/bin ] 2>/dev/null; then
        install_location="/usr/local/bin/remote-control"
        log_info "Installing to /usr/local/bin (system-wide)..."
    else
        # Fallback to user-local installation
        install_location="${HOME}/.local/bin/remote-control"
        mkdir -p "${HOME}/.local/bin"
        log_info "Installing to ${HOME}/.local/bin (user-local)..."

        # Check if in PATH
        case ":${PATH}:" in
            *:"${HOME}/.local/bin":*)
                # Already in PATH
                ;;
            *)
                log_warning "Note: ${HOME}/.local/bin is not in your PATH"
                log_info "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
                log_info "  export PATH=\"\${HOME}/.local/bin:\${PATH}\""
                ;;
        esac
    fi

    if ! cp "$binary_path" "$install_location"; then
        log_error "Failed to copy binary to $install_location"
        exit 1
    fi

    if ! chmod +x "$install_location"; then
        log_error "Failed to make binary executable"
        exit 1
    fi

    log_success "Binary installed to $install_location"
    echo "$install_location"
}

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

# Verify installation
verify_installation() {
    local install_path="$1"

    log_info "Verifying installation..."

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
    local version_output
    if ! version_output=$("$install_path" version 2>&1); then
        log_error "Binary at $install_path failed to run"
        log_error "Output: $version_output"
        return 1
    fi

    log_success "Installation verified successfully"
    log_info "Version: $version_output"
    return 0
}

# Main installation flow
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

# Run main function
main
