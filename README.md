# Remote Control

Secure remote control for terminal-based applications with mutual TLS authentication.

## Quick Start

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/IBM/remote-control/main/install.sh | sh

# Initialize mTLS
remote-control init

# Run the server
remote-control server

# Launch a command
remote-control opencode

# Connect a client
remote-control connect
```

## Why Remote Control

Remote Control enables you to:

- **Control from anywhere**: Start a command locally, control it from anywhere
- **Secure communication**: Encrypted traffic and client authentication with mutual TLS (mTLS)
- **Flexible deployment**: Deploy the control server on your LAN, VPN, or as a public server endpoint

Common use cases:
- Control coding agent sessions from anywhere
- Manage long-running processes through network interruptions
- Collaborative debugging from multiple locations

---

## Project Architecture

```
┌────────────┐  mTLS  ┌───────────────┐  mTLS  ┌────────────┐
│    Host    │◄──────►│     Server    │◄──────►│   Client   │
│ (runs cmd) │        │ (buffers I/O) │        │  (remote)  │
└────────────┘        └───────────────┘        └────────────┘
```

**Components:**

- **Host**: Wraps the target command, proxies stdout to the server and stdin from server
- **Server**: Maintains session state, buffers I/O for multiple clients
- **Client**: Connects to observe output and submit stdin to existing sessions

**Key features:**
- WebSocket-based bidirectional communication
- Session approval workflows
- Multi-client attachment support

---

## Security

Remote Control uses **mutual TLS (mTLS)** for all communications:

- **Server authentication**: Clients verify the server certificate against a trusted CA
- **Client authentication**: Server verifies client certificates against a trusted CA
- **Encrypted transport**: All traffic is encrypted via TLS
- **Separate CAs**: Server and client sides use separate CA certificates for defense in depth

**Certificate management:**

```bash
# List certificates with expiry dates
remote-control cert list

# Verify configured certificates
remote-control cert verify

# Issue a new client certificate
remote-control cert issue <name>
```

---

## Contributing

### Development Setup

1. Build in debug mode (no optimizations):
   ```bash
   make build.debug
   ```

2. Run tests:
   ```bash
   make test
   ```

3. Generate coverage report:
   ```bash
   make coverage-html
   ```

### Running the Server

```bash
# With default listen address
remote-control server

# Custom listen address
remote-control server --addr :9443
```

### Configuration

Configuration is stored in `~/.remote-control/config.json` by default. Customize via:

- `REMOTE_CONTROL_HOME` environment variable
- CLI flags: `--server`, `--client-cert`, `--client-key`, `--client-ca`

---

## License

See LICENSE file for details.
