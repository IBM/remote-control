package tlsconfig

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IBM/remote-control/internal/common/types"
	testmain "github.com/IBM/remote-control/test"
)

func TestMain(m *testing.M) {
	testmain.TestMain(m)
}

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
	return generateSignedWithSANs(t, dir, cn, caCert, caKey, nil, nil)
}

// generateSignedWithSANs creates a CA-signed cert+key in dir with custom SANs.
func generateSignedWithSANs(t *testing.T, dir, cn, caCert, caKey string, dnsNames []string, ipAddresses []string) (certFile, keyFile string) {
	t.Helper()
	certFile = filepath.Join(dir, cn+".crt")
	keyFile = filepath.Join(dir, cn+".key")
	if err := GenerateSignedCert(cn, certFile, keyFile, caCert, caKey, dnsNames, ipAddresses); err != nil {
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
		nil, nil,
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
		nil, nil,
	)
	if err == nil {
		t.Fatal("expected error for invalid CA key PEM")
	}
}

// --- GenerateSignedCert SANs ---

func TestGenerateSignedCertDefaultSANs(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSigned(t, dir, "server", caCert, caKey)

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	if len(cert.DNSNames) != 2 {
		t.Errorf("expected 2 DNS SANs, got %d: %v", len(cert.DNSNames), cert.DNSNames)
	}
	if !containsStr(cert.DNSNames, "localhost") {
		t.Errorf("expected 'localhost' in DNS SANs: %v", cert.DNSNames)
	}
	if !containsStr(cert.DNSNames, "server") {
		t.Errorf("expected 'server' (CN) in DNS SANs: %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 2 {
		t.Errorf("expected 2 IP SANs, got %d: %v", len(cert.IPAddresses), cert.IPAddresses)
	}
	hasIPv4 := false
	hasIPv6 := false
	for _, ip := range cert.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			hasIPv4 = true
		}
		if ip.Equal(net.ParseIP("::1")) {
			hasIPv6 = true
		}
	}
	if !hasIPv4 {
		t.Error("expected 127.0.0.1 in IP SANs")
	}
	if !hasIPv6 {
		t.Error("expected ::1 in IP SANs")
	}
}

func TestGenerateSignedCertCustomDNSNames(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSignedWithSANs(t, dir, "api", caCert, caKey,
		[]string{"api.example.com", "api.internal.local"}, nil)

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	// Should have custom DNS names + CN + "localhost"
	expectedDNS := map[string]bool{"api.example.com": false, "api.internal.local": false, "api": false, "localhost": false}
	for _, dns := range cert.DNSNames {
		if _, ok := expectedDNS[dns]; ok {
			expectedDNS[dns] = true
		}
	}
	for dns, found := range expectedDNS {
		if !found {
			t.Errorf("expected DNS name %q in SANs: %v", dns, cert.DNSNames)
		}
	}
}

func TestGenerateSignedCertCustomIPs(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSignedWithSANs(t, dir, "server", caCert, caKey,
		nil, []string{"10.0.0.1", "192.168.1.100"})

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	if len(cert.IPAddresses) != 2 {
		t.Errorf("expected 2 IP SANs, got %d: %v", len(cert.IPAddresses), cert.IPAddresses)
	}
	ips := make(map[string]bool)
	for _, ip := range cert.IPAddresses {
		ips[ip.String()] = true
	}
	if !ips["10.0.0.1"] {
		t.Errorf("expected 10.0.0.1 in IP SANs: %v", cert.IPAddresses)
	}
	if !ips["192.168.1.100"] {
		t.Errorf("expected 192.168.1.100 in IP SANs: %v", cert.IPAddresses)
	}
}

func TestGenerateSignedCertMixedCustomSANs(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSignedWithSANs(t, dir, "myhost", caCert, caKey,
		[]string{"myhost.example.com"}, []string{"10.0.0.5"})

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	// Custom DNS + CN + localhost
	if len(cert.DNSNames) != 3 {
		t.Errorf("expected 3 DNS SANs, got %d: %v", len(cert.DNSNames), cert.DNSNames)
	}
	// Single custom IP
	if len(cert.IPAddresses) != 1 {
		t.Errorf("expected 1 IP SAN, got %d: %v", len(cert.IPAddresses), cert.IPAddresses)
	}
	if !containsStr(cert.DNSNames, "myhost.example.com") {
		t.Errorf("expected 'myhost.example.com' in DNS SANs: %v", cert.DNSNames)
	}
	if !containsStr(cert.DNSNames, "myhost") {
		t.Errorf("expected 'myhost' (CN) in DNS SANs: %v", cert.DNSNames)
	}
	if !containsStr(cert.DNSNames, "localhost") {
		t.Errorf("expected 'localhost' in DNS SANs: %v", cert.DNSNames)
	}
}

func TestGenerateSignedCertDedupeDNSNames(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSignedWithSANs(t, dir, "localhost", caCert, caKey,
		[]string{"localhost", "example.com"}, nil)

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	// CN is "localhost" (duplicate of provided name), should only appear once
	localhostCount := 0
	for _, dns := range cert.DNSNames {
		if dns == "localhost" {
			localhostCount++
		}
	}
	if localhostCount != 1 {
		t.Errorf("expected 'localhost' to appear once, got %d: %v", localhostCount, cert.DNSNames)
	}
}

func TestGenerateSignedCertInvalidIPIgnored(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	certFile, _ := generateSignedWithSANs(t, dir, "server", caCert, caKey,
		nil, []string{"not-an-ip", "10.0.0.1"})

	cert, err := LoadCACert(certFile)
	if err != nil {
		t.Fatalf("LoadCACert: %v", err)
	}
	if len(cert.IPAddresses) != 1 {
		t.Errorf("expected 1 IP SAN (invalid IP ignored), got %d: %v", len(cert.IPAddresses), cert.IPAddresses)
	}
	if !cert.IPAddresses[0].Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("expected only 10.0.0.1, got: %v", cert.IPAddresses)
	}
}

func containsStr(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
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

	tlsCfg, err := BuildClientTLSConfig(clientCert, clientKey, caCert, false, types.AuthModeMTLS)
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
	if tlsCfg.VerifyConnection != nil {
		t.Error("expected VerifyConnection to be nil when skipHostnameVerification=false")
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be false when skipHostnameVerification=false")
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

	_, err := BuildClientTLSConfig(badCert, badKey, caCert, false, types.AuthModeMTLS)
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

	// Bad CA should fall back to system CAs (no error)
	tlsCfg, err := BuildClientTLSConfig(clientCert, clientKey, badCA, false, types.AuthModeMTLS)
	if err != nil {
		t.Fatalf("BuildClientTLSConfig error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	// Client cert should still be loaded
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("expected 1 certificate, got %d", len(tlsCfg.Certificates))
	}
}

// --- BuildClientTLSConfig with partial credentials ---

func TestBuildClientTLSConfigNoServerCA(t *testing.T) {
	_ = t.TempDir()
	// No server CA at all — should succeed (uses system CAs)
	tlsCfg, err := BuildClientTLSConfig("", "", "", false, types.AuthModeMTLS)
	if err != nil {
		t.Fatalf("BuildClientTLSConfig error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if tlsCfg.VerifyConnection != nil {
		t.Error("expected VerifyConnection to be nil when skipHostnameVerification=false")
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be false when skipHostnameVerification=false")
	}
}

func TestBuildClientTLSConfigSkipHostnameVerification(t *testing.T) {
	_ = t.TempDir()
	tlsCfg, err := BuildClientTLSConfig("", "", "", true, types.AuthModeMTLS)
	if err != nil {
		t.Fatalf("BuildClientTLSConfig error: %v", err)
	}
	if tlsCfg.VerifyConnection == nil {
		t.Error("expected VerifyConnection to be set when skipHostnameVerification=true")
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be true when skipHostnameVerification=false")
	}
}

func TestBuildClientTLSConfigPartialClientCerts(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	clientCert, _ := generateSigned(t, dir, "client", caCert, caKey)

	// Only cert, no key — should succeed but not load the cert
	tlsCfg, err := BuildClientTLSConfig(clientCert, "", caCert, false, types.AuthModeMTLS)
	if err != nil {
		t.Fatalf("BuildClientTLSConfig error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if len(tlsCfg.Certificates) != 0 {
		t.Errorf("expected 0 client certificates (key missing), got %d", len(tlsCfg.Certificates))
	}
}

func TestBuildClientTLSConfigPartialClientCertsKeyOnly(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	_, clientKey := generateSigned(t, dir, "client", caCert, caKey)

	// Only key, no cert — should succeed but not load the cert
	tlsCfg, err := BuildClientTLSConfig("", clientKey, caCert, false, types.AuthModeMTLS)
	if err != nil {
		t.Fatalf("BuildClientTLSConfig error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if len(tlsCfg.Certificates) != 0 {
		t.Errorf("expected 0 client certificates (cert missing), got %d", len(tlsCfg.Certificates))
	}
}

// --- BuildServerTLSConfig ---

func TestBuildServerTLSConfig(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := generateCA(t, dir)
	serverCert, serverKey := generateSigned(t, dir, "server", caCert, caKey)

	tlsCfg, err := BuildServerTLSConfig(serverCert, serverKey, caCert, types.AuthModeMTLS)
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

	_, err := BuildServerTLSConfig(badCert, badKey, caCert, types.AuthModeMTLS)
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
