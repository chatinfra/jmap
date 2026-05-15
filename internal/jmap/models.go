package jmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	CapabilityCore       = "urn:ietf:params:jmap:core"
	CapabilityCalendars  = "urn:ietf:params:jmap:calendars"
	CapabilityContacts   = "https://cyrusimap.org/ns/jmap/contacts"
	CapabilityMail       = "urn:ietf:params:jmap:mail"
	CapabilityPrincipals = "urn:ietf:params:jmap:principals"
)

var DefaultCapabilities = []string{
	CapabilityCore,
	CapabilityCalendars,
	CapabilityContacts,
	CapabilityMail,
	CapabilityPrincipals,
}

type Request struct {
	Using       []string     `json:"using"`
	MethodCalls []MethodCall `json:"methodCalls"`
}

type MethodCall struct {
	Name   string
	Params any
	ID     string
}

func (c MethodCall) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{c.Name, c.Params, c.ID})
}

func (c *MethodCall) UnmarshalJSON(data []byte) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	if len(arr) != 3 {
		return fmt.Errorf("jmap method call must contain 3 items, got %d", len(arr))
	}
	if err := json.Unmarshal(arr[0], &c.Name); err != nil {
		return err
	}
	c.Params = json.RawMessage(arr[1])
	if err := json.Unmarshal(arr[2], &c.ID); err != nil {
		return err
	}
	return nil
}

type Response struct {
	MethodResponses []MethodResponse `json:"methodResponses"`
	SessionState    string           `json:"sessionState,omitempty"`
}

type MethodResponse struct {
	Name   string
	Params json.RawMessage
	ID     string
}

func (r *MethodResponse) UnmarshalJSON(data []byte) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	if len(arr) != 3 {
		return fmt.Errorf("jmap method response must contain 3 items, got %d", len(arr))
	}
	if err := json.Unmarshal(arr[0], &r.Name); err != nil {
		return err
	}
	r.Params = append(r.Params[:0], arr[1]...)
	if err := json.Unmarshal(arr[2], &r.ID); err != nil {
		return err
	}
	return nil
}

func (r MethodResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{r.Name, json.RawMessage(r.Params), r.ID})
}

func (r MethodResponse) Decode(v any) error {
	if len(r.Params) == 0 {
		return errors.New("empty JMAP method response params")
	}
	return json.Unmarshal(r.Params, v)
}

func (r MethodResponse) ParamsMap() (map[string]json.RawMessage, error) {
	var params map[string]json.RawMessage
	if err := r.Decode(&params); err != nil {
		return nil, err
	}
	return params, nil
}

type MethodError struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Method      string `json:"method,omitempty"`
}

func (e MethodError) Error() string {
	parts := []string{"jmap method error"}
	if e.Method != "" {
		parts = append(parts, e.Method)
	}
	if e.Type != "" {
		parts = append(parts, e.Type)
	}
	if e.Description != "" {
		parts = append(parts, e.Description)
	}
	return strings.Join(parts, ": ")
}

type Calendar struct {
	ID                    string         `json:"id,omitempty"`
	Name                  string         `json:"name,omitempty"`
	Description           string         `json:"description,omitempty"`
	SortOrder             int            `json:"sortOrder,omitempty"`
	IsVisible             bool           `json:"isVisible,omitempty"`
	IsSubscribed          bool           `json:"isSubscribed,omitempty"`
	IncludeInAvailability bool           `json:"includeInAvailability,omitempty"`
	TimeZone              string         `json:"timeZone,omitempty"`
	MyRights              map[string]any `json:"myRights,omitempty"`
	ShareWith             map[string]any `json:"shareWith,omitempty"`
}

type Event struct {
	Type                   string           `json:"@type,omitempty"`
	ID                     string           `json:"id,omitempty"`
	UID                    string           `json:"uid,omitempty"`
	Title                  string           `json:"title,omitempty"`
	Description            string           `json:"description,omitempty"`
	DescriptionContentType string           `json:"descriptionContentType,omitempty"`
	CalendarIDs            map[string]bool  `json:"calendarIds,omitempty"`
	UTCStart               string           `json:"utcStart,omitempty"`
	Start                  string           `json:"start,omitempty"`
	TimeZone               string           `json:"timeZone,omitempty"`
	Duration               string           `json:"duration,omitempty"`
	Created                string           `json:"created,omitempty"`
	Updated                string           `json:"updated,omitempty"`
	FreeBusyStatus         string           `json:"freeBusyStatus,omitempty"`
	UseDefaultAlerts       bool             `json:"useDefaultAlerts,omitempty"`
	RecurrenceRules        []RecurrenceRule `json:"recurrenceRules,omitempty"`
	RecurrenceOverrides    map[string]any   `json:"recurrenceOverrides,omitempty"`
	Raw                    map[string]any   `json:"-"`
}

func (e *Event) UnmarshalJSON(data []byte) error {
	type alias Event
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	*e = Event(a)
	e.Raw = raw
	return nil
}

type RecurrenceRule struct {
	Type         string `json:"@type,omitempty"`
	Frequency    string `json:"frequency,omitempty"`
	Interval     int    `json:"interval,omitempty"`
	RScale       string `json:"rscale,omitempty"`
	Skip         string `json:"skip,omitempty"`
	FirstDayWeek string `json:"firstDayOfWeek,omitempty"`
}

type Contact struct {
	ID            string    `json:"id,omitempty"`
	UID           string    `json:"uid,omitempty"`
	AddressBookID string    `json:"addressbookId,omitempty"`
	FirstName     string    `json:"firstName,omitempty"`
	LastName      string    `json:"lastName,omitempty"`
	Company       string    `json:"company,omitempty"`
	Addresses     []Address `json:"addresses,omitempty"`
	Emails        []Email   `json:"emails,omitempty"`
	Phones        []Phone   `json:"phones,omitempty"`
}

func (c Contact) DisplayName() string {
	return strings.TrimSpace(strings.Join([]string{c.FirstName, c.LastName}, " "))
}

type Address struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

type Email struct {
	Type      string `json:"type,omitempty"`
	Value     string `json:"value,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

type Phone struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

type AddressBook struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	MyRights    map[string]any `json:"myRights,omitempty"`
}

type Mailbox struct {
	ID            string         `json:"id,omitempty"`
	Name          string         `json:"name,omitempty"`
	ParentID      string         `json:"parentId,omitempty"`
	Role          string         `json:"role,omitempty"`
	TotalEmails   int            `json:"totalEmails,omitempty"`
	UnreadEmails  int            `json:"unreadEmails,omitempty"`
	TotalThreads  int            `json:"totalThreads,omitempty"`
	UnreadThreads int            `json:"unreadThreads,omitempty"`
	SortOrder     int            `json:"sortOrder,omitempty"`
	IsSubscribed  bool           `json:"isSubscribed,omitempty"`
	MyRights      map[string]any `json:"myRights,omitempty"`
}

type Message struct {
	ID            string            `json:"id,omitempty"`
	BlobID        string            `json:"blobId,omitempty"`
	ThreadID      string            `json:"threadId,omitempty"`
	MailboxIDs    map[string]bool   `json:"mailboxIds,omitempty"`
	Keywords      map[string]string `json:"keywords,omitempty"`
	Size          int               `json:"size,omitempty"`
	ReceivedAt    string            `json:"receivedAt,omitempty"`
	References    []string          `json:"references,omitempty"`
	Sender        []string          `json:"sender,omitempty"`
	ReplyTo       []string          `json:"replyTo,omitempty"`
	MessageID     []string          `json:"messageId,omitempty"`
	InReplyTo     []string          `json:"inReplyTo,omitempty"`
	From          []string          `json:"from,omitempty"`
	To            []string          `json:"to,omitempty"`
	CC            []string          `json:"cc,omitempty"`
	BCC           []string          `json:"bcc,omitempty"`
	Subject       string            `json:"subject,omitempty"`
	SentAt        string            `json:"sentAt,omitempty"`
	BodyValues    map[string]any    `json:"bodyValues,omitempty"`
	TextBody      []Body            `json:"textBody,omitempty"`
	HTMLBody      []Body            `json:"htmlBody,omitempty"`
	Attachments   []Body            `json:"attachments,omitempty"`
	HasAttachment bool              `json:"hasAttachment,omitempty"`
}

type Body struct {
	PartID      string `json:"partId,omitempty"`
	BlobID      string `json:"blobId,omitempty"`
	Size        int    `json:"size,omitempty"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Charset     string `json:"charset,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	CID         string `json:"cid,omitempty"`
	Language    string `json:"language,omitempty"`
	Location    string `json:"location,omitempty"`
}

type PrincipalQuery struct {
	QueryState          string   `json:"queryState,omitempty"`
	CanCalculateChanges bool     `json:"canCalculateChanges,omitempty"`
	Position            int      `json:"position,omitempty"`
	Total               int      `json:"total,omitempty"`
	IDs                 []string `json:"ids,omitempty"`
	AccountID           string   `json:"accountId,omitempty"`
}

type Participant struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type EventQueryResponse struct {
	Filter              map[string]any `json:"filter,omitempty"`
	QueryState          string         `json:"queryState,omitempty"`
	CanCalculateChanges bool           `json:"canCalculateChanges,omitempty"`
	Position            int            `json:"position,omitempty"`
	Total               int            `json:"total,omitempty"`
	IDs                 []string       `json:"ids,omitempty"`
}

type BusyPeriod struct {
	UTCStart   string    `json:"utcStart,omitempty"`
	UTCEnd     string    `json:"utcEnd,omitempty"`
	BusyStatus string    `json:"busyStatus,omitempty"`
	Event      BusyEvent `json:"event,omitempty"`
}

type BusyEvent struct {
	Type           string `json:"@type,omitempty"`
	ID             string `json:"id,omitempty"`
	UID            string `json:"uid,omitempty"`
	Title          string `json:"title,omitempty"`
	UTCStart       string `json:"utcStart,omitempty"`
	Start          string `json:"start,omitempty"`
	TimeZone       string `json:"timeZone,omitempty"`
	Duration       string `json:"duration,omitempty"`
	FreeBusyStatus string `json:"freeBusyStatus,omitempty"`
}

func DecodeList[T any](response MethodResponse) ([]T, error) {
	params, err := response.ParamsMap()
	if err != nil {
		return nil, err
	}
	raw, ok := params["list"]
	if !ok || string(raw) == "null" {
		return []T{}, nil
	}
	var list []T
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func DecodeIDs(response MethodResponse) ([]string, error) {
	params, err := response.ParamsMap()
	if err != nil {
		return nil, err
	}
	raw, ok := params["ids"]
	if !ok || string(raw) == "null" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func DecodeRawList(response MethodResponse) ([]json.RawMessage, error) {
	params, err := response.ParamsMap()
	if err != nil {
		return nil, err
	}
	raw, ok := params["list"]
	if !ok || string(raw) == "null" {
		return []json.RawMessage{}, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func ExtractCreatedID(response MethodResponse, createID string) (string, error) {
	params, err := response.ParamsMap()
	if err != nil {
		return "", err
	}
	if raw := params["notCreated"]; len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" {
		return "", fmt.Errorf("jmap create failed: %s", raw)
	}
	raw, ok := params["created"]
	if !ok || string(raw) == "null" {
		return "", fmt.Errorf("jmap response missing created id for %s", createID)
	}
	var created map[string]struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", err
	}
	if item, ok := created[createID]; ok && item.ID != "" {
		return item.ID, nil
	}
	if len(created) == 1 {
		for _, item := range created {
			if item.ID != "" {
				return item.ID, nil
			}
		}
	}
	return "", fmt.Errorf("jmap response missing created id for %s", createID)
}

func UniqueCapabilities(capabilities []string) []string {
	if len(capabilities) == 0 {
		capabilities = DefaultCapabilities
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(capabilities)+1)
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	add(CapabilityCore)
	for _, capability := range capabilities {
		add(capability)
	}
	return out
}

func ParseTime(value string, loc *time.Location) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	formats := []string{"2006-01-02T15:04:05", "2006-01-02T15:04"}
	for _, format := range formats {
		if t, err := time.ParseInLocation(format, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q", value)
}

func ParseDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, errors.New("empty duration")
	}
	if d, err := time.ParseDuration(value); err == nil {
		return d, nil
	}
	if !strings.HasPrefix(value, "PT") {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	var total time.Duration
	var number strings.Builder
	for _, r := range strings.TrimPrefix(value, "PT") {
		if r >= '0' && r <= '9' || r == '.' {
			number.WriteRune(r)
			continue
		}
		if number.Len() == 0 {
			return 0, fmt.Errorf("invalid ISO duration %q", value)
		}
		f, err := strconv.ParseFloat(number.String(), 64)
		if err != nil {
			return 0, err
		}
		switch r {
		case 'H':
			total += time.Duration(f * float64(time.Hour))
		case 'M':
			total += time.Duration(f * float64(time.Minute))
		case 'S':
			total += time.Duration(f * float64(time.Second))
		default:
			return 0, fmt.Errorf("unsupported ISO duration unit %q in %q", r, value)
		}
		number.Reset()
	}
	if number.Len() > 0 {
		return 0, fmt.Errorf("invalid ISO duration %q", value)
	}
	return total, nil
}

func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d == 0 {
		return "PT0S"
	}
	remaining := d.Round(time.Second)
	h := remaining / time.Hour
	remaining -= h * time.Hour
	m := remaining / time.Minute
	remaining -= m * time.Minute
	s := remaining / time.Second
	var b strings.Builder
	b.WriteString("PT")
	if h > 0 {
		fmt.Fprintf(&b, "%dH", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dM", m)
	}
	if s > 0 || b.Len() == 2 {
		fmt.Fprintf(&b, "%dS", s)
	}
	return b.String()
}

func FormatJMAPTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func (e Event) StartTime() (time.Time, error) {
	loc := time.UTC
	if e.TimeZone != "" {
		if loaded, err := time.LoadLocation(e.TimeZone); err == nil {
			loc = loaded
		}
	}
	if e.UTCStart != "" {
		return ParseTime(e.UTCStart, time.UTC)
	}
	return ParseTime(e.Start, loc)
}

func (e Event) EndTime() (time.Time, error) {
	start, err := e.StartTime()
	if err != nil {
		return time.Time{}, err
	}
	d, err := ParseDuration(e.Duration)
	if err != nil {
		return time.Time{}, err
	}
	return start.Add(d), nil
}

func (e Event) ActiveCalendarIDs() []string {
	ids := make([]string, 0, len(e.CalendarIDs))
	for id, active := range e.CalendarIDs {
		if active {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (p BusyPeriod) StartTime() (time.Time, error) {
	if p.UTCStart != "" {
		return ParseTime(p.UTCStart, time.UTC)
	}
	return ParseTime(p.Event.UTCStart, time.UTC)
}

func (p BusyPeriod) EndTime() (time.Time, error) {
	if p.UTCEnd != "" {
		return ParseTime(p.UTCEnd, time.UTC)
	}
	start, err := p.StartTime()
	if err != nil {
		return time.Time{}, err
	}
	d, err := ParseDuration(p.Event.Duration)
	if err != nil {
		return time.Time{}, err
	}
	return start.Add(d), nil
}

func (p BusyPeriod) EventKey() string {
	if p.Event.UID != "" {
		return p.Event.UID
	}
	return p.Event.ID
}
