package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

type SessionsFile struct {
	Sessions map[string]string `json:"sessions"`
}

type EventsFile struct {
	Snapshot jmap.CalendarSnapshot `json:"snapshot"`
}

type StatusFile struct {
	JMAPConnected          bool       `json:"jmapConnected"`
	PushState              string     `json:"pushState"`
	LastSnapshotRefreshAt  *time.Time `json:"lastSnapshotRefreshAt,omitempty"`
	LastVALARMFiredAt      *time.Time `json:"lastValarmFiredAt,omitempty"`
	LastPromptCompletedAt  *time.Time `json:"lastPromptCompletedAt,omitempty"`
	LastError              string     `json:"lastError,omitempty"`
	CalendarSessionCount   int        `json:"calendarSessionCount"`
	DaemonStartedAt        time.Time  `json:"daemonStartedAt"`
	RegisteredListenerKeys []string   `json:"registeredListenerKeys,omitempty"`
}

type StateStore struct {
	dir string
}

func NewStateStore(dir string) *StateStore { return &StateStore{dir: dir} }

func (s *StateStore) LoadSessions() (map[string]string, error) {
	path := filepath.Join(s.dir, "sessions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var file SessionsFile
	if err := json.Unmarshal(data, &file); err == nil && file.Sessions != nil {
		return file.Sessions, nil
	}
	var legacy map[string]string
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}
	if legacy == nil {
		legacy = map[string]string{}
	}
	return legacy, nil
}

func (s *StateStore) SaveSessions(sessions map[string]string) error {
	copyMap := make(map[string]string, len(sessions))
	for key, value := range sessions {
		copyMap[key] = value
	}
	return s.writeJSONAtomic("sessions.json", SessionsFile{Sessions: copyMap}, 0o600)
}

func (s *StateStore) LoadEvents() (jmap.CalendarSnapshot, error) {
	path := filepath.Join(s.dir, "events.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return jmap.CalendarSnapshot{}, nil
		}
		return jmap.CalendarSnapshot{}, err
	}
	var file EventsFile
	if err := json.Unmarshal(data, &file); err == nil && !file.Snapshot.RefreshedAt.IsZero() {
		return file.Snapshot, nil
	}
	var legacy jmap.CalendarSnapshot
	if err := json.Unmarshal(data, &legacy); err != nil {
		return jmap.CalendarSnapshot{}, err
	}
	return legacy, nil
}

func (s *StateStore) SaveEvents(snapshot jmap.CalendarSnapshot) error {
	return s.writeJSONAtomic("events.json", EventsFile{Snapshot: snapshot}, 0o600)
}

func (s *StateStore) SaveStatus(status StatusFile) error {
	return s.writeJSONAtomic("status.json", status, 0o644)
}

func (s *StateStore) writeJSONAtomic(name string, value any, perm os.FileMode) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(s.dir, name)
	tmp := filepath.Join(s.dir, fmt.Sprintf(".%s.%d.tmp", name, os.Getpid()))
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}
