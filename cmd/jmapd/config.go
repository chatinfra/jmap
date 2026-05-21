package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

type Config struct {
	JMAPBaseURL       string
	JMAPUser          string
	JMAPPassword      string
	OpencodeBaseURL   string
	OpencodeDirectory string
	AgentID           string
	StateDir          string
	PromptTimeout     time.Duration
	PollInterval      time.Duration
	AlarmWindow       time.Duration
}

func ConfigFromEnv() (Config, error) {
	baseURL := firstEnv("OPENCODE_BASE_URL", "OPENCODE_URL")
	if baseURL == "" {
		port := strings.TrimSpace(os.Getenv("OPENCODE_PORT"))
		if port != "" {
			host := firstEnv("OPENCODE_HOST")
			if host == "" {
				host = "127.0.0.1"
			}
			baseURL = "http://" + host + ":" + port
		}
	}
	cfg := Config{
		JMAPBaseURL:       firstEnv("JMAP_BASE_URL", "JMAP_URL"),
		JMAPUser:          firstEnv("JMAP_USER"),
		JMAPPassword:      firstEnv("JMAP_PASS", "JMAP_PASSWORD"),
		OpencodeBaseURL:   baseURL,
		OpencodeDirectory: firstEnv("OPENCODE_DIRECTORY", "OPENCODE_DIR"),
		AgentID:           firstEnv("OPENCODE_AGENT", "AGENT_ID"),
		StateDir:          firstEnv("JMAPD_STATE_DIR", "STATE_DIR"),
	}
	var err error
	if cfg.PromptTimeout, err = envDuration("OPENCODE_PROMPT_TIMEOUT", 0); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = envDuration("JMAP_POLL_INTERVAL", jmap.DefaultPollingInterval); err != nil {
		return Config{}, err
	}
	if cfg.AlarmWindow, err = envDuration("JMAP_ALARM_WINDOW", jmap.DefaultAlarmWindow); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.JMAPBaseURL) == "" {
		missing = append(missing, "JMAP_BASE_URL")
	} else if parsed, err := url.Parse(c.JMAPBaseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid JMAP base URL %q", c.JMAPBaseURL)
	}
	if strings.TrimSpace(c.JMAPUser) == "" {
		missing = append(missing, "JMAP_USER")
	}
	if strings.TrimSpace(c.JMAPPassword) == "" {
		missing = append(missing, "JMAP_PASS")
	}
	if strings.TrimSpace(c.OpencodeBaseURL) == "" {
		missing = append(missing, "OPENCODE_BASE_URL or OPENCODE_PORT")
	} else if parsed, err := url.Parse(c.OpencodeBaseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid opencode base URL %q", c.OpencodeBaseURL)
	}
	if strings.TrimSpace(c.OpencodeDirectory) == "" {
		missing = append(missing, "OPENCODE_DIRECTORY")
	}
	if strings.TrimSpace(c.AgentID) == "" {
		missing = append(missing, "OPENCODE_AGENT or AGENT_ID")
	}
	if strings.TrimSpace(c.StateDir) == "" {
		missing = append(missing, "JMAPD_STATE_DIR")
	}
	if c.PollInterval <= 0 {
		return errors.New("JMAP_POLL_INTERVAL must be positive")
	}
	if c.AlarmWindow <= 0 {
		return errors.New("JMAP_ALARM_WINDOW must be positive")
	}
	if len(missing) > 0 {
		return errors.New("missing required environment: " + strings.Join(missing, ", "))
	}
	return nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
