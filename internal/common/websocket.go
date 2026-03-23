package types

import "net/url"

// deriveWebSocketURL converts http(s):// URLs to ws(s):// URLs
// It strips any existing path and query parameters since the WebSocket path is constructed separately
func DeriveWebSocketURL(httpURL string) string {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return httpURL
	}

	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		// Return as-is if already ws/wss or unknown
		return httpURL
	}

	// Reset path and query - the caller will add /ws/{sessionID}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String()
}
