package client

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
)

const ansiRed = "\033[31m"
const ansiReset = "\033[0m"

// renderChunk writes an output chunk to the appropriate local stream.
// In raw mode (interactive terminal), writes stderr directly without color wrapping
// to avoid corrupting TUI output. In cooked mode, adds red color to stderr.
func renderChunk(chunk OutputChunk) {
	data, err := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[remote-control] decode error: %v\n", err)
		return
	}

	isRawMode := term.IsTerminal(int(os.Stdin.Fd()))

	switch chunk.Stream {
	case "stdout":
		os.Stdout.Write(data) //nolint:errcheck
	case "stderr":
		if isRawMode {
			// In raw mode, write stderr directly without color codes
			// to avoid corrupting TUI display
			os.Stderr.Write(data) //nolint:errcheck
		} else {
			// In cooked mode, add red color for visibility
			fmt.Fprintf(os.Stderr, "%s%s%s", ansiRed, data, ansiReset)
		}
	}
}

// parseTimestamp parses an RFC3339Nano timestamp string.
// Returns zero time on parse error.
func parseTimestamp(s string) time.Time {
	t, err := timestampToTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}
