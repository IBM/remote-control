package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/spf13/cobra"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
	"github.com/gabe-l-hart/remote-control/internal/server"
	"github.com/gabe-l-hart/remote-control/internal/server/session"
)

var ch = alog.UseChannel("SERVER")

var (
	serverAddr         string
	serverListSessions bool
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the remote-control server",
	Long:  `Start the remote-control server that holds buffered session I/O.`,
	RunE:  runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.Flags().StringVar(&serverAddr, "addr", ":8443", "Address to listen on")
	serverCmd.Flags().BoolVar(&serverListSessions, "list-sessions", false, "Print active sessions table and exit")
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// --list-sessions: query a running server and exit.
	if serverListSessions {
		return listRemoteSessions(cfg)
	}

	store, err := session.NewStore("memory", session.StoreOptions{})
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	// Warn on expiring certs.
	tlsconfig.CheckCertExpiry("server cert", cfg.ServerTLS.CertFile)
	tlsconfig.CheckCertExpiry("server CA", cfg.ServerTLS.TrustedCAFile)
	tlsconfig.CheckCertExpiry("client CA", cfg.ClientTLS.TrustedCAFile)

	srv := server.NewServer(serverAddr, store, cfg)
	ch.Log(alog.INFO, "[remote-control] server listening on %s", serverAddr)

	// Graceful shutdown on SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		ch.Log(alog.INFO, "[remote-control] shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			ch.Log(alog.INFO, "[remote-control] shutdown error: %v", err)
		}
	}()

	// Use TLS if server cert/key are configured.
	if cfg.ServerTLS.CertFile != "" && cfg.ServerTLS.KeyFile != "" {
		tlsCfg, err := tlsconfig.BuildServerTLSConfig(
			cfg.ServerTLS.CertFile,
			cfg.ServerTLS.KeyFile,
			cfg.ServerTLS.TrustedCAFile,
		)
		if err != nil {
			return fmt.Errorf("build TLS config: %w", err)
		}
		if err := srv.ListenAndServeTLS(tlsCfg); err != nil && err != http.ErrServerClosed {
			return err
		}
	} else {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return nil
}

// listRemoteSessions queries the running server for its session list and prints it.
func listRemoteSessions(cfg *config.Config) error {
	resp, err := http.Get(cfg.ServerURL + "/sessions") //nolint:noctx
	if err != nil {
		return fmt.Errorf("query server: %w", err)
	}
	defer resp.Body.Close()

	var sessions []struct {
		ID      string   `json:"id"`
		Command []string `json:"command"`
		Status  string   `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCOMMAND\tSTATUS")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%s\t%v\t%s\n", s.ID, s.Command, s.Status)
	}
	return tw.Flush()
}
