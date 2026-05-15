package jmap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Provider struct {
	Client    *Client
	AccountID string
}

func NewProvider(client *Client) Provider {
	return Provider{Client: client, AccountID: client.Username()}
}

func (p Provider) Check(ctx context.Context) (map[string]any, error) {
	resp, err := p.Client.Call(ctx, "Calendar/get", map[string]any{"ids": nil}, CapabilityCalendars)
	if err != nil {
		return nil, err
	}
	params, err := resp.ParamsMap()
	if err != nil {
		return nil, err
	}
	return map[string]any{"connected": true, "accountId": p.AccountID, "response": params}, nil
}

func (p Provider) RawCall(ctx context.Context, method string, params json.RawMessage, capabilities []string) (MethodResponse, error) {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if !json.Valid(params) {
		return MethodResponse{}, errors.New("raw params must be valid JSON")
	}
	return p.Client.Call(ctx, method, params, capabilities...)
}

func (p Provider) Calendars(ctx context.Context) ([]Calendar, error) {
	resp, err := p.Client.Call(ctx, "Calendar/get", map[string]any{"ids": nil}, CapabilityCalendars)
	if err != nil {
		return nil, err
	}
	return DecodeList[Calendar](resp)
}

func (p Provider) CreateCalendar(ctx context.Context, name string) (Calendar, error) {
	if strings.Contains(name, "_") {
		return Calendar{}, fmt.Errorf("underscore is not allowed in calendar name %s", name)
	}
	createID := newCreateID()
	resp, err := p.Client.Call(ctx, "Calendar/set", map[string]any{
		"accountId": p.AccountID,
		"create":    map[string]any{createID: map[string]any{"name": name}},
	}, CapabilityCalendars)
	if err != nil {
		return Calendar{}, err
	}
	id, err := ExtractCreatedID(resp, createID)
	if err != nil {
		return Calendar{}, err
	}
	return Calendar{ID: id, Name: name}, nil
}

func (p Provider) GetCalendarByName(ctx context.Context, name string) (Calendar, bool, error) {
	calendars, err := p.Calendars(ctx)
	if err != nil {
		return Calendar{}, false, err
	}
	for _, calendar := range calendars {
		if calendar.Name == name {
			return calendar, true, nil
		}
	}
	return Calendar{}, false, nil
}

func (p Provider) GetOrCreateCalendar(ctx context.Context, name string) (Calendar, bool, error) {
	calendar, ok, err := p.GetCalendarByName(ctx, name)
	if err != nil || ok {
		return calendar, false, err
	}
	created, err := p.CreateCalendar(ctx, name)
	return created, true, err
}

func (p Provider) DeleteCalendar(ctx context.Context, id string) error {
	_, err := p.Client.Call(ctx, "Calendar/set", map[string]any{"accountId": p.AccountID, "destroy": []string{id}}, CapabilityCalendars)
	return err
}

func (p Provider) EventsRaw(ctx context.Context, calendarIDs []string) ([]json.RawMessage, error) {
	resp, err := p.Client.Call(ctx, "CalendarEvent/get", map[string]any{"accountId": p.AccountID}, CapabilityCalendars)
	if err != nil {
		return nil, err
	}
	list, err := DecodeRawList(resp)
	if err != nil || len(calendarIDs) == 0 {
		return list, err
	}
	allowed := map[string]bool{}
	for _, id := range calendarIDs {
		allowed[id] = true
	}
	filtered := make([]json.RawMessage, 0, len(list))
	for _, raw := range list {
		var obj struct {
			CalendarIDs map[string]bool `json:"calendarIds"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		for id, active := range obj.CalendarIDs {
			if active && allowed[id] {
				filtered = append(filtered, raw)
				break
			}
		}
	}
	return filtered, nil
}

func (p Provider) Events(ctx context.Context, calendarIDs []string) ([]Event, error) {
	raw, err := p.EventsRaw(ctx, calendarIDs)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(raw))
	for _, item := range raw {
		var event Event
		if err := json.Unmarshal(item, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (p Provider) GetEventRaw(ctx context.Context, id string) (json.RawMessage, error) {
	resp, err := p.Client.Call(ctx, "CalendarEvent/get", map[string]any{"accountId": p.AccountID, "ids": []string{id}}, CapabilityCalendars)
	if err != nil {
		return nil, err
	}
	list, err := DecodeRawList(resp)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no event id %s", id)
	}
	return list[0], nil
}

func (p Provider) GetEvent(ctx context.Context, id string) (Event, error) {
	raw, err := p.GetEventRaw(ctx, id)
	if err != nil {
		return Event{}, err
	}
	var event Event
	return event, json.Unmarshal(raw, &event)
}

func (p Provider) QueryEvents(ctx context.Context, after, before *time.Time, calendarIDs []string) (EventQueryResponse, error) {
	filter := map[string]any{}
	if after != nil {
		filter["after"] = strings.TrimSuffix(FormatJMAPTime(*after), "Z")
	}
	if before != nil {
		filter["before"] = strings.TrimSuffix(FormatJMAPTime(*before), "Z")
	}
	if len(calendarIDs) > 0 {
		filter["inCalendars"] = calendarIDs
	}
	resp, err := p.Client.Call(ctx, "CalendarEvent/query", map[string]any{
		"accountId": p.AccountID,
		"filter":    filter,
		"timeZone":  "Etc/UTC",
	}, CapabilityCalendars)
	if err != nil {
		return EventQueryResponse{}, err
	}
	var out EventQueryResponse
	return out, resp.Decode(&out)
}

func (p Provider) CreateEvent(ctx context.Context, title string, start time.Time, duration time.Duration, description string, calendarIDs []string, recurrence string) (string, error) {
	createID := newCreateID()
	if len(calendarIDs) == 0 {
		calendarIDs = []string{"Default"}
	}
	calMap := map[string]bool{}
	for _, id := range calendarIDs {
		calMap[id] = true
	}
	event := map[string]any{
		"title":       title,
		"calendarIds": calMap,
		"utcStart":    FormatJMAPTime(start),
		"duration":    FormatDuration(duration),
		"description": description,
		"created":     FormatJMAPTime(time.Now()),
	}
	if recurrence != "" {
		event["recurrenceRules"] = []map[string]any{{"@type": "RecurrenceRule", "frequency": recurrence}}
	}
	resp, err := p.Client.Call(ctx, "CalendarEvent/set", map[string]any{
		"accountId": p.AccountID,
		"create":    map[string]any{createID: event},
	}, CapabilityCalendars)
	if err != nil {
		return "", err
	}
	return ExtractCreatedID(resp, createID)
}

func (p Provider) CreateEventRaw(ctx context.Context, raw json.RawMessage, calendarIDs []string) (string, error) {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return "", err
	}
	delete(event, "id")
	delete(event, "uid")
	if len(calendarIDs) > 0 {
		calMap := map[string]bool{}
		for _, id := range calendarIDs {
			calMap[id] = true
		}
		event["calendarIds"] = calMap
	}
	createID := newCreateID()
	resp, err := p.Client.Call(ctx, "CalendarEvent/set", map[string]any{
		"accountId": p.AccountID,
		"create":    map[string]any{createID: event},
	}, CapabilityCalendars)
	if err != nil {
		return "", err
	}
	return ExtractCreatedID(resp, createID)
}

func (p Provider) UpdateEventRaw(ctx context.Context, id string, raw json.RawMessage) error {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	delete(event, "id")
	_, err := p.Client.Call(ctx, "CalendarEvent/set", map[string]any{"accountId": p.AccountID, "update": map[string]any{id: event}}, CapabilityCalendars)
	return err
}

func (p Provider) DeleteEvent(ctx context.Context, id string) error {
	_, err := p.Client.Call(ctx, "CalendarEvent/set", map[string]any{"accountId": p.AccountID, "destroy": []string{id}}, CapabilityCalendars)
	return err
}

func (p Provider) Availability(ctx context.Context, start, end time.Time) ([]BusyPeriod, error) {
	resp, err := p.Client.Call(ctx, "Principal/getAvailability", map[string]any{
		"id":          p.AccountID,
		"utcStart":    FormatJMAPTime(start),
		"utcEnd":      FormatJMAPTime(end),
		"showDetails": true,
	}, CapabilityCalendars, CapabilityPrincipals)
	if err != nil {
		return nil, err
	}
	return DecodeList[BusyPeriod](resp)
}

func (p Provider) Principals(ctx context.Context) (PrincipalQuery, error) {
	resp, err := p.Client.Call(ctx, "Principal/query", map[string]any{"accountId": p.AccountID}, CapabilityPrincipals)
	if err != nil {
		return PrincipalQuery{}, err
	}
	var out PrincipalQuery
	return out, resp.Decode(&out)
}

func (p Provider) Participants(ctx context.Context) ([]Participant, error) {
	resp, err := p.Client.Call(ctx, "ParticipantIdentity/get", map[string]any{"ids": nil}, CapabilityPrincipals)
	if err != nil {
		return nil, err
	}
	return DecodeList[Participant](resp)
}

func (p Provider) CreateParticipant(ctx context.Context, name, scheduleID string, sendTo map[string]string) (string, error) {
	createID := newCreateID()
	obj := map[string]any{"name": name}
	if scheduleID != "" {
		obj["scheduleId"] = scheduleID
	}
	if len(sendTo) > 0 {
		obj["sendTo"] = sendTo
	}
	resp, err := p.Client.Call(ctx, "ParticipantIdentity/set", map[string]any{"create": map[string]any{createID: obj}}, CapabilityPrincipals)
	if err != nil {
		return "", err
	}
	return ExtractCreatedID(resp, createID)
}

func (p Provider) AddressBooks(ctx context.Context) ([]AddressBook, error) {
	resp, err := p.Client.Call(ctx, "AddressBook/get", map[string]any{}, CapabilityContacts)
	if err != nil {
		return nil, err
	}
	return DecodeList[AddressBook](resp)
}

func (p Provider) ContactsRaw(ctx context.Context) ([]json.RawMessage, error) {
	resp, err := p.Client.Call(ctx, "Contact/get", map[string]any{"accountId": p.AccountID}, CapabilityContacts)
	if err != nil {
		return nil, err
	}
	return DecodeRawList(resp)
}

func (p Provider) Contacts(ctx context.Context) ([]Contact, error) {
	resp, err := p.Client.Call(ctx, "Contact/get", map[string]any{"accountId": p.AccountID}, CapabilityContacts)
	if err != nil {
		return nil, err
	}
	return DecodeList[Contact](resp)
}

func (p Provider) GetContactRaw(ctx context.Context, id string) (json.RawMessage, error) {
	resp, err := p.Client.Call(ctx, "Contact/get", map[string]any{"accountId": p.AccountID, "ids": []string{id}}, CapabilityContacts)
	if err != nil {
		return nil, err
	}
	list, err := DecodeRawList(resp)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no contact id %s", id)
	}
	return list[0], nil
}

func (p Provider) GetContact(ctx context.Context, id string) (Contact, error) {
	raw, err := p.GetContactRaw(ctx, id)
	if err != nil {
		return Contact{}, err
	}
	var contact Contact
	return contact, json.Unmarshal(raw, &contact)
}

func (p Provider) CreateContact(ctx context.Context, contact Contact) (string, error) {
	createID := newCreateID()
	payload := map[string]any{
		"firstName": contact.FirstName,
		"addresses": contact.Addresses,
		"emails":    contact.Emails,
		"phones":    contact.Phones,
	}
	if contact.LastName != "" {
		payload["lastName"] = contact.LastName
	}
	if contact.Company != "" {
		payload["company"] = contact.Company
	}
	resp, err := p.Client.Call(ctx, "Contact/set", map[string]any{"accountId": p.AccountID, "create": map[string]any{createID: payload}}, CapabilityContacts)
	if err != nil {
		return "", err
	}
	return ExtractCreatedID(resp, createID)
}

func (p Provider) CreateContactRaw(ctx context.Context, raw json.RawMessage) (string, error) {
	var contact map[string]any
	if err := json.Unmarshal(raw, &contact); err != nil {
		return "", err
	}
	for _, key := range []string{"id", "x-hasPhoto", "x-href", "blobId", "size"} {
		delete(contact, key)
	}
	createID := newCreateID()
	resp, err := p.Client.Call(ctx, "Contact/set", map[string]any{"accountId": p.AccountID, "create": map[string]any{createID: contact}}, CapabilityContacts)
	if err != nil {
		return "", err
	}
	return ExtractCreatedID(resp, createID)
}

func (p Provider) UpdateContact(ctx context.Context, contact Contact) error {
	if contact.ID == "" {
		return errors.New("contact id is required")
	}
	update := map[string]any{
		"firstName": contact.FirstName,
		"lastName":  contact.LastName,
		"company":   contact.Company,
		"addresses": contact.Addresses,
		"emails":    contact.Emails,
		"phones":    contact.Phones,
	}
	_, err := p.Client.Call(ctx, "Contact/set", map[string]any{"accountId": p.AccountID, "update": map[string]any{contact.ID: update}}, CapabilityContacts)
	return err
}

func (p Provider) UpdateContactRaw(ctx context.Context, id string, raw json.RawMessage) error {
	var contact map[string]any
	if err := json.Unmarshal(raw, &contact); err != nil {
		return err
	}
	delete(contact, "id")
	_, err := p.Client.Call(ctx, "Contact/set", map[string]any{"accountId": p.AccountID, "update": map[string]any{id: contact}}, CapabilityContacts)
	return err
}

func (p Provider) DeleteContact(ctx context.Context, id string) error {
	_, err := p.Client.Call(ctx, "Contact/set", map[string]any{"accountId": p.AccountID, "destroy": []string{id}}, CapabilityContacts)
	return err
}

type ContactSearchResult struct {
	Status   string    `json:"status"`
	Contacts []Contact `json:"contacts,omitempty"`
	Contact  *Contact  `json:"contact,omitempty"`
}

func (p Provider) SearchContacts(ctx context.Context, search string) (ContactSearchResult, error) {
	contacts, err := p.Contacts(ctx)
	if err != nil {
		return ContactSearchResult{}, err
	}
	matches := matchContacts(contacts, search)
	if len(matches) == 1 {
		return ContactSearchResult{Status: "exact", Contact: &matches[0], Contacts: matches}, nil
	}
	if len(matches) > 1 {
		return ContactSearchResult{Status: "multiple", Contacts: matches}, nil
	}
	return ContactSearchResult{Status: "none"}, nil
}

func (p Provider) GetOrCreateContactByPhone(ctx context.Context, phone string, fallback Contact) (Contact, bool, error) {
	contacts, err := p.Contacts(ctx)
	if err != nil {
		return Contact{}, false, err
	}
	normalized := normalizePhone(phone)
	for _, contact := range contacts {
		for _, candidate := range contact.Phones {
			if normalizePhone(candidate.Value) == normalized {
				return contact, false, nil
			}
		}
	}
	if fallback.FirstName == "" {
		fallback.FirstName = phone
	}
	if len(fallback.Phones) == 0 {
		fallback.Phones = []Phone{{Type: "home", Value: phone}}
	}
	id, err := p.CreateContact(ctx, fallback)
	if err != nil {
		return Contact{}, false, err
	}
	fallback.ID = id
	return fallback, true, nil
}

func (p Provider) GetOrCreateContactByEmail(ctx context.Context, email string, fallback Contact) (Contact, bool, error) {
	contacts, err := p.Contacts(ctx)
	if err != nil {
		return Contact{}, false, err
	}
	for _, contact := range contacts {
		for _, candidate := range contact.Emails {
			if strings.EqualFold(candidate.Value, email) {
				return contact, false, nil
			}
		}
	}
	if fallback.FirstName == "" {
		fallback.FirstName = email
	}
	if len(fallback.Emails) == 0 {
		fallback.Emails = []Email{{Type: "personal", Value: email}}
	}
	id, err := p.CreateContact(ctx, fallback)
	if err != nil {
		return Contact{}, false, err
	}
	fallback.ID = id
	return fallback, true, nil
}

func (p Provider) GetOrCreateContactByName(ctx context.Context, name string, fallback Contact) (Contact, bool, error) {
	contacts, err := p.Contacts(ctx)
	if err != nil {
		return Contact{}, false, err
	}
	for _, contact := range contacts {
		if strings.EqualFold(strings.TrimSpace(contact.DisplayName()), strings.TrimSpace(name)) {
			return contact, false, nil
		}
	}
	if fallback.FirstName == "" && fallback.LastName == "" {
		parts := strings.Fields(name)
		if len(parts) == 0 {
			fallback.FirstName = name
		} else {
			fallback.FirstName = parts[0]
			if len(parts) > 1 {
				fallback.LastName = strings.Join(parts[1:], " ")
			}
		}
	}
	id, err := p.CreateContact(ctx, fallback)
	if err != nil {
		return Contact{}, false, err
	}
	fallback.ID = id
	return fallback, true, nil
}

func (p Provider) Mailboxes(ctx context.Context) ([]Mailbox, error) {
	resp, err := p.Client.Call(ctx, "Mailbox/get", map[string]any{"accountId": p.AccountID}, CapabilityMail)
	if err != nil {
		return nil, err
	}
	return DecodeList[Mailbox](resp)
}

func (p Provider) QueryMessageIDs(ctx context.Context, mailboxID string) ([]string, error) {
	params := map[string]any{"accountId": p.AccountID}
	if mailboxID != "" {
		params["filter"] = map[string]any{"inMailbox": mailboxID}
	}
	resp, err := p.Client.Call(ctx, "Email/query", params, CapabilityMail)
	if err != nil {
		return nil, err
	}
	return DecodeIDs(resp)
}

func (p Provider) GetMessage(ctx context.Context, id string) (Message, error) {
	resp, err := p.Client.Call(ctx, "Email/get", map[string]any{"accountId": p.AccountID, "ids": []string{id}}, CapabilityMail)
	if err != nil {
		return Message{}, err
	}
	list, err := DecodeList[Message](resp)
	if err != nil {
		return Message{}, err
	}
	if len(list) == 0 {
		return Message{}, fmt.Errorf("no message id %s", id)
	}
	return list[0], nil
}

func (p Provider) CreateMessage(ctx context.Context, mailboxID, from, subject, messageID, body string) (string, error) {
	if messageID == "" {
		messageID = newCreateID()
	}
	if body == "" {
		body = "\n"
	}
	create := map[string]any{
		"messageId":  []string{messageID},
		"subject":    subject,
		"mailboxIds": map[string]bool{mailboxID: true},
		"bodyStructure": map[string]any{
			"type":                    "text/plain",
			"partId":                  "bd48",
			"header:Content-Language": "en",
			"header:From":             from,
		},
		"bodyValues": map[string]any{"bd48": map[string]any{"value": body, "isTruncated": false}},
	}
	resp, err := p.Client.Call(ctx, "Email/set", map[string]any{"accountId": p.AccountID, "create": map[string]any{messageID: create}}, CapabilityMail)
	if err != nil {
		return "", err
	}
	return ExtractCreatedID(resp, messageID)
}

func (p Provider) DeleteMessage(ctx context.Context, id string) error {
	_, err := p.Client.Call(ctx, "Email/set", map[string]any{"accountId": p.AccountID, "destroy": []string{id}}, CapabilityMail)
	return err
}

func matchContacts(contacts []Contact, search string) []Contact {
	search = strings.TrimSpace(search)
	if search == "" {
		return nil
	}
	rules := []func(Contact) bool{
		func(c Contact) bool { return strings.EqualFold(strings.TrimSpace(c.DisplayName()), search) },
		func(c Contact) bool { return strings.EqualFold(strings.TrimSpace(c.LastName), search) },
		func(c Contact) bool { return strings.EqualFold(strings.TrimSpace(c.FirstName), search) },
		func(c Contact) bool { return c.DisplayName() != "" && soundex(c.DisplayName()) == soundex(search) },
		func(c Contact) bool { return c.LastName != "" && soundex(c.LastName) == soundex(search) },
		func(c Contact) bool { return c.FirstName != "" && soundex(c.FirstName) == soundex(search) },
		func(c Contact) bool {
			for _, phone := range c.Phones {
				if strings.Contains(normalizePhone(phone.Value), normalizePhone(search)) {
					return true
				}
			}
			return false
		},
	}
	for _, rule := range rules {
		var matches []Contact
		for _, contact := range contacts {
			if rule(contact) {
				matches = append(matches, contact)
			}
		}
		if len(matches) > 0 {
			return matches
		}
	}
	return nil
}

func normalizePhone(value string) string {
	var digits strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	out := digits.String()
	if len(out) == 10 {
		out = "1" + out
	}
	return out
}

func soundex(value string) string {
	value = strings.ToUpper(value)
	letters := make([]rune, 0, len(value))
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			letters = append(letters, r)
		}
	}
	if len(letters) == 0 {
		return ""
	}
	codes := map[rune]byte{}
	for _, r := range "BFPV" {
		codes[r] = '1'
	}
	for _, r := range "CGJKQSXZ" {
		codes[r] = '2'
	}
	for _, r := range "DT" {
		codes[r] = '3'
	}
	codes['L'] = '4'
	for _, r := range "MN" {
		codes[r] = '5'
	}
	codes['R'] = '6'
	out := []byte{byte(letters[0])}
	last := codes[letters[0]]
	for _, r := range letters[1:] {
		code := codes[r]
		if code == 0 {
			last = 0
			continue
		}
		if code != last {
			out = append(out, code)
		}
		last = code
		if len(out) == 4 {
			break
		}
	}
	for len(out) < 4 {
		out = append(out, '0')
	}
	return string(out)
}

func newCreateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("id%d", time.Now().UnixNano())
}

func SortCalendars(calendars []Calendar) {
	sort.Slice(calendars, func(i, j int) bool { return calendars[i].Name < calendars[j].Name })
}
