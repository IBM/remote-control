package testmain

import (
	"os"
	"testing"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

var TestCh = alog.UseChannel("TEST")

func TestMain(m *testing.M) {
	// Configure logging
	log_level := "info"
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		log_level = v
	}
	log_filters := ""
	if v := os.Getenv("LOG_FILTERS"); v != "" {
		log_filters = v
	}
	level, _ := alog.LevelFromString(log_level)
	chanMap, _ := alog.ParseChannelFilter(log_filters)
	alog.Config(level, chanMap)
	alog.EnableGID()

	// Run the test and exit
	os.Exit(m.Run())
}
