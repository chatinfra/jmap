package appointment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Service struct {
	Name        string `json:"name"`
	Duration    string `json:"duration"`
	Display     bool   `json:"display"`
	CostDollars int    `json:"costDollars"`
}

type Entry struct {
	EventID     string    `json:"eventId"`
	ContactID   string    `json:"contactId"`
	ContactName string    `json:"contactName,omitempty"`
	Title       string    `json:"title,omitempty"`
	Start       time.Time `json:"start,omitempty"`
	Duration    string    `json:"duration,omitempty"`
	Services    []Service `json:"services"`
	CreatedAt   time.Time `json:"createdAt"`
}

type WaitingListRequest struct {
	ContactID  string    `json:"contactId"`
	Days       []string  `json:"days,omitempty"`
	Times      []string  `json:"times,omitempty"`
	ServiceIDs []string  `json:"serviceIds,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Notification struct {
	EventID string    `json:"eventId"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	TS      time.Time `json:"ts"`
}

type State struct {
	Account       string                          `json:"account"`
	Entries       map[string]Entry                `json:"entries"`
	Notifications map[string]Notification         `json:"notifications"`
	Waiting       map[string][]WaitingListRequest `json:"waiting"`
}

type Store struct {
	root    string
	account string
	path    string
}

func DefaultServices() []Service {
	return []Service{
		{Name: "service1", Duration: "10m", Display: true, CostDollars: 100},
		{Name: "service2", Duration: "17m", Display: true, CostDollars: 102},
		{Name: "service3", Duration: "60m", Display: true, CostDollars: 100},
		{Name: "service4", Duration: "17m", Display: true, CostDollars: 111},
		{Name: "service5", Duration: "31m", Display: true, CostDollars: 100},
	}
}

func ConfigDefaultServices() []Service {
	services := DefaultServices()
	return append([]Service{}, services[1:3]...)
}

func ServicesDuration(services []Service) (time.Duration, error) {
	var total time.Duration
	for _, service := range services {
		d, err := time.ParseDuration(service.Duration)
		if err != nil {
			return 0, fmt.Errorf("service %s duration: %w", service.Name, err)
		}
		total += d
	}
	return total, nil
}

func SelectServices(names []string) ([]Service, error) {
	all := map[string]Service{}
	for _, service := range DefaultServices() {
		all[service.Name] = service
	}
	if len(names) == 0 {
		return nil, nil
	}
	selected := make([]Service, 0, len(names))
	for _, name := range names {
		service, ok := all[name]
		if !ok {
			return nil, fmt.Errorf("unknown service %q", name)
		}
		selected = append(selected, service)
	}
	return selected, nil
}

func AccountKey(baseURL, username string) string {
	if username == "" && baseURL == "" {
		return "default"
	}
	return username + "@" + baseURL
}

func DefaultStateRoot(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if value := getenv("JMAP_STATE_ROOT"); value != "" {
		return value
	}
	if value := getenv("SUPER_TMP_DIR"); value != "" {
		return filepath.Join(value, "jmap-state")
	}
	if config, err := os.UserConfigDir(); err == nil && config != "" {
		return filepath.Join(config, "jmap", "state")
	}
	return filepath.Join(".", "tmp", "jmap-state")
}

func NewStore(root, account string) (*Store, error) {
	if root == "" {
		return nil, errors.New("state root is required")
	}
	if account == "" {
		account = "default"
	}
	hash := sha256.Sum256([]byte(account))
	name := hex.EncodeToString(hash[:])[:24] + ".json"
	return &Store{root: root, account: account, path: filepath.Join(root, name)}, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Load() (State, error) {
	state := State{Account: s.account, Entries: map[string]Entry{}, Notifications: map[string]Notification{}, Waiting: map[string][]WaitingListRequest{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Account == "" {
		state.Account = s.account
	}
	if state.Entries == nil {
		state.Entries = map[string]Entry{}
	}
	if state.Notifications == nil {
		state.Notifications = map[string]Notification{}
	}
	if state.Waiting == nil {
		state.Waiting = map[string][]WaitingListRequest{}
	}
	return state, nil
}

func (s *Store) Save(state State) error {
	state.Account = s.account
	if state.Entries == nil {
		state.Entries = map[string]Entry{}
	}
	if state.Notifications == nil {
		state.Notifications = map[string]Notification{}
	}
	if state.Waiting == nil {
		state.Waiting = map[string][]WaitingListRequest{}
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0o600)
}

func (s *Store) PutEntry(entry Entry) error {
	if entry.EventID == "" {
		return errors.New("event id is required")
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	state.Entries[entry.EventID] = entry
	return s.Save(state)
}

func (s *Store) GetEntry(eventID string) (Entry, bool, error) {
	state, err := s.Load()
	if err != nil {
		return Entry{}, false, err
	}
	entry, ok := state.Entries[eventID]
	return entry, ok, nil
}

func (s *Store) ListEntries() ([]Entry, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(state.Entries))
	for _, entry := range state.Entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Start.Before(entries[j].Start) })
	return entries, nil
}

func (s *Store) ListFuture(now time.Time, contactID string) ([]Entry, error) {
	entries, err := s.ListEntries()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, entry := range entries {
		if contactID != "" && entry.ContactID != contactID {
			continue
		}
		if entry.Start.IsZero() || !entry.Start.Before(now.Add(-10*time.Second)) {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s *Store) UpdateContactName(contactID, name string) ([]Entry, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	updated := []Entry{}
	for id, entry := range state.Entries {
		if entry.ContactID == contactID {
			entry.ContactName = name
			state.Entries[id] = entry
			updated = append(updated, entry)
		}
	}
	if err := s.Save(state); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DefaultServicesForContact(contactID string) ([]Service, error) {
	entries, err := s.ListEntries()
	if err != nil {
		return nil, err
	}
	var last *Entry
	for i := range entries {
		entry := entries[i]
		if contactID != "" && entry.ContactID != contactID {
			continue
		}
		if last == nil || entry.Start.After(last.Start) {
			copy := entry
			last = &copy
		}
	}
	if last != nil && len(last.Services) > 0 {
		return append([]Service{}, last.Services...), nil
	}
	return ConfigDefaultServices(), nil
}

func (s *Store) AddWaitingList(req WaitingListRequest) error {
	if req.ContactID == "" {
		return errors.New("contact id is required")
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	state.Waiting[req.ContactID] = append(state.Waiting[req.ContactID], req)
	return s.Save(state)
}

func (s *Store) WaitingList(contactID string) ([]WaitingListRequest, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	if contactID != "" {
		return append([]WaitingListRequest{}, state.Waiting[contactID]...), nil
	}
	var out []WaitingListRequest
	for _, requests := range state.Waiting {
		out = append(out, requests...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) MarkNotificationSent(eventID string, ts time.Time) error {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return s.putNotification(Notification{EventID: eventID, Status: "sent", TS: ts})
}

func (s *Store) MarkNotificationFailed(eventID, message string, ts time.Time) error {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return s.putNotification(Notification{EventID: eventID, Status: "failed", Error: message, TS: ts})
}

func (s *Store) putNotification(notification Notification) error {
	if notification.EventID == "" {
		return errors.New("event id is required")
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	state.Notifications[notification.EventID] = notification
	return s.Save(state)
}

func (s *Store) IsNotificationMarked(eventID string) (bool, error) {
	state, err := s.Load()
	if err != nil {
		return false, err
	}
	_, ok := state.Notifications[eventID]
	return ok, nil
}

func (s *Store) Notifications() ([]Notification, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	out := make([]Notification, 0, len(state.Notifications))
	for _, notification := range state.Notifications {
		out = append(out, notification)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

func (s *Store) NotifyDue(now time.Time, within time.Duration) ([]Entry, error) {
	if within == 0 {
		within = 24 * time.Hour
	}
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, entry := range state.Entries {
		if _, marked := state.Notifications[entry.EventID]; marked {
			continue
		}
		if entry.Start.IsZero() {
			continue
		}
		if !entry.Start.Before(now) && !entry.Start.After(now.Add(within)) {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
