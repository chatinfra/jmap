package main

import (
	"context"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
)

type realCalendarClient struct {
	cfg      Config
	client   *jmap.Client
	provider jmap.Provider
	session  jmap.SessionInfo
}

func newRealCalendarClient(cfg Config) *realCalendarClient {
	return &realCalendarClient{cfg: cfg}
}

func (c *realCalendarClient) Connect(ctx context.Context) (bool, error) {
	c.client = jmap.NewClient(jmap.Config{
		BaseURL:  c.cfg.JMAPBaseURL,
		Username: c.cfg.JMAPUser,
		Password: c.cfg.JMAPPassword,
		Timeout:  30 * time.Second,
	})
	provider, session, err := jmap.NewProviderFromSession(ctx, c.client)
	if err != nil {
		return false, err
	}
	c.provider = provider
	c.session = session
	return session.EventSourceURL != "", nil
}

func (c *realCalendarClient) Snapshot(ctx context.Context) (jmap.CalendarSnapshot, error) {
	return c.provider.Snapshot(ctx)
}

func (c *realCalendarClient) SubscribeStateChanges(ctx context.Context) (<-chan jmap.StateChange, <-chan error, func(), error) {
	return c.client.SubscribeStateChanges(ctx, c.session, c.provider.AccountID, allSubscriptionDataTypes())
}

func (c *realCalendarClient) Close() error { return nil }

// Mail + contact access delegate to the connected provider so the email and
// contact modules reuse the same JMAP session the calendar listener opened.
// These satisfy the mailSource and contactSource interfaces the bridge wires
// on construction; the provider is set during Connect, before any refresh.

func (c *realCalendarClient) Mailboxes(ctx context.Context) ([]jmap.Mailbox, error) {
	return c.provider.Mailboxes(ctx)
}

func (c *realCalendarClient) QueryMessageIDs(ctx context.Context, mailboxID string) ([]string, error) {
	return c.provider.QueryMessageIDs(ctx, mailboxID)
}

func (c *realCalendarClient) GetMessage(ctx context.Context, id string) (jmap.Message, error) {
	return c.provider.GetMessage(ctx, id)
}

func (c *realCalendarClient) Contacts(ctx context.Context) ([]jmap.Contact, error) {
	return c.provider.Contacts(ctx)
}

func (c *realCalendarClient) GetOrCreateContactByEmail(ctx context.Context, email string, fallback jmap.Contact) (jmap.Contact, bool, error) {
	return c.provider.GetOrCreateContactByEmail(ctx, email, fallback)
}
