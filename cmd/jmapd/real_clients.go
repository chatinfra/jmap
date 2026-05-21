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
	return c.client.SubscribeStateChanges(ctx, c.session, c.provider.AccountID, calendarListenerDataTypes())
}

func (c *realCalendarClient) Close() error { return nil }
