package client

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"
)

const ansiRed = "\033[31m"
const ansiReset = "\033[0m"

// renderChunk writes an output chunk to the appropriate local stream.
// stdout chunks go to os.Stdout; stderr chunks are written in red to os.Stderr.
func renderChunk(chunk OutputChunk) {
	data, err := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[remote-control] decode error: %v\n", err)
		return
	}
	switch chunk.Stream {
	case "stdout":
		os.Stdout.Write(data) //nolint:errcheck
	case "stderr":
		fmt.Fprintf(os.Stderr, "%s%s%s", ansiRed, data, ansiReset)
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
