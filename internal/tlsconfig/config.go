package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// BuildServerTLSConfig constructs the TLS configuration for the remote-control server.
// serverCertFile/serverKeyFile: this server's identity certificate and key.
// clientCAFile: CA certificate to trust when verifying connecting client certificates.
func BuildServerTLSConfig(serverCertFile, serverKeyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(serverCertFile, serverKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}

	clientCA, err := loadCertPool(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("load client CA: %w", err)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    clientCA,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

// BuildClientTLSConfig constructs the TLS configuration for clients (host wrapper, connect).
// clientCertFile/clientKeyFile: this client's identity certificate and key.
// serverCAFile: CA certificate to trust when verifying the server certificate.
func BuildClientTLSConfig(clientCertFile, clientKeyFile, serverCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}

	serverCA, err := loadCertPool(serverCAFile)
	if err != nil {
		return nil, fmt.Errorf("load server CA: %w", err)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      serverCA,
	}, nil
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
