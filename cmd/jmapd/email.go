package main

import (
	"context"
	"fmt"
	"log"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

// mailSessionPrefix namespaces per-mailbox opencode sessions in the shared
// sessions map so they never collide with per-calendar session keys.
const mailSessionPrefix = "mail:"

// maxEmailBodyExcerpt bounds how much message body text is placed in a prompt.
const maxEmailBodyExcerpt = 2000

// mailSource is the subset of jmap.Provider the email listener needs to realize
// automatic agent mail handling. jmap.Provider satisfies it directly.
type mailSource interface {
	Mailboxes(ctx context.Context) ([]jmap.Mailbox, error)
	QueryMessageIDs(ctx context.Context, mailboxID string) ([]string, error)
	GetMessage(ctx context.Context, id string) (jmap.Message, error)
}

// emailListener realizes automatic agent mail handling: it watches the bound
// JMAP account's inbox, detects newly-arrived messages between refreshes, and
// submits each one to the opencode agent through the bridge's shared prompt
// submitter so the agent can handle mail automatically. It reuses the
// provider's Mailboxes/QueryMessageIDs/GetMessage operations that were
// previously reachable only from the operator CLI.
//
// On a first-ever start it baselines the existing inbox as already-seen so it
// does not retroactively dump pre-existing mail to the agent (mirroring the way
// calendar past-due VALARMs are not retroactively fired). It never writes back
// to JMAP; sender-to-contact resolution is read-only.
type emailListener struct {
	source   mailSource
	contacts *contactDirectory
	store    *StateStore
	logger   *log.Logger
	submit   func(sessionKey, prompt string, receivedAt time.Time)

	mu          sync.Mutex
	initialized bool
	seen        map[string]bool
}

func newEmailListener(source mailSource, contacts *contactDirectory, store *StateStore, logger *log.Logger, submit func(sessionKey, prompt string, receivedAt time.Time)) *emailListener {
	if logger == nil {
		logger = log.Default()
	}
	return &emailListener{source: source, contacts: contacts, store: store, logger: logger, submit: submit, seen: map[string]bool{}}
}

// Load restores the persisted seen-message set so a restart continues to treat
// previously-observed inbox messages as already-handled.
func (l *emailListener) Load() error {
	file, err := l.store.LoadMessages()
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.initialized = file.Initialized
	l.seen = map[string]bool{}
	for _, id := range file.SeenIDs {
		l.seen[id] = true
	}
	l.mu.Unlock()
	return nil
}

// Refresh polls the inbox, diffs against the seen set, and submits each newly
// arrived message to the agent.
func (l *emailListener) Refresh(ctx context.Context) error {
	mailboxes, err := l.source.Mailboxes(ctx)
	if err != nil {
		return err
	}
	inbox := selectInbox(mailboxes)
	ids, err := l.source.QueryMessageIDs(ctx, inbox.ID)
	if err != nil {
		return err
	}

	l.mu.Lock()
	firstRun := !l.initialized
	previous := l.seen
	var candidates []string
	if !firstRun {
		for _, id := range ids {
			if !previous[id] {
				candidates = append(candidates, id)
			}
		}
	}
	l.mu.Unlock()

	if firstRun {
		// Baseline the current inbox without submitting anything.
		l.commitSeen(ids, nil)
		return nil
	}

	failed := map[string]bool{}
	for _, id := range candidates {
		msg, err := l.source.GetMessage(ctx, id)
		if err != nil {
			l.logger.Printf("email listener: fetch message %s failed: %v", id, err)
			failed[id] = true
			continue
		}
		contact, found := l.resolveSender(msg)
		prompt := FormatEmailPrompt(inbox.Name, msg, contact, found)
		l.submit(mailSessionKey(inbox.ID), prompt, messageReceivedAt(msg))
	}
	// Seen := current inbox minus any message we failed to fetch this round, so
	// a transient fetch failure is retried on the next refresh while deleted
	// messages drop out of the bounded set.
	l.commitSeen(ids, failed)
	return nil
}

func (l *emailListener) commitSeen(ids []string, failed map[string]bool) {
	seen := make(map[string]bool, len(ids))
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if failed[id] {
			continue
		}
		if !seen[id] {
			seen[id] = true
			kept = append(kept, id)
		}
	}
	l.mu.Lock()
	l.initialized = true
	l.seen = seen
	l.mu.Unlock()
	if err := l.store.SaveMessages(MessagesFile{Initialized: true, SeenIDs: kept}); err != nil {
		l.logger.Printf("email listener: persist seen set failed: %v", err)
	}
}

func (l *emailListener) resolveSender(msg jmap.Message) (jmap.Contact, bool) {
	if l.contacts == nil {
		return jmap.Contact{}, false
	}
	email := senderEmail(msg)
	if email == "" {
		return jmap.Contact{}, false
	}
	return l.contacts.Resolve(email)
}

func mailSessionKey(mailboxID string) string {
	if strings.TrimSpace(mailboxID) == "" {
		return mailSessionPrefix + "inbox"
	}
	return mailSessionPrefix + mailboxID
}

func isMailSessionKey(key string) bool { return strings.HasPrefix(key, mailSessionPrefix) }

// selectInbox prefers the mailbox whose role is "inbox"; if none advertises
// that role it falls back to an empty mailbox (account-wide message query).
func selectInbox(mailboxes []jmap.Mailbox) jmap.Mailbox {
	for _, mailbox := range mailboxes {
		if strings.EqualFold(strings.TrimSpace(mailbox.Role), "inbox") {
			return mailbox
		}
	}
	return jmap.Mailbox{Name: "Inbox"}
}

func senderEmail(msg jmap.Message) string {
	for _, header := range append(append([]string{}, msg.From...), msg.Sender...) {
		if addr := parseEmailAddress(header); addr != "" {
			return addr
		}
	}
	return ""
}

func parseEmailAddress(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if parsed, err := mail.ParseAddress(header); err == nil {
		return parsed.Address
	}
	// Fall back to the bare token when the header is already a raw address.
	if strings.Contains(header, "@") && !strings.ContainsAny(header, " <>") {
		return header
	}
	return ""
}

func messageReceivedAt(msg jmap.Message) time.Time {
	for _, value := range []string{msg.ReceivedAt, msg.SentAt} {
		if parsed, err := jmap.ParseTime(value, time.UTC); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

// FormatEmailPrompt renders the agent prompt for a newly-arrived message as
// plain text (no JSON envelope, no templating engine), mirroring the calendar
// VALARM prompt discipline. When the sender resolves to a shared contact record
// the record is included so the agent can use the tenant's shared contacts.
func FormatEmailPrompt(mailboxName string, msg jmap.Message, contact jmap.Contact, contactFound bool) string {
	var b strings.Builder
	b.WriteString("New mail received.\n")
	if name := strings.TrimSpace(mailboxName); name != "" {
		fmt.Fprintf(&b, "  Mailbox:  %s\n", name)
	}
	if from := strings.TrimSpace(strings.Join(msg.From, ", ")); from != "" {
		fmt.Fprintf(&b, "  From:     %s\n", from)
	}
	if to := strings.TrimSpace(strings.Join(msg.To, ", ")); to != "" {
		fmt.Fprintf(&b, "  To:       %s\n", to)
	}
	fmt.Fprintf(&b, "  Subject:  %s\n", strings.TrimSpace(msg.Subject))
	if when := messageReceivedAt(msg); !when.IsZero() {
		fmt.Fprintf(&b, "  Received: %s\n", when.Format("2006-01-02 15:04:05Z"))
	}
	fmt.Fprintf(&b, "  Message:  %s\n", firstNonEmptyString(strings.Join(msg.MessageID, " "), msg.ID))
	if contactFound {
		fmt.Fprintf(&b, "  Contact:  %s\n", formatContactLine(contact))
	} else {
		b.WriteString("  Contact:  (no matching shared contact)\n")
	}
	if body := emailBodyExcerpt(msg); body != "" {
		fmt.Fprintf(&b, "  Body:\n%s", body)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatContactLine(contact jmap.Contact) string {
	parts := []string{}
	if name := strings.TrimSpace(contact.DisplayName()); name != "" {
		parts = append(parts, name)
	}
	if contact.Company != "" {
		parts = append(parts, "("+strings.TrimSpace(contact.Company)+")")
	}
	if len(contact.Emails) > 0 {
		parts = append(parts, "<"+strings.TrimSpace(contact.Emails[0].Value)+">")
	}
	if contact.ID != "" {
		parts = append(parts, "id="+contact.ID)
	}
	if len(parts) == 0 {
		return "(unnamed shared contact)"
	}
	return strings.Join(parts, " ")
}

func emailBodyExcerpt(msg jmap.Message) string {
	value := firstTextBodyValue(msg)
	if value == "" {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) > maxEmailBodyExcerpt {
		value = value[:maxEmailBodyExcerpt] + "…"
	}
	var b strings.Builder
	for _, line := range strings.Split(value, "\n") {
		fmt.Fprintf(&b, "    %s\n", strings.TrimRight(line, "\r"))
	}
	return b.String()
}

func firstTextBodyValue(msg jmap.Message) string {
	for _, body := range msg.TextBody {
		if value := bodyValue(msg.BodyValues, body.PartID); value != "" {
			return value
		}
	}
	// Fall back to any body value present.
	for _, raw := range msg.BodyValues {
		if value := stringField(raw, "value"); value != "" {
			return value
		}
	}
	return ""
}

func bodyValue(bodyValues map[string]any, partID string) string {
	if partID == "" || bodyValues == nil {
		return ""
	}
	return stringField(bodyValues[partID], "value")
}

func stringField(raw any, field string) string {
	obj, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	value, ok := obj[field].(string)
	if !ok {
		return ""
	}
	return value
}
