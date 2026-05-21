package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const jmapdHelp = `jmapd - bridge JMAP calendar VALARMs to an opencode agent

Usage:
  jmapd
  jmapd --help

Required environment:
  JMAP_BASE_URL            JMAP server base URL
  JMAP_USER                JMAP account user identifier
  JMAP_PASS                JMAP account password
  OPENCODE_BASE_URL        opencode API base URL (or OPENCODE_PORT)
  OPENCODE_DIRECTORY       opencode working directory
  OPENCODE_AGENT           opencode agent ID (or AGENT_ID)
  JMAPD_STATE_DIR          directory for events.json, sessions.json, status.json

Optional environment:
  JMAP_POLL_INTERVAL       polling fallback interval as a Go duration (default 60s)
  JMAP_ALARM_WINDOW        VALARM expansion window as a Go duration (default 168h)
  OPENCODE_PROMPT_TIMEOUT  prompt timeout as a Go duration
`

func main() {
	if wantsHelp(os.Args[1:]) {
		fmt.Fprint(os.Stdout, jmapdHelp)
		return
	}
	logger := log.New(os.Stderr, "jmapd: ", log.LstdFlags|log.LUTC)
	cfg, err := ConfigFromEnv()
	if err != nil {
		logger.Printf("configuration error: %v", err)
		os.Exit(1)
	}
	bridge, err := NewBridge(cfg, logger)
	if err != nil {
		logger.Printf("initialization error: %v", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.Run(ctx); err != nil {
		logger.Printf("fatal error: %v", err)
		os.Exit(1)
	}
}

func wantsHelp(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--help", "-h", "help":
		return true
	default:
		return false
	}
}
