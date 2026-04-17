# Manual Tests

This directory holds a set of standalone testing tools for use with manual anecdotal testing. It is wrapped into the testing framework for convenience, but should not contain concrete tests.

## Test Tools

### Auth Proxy

This tool implements a simplistic Auth Proxy that is useful for evaluating how the system would behave behind a production auth proxy without first deploying it.

```sh
# Build the binary
make

# Build the auth proxy tool
go test ./test/manual/auth_proxy

# Initialize a temporary TLS setup
REMOTE_CONTROL_HOME=$PWD/tmp ./remote-control init

# Run the proxy on 9443 -> 8443
API_KEY=foobar DUMMY_PROXY_RUN=1 PROXY_TARGET_URL=http://localhost:8443 LISTEN_ADDR=":9443" TLS_CERT_PEM=./tmp/server.crt TLS_KEY_PEM=./tmp/server.key ./auth_proxy.test

# Run the server in proxy mode
REMOTE_CONTROL_HOME=$PWD/tmp ./remote-control server --auth-mode proxy

# Run a host pointed at the proxy
REMOTE_CONTROL_API_KEY=foobar REMOTE_CONTROL_HOME=$PWD/tmp REMOTE_CONTROL_AUTH_MODE=proxy ./remote-control --server https://localhost:9443 bash
```
