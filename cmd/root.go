package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/host"
)

var (
	flagServer     string
	flagClientCert string
	flagClientKey  string
	flagClientCA   string
	flagCExpr      string
)

// knownSubcommands are named subcommands that take priority over wrap mode.
var knownSubcommands = map[string]bool{
	"server":  true,
	"connect": true,
	"cert":    true,
	"init":    true,
	"help":    true,
	"version": true,
	"completion": true,
}

// knownRCFlagValues are RC flags that consume the next argument as their value.
// Used when scanning os.Args to find the wrapped command boundary.
var knownRCFlagValues = map[string]bool{
	"--server":      true,
	"--client-cert": true,
	"--client-key":  true,
	"--client-ca":   true,
	"-c":            true,
	"--c":           true,
}


var rootCmd = &cobra.Command{
	Use:   "remote-control",
	Short: "Wrap a terminal command and control it remotely",
	Long: `remote-control lets you wrap a terminal command and control it from a remote location.
The server holds buffered session I/O; the host wrapper proxies subprocess I/O to the server;
remote clients poll for output and submit stdin.`,
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	Args:               cobra.ArbitraryArgs,
	RunE:               runWrapMode,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagServer, "server", "", "Remote control server URL")
	rootCmd.PersistentFlags().StringVar(&flagClientCert, "client-cert", "", "Client TLS certificate file")
	rootCmd.PersistentFlags().StringVar(&flagClientKey, "client-key", "", "Client TLS key file")
	rootCmd.PersistentFlags().StringVar(&flagClientCA, "client-ca", "", "CA cert file to trust for server certificate")
	rootCmd.Flags().StringVarP(&flagCExpr, "c", "c", "", "Shell expression to execute via sh -c")
}

// runWrapMode is invoked when no named subcommand is given. It scans os.Args
// directly to find the wrapped command boundary, since cobra drops unknown flags
// before they reach RunE's args slice.
func runWrapMode(cmd *cobra.Command, args []string) error {
	var wrappedCmd []string

	// -c flag takes priority: run sh -c <expr>
	if flagCExpr != "" {
		wrappedCmd = []string{"sh", "-c", flagCExpr}
	} else {
		// Scan os.Args[1:] directly to find the wrapped command boundary.
		// This preserves any flags that belong to the wrapped command.
		wrappedCmd = findWrappedCommandFromOSArgs(os.Args[1:])
	}

	if len(wrappedCmd) == 0 {
		return cmd.Help()
	}

	cfg, err := config.Load(cliOverrides())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	h := host.NewHost(cfg)
	return h.Run(cmd.Context(), wrappedCmd)
}

// findWrappedCommandFromOSArgs scans raw os.Args (excluding argv[0]) and returns
// everything from the first non-RC-flag, non-subcommand positional argument onward.
func findWrappedCommandFromOSArgs(rawArgs []string) []string {
	i := 0
	for i < len(rawArgs) {
		arg := rawArgs[i]

		if arg == "--" {
			// Explicit end-of-flags: everything after is the wrapped command.
			return rawArgs[i+1:]
		}

		if strings.HasPrefix(arg, "-") {
			// Known RC flag that consumes a value.
			if knownRCFlagValues[arg] {
				i += 2 // skip flag + value
			} else {
				// Handle --flag=value form
				if strings.Contains(arg, "=") {
					i++
				} else {
					i++ // boolean flag or unknown flag — skip just the flag
				}
			}
			continue
		}

		// Non-flag argument: check if it's a known subcommand.
		if knownSubcommands[arg] {
			// Let cobra handle it — no wrap mode.
			return nil
		}

		// First non-flag, non-subcommand positional argument: this is the start
		// of the wrapped command. Everything from here (including remaining args
		// which may be flags for the wrapped command) is the wrapped command.
		return rawArgs[i:]
	}
	return nil
}

// cliOverrides builds a map of CLI-specified overrides for config loading.
func cliOverrides() map[string]string {
	overrides := make(map[string]string)
	if flagServer != "" {
		overrides["server"] = flagServer
	}
	if flagClientCert != "" {
		overrides["client-cert"] = flagClientCert
	}
	if flagClientKey != "" {
		overrides["client-key"] = flagClientKey
	}
	if flagClientCA != "" {
		overrides["client-ca"] = flagClientCA
	}
	return overrides
}

