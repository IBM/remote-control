package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gabe-l-hart/remote-control/internal/client"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
)

var (
	connectSession string
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect to a remote session",
	Long:  `Connect to an existing remote-control session to observe output and submit stdin.`,
	RunE:  runConnect,
}

func init() {
	rootCmd.AddCommand(connectCmd)
	connectCmd.Flags().StringVar(&connectSession, "session", "", "Session ID to connect to")
}

func runConnect(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	c := client.NewClient(cfg)
	return c.Run(cmd.Context(), connectSession)
}
