package schedule

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestAvailableSlotsRespectBusinessHoursAndBusyConflicts(t *testing.T) {
	config := DefaultConfig()
	from := mustTime(t, "2026-01-05T14:00:00Z") // Monday 09:00 Eastern.
	to := mustTime(t, "2026-01-05T22:00:00Z")   // Monday 17:00 Eastern.
	busy := []Period{{Start: mustTime(t, "2026-01-05T15:00:00Z"), End: mustTime(t, "2026-01-05T15:30:00Z"), EventUID: "appt-1", Kind: "appointment"}}

	slots := AvailableSlots(config, from, to, 30*time.Minute, busy)
	if len(slots) != 15 {
		t.Fatalf("slots = %d, want 15", len(slots))
	}
	for _, slot := range slots {
		if Intersects(slot.Start, slot.End, busy[0].Start, busy[0].End) {
			t.Fatalf("slot intersects busy period: %#v", slot)
		}
		localEnd := slot.End.In(config.Location())
		if localEnd.Hour() > 17 || (localEnd.Hour() == 17 && localEnd.Minute() > 0) {
			t.Fatalf("slot ends after hours: %s", localEnd)
		}
	}
}

func TestRejectsSlotEndingOutsideBusinessHours(t *testing.T) {
	config := DefaultConfig()
	start := mustTime(t, "2026-01-05T21:45:00Z")
	end := mustTime(t, "2026-01-05T22:15:00Z")
	if IsWithinWeeklyHours(config, start, end) {
		t.Fatal("slot ending after close should not be within business hours")
	}
}

func TestAvailableSlotsForLocalDate(t *testing.T) {
	slots, err := AvailableSlotsForLocalDate(DefaultConfig(), "2026-01-05", 2*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 4 {
		t.Fatalf("slots = %d, want 4", len(slots))
	}
	if slots[0].Start.In(DefaultConfig().Location()).Hour() != 9 {
		t.Fatalf("first slot = %s", slots[0].Start)
	}
}
