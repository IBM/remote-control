// Package integration contains in-process integration tests for the host package.
// This file also provides a dummy auth proxy tool that can be run as a standalone
// binary to proxy HTTPS traffic to a target endpoint, simulating an auth proxy.
package authproxy

import (
	"crypto/tls"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"testing"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

var log = alog.UseChannel("dummy-proxy")

func TestMain(m *testing.M) {
	// Configure logging
	log_level := "info"
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		log_level = v
	}
	log_filters := ""
	if v := os.Getenv("LOG_FILTERS"); v != "" {
		log_filters = v
	}
	level, _ := alog.LevelFromString(log_level)
	chanMap, _ := alog.ParseChannelFilter(log_filters)
	alog.Config(level, chanMap)
	alog.EnableGID()

	// Check if running as standalone binary
	if os.Getenv("DUMMY_PROXY_RUN") == "1" {
		runProxy()
		return
	}

	// Run tests
	os.Exit(m.Run())
}

func runProxy() {
	// Parse configuration
	targetURL := os.Getenv("PROXY_TARGET_URL")
	if targetURL == "" {
		log.Fatalf(alog.FATAL, "PROXY_TARGET_URL environment variable is required")
	}

	certPEM := os.Getenv("TLS_CERT_PEM")
	keyPEM := os.Getenv("TLS_KEY_PEM")
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8443"
	}

	// Load or generate certificate
	var tlsCert tls.Certificate
	var err error

	if certPEM != "" && keyPEM != "" {
		tlsCert, err = tls.LoadX509KeyPair(certPEM, keyPEM)
		if err != nil {
			log.Fatalf(alog.FATAL, "Failed to load TLS cert/key: %v", err)
		}
	} else {
		log.Fatalf(alog.FATAL, "No certs provided: %v", err)
	}

	// Parse target URL
	target, err := url.Parse(targetURL)
	if err != nil {
		log.Fatalf(alog.FATAL, "Invalid target URL: %v", err)
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Custom error handler
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Log(alog.ERROR, "Proxy error: path=%s, err=%v", r.URL.Path, err)
		http.Error(w, "Proxy error", http.StatusBadGateway)
	}

	// Proxy handler: forwards all requests (including WebSocket) to target
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Log(alog.DEBUG, "Proxying request: %s %s", r.Method, r.URL.Path)
		proxy.ServeHTTP(w, r)
	})

	// Create TLS config
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}

	// Start HTTPS server
	server := &http.Server{
		Addr:      listenAddr,
		Handler:   handler,
		TLSConfig: tlsConfig,
	}

	log.Log(alog.INFO, "Starting dummy auth proxy. %s -> %s", listenAddr, targetURL)
	server.ListenAndServeTLS("", "")
}
