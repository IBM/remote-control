package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/tlsconfig"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize remote-control configuration",
	Long:  `Interactive wizard to set up remote-control configuration and generate TLS certificates.`,
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	// Determine config home.
	defaultHome := "~/.remote-control"
	if h := os.Getenv("REMOTE_CONTROL_HOME"); h != "" {
		defaultHome = h
	}
	fmt.Fprintf(os.Stderr, "Config home [%s]: ", defaultHome)
	home, _ := reader.ReadString('\n')
	home = strings.TrimSpace(home)
	if home == "" {
		home = defaultHome
	}

	// Expand tilde.
	if home == "~" {
		h, _ := os.UserHomeDir()
		home = h
	} else if strings.HasPrefix(home, "~/") {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, home[2:])
	}

	// Determine server URL.
	defaultURL := "https://localhost:8443"
	fmt.Fprintf(os.Stderr, "Server URL [%s]: ", defaultURL)
	serverURL, _ := reader.ReadString('\n')
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = defaultURL
	}

	// Confirm cert generation.
	fmt.Fprintf(os.Stderr, "Generate certificates? [Y/n]: ")
	genCerts, _ := reader.ReadString('\n')
	genCerts = strings.TrimSpace(strings.ToLower(genCerts))

	cfg := &config.Config{
		ConfigDir:         home,
		ServerURL:         serverURL,
		RequireApproval:   true,
		DefaultPermission: "read-write",
		PollIntervalMs:    500,
	}

	if genCerts == "" || genCerts == "y" || genCerts == "yes" {
		if err := os.MkdirAll(home, 0700); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}

		serverCAcert := filepath.Join(home, "ca-server.crt")
		serverCAkey := filepath.Join(home, "ca-server.key")
		fmt.Fprintf(os.Stderr, "  Generating CA...             ")
		if err := tlsconfig.GenerateCACert(serverCAcert, serverCAkey); err != nil {
			return fmt.Errorf("generate CA: %w", err)
		}
		fmt.Fprintln(os.Stderr, "done")

		serverCert := filepath.Join(home, "server.crt")
		serverKey := filepath.Join(home, "server.key")
		fmt.Fprintf(os.Stderr, "  Generating server cert...    ")
		if err := tlsconfig.GenerateSignedCert("server", serverCert, serverKey, serverCAcert, serverCAkey); err != nil {
			return fmt.Errorf("generate server cert: %w", err)
		}
		fmt.Fprintln(os.Stderr, "done")

		clientCAcert := filepath.Join(home, "ca-client.crt")
		clientCAkey := filepath.Join(home, "ca-client.key")
		if err := tlsconfig.GenerateCACert(clientCAcert, clientCAkey); err != nil {
			return fmt.Errorf("generate client CA: %w", err)
		}

		hostCert := filepath.Join(home, "host.crt")
		hostKey := filepath.Join(home, "host.key")
		fmt.Fprintf(os.Stderr, "  Generating host cert...      ")
		if err := tlsconfig.GenerateSignedCert("host", hostCert, hostKey, clientCAcert, clientCAkey); err != nil {
			return fmt.Errorf("generate host cert: %w", err)
		}
		fmt.Fprintln(os.Stderr, "done")

		cfg.ServerTLS = config.TLSBundle{
			CertFile:      serverCert,
			KeyFile:       serverKey,
			TrustedCAFile: clientCAcert,
		}
		cfg.ClientTLS = config.TLSBundle{
			CertFile:      hostCert,
			KeyFile:       hostKey,
			TrustedCAFile: serverCAcert,
		}
	}

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Config written to %s\n", filepath.Join(home, "config.json"))
	return nil
}
