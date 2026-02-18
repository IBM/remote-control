package tlsconfig

import (
	"fmt"
	"log"
	"os"
	"time"
)

const certExpiryWarnThreshold = 30 * 24 * time.Hour

// CheckCertExpiry logs a warning if a certificate file will expire within 30 days.
// It silently returns if the file doesn't exist or isn't a certificate.
func CheckCertExpiry(label, certFile string) {
	if certFile == "" {
		return
	}
	if _, err := os.Stat(certFile); err != nil {
		return
	}
	expiry, err := CertExpiry(certFile)
	if err != nil {
		return
	}
	until := time.Until(expiry)
	if until <= 0 {
		log.Printf("[remote-control] WARNING: %s has EXPIRED (%s)", label, certFile)
	} else if until < certExpiryWarnThreshold {
		log.Printf("[remote-control] WARNING: %s expires in %s (%s)", label, formatDuration(until), certFile)
	}
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}
