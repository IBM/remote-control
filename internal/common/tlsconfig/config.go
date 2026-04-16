package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// BuildServerTLSConfig constructs the TLS configuration for the remote-control server.
// serverCertFile/serverKeyFile: this server's identity certificate and key.
// clientCAFile: CA certificate to trust when verifying connecting client certificates.
// authMode: determines whether client certificates are required (mtls) or optional (proxy/none).
func BuildServerTLSConfig(serverCertFile, serverKeyFile, clientCAFile string, authMode types.AuthMode) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(serverCertFile, serverKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}

	var clientAuth tls.ClientAuthType
	var clientCAs *x509.CertPool

	switch authMode {
	case types.AuthModeMTLS:
		// Require and verify client certificates
		clientCA, err := loadCertPool(clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("load client CA: %w", err)
		}
		clientAuth = tls.RequireAndVerifyClientCert
		clientCAs = clientCA

	case types.AuthModeProxy, types.AuthModeNone:
		// No client certificate required
		clientAuth = tls.NoClientCert
		clientCAs = nil
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
	serverCA, err := loadCertPool(serverCAFile)
	if err != nil {
		return nil, fmt.Errorf("load server CA: %w", err)
	}

	config := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    serverCA,
	}

	// Only load client cert in mTLS mode
	if authMode == types.AuthModeMTLS {
		if clientCertFile == "" || clientKeyFile == "" {
			return nil, fmt.Errorf("client cert/key required for mTLS mode")
		}
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
