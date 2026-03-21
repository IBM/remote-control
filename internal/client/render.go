package client

import (
	"encoding/base64"
	"fmt"
	"os"

	types "github.com/gabe-l-hart/remote-control/internal/common"
	"golang.org/x/term"
)

const ansiRed = "\033[31m"
const ansiReset = "\033[0m"

func renderChunk(chunk types.OutputChunk) {
	data, err := base64.StdEncoding.DecodeString(string(chunk.Data))
	if err != nil {
		data = chunk.Data
	}

	isRawMode := term.IsTerminal(int(os.Stdin.Fd()))

	switch chunk.Stream {
	case types.StreamStdout:
		os.Stdout.Write(data)
	case types.StreamStderr:
		if isRawMode {
			os.Stderr.Write(data)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s%s", ansiRed, data, ansiReset)
		}
	}
}
