package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

func FormatPrompt(alarm jmap.AlarmOccurrence) string {
	var b strings.Builder
	b.WriteString("VALARM fired.\n")
	fmt.Fprintf(&b, "  Calendar: %s\n", firstNonEmptyString(alarm.CalendarName, alarm.CalendarID))
	fmt.Fprintf(&b, "  Event:    %s\n", firstNonEmptyString(alarm.Summary, alarm.EventID))
	fmt.Fprintf(&b, "  Starts:   %s\n", formatUTC(alarm.Start))
	if strings.TrimSpace(alarm.Location) != "" {
		fmt.Fprintf(&b, "  Location: %s\n", strings.TrimSpace(alarm.Location))
	}
	if len(alarm.Attendees) > 0 {
		fmt.Fprintf(&b, "  Attendees: %s\n", strings.Join(alarm.Attendees, ", "))
	}
	if strings.TrimSpace(alarm.Description) != "" {
		fmt.Fprintf(&b, "  Description: %s\n", strings.TrimSpace(alarm.Description))
	}
	fmt.Fprintf(&b, "  UID:      %s\n", firstNonEmptyString(alarm.EventUID, alarm.EventID))
	fmt.Fprintf(&b, "  VALARM:   TRIGGER=%s ACTION=%s", alarm.Alarm.Trigger, alarm.Alarm.Action)
	return b.String()
}

func formatUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
