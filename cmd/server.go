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
	"github.com/gabe-l-hart/remote-control/internal/common/types"
	"github.com/gabe-l-hart/remote-control/internal/server"
)

var ch = alog.UseChannel("SERVER")

var (
	serverAddr              string
	authMode                string
	authProxyIdentityHeader string
	authProxyRequireTLS     bool
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
	serverCmd.Flags().StringVar(&authMode, "auth-mode", "", "Authentication mode: mtls, proxy, none")
	serverCmd.Flags().StringVar(&authProxyIdentityHeader, "auth-proxy-identity-header", "", "Identity header name for proxy auth")
	serverCmd.Flags().BoolVar(&authProxyRequireTLS, "auth-proxy-require-tls", false, "Require TLS for proxy auth")
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Configure auth mode
	if authMode != "" {
		if authMode != "mtls" && authMode != "proxy" && authMode != "none" {
			return fmt.Errorf("invalid auth mode: %s", authMode)
		}
		cfg.Auth.Mode = types.AuthMode(authMode)
	}
	if authProxyIdentityHeader != "" {
		cfg.Auth.Proxy.IdentityHeader = authProxyIdentityHeader
	}
	if authProxyRequireTLS {
		cfg.Auth.Proxy.RequireTLS = authProxyRequireTLS
	}

	// Log authentication mode
	ch.Log(alog.INFO, "[remote-control] Authentication mode: %s", cfg.Auth.Mode)

	// Warn on expiring certs (only in mTLS mode)
	if cfg.Auth.Mode == types.AuthModeMTLS {
		tlsconfig.CheckCertExpiry("server cert", cfg.ServerTLS.CertFile)
		tlsconfig.CheckCertExpiry("server CA", cfg.ServerTLS.TrustedCAFile)
		tlsconfig.CheckCertExpiry("client CA", cfg.ClientTLS.TrustedCAFile)
	}

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
			cfg.Auth.Mode,
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
