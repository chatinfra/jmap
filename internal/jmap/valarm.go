package jmap

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultAlarmWindow = 7 * 24 * time.Hour

type VALARM struct {
	Action      string         `json:"action"`
	Trigger     string         `json:"trigger"`
	Related     string         `json:"related,omitempty"`
	Description string         `json:"description,omitempty"`
	Absolute    *time.Time     `json:"-"`
	Offset      *time.Duration `json:"-"`
}

type AlarmOccurrence struct {
	CalendarID   string    `json:"calendarId"`
	CalendarName string    `json:"calendarName"`
	EventID      string    `json:"eventId"`
	EventUID     string    `json:"eventUid,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Description  string    `json:"description,omitempty"`
	Location     string    `json:"location,omitempty"`
	Attendees    []string  `json:"attendees,omitempty"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	TriggerTime  time.Time `json:"triggerTime"`
	Alarm        VALARM    `json:"alarm"`
	AlarmIndex   int       `json:"alarmIndex"`
	Occurrence   int       `json:"occurrence"`
}

type eventDetails struct {
	Summary     string
	UID         string
	Description string
	Location    string
	Attendees   []string
	Start       time.Time
	End         time.Time
	RRULEs      []string
	Alarms      []VALARM
}

type icalProperty struct {
	Name   string
	Params map[string]string
	Value  string
}

func AlarmOccurrences(event Event, calendarID, calendarName string, now time.Time, window time.Duration) ([]AlarmOccurrence, []error) {
	if window <= 0 {
		window = DefaultAlarmWindow
	}
	details, errs := ParseVALARMs(event)
	if len(details.Alarms) == 0 {
		return nil, errs
	}
	if details.Start.IsZero() {
		errs = append(errs, fmt.Errorf("event %s has no parseable start time", eventID(event)))
		return nil, errs
	}
	if details.End.IsZero() || details.End.Before(details.Start) {
		details.End = details.Start
	}
	windowEnd := now.Add(window)
	starts := expandEventStarts(details.Start, details.RRULEs, event.RecurrenceRules, now, windowEnd)
	duration := details.End.Sub(details.Start)
	out := make([]AlarmOccurrence, 0, len(starts)*len(details.Alarms))
	for occurrenceIndex, start := range starts {
		end := start.Add(duration)
		for alarmIndex, alarm := range details.Alarms {
			trigger, ok := alarmTriggerTime(alarm, start, end, occurrenceIndex)
			if !ok || !trigger.After(now) || trigger.After(windowEnd) {
				continue
			}
			out = append(out, AlarmOccurrence{
				CalendarID:   calendarID,
				CalendarName: calendarName,
				EventID:      eventID(event),
				EventUID:     firstNonEmpty(details.UID, event.UID),
				Summary:      firstNonEmpty(details.Summary, event.Title),
				Description:  firstNonEmpty(details.Description, event.Description),
				Location:     firstNonEmpty(details.Location, eventLocation(event)),
				Attendees:    firstNonEmptySlice(details.Attendees, eventAttendees(event)),
				Start:        start.UTC(),
				End:          end.UTC(),
				TriggerTime:  trigger.UTC(),
				Alarm:        alarm,
				AlarmIndex:   alarmIndex,
				Occurrence:   occurrenceIndex,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TriggerTime.Equal(out[j].TriggerTime) {
			return out[i].TriggerTime.Before(out[j].TriggerTime)
		}
		if out[i].CalendarID != out[j].CalendarID {
			return out[i].CalendarID < out[j].CalendarID
		}
		if out[i].EventID != out[j].EventID {
			return out[i].EventID < out[j].EventID
		}
		return out[i].AlarmIndex < out[j].AlarmIndex
	})
	return out, errs
}

func ParseVALARMs(event Event) (eventDetails, []error) {
	details := eventDetails{
		Summary:     event.Title,
		UID:         event.UID,
		Description: event.Description,
		Location:    eventLocation(event),
		Attendees:   eventAttendees(event),
	}
	var errs []error
	if start, err := event.StartTime(); err == nil {
		details.Start = start.UTC()
	}
	if end, err := event.EndTime(); err == nil {
		details.End = end.UTC()
	} else if !details.Start.IsZero() {
		details.End = details.Start
	}
	ical := EventICalendar(event)
	if strings.TrimSpace(ical) == "" {
		details.Attendees = uniqueStrings(details.Attendees)
		alerts, alertErrs := parseJSCalendarAlerts(event, details.Start, details.End, event.TimeZone)
		details.Alarms = append(details.Alarms, alerts...)
		errs = append(errs, alertErrs...)
		return details, errs
	}
	props := parseICalendarProperties(ical)
	var inEvent bool
	var inAlarm bool
	var alarmProps []icalProperty
	for _, prop := range props {
		switch prop.Name {
		case "BEGIN":
			if strings.EqualFold(prop.Value, "VEVENT") {
				inEvent = true
			}
			if strings.EqualFold(prop.Value, "VALARM") && inEvent {
				inAlarm = true
				alarmProps = nil
			}
			continue
		case "END":
			if strings.EqualFold(prop.Value, "VALARM") && inAlarm {
				alarm, err := buildVALARM(alarmProps, details.Start, details.End, event.TimeZone)
				if err != nil {
					errs = append(errs, fmt.Errorf("event %s VALARM ignored: %w", eventID(event), err))
				} else {
					details.Alarms = append(details.Alarms, alarm)
				}
				inAlarm = false
				alarmProps = nil
				continue
			}
			if strings.EqualFold(prop.Value, "VEVENT") {
				inEvent = false
			}
		}
		if !inEvent {
			continue
		}
		if inAlarm {
			alarmProps = append(alarmProps, prop)
			continue
		}
		switch prop.Name {
		case "SUMMARY":
			details.Summary = unescapeICalText(prop.Value)
		case "UID":
			details.UID = prop.Value
		case "DESCRIPTION":
			details.Description = unescapeICalText(prop.Value)
		case "LOCATION":
			details.Location = unescapeICalText(prop.Value)
		case "ATTENDEE":
			details.Attendees = append(details.Attendees, attendeeText(prop))
		case "DTSTART":
			if parsed, err := parseICalDateTime(prop.Value, prop.Params["TZID"]); err == nil {
				details.Start = parsed.UTC()
			} else {
				errs = append(errs, fmt.Errorf("event %s DTSTART: %w", eventID(event), err))
			}
		case "DTEND":
			if parsed, err := parseICalDateTime(prop.Value, prop.Params["TZID"]); err == nil {
				details.End = parsed.UTC()
			} else {
				errs = append(errs, fmt.Errorf("event %s DTEND: %w", eventID(event), err))
			}
		case "RRULE":
			details.RRULEs = append(details.RRULEs, prop.Value)
		}
	}
	details.Attendees = uniqueStrings(details.Attendees)
	if len(details.Alarms) == 0 {
		alerts, alertErrs := parseJSCalendarAlerts(event, details.Start, details.End, event.TimeZone)
		details.Alarms = append(details.Alarms, alerts...)
		errs = append(errs, alertErrs...)
	}
	return details, errs
}

func EventICalendar(event Event) string {
	if strings.TrimSpace(event.ICalendar) != "" {
		return event.ICalendar
	}
	if strings.TrimSpace(event.ICalendarLower) != "" {
		return event.ICalendarLower
	}
	for _, key := range []string{"iCalendar", "icalendar", "iCalendarData", "icalendarData", "chatinfra.com:iCalendar"} {
		if value, ok := event.Raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseJSCalendarAlerts(event Event, start, end time.Time, eventTZ string) ([]VALARM, []error) {
	rawAlerts, ok := rawObject(event.Raw["alerts"])
	if !ok || len(rawAlerts) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(rawAlerts))
	for key := range rawAlerts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	alarms := make([]VALARM, 0, len(keys))
	var errs []error
	for _, key := range keys {
		alarm, err := buildJSCalendarAlert(rawAlerts[key], start, end, eventTZ)
		if err != nil {
			errs = append(errs, fmt.Errorf("event %s alert %s ignored: %w", eventID(event), key, err))
			continue
		}
		alarms = append(alarms, alarm)
	}
	return alarms, errs
}

func buildJSCalendarAlert(raw any, start, end time.Time, eventTZ string) (VALARM, error) {
	obj, ok := rawObject(raw)
	if !ok {
		return VALARM{}, errorsf("alert is not an object")
	}
	trigger, ok := rawObject(obj["trigger"])
	if !ok {
		return VALARM{}, errorsf("missing trigger object")
	}
	action := strings.ToUpper(firstNonEmpty(rawString(obj["action"]), "DISPLAY"))
	alarm := VALARM{
		Action:      action,
		Description: rawString(obj["description"]),
	}
	switch strings.TrimSpace(rawString(trigger["@type"])) {
	case "AbsoluteTrigger":
		when := strings.TrimSpace(rawString(trigger["when"]))
		if when == "" {
			return VALARM{}, errorsf("AbsoluteTrigger missing when")
		}
		parsed, err := ParseTime(when, time.UTC)
		if err != nil && eventTZ != "" {
			if loc, locErr := time.LoadLocation(eventTZ); locErr == nil {
				parsed, err = ParseTime(when, loc)
			}
		}
		if err != nil {
			return VALARM{}, err
		}
		parsed = parsed.UTC()
		alarm.Trigger = when
		alarm.Absolute = &parsed
	case "OffsetTrigger":
		offset := strings.TrimSpace(rawString(trigger["offset"]))
		if offset == "" {
			return VALARM{}, errorsf("OffsetTrigger missing offset")
		}
		duration, err := parseICalDuration(offset)
		if err != nil {
			return VALARM{}, err
		}
		related := strings.ToUpper(firstNonEmpty(rawString(trigger["relativeTo"]), "start"))
		switch related {
		case "START":
			related = "START"
		case "END":
			related = "END"
		default:
			return VALARM{}, errorsf("unsupported OffsetTrigger relativeTo=%s", related)
		}
		if related == "START" && start.IsZero() {
			return VALARM{}, errorsf("OffsetTrigger relativeTo=start but event has no start time")
		}
		if related == "END" && end.IsZero() {
			return VALARM{}, errorsf("OffsetTrigger relativeTo=end but event has no end time")
		}
		alarm.Trigger = offset
		alarm.Related = related
		alarm.Offset = &duration
	default:
		return VALARM{}, errorsf("unsupported trigger type %q", rawString(trigger["@type"]))
	}
	return alarm, nil
}

func rawObject(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	return obj, ok
}

func rawString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func buildVALARM(props []icalProperty, start, end time.Time, eventTZ string) (VALARM, error) {
	var alarm VALARM
	for _, prop := range props {
		switch prop.Name {
		case "ACTION":
			alarm.Action = strings.ToUpper(strings.TrimSpace(prop.Value))
		case "DESCRIPTION":
			alarm.Description = unescapeICalText(prop.Value)
		case "TRIGGER":
			alarm.Trigger = strings.TrimSpace(prop.Value)
			alarm.Related = strings.ToUpper(strings.TrimSpace(prop.Params["RELATED"]))
			if alarm.Related == "" {
				alarm.Related = "START"
			}
			if isAbsoluteTrigger(prop) {
				parsed, err := parseICalDateTime(prop.Value, prop.Params["TZID"])
				if err != nil && eventTZ != "" {
					parsed, err = parseICalDateTime(prop.Value, eventTZ)
				}
				if err != nil {
					return VALARM{}, err
				}
				parsed = parsed.UTC()
				alarm.Absolute = &parsed
			} else {
				duration, err := parseICalDuration(prop.Value)
				if err != nil {
					return VALARM{}, err
				}
				alarm.Offset = &duration
			}
		}
	}
	if alarm.Action == "" {
		alarm.Action = "DISPLAY"
	}
	if alarm.Trigger == "" {
		return VALARM{}, errorsf("missing TRIGGER")
	}
	if alarm.Absolute == nil && alarm.Offset == nil {
		return VALARM{}, errorsf("unparseable TRIGGER %q", alarm.Trigger)
	}
	if alarm.Related != "START" && alarm.Related != "END" {
		return VALARM{}, errorsf("unsupported TRIGGER RELATED=%s", alarm.Related)
	}
	if alarm.Offset != nil {
		if alarm.Related == "END" && end.IsZero() {
			return VALARM{}, errorsf("TRIGGER RELATED=END but event has no end time")
		}
		if alarm.Related == "START" && start.IsZero() {
			return VALARM{}, errorsf("TRIGGER RELATED=START but event has no start time")
		}
	}
	return alarm, nil
}

func alarmTriggerTime(alarm VALARM, start, end time.Time, occurrenceIndex int) (time.Time, bool) {
	if alarm.Absolute != nil {
		if occurrenceIndex != 0 {
			return time.Time{}, false
		}
		return *alarm.Absolute, true
	}
	if alarm.Offset == nil {
		return time.Time{}, false
	}
	reference := start
	if alarm.Related == "END" {
		reference = end
	}
	return reference.Add(*alarm.Offset), true
}

func isAbsoluteTrigger(prop icalProperty) bool {
	value := strings.TrimSpace(strings.ToUpper(prop.Value))
	if strings.EqualFold(prop.Params["VALUE"], "DATE-TIME") {
		return true
	}
	return !strings.HasPrefix(value, "P") && !strings.HasPrefix(value, "+P") && !strings.HasPrefix(value, "-P")
}

func parseICalendarProperties(data string) []icalProperty {
	lines := unfoldICalLines(data)
	props := make([]icalProperty, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		left, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		parts := strings.Split(left, ";")
		prop := icalProperty{Name: strings.ToUpper(strings.TrimSpace(parts[0])), Params: map[string]string{}, Value: value}
		for _, rawParam := range parts[1:] {
			key, val, ok := strings.Cut(rawParam, "=")
			if !ok {
				continue
			}
			prop.Params[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(val), "\"")
		}
		props = append(props, prop)
	}
	return props
}

func unfoldICalLines(data string) []string {
	rawLines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(out) > 0 {
				out[len(out)-1] += strings.TrimLeft(line, " \t")
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

func parseICalDateTime(value, tzid string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errorsf("empty date-time")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	loc := time.UTC
	if tzid != "" {
		if loaded, err := time.LoadLocation(tzid); err == nil {
			loc = loaded
		}
	}
	formatsUTC := []string{"20060102T150405Z", "20060102T1504Z", "20060102T15Z"}
	for _, format := range formatsUTC {
		if t, err := time.Parse(format, value); err == nil {
			return t, nil
		}
	}
	formatsLocal := []string{"20060102T150405", "20060102T1504", "20060102T15", "20060102"}
	for _, format := range formatsLocal {
		if t, err := time.ParseInLocation(format, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errorsf("invalid date-time %q", value)
}

func parseICalDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, errorsf("empty duration")
	}
	sign := 1.0
	if strings.HasPrefix(value, "-") {
		sign = -1
		value = strings.TrimPrefix(value, "-")
	} else if strings.HasPrefix(value, "+") {
		value = strings.TrimPrefix(value, "+")
	}
	if !strings.HasPrefix(value, "P") {
		return 0, errorsf("invalid duration %q", value)
	}
	value = strings.TrimPrefix(value, "P")
	var total time.Duration
	var number strings.Builder
	inTime := false
	for _, r := range value {
		if r >= '0' && r <= '9' || r == '.' {
			number.WriteRune(r)
			continue
		}
		if r == 'T' {
			inTime = true
			continue
		}
		if number.Len() == 0 {
			return 0, errorsf("invalid duration %q", value)
		}
		f, err := strconv.ParseFloat(number.String(), 64)
		if err != nil {
			return 0, err
		}
		switch r {
		case 'W':
			total += time.Duration(f * float64(7*24*time.Hour))
		case 'D':
			total += time.Duration(f * float64(24*time.Hour))
		case 'H':
			total += time.Duration(f * float64(time.Hour))
		case 'M':
			if !inTime {
				return 0, errorsf("month durations are not supported in %q", value)
			}
			total += time.Duration(f * float64(time.Minute))
		case 'S':
			total += time.Duration(f * float64(time.Second))
		default:
			return 0, errorsf("unsupported duration unit %q in %q", r, value)
		}
		number.Reset()
	}
	if number.Len() > 0 {
		return 0, errorsf("invalid duration %q", value)
	}
	return time.Duration(sign * float64(total)), nil
}

func expandEventStarts(start time.Time, rawRules []string, rules []RecurrenceRule, now, windowEnd time.Time) []time.Time {
	specs := make([]rruleSpec, 0, len(rawRules)+len(rules))
	for _, raw := range rawRules {
		if spec, ok := parseRRULE(raw); ok {
			specs = append(specs, spec)
		}
	}
	for _, rule := range rules {
		if strings.TrimSpace(rule.Frequency) == "" {
			continue
		}
		interval := rule.Interval
		if interval <= 0 {
			interval = 1
		}
		specs = append(specs, rruleSpec{Frequency: strings.ToUpper(rule.Frequency), Interval: interval})
	}
	if len(specs) == 0 {
		return []time.Time{start}
	}
	seen := map[string]bool{}
	var out []time.Time
	for _, spec := range specs {
		interval := spec.Interval
		if interval <= 0 {
			interval = 1
		}
		count := 0
		for occurrence := start; !occurrence.After(windowEnd.Add(24 * time.Hour)); occurrence = addFrequency(occurrence, spec.Frequency, interval) {
			count++
			if spec.Count > 0 && count > spec.Count {
				break
			}
			if !spec.Until.IsZero() && occurrence.After(spec.Until) {
				break
			}
			key := occurrence.UTC().Format(time.RFC3339Nano)
			if !seen[key] && occurrence.Add(24*time.Hour).After(now) {
				seen[key] = true
				out = append(out, occurrence)
			}
			if count > 1000 {
				break
			}
		}
	}
	if len(out) == 0 {
		return []time.Time{start}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

type rruleSpec struct {
	Frequency string
	Interval  int
	Count     int
	Until     time.Time
}

func parseRRULE(raw string) (rruleSpec, bool) {
	spec := rruleSpec{Interval: 1}
	for _, item := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(key)) {
		case "FREQ":
			spec.Frequency = strings.ToUpper(strings.TrimSpace(value))
		case "INTERVAL":
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
				spec.Interval = parsed
			}
		case "COUNT":
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
				spec.Count = parsed
			}
		case "UNTIL":
			if parsed, err := parseICalDateTime(value, ""); err == nil {
				spec.Until = parsed
			}
		}
	}
	return spec, spec.Frequency != ""
}

func addFrequency(t time.Time, freq string, interval int) time.Time {
	switch strings.ToUpper(freq) {
	case "DAILY":
		return t.AddDate(0, 0, interval)
	case "WEEKLY":
		return t.AddDate(0, 0, interval*7)
	case "MONTHLY":
		return t.AddDate(0, interval, 0)
	case "YEARLY":
		return t.AddDate(interval, 0, 0)
	default:
		return t.AddDate(0, 0, interval)
	}
}

func eventID(event Event) string {
	return firstNonEmpty(event.ID, event.UID)
}

func eventLocation(event Event) string {
	if strings.TrimSpace(event.Location) != "" {
		return strings.TrimSpace(event.Location)
	}
	for _, value := range event.Locations {
		if obj, ok := value.(map[string]any); ok {
			for _, key := range []string{"name", "description", "location"} {
				if text, ok := obj[key].(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func eventAttendees(event Event) []string {
	var out []string
	for _, value := range event.Participants {
		obj, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, _ := obj["name"].(string)
		email, _ := obj["email"].(string)
		if email == "" {
			email, _ = obj["sendTo"].(string)
		}
		text := strings.TrimSpace(email)
		if name != "" && email != "" {
			text = strings.TrimSpace(name) + " <" + strings.TrimSpace(email) + ">"
		} else if name != "" {
			text = strings.TrimSpace(name)
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return uniqueStrings(out)
}

func attendeeText(prop icalProperty) string {
	cn := strings.TrimSpace(prop.Params["CN"])
	value := strings.TrimSpace(strings.TrimPrefix(prop.Value, "mailto:"))
	if cn != "" && value != "" {
		return cn + " <" + value + ">"
	}
	if cn != "" {
		return cn
	}
	return value
}

func unescapeICalText(value string) string {
	value = strings.ReplaceAll(value, `\n`, "\n")
	value = strings.ReplaceAll(value, `\N`, "\n")
	value = strings.ReplaceAll(value, `\,`, ",")
	value = strings.ReplaceAll(value, `\;`, ";")
	value = strings.ReplaceAll(value, `\\`, `\`)
	return value
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptySlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func errorsf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
