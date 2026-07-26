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

// ContactsFile persists the tenant's shared contact records that the contact
// directory has loaded, so the agent-facing directory stays warm across
// restarts. It is a read-model cache, not a source of truth.
type ContactsFile struct {
	Contacts    []jmap.Contact `json:"contacts"`
	RefreshedAt time.Time      `json:"refreshedAt"`
}

// MessagesFile persists the set of inbound message IDs the email listener has
// already observed. Initialized distinguishes a first-ever start (baseline the
// existing inbox without submitting) from a restart with a persisted set.
type MessagesFile struct {
	Initialized bool     `json:"initialized"`
	SeenIDs     []string `json:"seenIds"`
}

type StatusFile struct {
	JMAPConnected          bool       `json:"jmapConnected"`
	PushState              string     `json:"pushState"`
	LastSnapshotRefreshAt  *time.Time `json:"lastSnapshotRefreshAt,omitempty"`
	LastVALARMFiredAt      *time.Time `json:"lastValarmFiredAt,omitempty"`
	LastEmailReceivedAt    *time.Time `json:"lastEmailReceivedAt,omitempty"`
	LastContactRefreshAt   *time.Time `json:"lastContactRefreshAt,omitempty"`
	LastPromptCompletedAt  *time.Time `json:"lastPromptCompletedAt,omitempty"`
	LastError              string     `json:"lastError,omitempty"`
	CalendarSessionCount   int        `json:"calendarSessionCount"`
	MailSessionCount       int        `json:"mailSessionCount"`
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

func (s *StateStore) LoadContacts() (ContactsFile, error) {
	path := filepath.Join(s.dir, "contacts.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ContactsFile{}, nil
		}
		return ContactsFile{}, err
	}
	var file ContactsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ContactsFile{}, err
	}
	return file, nil
}

func (s *StateStore) SaveContacts(file ContactsFile) error {
	return s.writeJSONAtomic("contacts.json", file, 0o600)
}

func (s *StateStore) LoadMessages() (MessagesFile, error) {
	path := filepath.Join(s.dir, "messages.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MessagesFile{}, nil
		}
		return MessagesFile{}, err
	}
	var file MessagesFile
	if err := json.Unmarshal(data, &file); err != nil {
		return MessagesFile{}, err
	}
	return file, nil
}

func (s *StateStore) SaveMessages(file MessagesFile) error {
	return s.writeJSONAtomic("messages.json", file, 0o600)
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
	tmpFile, err := os.CreateTemp(s.dir, fmt.Sprintf(".%s.*.tmp", name))
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanupTmp = false
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}
