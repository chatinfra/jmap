package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

// fakeJMAPClient implements calendarClient plus the mailSource and contactSource
// interfaces so the bridge wires the email listener and contact directory, the
// same way the real client does.
type fakeJMAPClient struct {
	mu sync.Mutex

	snapshot   jmap.CalendarSnapshot
	push       bool
	connectErr error

	mailboxes  []jmap.Mailbox
	messageIDs []string
	messages   map[string]jmap.Message

	contacts     []jmap.Contact
	ensureCalls  []string
	ensureResult jmap.Contact
	ensureFound  bool
}

func newFakeJMAPClient() *fakeJMAPClient {
	return &fakeJMAPClient{
		snapshot:  jmap.CalendarSnapshot{AccountID: "acc", RefreshedAt: time.Now().UTC()},
		mailboxes: []jmap.Mailbox{{ID: "inbox-1", Name: "Inbox", Role: "inbox"}},
		messages:  map[string]jmap.Message{},
	}
}

func (f *fakeJMAPClient) Connect(context.Context) (bool, error) { return f.push, f.connectErr }

func (f *fakeJMAPClient) Snapshot(context.Context) (jmap.CalendarSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}

func (f *fakeJMAPClient) SubscribeStateChanges(context.Context) (<-chan jmap.StateChange, <-chan error, func(), error) {
	changes := make(chan jmap.StateChange)
	errs := make(chan error)
	var once sync.Once
	stop := func() { once.Do(func() { close(changes); close(errs) }) }
	return changes, errs, stop, nil
}

func (f *fakeJMAPClient) Close() error { return nil }

func (f *fakeJMAPClient) Mailboxes(context.Context) ([]jmap.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mailboxes, nil
}

func (f *fakeJMAPClient) QueryMessageIDs(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.messageIDs))
	copy(out, f.messageIDs)
	return out, nil
}

func (f *fakeJMAPClient) GetMessage(_ context.Context, id string) (jmap.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[id], nil
}

func (f *fakeJMAPClient) Contacts(context.Context) ([]jmap.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]jmap.Contact, len(f.contacts))
	copy(out, f.contacts)
	return out, nil
}

func (f *fakeJMAPClient) GetOrCreateContactByEmail(_ context.Context, email string, _ jmap.Contact) (jmap.Contact, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls = append(f.ensureCalls, email)
	return f.ensureResult, !f.ensureFound, nil
}

func testBridgeWithModules(stateDir string, j *fakeJMAPClient, oc *fakeOpencode) *Bridge {
	return NewBridgeWithClients(testConfig(stateDir), log.New(&bytes.Buffer{}, "", 0), j, oc)
}

func TestEmailListenerSubmitsNewlyArrivedMailToAgent(t *testing.T) {
	stateDir := t.TempDir()
	// A prior run has already initialized the seen set (empty), so the inbox
	// message counts as newly arrived rather than a first-run baseline.
	if err := NewStateStore(stateDir).SaveMessages(MessagesFile{Initialized: true, SeenIDs: nil}); err != nil {
		t.Fatal(err)
	}

	j := newFakeJMAPClient()
	j.contacts = []jmap.Contact{{
		ID:        "c-1",
		FirstName: "Dana",
		LastName:  "Rivera",
		Company:   "Acme",
		Emails:    []jmap.Email{{Type: "work", Value: "dana@acme.example"}},
	}}
	j.messageIDs = []string{"m-1"}
	j.messages = map[string]jmap.Message{
		"m-1": {
			ID:         "m-1",
			From:       []string{"Dana Rivera <dana@acme.example>"},
			To:         []string{"agent@tenant.example"},
			Subject:    "Quarterly numbers",
			ReceivedAt: "2026-05-21T14:00:00Z",
			MessageID:  []string{"<abc@acme.example>"},
			TextBody:   []jmap.Body{{PartID: "p1"}},
			BodyValues: map[string]any{"p1": map[string]any{"value": "Please review the attached figures."}},
		},
	}

	oc := newFakeOpencode("mail-ses")
	done := make(chan struct{})
	oc.afterPrompt = func() { close(done) }

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridgeWithModules(stateDir, j, oc).Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}

	prompts := oc.promptCalls()
	if len(prompts) != 1 {
		t.Fatalf("expected one mail prompt, got %+v", prompts)
	}
	if prompts[0].sessionID != "mail-ses" {
		t.Fatalf("mail prompt used session %q, want the mail session", prompts[0].sessionID)
	}
	for _, want := range []string{"New mail received.", "From:", "dana@acme.example", "Subject:  Quarterly numbers", "Contact:  Dana Rivera", "Please review the attached figures."} {
		if !strings.Contains(prompts[0].text, want) {
			t.Fatalf("mail prompt missing %q:\n%s", want, prompts[0].text)
		}
	}

	// The per-mailbox session must be persisted under a mail-namespaced key.
	var sessions SessionsFile
	readJSON(t, filepath.Join(stateDir, "sessions.json"), &sessions)
	if sessions.Sessions["mail:inbox-1"] != "mail-ses" {
		t.Fatalf("mail session not persisted: %+v", sessions)
	}

	var status StatusFile
	readJSON(t, filepath.Join(stateDir, "status.json"), &status)
	if status.LastEmailReceivedAt == nil {
		t.Fatalf("status did not record last email received: %+v", status)
	}
	if status.MailSessionCount != 1 {
		t.Fatalf("status mail session count=%d want 1", status.MailSessionCount)
	}

	// The submitted message is now marked seen and would not resubmit.
	var messages MessagesFile
	readJSON(t, filepath.Join(stateDir, "messages.json"), &messages)
	if !messages.Initialized || !containsTestString(messages.SeenIDs, "m-1") {
		t.Fatalf("message m-1 not recorded as seen: %+v", messages)
	}
}

func TestEmailListenerBaselinesExistingInboxOnFirstRun(t *testing.T) {
	stateDir := t.TempDir()
	j := newFakeJMAPClient()
	j.messageIDs = []string{"old-1", "old-2"}
	j.messages = map[string]jmap.Message{
		"old-1": {ID: "old-1", Subject: "old one"},
		"old-2": {ID: "old-2", Subject: "old two"},
	}
	oc := newFakeOpencode("mail-ses")

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridgeWithModules(stateDir, j, oc).Run(ctx) }()

	// Wait for the first-run baseline to persist, then assert no mail was submitted.
	waitForMessages(t, filepath.Join(stateDir, "messages.json"), func(m MessagesFile) bool {
		return m.Initialized && len(m.SeenIDs) == 2
	})
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if got := oc.promptCalls(); len(got) != 0 {
		t.Fatalf("first-run baseline must not submit pre-existing mail, got %+v", got)
	}
}

func TestEmailListenerReportsMissingSharedContact(t *testing.T) {
	stateDir := t.TempDir()
	if err := NewStateStore(stateDir).SaveMessages(MessagesFile{Initialized: true}); err != nil {
		t.Fatal(err)
	}
	j := newFakeJMAPClient()
	j.messageIDs = []string{"m-9"}
	j.messages = map[string]jmap.Message{
		"m-9": {ID: "m-9", From: []string{"stranger@nowhere.example"}, Subject: "Hello"},
	}
	oc := newFakeOpencode("mail-ses")
	done := make(chan struct{})
	oc.afterPrompt = func() { close(done) }
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridgeWithModules(stateDir, j, oc).Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	prompts := oc.promptCalls()
	if len(prompts) != 1 || !strings.Contains(prompts[0].text, "(no matching shared contact)") {
		t.Fatalf("expected a no-contact prompt, got %+v", prompts)
	}
}

// fakeContactSource is a minimal contactSource for directory unit tests.
type fakeContactSource struct {
	contacts    []jmap.Contact
	ensureEmail string
	ensureOut   jmap.Contact
	ensureNew   bool
}

func (f *fakeContactSource) Contacts(context.Context) ([]jmap.Contact, error) { return f.contacts, nil }

func (f *fakeContactSource) GetOrCreateContactByEmail(_ context.Context, email string, _ jmap.Contact) (jmap.Contact, bool, error) {
	f.ensureEmail = email
	return f.ensureOut, f.ensureNew, nil
}

func TestContactDirectoryLoadsResolvesAndProvisionsSharedContacts(t *testing.T) {
	stateDir := t.TempDir()
	source := &fakeContactSource{
		contacts: []jmap.Contact{
			{ID: "c-1", FirstName: "Dana", LastName: "Rivera", Emails: []jmap.Email{{Value: "dana@acme.example"}}},
			{ID: "c-2", FirstName: "Sam", Emails: []jmap.Email{{Value: "sam@acme.example"}}},
		},
		ensureOut: jmap.Contact{ID: "c-3", FirstName: "new@acme.example"},
		ensureNew: true,
	}
	var refreshed time.Time
	dir := newContactDirectory(source, NewStateStore(stateDir), log.New(&bytes.Buffer{}, "", 0), func(at time.Time) { refreshed = at })

	if err := dir.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshed.IsZero() {
		t.Fatal("refresh callback did not fire")
	}
	if got := dir.Records(); len(got) != 2 {
		t.Fatalf("directory records=%+v", got)
	}
	contact, ok := dir.Resolve("DANA@acme.example")
	if !ok || contact.ID != "c-1" {
		t.Fatalf("resolve mismatch ok=%t contact=%+v", ok, contact)
	}
	if _, ok := dir.Resolve("unknown@acme.example"); ok {
		t.Fatal("resolve of unknown email must miss")
	}

	// A fresh directory warms itself from the persisted cache.
	warm := newContactDirectory(source, NewStateStore(stateDir), log.New(&bytes.Buffer{}, "", 0), nil)
	if err := warm.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := warm.Resolve("sam@acme.example"); !ok {
		t.Fatal("warm directory did not load persisted contacts")
	}

	// The on-demand lookup-or-provision capability delegates to the provider.
	created, isNew, err := dir.EnsureByEmail(context.Background(), "new@acme.example", jmap.Contact{})
	if err != nil || !isNew || created.ID != "c-3" {
		t.Fatalf("ensure by email: contact=%+v new=%t err=%v", created, isNew, err)
	}
	if source.ensureEmail != "new@acme.example" {
		t.Fatalf("ensure delegated with email %q", source.ensureEmail)
	}
}

func waitForMessages(t *testing.T, path string, matches func(MessagesFile) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var file MessagesFile
		if readJSONSoft(path, &file) && matches(file) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for messages.json at %s", path)
}

func readJSONSoft(path string, out any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}
