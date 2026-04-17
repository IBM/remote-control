package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

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
		// Require and verify client certificates
		clientCA, err := loadCertPool(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("load client CA: %w", err)
		}
		clientAuth = tls.RequireAndVerifyClientCert
		clientCAs = clientCA

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
// clientCertFile/clientKeyFile: this client's identity certificate and key.
// serverCAFile: CA certificate to trust when verifying the server certificate.
// authMode: determines whether client certificates are sent (mtls) or not (proxy/none).
func BuildClientTLSConfig(clientCertFile, clientKeyFile, serverCAFile string, authMode types.AuthMode) (*tls.Config, error) {
	switch authMode {
	// If no auth, no TLS
	case types.AuthModeNone:
		return nil, nil
	case types.AuthModeMTLS:
		// mTLS mode - load client cert
		if clientCertFile == "" || clientKeyFile == "" {
			ch.Log(alog.WARNING, "[remote-control] mTLS mode but client certs not configured")
			return nil, fmt.Errorf("mTLS mode missing client credential")
		}
	case types.AuthModeProxy:
		// proxy mode - ignore client cert
		if clientCertFile != "" || clientKeyFile != "" {
			ch.Log(alog.WARNING, "Ignoring client key/cert in proxy auth mode")
			clientCertFile = ""
			clientKeyFile = ""
		}
	}

	// NOTE: If empty, default to system CAs
	serverCA, err := loadCertPool(serverCAFile)
	if err != nil {
		return nil, fmt.Errorf("load server CA: %w", err)
	}

	config := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    serverCA,
	}

	// Only load client cert in mTLS mode
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
