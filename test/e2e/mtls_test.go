package e2e

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabe-l-hart/remote-control/internal/api"
	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/session"
	"github.com/gabe-l-hart/remote-control/internal/tlsconfig"
)

// mtlsServer starts a TLS server with client cert verification.
// Returns the server URL and the CA cert file path.
func mtlsServer(t *testing.T, dir string) (serverURL, serverCAFile, clientCAFile string) {
	t.Helper()

	// Generate CA and server cert.
	serverCAcert := filepath.Join(dir, "server-ca.crt")
	serverCAkey := filepath.Join(dir, "server-ca.key")
	if err := tlsconfig.GenerateCACert(serverCAcert, serverCAkey); err != nil {
		t.Fatalf("generate server CA: %v", err)
	}
	serverCert := filepath.Join(dir, "server.crt")
	serverKey := filepath.Join(dir, "server.key")
	if err := tlsconfig.GenerateSignedCert("localhost", serverCert, serverKey, serverCAcert, serverCAkey); err != nil {
		t.Fatalf("generate server cert: %v", err)
	}

	// Generate client CA and client cert.
	clientCAcert := filepath.Join(dir, "client-ca.crt")
	clientCAkey := filepath.Join(dir, "client-ca.key")
	if err := tlsconfig.GenerateCACert(clientCAcert, clientCAkey); err != nil {
		t.Fatalf("generate client CA: %v", err)
	}

	store, _ := session.NewStore("memory", session.StoreOptions{})
	cfg := &config.Config{RequireApproval: false}
	srv := api.NewServer("127.0.0.1:0", store, cfg)

	tlsCfg, err := tlsconfig.BuildServerTLSConfig(serverCert, serverKey, clientCAcert)
	if err != nil {
		t.Fatalf("build server TLS: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr).String()

	go func() {
		hs := &http.Server{Handler: srv.Handler()}
		hs.Serve(ln) //nolint:errcheck
	}()
	t.Cleanup(func() { ln.Close() })

	return "https://" + addr, serverCAcert, clientCAcert
}

func TestMTLSClientWithValidCert(t *testing.T) {
	dir := t.TempDir()
	serverURL, serverCAFile, clientCAFile := mtlsServer(t, dir)

	// Generate a valid client cert.
	clientCAkey := filepath.Join(dir, "client-ca.key")
	clientCert := filepath.Join(dir, "client.crt")
	clientKey := filepath.Join(dir, "client.key")
	if err := tlsconfig.GenerateSignedCert("test-client", clientCert, clientKey, clientCAFile, clientCAkey); err != nil {
		t.Fatalf("generate client cert: %v", err)
	}

	clientTLSCfg, err := tlsconfig.BuildClientTLSConfig(clientCert, clientKey, serverCAFile)
	if err != nil {
		t.Fatalf("build client TLS: %v", err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
	}

	resp, err := httpClient.Get(serverURL + "/sessions")
	if err != nil {
		t.Fatalf("GET /sessions: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMTLSClientWithoutCert(t *testing.T) {
	dir := t.TempDir()
	serverURL, serverCAFile, _ := mtlsServer(t, dir)

	// Load server CA so we trust the server but present no client cert.
	caPEM, err := os.ReadFile(serverCAFile)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	clientTLSCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
	}

	_, err = httpClient.Get(serverURL + "/sessions")
	if err == nil {
		t.Error("expected TLS handshake error (no client cert), got nil")
	}
}
