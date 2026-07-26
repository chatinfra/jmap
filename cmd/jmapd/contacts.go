package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

// contactSource is the subset of jmap.Provider the contact directory needs to
// realize agent contact access. jmap.Provider satisfies it directly.
type contactSource interface {
	Contacts(ctx context.Context) ([]jmap.Contact, error)
	GetOrCreateContactByEmail(ctx context.Context, email string, fallback jmap.Contact) (jmap.Contact, bool, error)
}

// contactDirectory gives the agent access to the tenant's shared contact
// records held on the bound JMAP account. It refreshes its read model from
// `Contact/get` whenever the bridge observes a `ContactCard` state change (or
// a polling tick) and persists a warm cache to `contacts.json`. It reuses the
// provider's `Contacts` and `GetOrCreateContactByEmail` operations that were
// previously reachable only from the operator CLI.
type contactDirectory struct {
	source    contactSource
	store     *StateStore
	logger    *log.Logger
	onRefresh func(time.Time)

	mu      sync.RWMutex
	records []jmap.Contact
}

func newContactDirectory(source contactSource, store *StateStore, logger *log.Logger, onRefresh func(time.Time)) *contactDirectory {
	if logger == nil {
		logger = log.Default()
	}
	return &contactDirectory{source: source, store: store, logger: logger, onRefresh: onRefresh}
}

// Load warms the directory from the persisted cache so a restart continues to
// resolve contacts before the first live refresh completes.
func (d *contactDirectory) Load() error {
	file, err := d.store.LoadContacts()
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.records = file.Contacts
	d.mu.Unlock()
	return nil
}

// Refresh re-reads the tenant's shared contact records from JMAP and rewrites
// the persisted cache.
func (d *contactDirectory) Refresh(ctx context.Context) error {
	contacts, err := d.source.Contacts(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	d.mu.Lock()
	d.records = contacts
	d.mu.Unlock()
	if err := d.store.SaveContacts(ContactsFile{Contacts: contacts, RefreshedAt: now}); err != nil {
		return err
	}
	if d.onRefresh != nil {
		d.onRefresh(now)
	}
	return nil
}

// Records returns a copy of the currently-loaded shared contact records.
func (d *contactDirectory) Records() []jmap.Contact {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]jmap.Contact, len(d.records))
	copy(out, d.records)
	return out
}

// Resolve looks up a shared contact by email against the loaded read model. It
// performs no JMAP write, so it is safe to call while handling inbound mail
// (the bridge is fire-and-forget and must not mutate the account).
func (d *contactDirectory) Resolve(email string) (jmap.Contact, bool) {
	email = strings.TrimSpace(email)
	if email == "" {
		return jmap.Contact{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, contact := range d.records {
		for _, candidate := range contact.Emails {
			if strings.EqualFold(strings.TrimSpace(candidate.Value), email) {
				return contact, true
			}
		}
	}
	return jmap.Contact{}, false
}

// EnsureByEmail exposes lookup-or-provision of a shared contact by email as an
// explicit capability, delegating to the provider's GetOrCreateContactByEmail.
// It is NOT invoked by the automatic mail-handling path (that path stays
// read-only to preserve the bridge's fire-and-forget contract); it is the
// on-demand accessor the agent-facing contact capability provides.
func (d *contactDirectory) EnsureByEmail(ctx context.Context, email string, fallback jmap.Contact) (jmap.Contact, bool, error) {
	return d.source.GetOrCreateContactByEmail(ctx, email, fallback)
}
