package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
	"github.com/chatinfra/jmap/internal/opencode"
)

func TestBridgeFirstVALARMCreatesSessionAndFormatsPrompt(t *testing.T) {
	stateDir := t.TempDir()
	fireAt := time.Now().Add(50 * time.Millisecond)
	snapshot := testSnapshot(testEvent("cal-1", "evt-1", "uid-1", "Project review", fireAt, alarmSpec{Action: "DISPLAY", Trigger: fireAt}))
	j := &fakeCalendarClient{snapshot: snapshot}
	oc := newFakeOpencode("ses-1")
	done := make(chan struct{})
	oc.afterPrompt = func() { close(done) }
	bridge := testBridge(stateDir, j, oc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- bridge.Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if oc.createCount() != 1 {
		t.Fatalf("create count=%d", oc.createCount())
	}
	prompts := oc.promptCalls()
	if len(prompts) != 1 || prompts[0].sessionID != "ses-1" {
		t.Fatalf("prompts=%+v", prompts)
	}
	for _, want := range []string{"VALARM fired.", "Calendar: Work", "Event:    Project review", "Location: Zoom", "UID:      uid-1", "TRIGGER=", "ACTION=DISPLAY"} {
		if !strings.Contains(prompts[0].text, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompts[0].text)
		}
	}
	var sessions SessionsFile
	readJSON(t, filepath.Join(stateDir, "sessions.json"), &sessions)
	if sessions.Sessions["cal-1"] != "ses-1" {
		t.Fatalf("sessions=%+v", sessions)
	}
}

func TestBridgeReusesSessionForSameCalendar(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now()
	snapshot := testSnapshot(testEvent("cal-1", "evt-1", "uid-1", "Review", now.Add(50*time.Millisecond),
		alarmSpec{Action: "DISPLAY", Trigger: now.Add(50 * time.Millisecond)},
		alarmSpec{Action: "EMAIL", Trigger: now.Add(90 * time.Millisecond)},
	))
	j := &fakeCalendarClient{snapshot: snapshot}
	oc := newFakeOpencode("ses-1")
	done := make(chan struct{})
	oc.afterPromptN(2, done)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridge(stateDir, j, oc).Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if oc.createCount() != 1 {
		t.Fatalf("create count=%d", oc.createCount())
	}
	if got := oc.promptSessions(); len(got) != 2 || got[0] != "ses-1" || got[1] != "ses-1" {
		t.Fatalf("prompt sessions=%v", got)
	}
}

func TestBridgeRunsDifferentCalendarsConcurrently(t *testing.T) {
	stateDir := t.TempDir()
	fireAt := time.Now().Add(50 * time.Millisecond)
	snapshot := testSnapshot(
		testEvent("cal-1", "evt-1", "uid-1", "One", fireAt, alarmSpec{Action: "DISPLAY", Trigger: fireAt}),
		testEvent("cal-2", "evt-2", "uid-2", "Two", fireAt, alarmSpec{Action: "DISPLAY", Trigger: fireAt}),
	)
	j := &fakeCalendarClient{snapshot: snapshot}
	oc := newFakeOpencode("ses-1", "ses-2")
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	oc.promptFunc = func(ctx context.Context, sessionID, text string) (opencode.AssistantResponse, error) {
		started <- struct{}{}
		if len(started) == 2 {
			once.Do(func() { close(done) })
		}
		select {
		case <-release:
		case <-ctx.Done():
			return opencode.AssistantResponse{}, ctx.Err()
		}
		return opencode.AssistantResponse{SessionID: sessionID, Text: "ok"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridge(stateDir, j, oc).Run(ctx) }()
	waitForClosed(t, done)
	close(release)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if got := oc.promptSessions(); len(got) != 2 || got[0] == got[1] {
		t.Fatalf("prompt sessions=%v", got)
	}
}

func TestBridgePersistsAndReloadsSessions(t *testing.T) {
	stateDir := t.TempDir()
	if err := NewStateStore(stateDir).SaveSessions(map[string]string{"cal-1": "ses-old"}); err != nil {
		t.Fatal(err)
	}
	fireAt := time.Now().Add(50 * time.Millisecond)
	j := &fakeCalendarClient{snapshot: testSnapshot(testEvent("cal-1", "evt-1", "uid-1", "Review", fireAt, alarmSpec{Action: "DISPLAY", Trigger: fireAt}))}
	oc := newFakeOpencode()
	done := make(chan struct{})
	oc.afterPrompt = func() { close(done) }
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridge(stateDir, j, oc).Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if oc.createCount() != 0 {
		t.Fatalf("create count=%d", oc.createCount())
	}
	if got := oc.promptSessions(); len(got) != 1 || got[0] != "ses-old" {
		t.Fatalf("prompt sessions=%v", got)
	}
}

func TestBridgeDoesNotFirePastDueVALARMs(t *testing.T) {
	stateDir := t.TempDir()
	fireAt := time.Now().Add(-time.Second)
	j := &fakeCalendarClient{snapshot: testSnapshot(testEvent("cal-1", "evt-1", "uid-1", "Review", fireAt, alarmSpec{Action: "DISPLAY", Trigger: fireAt}))}
	oc := newFakeOpencode("ses-1")
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridge(stateDir, j, oc).Run(ctx) }()
	time.Sleep(80 * time.Millisecond)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if got := oc.promptCalls(); len(got) != 0 {
		t.Fatalf("past prompts=%+v", got)
	}
}

func TestBridgeTreatsVALARMActionsUniformly(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now()
	snapshot := testSnapshot(testEvent("cal-1", "evt-1", "uid-1", "Review", now.Add(time.Hour),
		alarmSpec{Action: "DISPLAY", Trigger: now.Add(40 * time.Millisecond)},
		alarmSpec{Action: "EMAIL", Trigger: now.Add(70 * time.Millisecond)},
		alarmSpec{Action: "AUDIO", Trigger: now.Add(100 * time.Millisecond)},
		alarmSpec{Action: "PROCEDURE", Trigger: now.Add(130 * time.Millisecond)},
	))
	j := &fakeCalendarClient{snapshot: snapshot}
	oc := newFakeOpencode("ses-1")
	done := make(chan struct{})
	oc.afterPromptN(4, done)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- testBridge(stateDir, j, oc).Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if got := oc.promptCalls(); len(got) != 4 {
		t.Fatalf("prompts=%+v", got)
	}
}

func TestBridgeOpencodeErrorIsLogOnlyAndRecorded(t *testing.T) {
	stateDir := t.TempDir()
	fireAt := time.Now().Add(50 * time.Millisecond)
	j := &fakeCalendarClient{snapshot: testSnapshot(testEvent("cal-1", "evt-1", "uid-1", "Review", fireAt, alarmSpec{Action: "DISPLAY", Trigger: fireAt}))}
	oc := newFakeOpencode("ses-1")
	done := make(chan struct{})
	oc.promptFunc = func(context.Context, string, string) (opencode.AssistantResponse, error) {
		close(done)
		return opencode.AssistantResponse{}, errors.New("opencode boom")
	}
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- NewBridgeWithClients(testConfig(stateDir), log.New(&logs, "", 0), j, oc).Run(ctx) }()
	waitForClosed(t, done)
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "opencode boom") {
		t.Fatalf("logs=%s", logs.String())
	}
	var status StatusFile
	readJSON(t, filepath.Join(stateDir, "status.json"), &status)
	if status.LastError != "opencode boom" || status.LastVALARMFiredAt == nil || status.LastSnapshotRefreshAt == nil {
		t.Fatalf("status=%+v", status)
	}
}

func TestBridgeContextShutdownFlushesStateAndFatalJMAPErrorReturns(t *testing.T) {
	stateDir := t.TempDir()
	j := &fakeCalendarClient{connectErr: errors.New("jmap unavailable")}
	err := testBridge(stateDir, j, newFakeOpencode()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "jmap unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestOnlyCalendarListenerIsRegistered(t *testing.T) {
	if !hasOnlyCalendarListener() {
		t.Fatalf("listeners=%v dataTypes=%v", registeredListenerKeys(), registeredSubscriptionDataTypes())
	}
}

func testBridge(stateDir string, j *fakeCalendarClient, oc *fakeOpencode) *Bridge {
	return NewBridgeWithClients(testConfig(stateDir), log.New(&bytes.Buffer{}, "", 0), j, oc)
}

func testConfig(stateDir string) Config {
	return Config{
		JMAPBaseURL:       "http://127.0.0.1:8080",
		JMAPUser:          "alice",
		JMAPPassword:      "secret",
		OpencodeBaseURL:   "http://127.0.0.1:2721",
		OpencodeDirectory: "/repo",
		AgentID:           "agent-1",
		StateDir:          stateDir,
		PromptTimeout:     time.Second,
		PollInterval:      time.Hour,
		AlarmWindow:       24 * time.Hour,
	}
}

type fakeCalendarClient struct {
	snapshot   jmap.CalendarSnapshot
	push       bool
	connectErr error
	closed     bool
}

func (f *fakeCalendarClient) Connect(context.Context) (bool, error) {
	return f.push, f.connectErr
}

func (f *fakeCalendarClient) Snapshot(context.Context) (jmap.CalendarSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeCalendarClient) SubscribeStateChanges(context.Context) (<-chan jmap.StateChange, <-chan error, func(), error) {
	changes := make(chan jmap.StateChange)
	errs := make(chan error)
	return changes, errs, func() { close(changes); close(errs) }, nil
}

func (f *fakeCalendarClient) Close() error {
	f.closed = true
	return nil
}

type fakeOpencode struct {
	mu          sync.Mutex
	sessions    []string
	created     int
	prompts     []promptCall
	promptFunc  func(context.Context, string, string) (opencode.AssistantResponse, error)
	afterPrompt func()
}

type promptCall struct{ sessionID, text string }

func newFakeOpencode(sessions ...string) *fakeOpencode {
	return &fakeOpencode{sessions: sessions}
}

func (f *fakeOpencode) CreateSession(context.Context) (opencode.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	if len(f.sessions) == 0 {
		return opencode.Session{ID: "ses-created"}, nil
	}
	sessionID := f.sessions[0]
	f.sessions = f.sessions[1:]
	return opencode.Session{ID: sessionID}, nil
}

func (f *fakeOpencode) Prompt(ctx context.Context, sessionID, text string) (opencode.AssistantResponse, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, promptCall{sessionID: sessionID, text: text})
	f.mu.Unlock()
	if f.promptFunc != nil {
		response, err := f.promptFunc(ctx, sessionID, text)
		if f.afterPrompt != nil {
			f.afterPrompt()
		}
		return response, err
	}
	if f.afterPrompt != nil {
		f.afterPrompt()
	}
	return opencode.AssistantResponse{SessionID: sessionID, Text: "ok"}, nil
}

func (f *fakeOpencode) afterPromptN(n int, done chan<- struct{}) {
	var count int
	var once sync.Once
	f.afterPrompt = func() {
		f.mu.Lock()
		count++
		current := count
		f.mu.Unlock()
		if current >= n {
			once.Do(func() { close(done) })
		}
	}
}

func (f *fakeOpencode) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

func (f *fakeOpencode) promptCalls() []promptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]promptCall, len(f.prompts))
	copy(out, f.prompts)
	return out
}

func (f *fakeOpencode) promptSessions() []string {
	prompts := f.promptCalls()
	out := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		out = append(out, prompt.sessionID)
	}
	return out
}

type alarmSpec struct {
	Action  string
	Trigger time.Time
}

func testSnapshot(events ...jmap.Event) jmap.CalendarSnapshot {
	return jmap.CalendarSnapshot{
		AccountID: "acc-cal",
		Calendars: []jmap.Calendar{
			{ID: "cal-1", Name: "Work"},
			{ID: "cal-2", Name: "Personal"},
		},
		Events:      events,
		RefreshedAt: time.Now().UTC(),
	}
}

func testEvent(calendarID, eventID, uid, summary string, start time.Time, alarms ...alarmSpec) jmap.Event {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\nBEGIN:VEVENT\n")
	b.WriteString("UID:" + uid + "\n")
	b.WriteString("SUMMARY:" + summary + "\n")
	b.WriteString("DESCRIPTION:Discuss milestones\n")
	b.WriteString("LOCATION:Zoom\n")
	b.WriteString("DTSTART:" + start.Add(time.Hour).UTC().Format(time.RFC3339Nano) + "\n")
	b.WriteString("DTEND:" + start.Add(2*time.Hour).UTC().Format(time.RFC3339Nano) + "\n")
	for _, alarm := range alarms {
		b.WriteString("BEGIN:VALARM\n")
		b.WriteString("ACTION:" + alarm.Action + "\n")
		b.WriteString("TRIGGER;VALUE=DATE-TIME:" + alarm.Trigger.UTC().Format(time.RFC3339Nano) + "\n")
		b.WriteString("END:VALARM\n")
	}
	b.WriteString("END:VEVENT\nEND:VCALENDAR")
	return jmap.Event{ID: eventID, UID: uid, Title: summary, CalendarIDs: map[string]bool{calendarID: true}, ICalendar: b.String()}
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func waitForClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}
