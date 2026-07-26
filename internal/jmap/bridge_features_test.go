package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestSessionDiscoversCalendarAccountAndSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jmap":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "alice" || pass != "secret" {
				t.Fatalf("bad auth user=%q pass=%q ok=%t", user, pass, ok)
			}
			respondJMAPJSON(t, w, map[string]any{
				"primaryAccounts": map[string]string{CapabilityCalendars: "acc-cal"},
				"accounts": map[string]any{
					"acc-cal": map[string]any{"accountCapabilities": map[string]any{CapabilityCalendars: map[string]any{}}},
				},
			})
		case "/jmap":
			var req Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode JMAP request: %v", err)
			}
			if len(req.MethodCalls) != 1 {
				t.Fatalf("method calls=%+v", req.MethodCalls)
			}
			switch req.MethodCalls[0].Name {
			case "Calendar/get":
				respondJMAPJSON(t, w, Response{MethodResponses: []MethodResponse{{Name: "Calendar/get", Params: mustRaw(t, map[string]any{"list": []Calendar{{ID: "cal-1", Name: "Work"}}}), ID: "c1"}}})
			case "CalendarEvent/get":
				respondJMAPJSON(t, w, Response{MethodResponses: []MethodResponse{{Name: "CalendarEvent/get", Params: mustRaw(t, map[string]any{"list": []Event{{ID: "evt-1", UID: "uid-1", Title: "Review", CalendarIDs: map[string]bool{"cal-1": true}, UTCStart: "2026-05-21T14:00:00Z", Duration: "PT1H"}}}), ID: "c1"}}})
			default:
				t.Fatalf("unexpected method %s", req.MethodCalls[0].Name)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Username: "alice", Password: "secret", Timeout: time.Second})
	provider, info, err := NewProviderFromSession(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if provider.AccountID != "acc-cal" || len(info.Accounts) != 1 {
		t.Fatalf("provider=%+v info=%+v", provider, info)
	}
	snapshot, err := provider.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AccountID != "acc-cal" || len(snapshot.Calendars) != 1 || len(snapshot.Events) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestSubscribeStateChangesParsesEventSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if got := r.URL.Query().Get("types"); got != "Calendar,CalendarEvent" {
			t.Fatalf("types query=%q", got)
		}
		_, _ = fmt.Fprintf(w, "data: {\"changed\":{\"acc-cal\":{\"Calendar\":\"s1\",\"CalendarEvent\":\"s2\",\"Email\":\"skip\"}}}\n\n")
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Username: "alice", Password: "secret", Timeout: time.Second})
	changes, errs, stop, err := client.SubscribeStateChanges(context.Background(), SessionInfo{EventSourceURL: server.URL + "/events"}, "acc-cal", []string{"Calendar", "CalendarEvent"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case change := <-changes:
		if change.AccountID != "acc-cal" || !reflect.DeepEqual(change.Types, []string{"Calendar", "CalendarEvent"}) {
			t.Fatalf("change=%+v", change)
		}
	case err := <-errs:
		t.Fatalf("stream err=%v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for change")
	}
}

func TestProbePushReportsAvailability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jmap":
			respondJMAPJSON(t, w, map[string]any{"eventSourceUrl": "/events"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Username: "alice", Password: "secret", Timeout: time.Second})
	info, ok, err := client.ProbePush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || info.EventSourceURL != "/events" {
		t.Fatalf("ok=%t info=%+v", ok, info)
	}
}

func TestCalendarDecodeToleratesCyrusStringAvailability(t *testing.T) {
	var calendar Calendar
	if err := json.Unmarshal([]byte(`{"id":"cal-1","name":"Work","isVisible":"true","includeInAvailability":"freeBusy"}`), &calendar); err != nil {
		t.Fatalf("unmarshal calendar: %v", err)
	}
	if calendar.ID != "cal-1" || !bool(calendar.IsVisible) || bool(calendar.IncludeInAvailability) {
		t.Fatalf("calendar=%+v", calendar)
	}
}

func TestVALARMParsingCoversAbsoluteRelativeAndMalformed(t *testing.T) {
	event := Event{ID: "evt-1", UID: "uid-1", Title: "Review", ICalendar: "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:uid-1\nSUMMARY:Project review\nDTSTART:20260521T140000Z\nDTEND:20260521T150000Z\nLOCATION:Zoom\nATTENDEE;CN=Jo:mailto:jo@example.com\nBEGIN:VALARM\nACTION:DISPLAY\nTRIGGER:20260521T130000Z\nEND:VALARM\nBEGIN:VALARM\nACTION:EMAIL\nTRIGGER:-PT10M\nEND:VALARM\nBEGIN:VALARM\nACTION:AUDIO\nTRIGGER;RELATED=END:-PT5M\nEND:VALARM\nBEGIN:VALARM\nACTION:DISPLAY\nTRIGGER:bogus\nEND:VALARM\nEND:VEVENT\nEND:VCALENDAR"}
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	occurrences, errs := AlarmOccurrences(event, "cal-1", "Work", now, 6*time.Hour)
	if len(errs) != 1 {
		t.Fatalf("errs=%v", errs)
	}
	if len(occurrences) != 3 {
		t.Fatalf("occurrences=%+v errs=%v", occurrences, errs)
	}
	want := []time.Time{
		time.Date(2026, 5, 21, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 21, 13, 50, 0, 0, time.UTC),
		time.Date(2026, 5, 21, 14, 55, 0, 0, time.UTC),
	}
	for i, occurrence := range occurrences {
		if !occurrence.TriggerTime.Equal(want[i]) {
			t.Fatalf("trigger[%d]=%s want %s", i, occurrence.TriggerTime, want[i])
		}
	}
	if occurrences[0].Location != "Zoom" || len(occurrences[0].Attendees) != 1 {
		t.Fatalf("event details=%+v", occurrences[0])
	}
}

func TestVALARMParsingFallsBackToJSCalendarAlerts(t *testing.T) {
	raw := mustRaw(t, map[string]any{
		"id":          "evt-js",
		"uid":         "uid-js",
		"title":       "JS alert review",
		"calendarIds": map[string]bool{"cal-1": true},
		"start":       "2026-05-21T14:00:00",
		"timeZone":    "Etc/UTC",
		"duration":    "PT1H",
		"alerts": map[string]any{
			"display": map[string]any{
				"@type":  "Alert",
				"action": "display",
				"trigger": map[string]any{
					"@type": "AbsoluteTrigger",
					"when":  "2026-05-21T13:00:00Z",
				},
			},
			"email": map[string]any{
				"@type":  "Alert",
				"action": "email",
				"trigger": map[string]any{
					"@type":      "OffsetTrigger",
					"offset":     "-PT10M",
					"relativeTo": "start",
				},
			},
		},
	})
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	occurrences, errs := AlarmOccurrences(event, "cal-1", "Work", now, 6*time.Hour)
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	if len(occurrences) != 2 {
		t.Fatalf("occurrences=%+v", occurrences)
	}
	if occurrences[0].Alarm.Action != "DISPLAY" || !occurrences[0].TriggerTime.Equal(time.Date(2026, 5, 21, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("absolute occurrence=%+v", occurrences[0])
	}
	if occurrences[1].Alarm.Action != "EMAIL" || !occurrences[1].TriggerTime.Equal(time.Date(2026, 5, 21, 13, 50, 0, 0, time.UTC)) {
		t.Fatalf("offset occurrence=%+v", occurrences[1])
	}
}

func TestRRULEExpansionWithinSlidingWindow(t *testing.T) {
	event := Event{ID: "evt-1", UID: "uid-1", ICalendar: "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:uid-1\nSUMMARY:Daily\nDTSTART:20260522T100000Z\nDTEND:20260522T110000Z\nRRULE:FREQ=DAILY;COUNT=10\nBEGIN:VALARM\nACTION:DISPLAY\nTRIGGER:-PT1H\nEND:VALARM\nEND:VEVENT\nEND:VCALENDAR"}
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	occurrences, errs := AlarmOccurrences(event, "cal-1", "Work", now, DefaultAlarmWindow)
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	if len(occurrences) < 2 {
		t.Fatalf("expected recurring occurrences, got %+v", occurrences)
	}
	deadline := now.Add(DefaultAlarmWindow)
	for _, occurrence := range occurrences {
		if occurrence.TriggerTime.After(deadline) {
			t.Fatalf("trigger outside window: %+v deadline=%s", occurrence, deadline)
		}
	}
}

func respondJMAPJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
