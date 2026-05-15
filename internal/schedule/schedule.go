package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Clock struct {
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}

type WeeklyWindow struct {
	Day   time.Weekday `json:"day"`
	Open  Clock        `json:"open"`
	Close Clock        `json:"close"`
}

type Config struct {
	AppointmentCalendarName string         `json:"appointmentCalendarName"`
	HoursCalendarName       string         `json:"hoursCalendarName"`
	AppointmentCalendars    []string       `json:"appointmentCalendars"`
	HoursCalendars          []string       `json:"hoursCalendars"`
	SlotDays                int            `json:"slotDays"`
	TimeZone                string         `json:"timeZone"`
	WeeklyHours             []WeeklyWindow `json:"weeklyHours"`
}

type Period struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	EventID   string    `json:"eventId,omitempty"`
	EventUID  string    `json:"eventUid,omitempty"`
	Title     string    `json:"title,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Calendar  string    `json:"calendar,omitempty"`
	BusyState string    `json:"busyState,omitempty"`
}

type Slot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func DefaultConfig() Config {
	weekly := make([]WeeklyWindow, 0, 5)
	for _, day := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
		weekly = append(weekly, WeeklyWindow{Day: day, Open: Clock{Hour: 9}, Close: Clock{Hour: 17}})
	}
	return Config{
		AppointmentCalendarName: "appts",
		HoursCalendarName:       "hours",
		AppointmentCalendars:    []string{"appts"},
		HoursCalendars:          []string{"hours"},
		SlotDays:                30,
		TimeZone:                "America/New_York",
		WeeklyHours:             weekly,
	}
}

func (c Config) Normalize() Config {
	if c.AppointmentCalendarName == "" {
		c.AppointmentCalendarName = "appts"
	}
	if c.HoursCalendarName == "" {
		c.HoursCalendarName = "hours"
	}
	if len(c.AppointmentCalendars) == 0 {
		c.AppointmentCalendars = []string{c.AppointmentCalendarName}
	}
	if len(c.HoursCalendars) == 0 {
		c.HoursCalendars = []string{c.HoursCalendarName}
	}
	if c.SlotDays == 0 {
		c.SlotDays = 30
	}
	if c.TimeZone == "" {
		c.TimeZone = "America/New_York"
	}
	if len(c.WeeklyHours) == 0 {
		c.WeeklyHours = DefaultConfig().WeeklyHours
	}
	return c
}

func (c Config) Location() *time.Location {
	c = c.Normalize()
	loc, err := time.LoadLocation(c.TimeZone)
	if err == nil {
		return loc
	}
	if strings.EqualFold(c.TimeZone, "Eastern") || strings.EqualFold(c.TimeZone, "US/Eastern") {
		if loc, err := time.LoadLocation("America/New_York"); err == nil {
			return loc
		}
	}
	return time.UTC
}

func ParseClock(value string) (Clock, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return Clock{}, fmt.Errorf("invalid clock %q", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return Clock{}, err
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return Clock{}, err
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return Clock{}, fmt.Errorf("invalid clock %q", value)
	}
	return Clock{Hour: hour, Minute: minute}, nil
}

func ParseWeeklyHours(value string) ([]WeeklyWindow, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var windows []WeeklyWindow
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		dayPart, rangePart, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("weekly hours item %q must use day=open-close", item)
		}
		openPart, closePart, ok := strings.Cut(rangePart, "-")
		if !ok {
			return nil, fmt.Errorf("weekly hours item %q must use open-close", item)
		}
		day, err := ParseWeekday(dayPart)
		if err != nil {
			return nil, err
		}
		open, err := ParseClock(openPart)
		if err != nil {
			return nil, err
		}
		close, err := ParseClock(closePart)
		if err != nil {
			return nil, err
		}
		windows = append(windows, WeeklyWindow{Day: day, Open: open, Close: close})
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Day < windows[j].Day })
	return windows, nil
}

func ParseWeekday(value string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tues", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return time.Sunday, fmt.Errorf("unknown weekday %q", value)
	}
}

func AtClock(day time.Time, clock Clock, loc *time.Location) time.Time {
	local := day.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), clock.Hour, clock.Minute, 0, 0, loc)
}

func NextWeeklyWindowStart(now time.Time, window WeeklyWindow, loc *time.Location) time.Time {
	local := now.In(loc)
	days := (int(window.Day) - int(local.Weekday()) + 7) % 7
	candidate := AtClock(local.AddDate(0, 0, days), window.Open, loc)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

func WeeklyHourPeriods(config Config, from, to time.Time) []Period {
	config = config.Normalize()
	loc := config.Location()
	startDay := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	endDay := time.Date(to.In(loc).Year(), to.In(loc).Month(), to.In(loc).Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	var periods []Period
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		for _, window := range config.WeeklyHours {
			if day.Weekday() != window.Day {
				continue
			}
			start := AtClock(day, window.Open, loc)
			end := AtClock(day, window.Close, loc)
			if !end.After(start) {
				continue
			}
			if Intersects(start, end, from, to) {
				key := fmt.Sprintf("hours-%s-%s", strings.ToLower(window.Day.String()), start.Format("20060102"))
				periods = append(periods, Period{Start: start, End: end, EventUID: key, Title: strings.ToLower(window.Day.String()) + "-hours", Kind: "hours", Calendar: config.HoursCalendarName})
			}
		}
	}
	return periods
}

func PeriodKeys(periods []Period, kind string) map[string]bool {
	keys := map[string]bool{}
	for _, period := range periods {
		if kind != "" && period.Kind != kind {
			continue
		}
		if period.EventUID != "" {
			keys[period.EventUID] = true
		}
		if period.EventID != "" {
			keys[period.EventID] = true
		}
	}
	return keys
}

func Intersects(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

func Contains(containerStart, containerEnd, start, end time.Time) bool {
	return !start.Before(containerStart) && !end.After(containerEnd)
}

func periodKey(period Period) string {
	if period.EventUID != "" {
		return period.EventUID
	}
	return period.EventID
}

func IsBusy(periods []Period, appointmentKeys map[string]bool, start, end time.Time) bool {
	return len(ConflictingAppointments(periods, appointmentKeys, start, end)) > 0
}

func ConflictingAppointments(periods []Period, appointmentKeys map[string]bool, start, end time.Time) []Period {
	var conflicts []Period
	for _, period := range periods {
		key := periodKey(period)
		isAppointment := period.Kind == "appointment" || appointmentKeys[key]
		if !isAppointment {
			continue
		}
		if Intersects(period.Start, period.End, start, end) {
			conflicts = append(conflicts, period)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Start.Before(conflicts[j].Start) })
	return conflicts
}

func LastConflictEnd(periods []Period, appointmentKeys map[string]bool, start, end time.Time) time.Time {
	last := time.Time{}
	for _, conflict := range ConflictingAppointments(periods, appointmentKeys, start, end) {
		if conflict.End.After(last) {
			last = conflict.End
		}
	}
	return last
}

func IsWithinBusinessHours(periods []Period, hoursKeys map[string]bool, start, end time.Time) bool {
	for _, period := range periods {
		key := periodKey(period)
		isHours := period.Kind == "hours" || hoursKeys[key]
		if !isHours {
			continue
		}
		if Contains(period.Start, period.End, start, end) {
			return true
		}
	}
	return false
}

func IsWithinWeeklyHours(config Config, start, end time.Time) bool {
	periods := WeeklyHourPeriods(config, start, end)
	return IsWithinBusinessHours(periods, PeriodKeys(periods, "hours"), start, end)
}

func AvailableSlots(config Config, from, to time.Time, duration time.Duration, busy []Period) []Slot {
	hours := WeeklyHourPeriods(config, from, to)
	periods := append(append([]Period{}, hours...), busy...)
	return AvailableSlotsRange(from, to, duration, periods, PeriodKeys(hours, "hours"), PeriodKeys(busy, "appointment"))
}

func AvailableSlotsRange(from, to time.Time, duration time.Duration, periods []Period, hoursKeys, appointmentKeys map[string]bool) []Slot {
	if duration <= 0 || !to.After(from) {
		return nil
	}
	ts := from.Truncate(time.Minute)
	if ts.Before(from) {
		ts = ts.Add(time.Minute)
	}
	var slots []Slot
	for attempts := 0; attempts < 1000 && ts.Before(to); attempts++ {
		candidate := Slot{Start: ts, End: ts.Add(duration)}
		if candidate.End.After(to) {
			break
		}
		boundaryStart := candidate.End.Add(-time.Minute)
		if boundaryStart.Before(candidate.Start) {
			boundaryStart = candidate.Start
		}
		within := IsWithinBusinessHours(periods, hoursKeys, candidate.Start, candidate.End) && IsWithinBusinessHours(periods, hoursKeys, boundaryStart, candidate.End)
		if !within {
			next, ok := NextBusinessStart(periods, hoursKeys, ts, duration)
			if !ok || !next.After(ts) {
				break
			}
			ts = next
			continue
		}
		if IsBusy(periods, appointmentKeys, candidate.Start, candidate.End) {
			last := LastConflictEnd(periods, appointmentKeys, candidate.Start, candidate.End)
			if last.After(ts) {
				ts = last.Truncate(time.Minute)
				if ts.Before(last) {
					ts = ts.Add(time.Minute)
				}
			} else {
				ts = candidate.End
			}
			continue
		}
		slots = append(slots, candidate)
		ts = candidate.End
	}
	return slots
}

func NextBusinessStart(periods []Period, hoursKeys map[string]bool, ts time.Time, duration time.Duration) (time.Time, bool) {
	var next time.Time
	for _, period := range periods {
		key := periodKey(period)
		isHours := period.Kind == "hours" || hoursKeys[key]
		if !isHours {
			continue
		}
		if Contains(period.Start, period.End, ts, ts.Add(duration)) {
			return ts, true
		}
		if period.Start.After(ts) && Contains(period.Start, period.End, period.Start, period.Start.Add(duration)) {
			if next.IsZero() || period.Start.Before(next) {
				next = period.Start
			}
		}
	}
	if next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

func AvailableSlotsForLocalDate(config Config, date string, duration time.Duration, busy []Period) ([]Slot, error) {
	loc := config.Normalize().Location()
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, err
	}
	return AvailableSlots(config, day, day.AddDate(0, 0, 1), duration, busy), nil
}

func AvailableDates(config Config, from time.Time, duration time.Duration, busy []Period) []string {
	config = config.Normalize()
	loc := config.Location()
	dates := []string{}
	for i := 1; i <= config.SlotDays; i++ {
		day := from.In(loc).AddDate(0, 0, i)
		date := day.Format("2006-01-02")
		slots, err := AvailableSlotsForLocalDate(config, date, duration, busy)
		if err == nil && len(slots) > 0 {
			dates = append(dates, date)
		}
	}
	return dates
}

func NextAvailable(config Config, from time.Time, duration time.Duration, busy []Period, limit int) []Slot {
	config = config.Normalize()
	if limit <= 0 {
		limit = 10
	}
	to := from.AddDate(0, 0, config.SlotDays)
	slots := AvailableSlots(config, from, to, duration, busy)
	if len(slots) > limit {
		return slots[:limit]
	}
	return slots
}
