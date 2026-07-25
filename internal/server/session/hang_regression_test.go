package session

import (
	"testing"

	"github.com/IBM/remote-control/internal/common/config"
	"github.com/IBM/remote-control/internal/common/types"
)

// TestConnectCycleDoesNotDuplicateClientRecord is a regression test for a
// production hang: every client connect calls Session.RegisterClient twice -
// once over HTTP with no connection yet (internal/client/client.go's
// pre-registration step), then again over the WebSocket upgrade using the
// clientID it just learned (internal/client/websocket.go's buildWSURL). The
// server used to ignore that clientID and mint a brand-new UUID on every
// call, so each single connect left behind one permanently-unreachable
// "zombie" record (the HTTP one, whose connection is nil forever) in
// addition to the real one. Every subsequent AppendOutput fan-out and every
// Send() to a zombie pays a growing, never-reclaimed cost, which is what
// made connections take progressively longer - and eventually hang - as
// users reconnected (each reconnect is a new process with a fresh clientID,
// so it stacks yet another zombie on top of the ones already there).
func TestConnectCycleDoesNotDuplicateClientRecord(t *testing.T) {
	sess := newSession("test", nil, &config.Config{MaxOutputBuffer: 1024}, "")

	const numConnects = 20
	for range numConnects {
		// HTTP pre-registration: no connection yet, always a fresh ID (a new
		// client process, exactly like a real reconnect).
		httpID, _ := sess.RegisterClient("", nil)
		_ = sess.ApproveClient(httpID, types.PermissionReadWrite)

		// WebSocket upgrade: identifies itself with the ID it just learned
		// from the HTTP step, as the real client does.
		sess.RegisterClient(httpID, nil)
	}

	if got := len(sess.clients); got != numConnects {
		t.Errorf(
			"expected exactly %d client record(s) (one per connect) after %d connects, got %d - "+
				"each connect is leaving behind a duplicate zombie client record",
			numConnects, numConnects, got,
		)
	}
}
