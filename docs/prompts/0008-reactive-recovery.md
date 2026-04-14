I want to plan for an automatic recovery feature in this project. As currently implemented, both the host and client processes will use a mix of REST and WebSocket endpoints to communicate with the server. If the WS connection dies, there is currently no fallback to REST or reconnect logic.

I've experimented with various architectures that may implement this, but I think the simplest (and most robust) way to implement recovery is to have the client-side of the connection (in either the host or client process) initiate a single recovery attempt any time it tries to interact with the server. There would be three main pieces to this architecture:

1. Any time a client-side connection attempts to send something on the WebSocket that fails, the failed message should be queued (with reasonable max size limits on the queue length after which earlier queued messages get dropped)
2. Any time a client-side connection receives notification of WebSocket disconnection, it should initiate a reconnect polling loop
3. In the reconnect polling loop, the client-side connection should attempt to re-establish the WebSocket connection and, if successful, flush all queued messages (in order) to the server.

All of this new logic should be implemented in `common/websocket.go` so that it is generic to both the `host` and `client` processes.