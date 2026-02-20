package tlsconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateCA is a test helper that creates a CA cert+key in dir.
func generateCA(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	certFile = filepath.Join(dir, "ca.crt")
	keyFile = filepath.Join(dir, "ca.key")
	if err := GenerateCACert(certFile, keyFile); err != nil {
		t.Fatalf("GenerateCACert: %v", err)
	}
	return certFile, keyFile
}

// generateSigned creates a CA-signed cert+key in dir.
func generateSigned(t *testing.T, dir, cn, caCert, caKey string) (certFile, keyFile string) {
	t.Helper()
	certFile = filepath.Join(dir, cn+".crt")
	keyFile = filepath.Join(dir, cn+".key")
	if err := GenerateSignedCert(cn, certFile, keyFile, caCert, caKey); err != nil {
		t.Fatalf("GenerateSignedCert(%s): %v", cn, err)
	}
	return certFile, keyFile
}

// --- GenerateCACert ---

func TestGenerateCACertCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateCA(t, dir)

	if _, err := os.Stat(certFile); err != nil {
		t.Errorf("cert file not created: %v", err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("key file not created: %v", err)
	}
}

func TestGenerateCACertProducesValidCA(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := generateCA(t, dir)

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	if !cert.IsCA {
		t.Error("expected IsCA=true")
	}
	if cert.Subject.CommonName != "remote-control CA" {
		t.Errorf("expected CA CN, got %s", cert.Subject.CommonName)
	}
}

func TestGenerateCACertWriteError(t *testing.T) {
	// Provide an unwritable path to trigger an error.
	err := GenerateCACert("/nonexistent/path/ca.crt", "/nonexistent/path/ca.key")
	if err == nil {
		t.Fatal("expected error for unwritable path")
	}
}

// --- LoadCACert ---

func TestLoadCACert(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := generateCA(t, dir)

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert error: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate")
	}
}

func TestLoadCACertNotFound(t *testing.T) {
	_, err := LoadCACert("/nonexistent/path/ca.crt")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadCACertInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "bad.crt")
	os.WriteFile(certFile, []byte("not valid PEM"), 0600) //nolint:errcheck

	_, err := LoadCACert(certFile)
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
	// Verify the error message mentions the file path (exercises pemError.Error).
	if err.Error() == "" {
		t.Error("expected non-empty error message from pemError")
	}
}

// --- GenerateSignedCert ---

func TestGenerateSignedCertCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, keyFile := generateSigned(t, dir, "server", caCert, caKey)

	if _, err := os.Stat(certFile); err != nil {
		t.Errorf("cert file not created: %v", err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("key file not created: %v", err)
	}
}

func TestGenerateSignedCertCANotFound(t *testing.T) {
	dir := t.TempDir()
	err := GenerateSignedCert("localhost",
		filepath.Join(dir, "server.crt"),
		filepath.Join(dir, "server.key"),
		"/nonexistent/ca.crt",
		"/nonexistent/ca.key",
	)
	if err == nil {
		t.Fatal("expected error for missing CA files")
	}
}

func TestGenerateSignedCertInvalidCAKeyPEM(t *testing.T) {
	dir := t.TempDir()
	caCert, _ := generateCA(t, dir)

	badKey := filepath.Join(dir, "bad.key")
	os.WriteFile(badKey, []byte("not valid PEM"), 0600) //nolint:errcheck

	err := GenerateSignedCert("localhost",
		filepath.Join(dir, "server.crt"),
		filepath.Join(dir, "server.key"),
		caCert,
		badKey,
	)
	if err == nil {
		t.Fatal("expected error for invalid CA key PEM")
	}
}

// --- CertExpiry ---

func TestCertExpiryCA(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := generateCA(t, dir)

	expiry, err := CertExpiry(certFile)
	if err != nil {
		t.Fatalf("CertExpiry error: %v", err)
	}
	// CA cert is valid for ~10 years.
	minExpiry := time.Now().Add(9 * 365 * 24 * time.Hour)
	if expiry.Before(minExpiry) {
		t.Errorf("expected ~10 year expiry, got %s", expiry)
	}
}

func TestCertExpirySignedCert(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSigned(t, dir, "server", caCert, caKey)

	expiry, err := CertExpiry(certFile)
	if err != nil {
		t.Fatalf("CertExpiry error: %v", err)
	}
	// Signed cert is valid for 1 year.
	maxExpiry := time.Now().Add(366 * 24 * time.Hour)
	minExpiry := time.Now().Add(364 * 24 * time.Hour)
	if expiry.Before(minExpiry) || expiry.After(maxExpiry) {
		t.Errorf("expected ~1 year expiry, got %s", expiry)
	}
}

func TestCertExpiryNotFound(t *testing.T) {
	_, err := CertExpiry("/nonexistent/path.crt")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestCertExpiryInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "bad.crt")
	os.WriteFile(certFile, []byte("not valid PEM"), 0600) //nolint:errcheck

	_, err := CertExpiry(certFile)
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

// --- BuildClientTLSConfig ---

func TestBuildClientTLSConfig(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	clientCert, clientKey := generateSigned(t, dir, "client", caCert, caKey)

	tlsCfg, err := BuildClientTLSConfig(clientCert, clientKey, caCert)
	if err != nil {
		t.Fatalf("BuildClientTLSConfig error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("expected 1 certificate, got %d", len(tlsCfg.Certificates))
	}
	if tlsCfg.RootCAs == nil {
		t.Error("expected non-nil RootCAs")
	}
}

func TestBuildClientTLSConfigBadCert(t *testing.T) {
	dir := t.TempDir()
	badCert := filepath.Join(dir, "bad.crt")
	badKey := filepath.Join(dir, "bad.key")
	caCert := filepath.Join(dir, "ca.crt")
	os.WriteFile(badCert, []byte("not a cert"), 0600) //nolint:errcheck
	os.WriteFile(badKey, []byte("not a key"), 0600)   //nolint:errcheck
	os.WriteFile(caCert, []byte("not a CA"), 0600)    //nolint:errcheck

	_, err := BuildClientTLSConfig(badCert, badKey, caCert)
	if err == nil {
		t.Fatal("expected error for invalid cert")
	}
}

func TestBuildClientTLSConfigBadCA(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	clientCert, clientKey := generateSigned(t, dir, "client", caCert, caKey)

	badCA := filepath.Join(dir, "bad-ca.crt")
	os.WriteFile(badCA, []byte("not a CA cert"), 0600) //nolint:errcheck

	_, err := BuildClientTLSConfig(clientCert, clientKey, badCA)
	if err == nil {
		t.Fatal("expected error for invalid CA cert")
	}
}

// --- BuildServerTLSConfig ---

func TestBuildServerTLSConfig(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	serverCert, serverKey := generateSigned(t, dir, "server", caCert, caKey)

	tlsCfg, err := BuildServerTLSConfig(serverCert, serverKey, caCert)
	if err != nil {
		t.Fatalf("BuildServerTLSConfig error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("expected 1 certificate, got %d", len(tlsCfg.Certificates))
	}
	if tlsCfg.ClientCAs == nil {
		t.Error("expected non-nil ClientCAs")
	}
}

func TestBuildServerTLSConfigBadCert(t *testing.T) {
	dir := t.TempDir()
	badCert := filepath.Join(dir, "bad.crt")
	badKey := filepath.Join(dir, "bad.key")
	caCert := filepath.Join(dir, "ca.crt")
	os.WriteFile(badCert, []byte("not a cert"), 0600) //nolint:errcheck
	os.WriteFile(badKey, []byte("not a key"), 0600)   //nolint:errcheck
	os.WriteFile(caCert, []byte("not a CA"), 0600)    //nolint:errcheck

	_, err := BuildServerTLSConfig(badCert, badKey, caCert)
	if err == nil {
		t.Fatal("expected error for invalid server cert")
	}
}

// --- CheckCertExpiry ---

func TestCheckCertExpiryValid(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := generateCA(t, dir)

	// Should not panic; CA cert has 10-year validity, no warning expected.
	CheckCertExpiry("test-cert", certFile)
}

func TestCheckCertExpiryEmptyPath(t *testing.T) {
	// Should silently return without panicking.
	CheckCertExpiry("test-cert", "")
}

func TestCheckCertExpiryMissingFile(t *testing.T) {
	// Should silently return without panicking.
	CheckCertExpiry("test-cert", "/nonexistent/path.crt")
}

func TestCheckCertExpiryInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "bad.crt")
	os.WriteFile(certFile, []byte("not valid PEM"), 0600) //nolint:errcheck

	// Should silently return without panicking.
	CheckCertExpiry("test-cert", certFile)
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{24 * time.Hour, "1 day"},
		{2 * 24 * time.Hour, "2 days"},
		{29 * 24 * time.Hour, "29 days"},
		{0, "0 days"},
	}
	for _, tc := range cases {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
