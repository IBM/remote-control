package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
)

var certCmd = &cobra.Command{
	Use:   "cert",
	Short: "Manage TLS certificates",
	Long:  `Commands for generating and managing TLS certificates for remote-control.`,
}

var certInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate initial TLS certificate bundle",
	Long:  `Generates a CA, server certificate, and host (client) certificate for mutual TLS.`,
	RunE:  runCertInit,
}

var certIssueCmd = &cobra.Command{
	Use:   "issue <name>",
	Short: "Issue a new client certificate signed by the client CA",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertIssue,
}

var certListCmd = &cobra.Command{
	Use:   "list",
	Short: "List certificates with expiry dates",
	RunE:  runCertList,
}

var certVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify all configured certificates exist and are valid",
	RunE:  runCertVerify,
}

func init() {
	rootCmd.AddCommand(certCmd)
	certCmd.AddCommand(certInitCmd)
	certCmd.AddCommand(certIssueCmd)
	certCmd.AddCommand(certListCmd)
	certCmd.AddCommand(certVerifyCmd)
}

func runCertInit(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return err
	}
	dir := cfg.ConfigDir
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// Generate server CA.
	serverCAcert := filepath.Join(dir, "ca-server.crt")
	serverCAkey := filepath.Join(dir, "ca-server.key")
	fmt.Fprintf(os.Stderr, "Generating server CA...        ")
	if err := tlsconfig.GenerateCACert(serverCAcert, serverCAkey); err != nil {
		return fmt.Errorf("generate server CA: %w", err)
	}
	fmt.Fprintln(os.Stderr, "done")

	// Generate server cert signed by server CA.
	serverCert := filepath.Join(dir, "server.crt")
	serverKey := filepath.Join(dir, "server.key")
	fmt.Fprintf(os.Stderr, "Generating server cert...      ")
	if err := tlsconfig.GenerateSignedCert("server", serverCert, serverKey, serverCAcert, serverCAkey); err != nil {
		return fmt.Errorf("generate server cert: %w", err)
	}
	fmt.Fprintln(os.Stderr, "done")

	// Generate client CA (can reuse server CA for simplicity; using separate one for flexibility).
	clientCAcert := filepath.Join(dir, "ca-client.crt")
	clientCAkey := filepath.Join(dir, "ca-client.key")
	fmt.Fprintf(os.Stderr, "Generating client CA...        ")
	if err := tlsconfig.GenerateCACert(clientCAcert, clientCAkey); err != nil {
		return fmt.Errorf("generate client CA: %w", err)
	}
	fmt.Fprintln(os.Stderr, "done")

	// Generate host (client) cert signed by client CA.
	hostCert := filepath.Join(dir, "host.crt")
	hostKey := filepath.Join(dir, "host.key")
	fmt.Fprintf(os.Stderr, "Generating host cert...        ")
	if err := tlsconfig.GenerateSignedCert("host", hostCert, hostKey, clientCAcert, clientCAkey); err != nil {
		return fmt.Errorf("generate host cert: %w", err)
	}
	fmt.Fprintln(os.Stderr, "done")

	// Update config with cert paths.
	cfg.ServerTLS = config.TLSBundle{
		CertFile:      serverCert,
		KeyFile:       serverKey,
		TrustedCAFile: clientCAcert, // server trusts client CA
	}
	cfg.ClientTLS = config.TLSBundle{
		CertFile:      hostCert,
		KeyFile:       hostKey,
		TrustedCAFile: serverCAcert, // clients trust server CA
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Config written to %s\n", filepath.Join(dir, "config.json"))
	return nil
}

func runCertIssue(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return err
	}
	dir := cfg.ConfigDir

	clientCAcert := filepath.Join(dir, "ca-client.crt")
	clientCAkey := filepath.Join(dir, "ca-client.key")

	certOut := filepath.Join(dir, name+".crt")
	keyOut := filepath.Join(dir, name+".key")

	fmt.Fprintf(os.Stderr, "Issuing cert for %q...\n", name)
	if err := tlsconfig.GenerateSignedCert(name, certOut, keyOut, clientCAcert, clientCAkey); err != nil {
		return fmt.Errorf("issue cert: %w", err)
	}
	fmt.Printf("Certificate: %s\nKey:         %s\n", certOut, keyOut)
	return nil
}

func runCertList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return err
	}
	dir := cfg.ConfigDir

	certFiles := []string{
		"ca-server.crt",
		"server.crt",
		"ca-client.crt",
		"host.crt",
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tEXPIRY\tSTATUS")
	for _, f := range certFiles {
		path := filepath.Join(dir, f)
		expiry, err := tlsconfig.CertExpiry(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			fmt.Fprintf(tw, "%s\t-\tERROR: %v\n", f, err)
			continue
		}
		status := "OK"
		if time.Until(expiry) < 30*24*time.Hour {
			status = "EXPIRING SOON"
		}
		if time.Now().After(expiry) {
			status = "EXPIRED"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", f, expiry.Format("2006-01-02"), status)
	}
	return tw.Flush()
}

func runCertVerify(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return err
	}

	files := []struct {
		label string
		path  string
	}{
		{"server cert", cfg.ServerTLS.CertFile},
		{"server key", cfg.ServerTLS.KeyFile},
		{"server CA", cfg.ServerTLS.TrustedCAFile},
		{"client cert", cfg.ClientTLS.CertFile},
		{"client key", cfg.ClientTLS.KeyFile},
		{"client CA", cfg.ClientTLS.TrustedCAFile},
	}

	ok := true
	for _, f := range files {
		if f.path == "" {
			fmt.Printf("%-20s not configured\n", f.label)
			continue
		}
		if _, err := os.Stat(f.path); err != nil {
			fmt.Printf("%-20s MISSING: %s\n", f.label, f.path)
			ok = false
		} else {
			expiry, err := tlsconfig.CertExpiry(f.path)
			if err != nil {
				// Not a cert file (e.g. key files).
				fmt.Printf("%-20s OK: %s\n", f.label, f.path)
				continue
			}
			if time.Now().After(expiry) {
				fmt.Printf("%-20s EXPIRED (%s): %s\n", f.label, expiry.Format("2006-01-02"), f.path)
				ok = false
			} else {
				fmt.Printf("%-20s OK (expires %s): %s\n", f.label, expiry.Format("2006-01-02"), f.path)
			}
		}
	}
	if !ok {
		return fmt.Errorf("certificate verification failed")
	}
	return nil
}
