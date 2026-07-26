package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const jmapdHelp = `jmapd - bridge JMAP calendar VALARMs, inbound mail, and shared contacts to an opencode agent

USAGE
  jmapd
  jmapd --help
  jmapd help

ENVIRONMENT
  JMAP_BASE_URL or JMAP_URL          JMAP server base URL
  JMAP_USER                         JMAP account user identifier
  JMAP_PASS or JMAP_PASSWORD        JMAP account password
  OPENCODE_BASE_URL or OPENCODE_URL opencode API base URL
  OPENCODE_PORT                     opencode API port when base URL is unset
  OPENCODE_HOST                     host used with OPENCODE_PORT (default: 127.0.0.1)
  OPENCODE_DIRECTORY or OPENCODE_DIR opencode working directory
  OPENCODE_AGENT or AGENT_ID        opencode agent ID
  JMAPD_STATE_DIR or STATE_DIR      directory for events.json, sessions.json, status.json
  JMAP_POLL_INTERVAL                polling fallback interval (default: 60s)
  JMAP_ALARM_WINDOW                 VALARM expansion window (default: 168h)
  OPENCODE_PROMPT_TIMEOUT           prompt timeout (default: no timeout)

OUTPUT
  stdout: explicit help writes only this help text.
  stderr: runtime logs, fatal errors, and configuration errors.
  state: events.json, sessions.json, and status.json persist under JMAPD_STATE_DIR.

EXAMPLES
  jmapd --help
  JMAP_BASE_URL=https://jmap.example.com JMAP_USER=user@example.com JMAP_PASS=replace-me \
    OPENCODE_BASE_URL=http://127.0.0.1:4096 OPENCODE_DIRECTORY=/data/opencode/work \
    OPENCODE_AGENT=agent_123 JMAPD_STATE_DIR=/data/opencode/state/jmapd jmapd
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
