package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/common/types"
)

// BuildServerTLSConfig constructs the TLS configuration for the remote-control server.
// serverCertFile/serverKeyFile: this server's identity certificate and key.
// clientCAFile: CA certificate to trust when verifying connecting client certificates.
// authMode: determines whether client certificates are required (mtls) or optional (proxy/none).
func BuildServerTLSConfig(serverCertFile, serverKeyFile, clientCAFile string, authMode types.AuthMode) (*tls.Config, error) {

	var clientAuth tls.ClientAuthType = tls.NoClientCert
	var clientCAs *x509.CertPool = nil

	switch authMode {
	case types.AuthModeMTLS:
		ch.Log(alog.DEBUG, "Configuring server mTLS Auth")
		// Require and verify client certificates (clientCAFile is optional, falls back to system CAs)
		clientCAs = loadCertPoolOrSystem(clientCAFile)
		if clientCAs != nil {
			clientAuth = tls.RequireAndVerifyClientCert
		}

	case types.AuthModeProxy:
		ch.Log(alog.DEBUG, "Configuring server w/out TLS for proxy auth")
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(serverCertFile, serverKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    clientCAs,
		ClientAuth:   clientAuth,
	}, nil
}

// BuildClientTLSConfig constructs the TLS configuration for clients (host wrapper, connect).
// clientCertFile/clientKeyFile: this client's identity certificate and key (both optional).
// serverCAFile: CA certificate to trust when verifying the server certificate (optional, falls back to system CAs).
// skipHostnameVerification: if true, skips only hostname verification (all other cert verification still occurs).
// authMode: determines whether client certificates are sent (mtls) or not (proxy/none).
func BuildClientTLSConfig(clientCertFile, clientKeyFile, serverCAFile string, skipHostnameVerification bool, authMode types.AuthMode) (*tls.Config, error) {
	switch authMode {
	// If no auth, no TLS
	case types.AuthModeNone:
		return nil, nil
	case types.AuthModeMTLS:
		// Log if client credentials are partially configured
		if clientCertFile == "" && clientKeyFile == "" {
			ch.Log(alog.DEBUG, "[remote-control] mTLS mode but no client credentials configured")
		} else if clientCertFile == "" || clientKeyFile == "" {
			ch.Log(alog.DEBUG, "[remote-control] mTLS mode with partial credentials (cert and key must both be present)")
		}
	case types.AuthModeProxy:
		// proxy mode - ignore client cert
		if clientCertFile != "" || clientKeyFile != "" {
			ch.Log(alog.WARNING, "Ignoring client key/cert in proxy auth mode")
			clientCertFile = ""
			clientKeyFile = ""
		}
	}

	// Load CA pool (falls back to system CAs if empty)
	ch.Log(alog.DEBUG, "[BuildClientTLSConfig] Attempting to load server CA from %s", serverCAFile)
	rootCAs := loadCertPoolOrSystem(serverCAFile)

	config := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    rootCAs,
	}

	if skipHostnameVerification {
		ch.Log(alog.DEBUG, "Skipping hostname verification")
		config.InsecureSkipVerify = true
		config.VerifyConnection = verifyConnectionNoHostname(rootCAs)
	}

	// Only load client cert when both cert AND key are present
	if clientCertFile != "" && clientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	return config, nil
}

// loadCertPool loads a PEM CA certificate into an x509.CertPool.
func loadCertPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid certificates found in %s", caFile)
	}
	return pool, nil
}

// loadCertPoolOrSystem loads a PEM CA certificate, or returns the system
// root CAs if caFile is empty. Returns nil only if caFile is empty and
// system roots are unavailable (very rare).
func loadCertPoolOrSystem(caFile string) *x509.CertPool {
	if caFile == "" {
		// Fall back to system root CAs
		pool, err := x509.SystemCertPool()
		if err != nil {
			ch.Log(alog.DEBUG, "[remote-control] no system cert pool available: %v", err)
			return nil
		}
		ch.Log(alog.DEBUG2, "[remote-control] loaded system cert pool")
		return pool
	}
	pool, err := loadCertPool(caFile)
	if err != nil {
		ch.Log(alog.DEBUG, "[remote-control] failed to load CA from %s, falling back to system CAs: %v", caFile, err)
		systemPool, sysErr := x509.SystemCertPool()
		if sysErr != nil {
			ch.Log(alog.WARNING, "[remote-control] no system cert pool available: %v", sysErr)
			return nil
		}
		return systemPool
	}
	ch.Log(alog.DEBUG2, "[remote-control] loaded cert pool from %s", caFile)
	return pool
}

func verifyConnectionNoHostname(rootCAs *x509.CertPool) func(cs tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		// When InsecureSkipVerify is true, we need to manually verify the cert chain
		if len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("no certificates received from server")
		}

		leaf := cs.PeerCertificates[0]
		intermediates := x509.NewCertPool()
		for _, cert := range cs.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}

		// Perform full verification WITHOUT hostname check
		// by not setting DNSName in VerifyOptions
		_, err := leaf.Verify(x509.VerifyOptions{
			Roots:         rootCAs,
			Intermediates: intermediates,
			CurrentTime:   time.Now(),
			// Explicitly NOT setting DNSName - this skips hostname verification
			// while still doing all other checks (chain, expiry, key usage, etc.)
		})
		if err != nil {
			return fmt.Errorf("certificate verification failed: %w", err)
		}

		ch.Log(alog.DEBUG2, "Certificate verified (hostname check skipped)")
		return nil
	}
}
