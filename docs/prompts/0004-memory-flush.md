Currently, all session data is managed in-memory using the MemoryStore implementation in `store.go` and the `Session` implementation in `session.go`. This can cause serious memory buildup as session content grows in size. I want to design two changes here:

1. When a session completes, it should be flushed from memory, so only active sessions should remain in memory

2. The memory system needs to be aware of which portions of the session have been sync'ed to all currently-active components of the system (host and any currently active clients). Once an `OutputChunk` has been consumed by all clients or a `StdinEntry` has been consumed by the host, it should be purged from the Session object.
