I want to build a tool that will allow me to remotely control a command run from my terminal. I want to write it in `go`. It should work as follows:

## Usage Flow

### Server Start

1. I run the tool in `server` mode in a location where it can be called by both the host machine (where the initial command runs) and the client machine (where the remote control runs)
2. The server provides an API (REST or gRPC, unsure which) that manages sessions
3. Each session holds a proxy space for STDIN, STDOUT, and STDERR

### Session Start

1. I launch a command with `remote-control` as the wrapper (eg `$> remote-control my-command --arg1 foo --arg2 bar asdf`)
2. The `remote-control` binary establishes a new `session` with the running `server`
    * We'll need to figure out secure server configuration and how to store it locally, probably in a `~/.remote-control` or similar directory
3. The `remote-control` wrapper acts as a proxy for STDIN, STDOUT, and STDERR and starts the wrapped command
4. Every time the wrapped command writes to STDIN or STDOUT, the `remote-control` wrapper sends a copy of the write to the server's session
5. Every time the wrapped command reads from STDIN, the `remote-control` wrapper reads from the server's session

### Session Join

1. The user starts a new client connection from a different location (`remote-control connect` from the terminal, or a native mobile app in the future)
2. The client reads all existing STDOUT / STDERR content for the session and renders it (in sequence)
3. Any time new content is published to the session's STDOUT / STDERR, the client fetches it and renders it
    * This could use a pub/sub architecture if we can keep that lightweight
    * The simplest solution would be a polling loop
    * I'd like to avoid anything that attempts to maintain a long-lived connection since I expect the client to be highly mobile and have connectivity interruptions regularly
4. Any time the server attempts to read from STDIN and there is not already any queued input, the client receives a prompt to provide the input
    * There's a race condition here as the host would also be prompted, so we'll need to handle the case where input is given at both locations gracefully

## Security

Since this tool will allow largely unfettered access to the host's terminal, security will be extremely important. Some ideas I have on how to harden this:

* Whenever a client attempts to join a session, the host device must approve the client from the host device
    * NOTE: This would make it impossible to join without being physically present at the host machine (or connected via some other method like ssh) which would work for my use case, but may cause difficulty for some, so it should be configurable.
* Permission scoping for client (read-only, read/write, etc)
* Approval-required mode for host device: Any STDIN input from the client must be accepted by the host
    * This would work well for session sharing where the host is still controlled directly by a user, but a remote client is also submitting inputs.
* Communication with the central server should use mTLS to establish bi-directional trust between the server and its clients
    * To enable this, the central server will need the ability to use an internal TLS secret to sign a generated client's certificate. This process will need to be handled with some flavor of elevated permissions (2fa?)
    * As an initial implementation, it's ok if the mTLS credentials are all generated statically and distributed manually to each part of the system

## Development Plan

I want you to help me construct a robust step-by-step plan to build this tool!