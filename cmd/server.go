package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/spf13/cobra"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
	"github.com/gabe-l-hart/remote-control/internal/server"
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
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Warn on expiring certs.
	tlsconfig.CheckCertExpiry("server cert", cfg.ServerTLS.CertFile)
	tlsconfig.CheckCertExpiry("server CA", cfg.ServerTLS.TrustedCAFile)
	tlsconfig.CheckCertExpiry("client CA", cfg.ClientTLS.TrustedCAFile)

	srv := server.NewServer(serverAddr, cfg)
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
