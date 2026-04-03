I'd like to provide a single script that a user can run directly from `curl` similar to `curl -fsSL https://ollama.com/install.sh | sh`. This script should:

1. Use the appropriate operating-system-specific tool to create a temporary working directory for the installation process
2. Check the version of `go` available on the user's system
3. If the version either doesn't exist or isn't compatible, install the necessary version of `go` into the working directory (https://go.dev/doc/install) and configure GOROOT to be within the temporary working directory
4. Clone the repository locally to the working dir
5. Build the `remote-control` binary
6. Install the `remote-control` binary to the os-standard local binary location (eg `/usr/local/bin` or `$HOME/.local`)
7. Clean up the working dir