package tlsconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"time"
)

// GenerateCACert creates a self-signed CA certificate and key pair.
// The certificate and key are written to certOut and keyOut respectively.
// This is a pure function: all inputs are explicit; no global state is read.
func GenerateCACert(certOut, keyOut string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"remote-control"},
			CommonName:   "remote-control CA",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	if err := writePEM(certOut, "CERTIFICATE", certDER); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(keyOut, "EC PRIVATE KEY", keyDER)
}

// GenerateSignedCert creates a certificate signed by the given CA.
// commonName is used as the certificate's CN and DNS SAN.
// This is a pure signing utility: the CA files contain all signing state.
// Future extension: replace the self-signing step with an ACME or Vault call.
func GenerateSignedCert(commonName, certOut, keyOut, caCertFile, caKeyFile string) error {
	// Load CA cert.
	caCertPEM, err := os.ReadFile(caCertFile)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(caCertPEM)
	if block == nil {
		return errInvalidPEM(caCertFile)
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}

	// Load CA key.
	caKeyPEM, err := os.ReadFile(caKeyFile)
	if err != nil {
		return err
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return errInvalidPEM(caKeyFile)
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}

	// Generate new leaf key.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"remote-control"},
			CommonName:   commonName,
		},
		DNSNames:  []string{commonName, "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore: time.Now().Add(-time.Minute),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return err
	}

	if err := writePEM(certOut, "CERTIFICATE", certDER); err != nil {
		return err
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return err
	}
	return writePEM(keyOut, "EC PRIVATE KEY", leafKeyDER)
}

// LoadCACert loads a CA certificate from a PEM file.
func LoadCACert(caFile string) (*x509.Certificate, error) {
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errInvalidPEM(caFile)
	}
	return x509.ParseCertificate(block.Bytes)
}

// CertExpiry returns the NotAfter time for the leaf cert in a PEM file.
func CertExpiry(certFile string) (time.Time, error) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}, errInvalidPEM(certFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

func writePEM(path, pemType string, der []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: pemType, Bytes: der})
}

type pemError struct{ file string }

func (e *pemError) Error() string { return "invalid PEM data in " + e.file }
func errInvalidPEM(file string) error { return &pemError{file} }
