// Package e2e contains end-to-end tests for the remote-control tool.
// Tests in this package start a real in-process API server and exercise
// the full host/client stack against it.
package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/server"
)

// testServer starts a real HTTP server on a free port and returns its URL.
// The server is shut down when the test ends.
func testServer(t *testing.T) string {
	t.Helper()

	cfg := &config.Config{RequireApproval: false, MaxOutputBuffer: 1024}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	srv := server.NewServer(addr, cfg)
	go func() {
		// Serve on the pre-bound listener.
		hs := &http.Server{Handler: srv.Handler()}
		hs.Serve(ln) //nolint:errcheck
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5)
		defer cancel()
		srv.Shutdown(ctx) //nolint:errcheck
	})

	return fmt.Sprintf("http://%s", addr)
}
