package jmap

import (
	"context"
	"time"
)

type CalendarSnapshot struct {
	AccountID   string     `json:"accountId"`
	Calendars   []Calendar `json:"calendars"`
	Events      []Event    `json:"events"`
	RefreshedAt time.Time  `json:"refreshedAt"`
}

func (p Provider) Snapshot(ctx context.Context) (CalendarSnapshot, error) {
	calendars, err := p.Calendars(ctx)
	if err != nil {
		return CalendarSnapshot{}, err
	}
	events, err := p.Events(ctx, nil)
	if err != nil {
		return CalendarSnapshot{}, err
	}
	return CalendarSnapshot{
		AccountID:   p.AccountID,
		Calendars:   calendars,
		Events:      events,
		RefreshedAt: time.Now().UTC(),
	}, nil
}

func (s CalendarSnapshot) CalendarByID() map[string]Calendar {
	out := make(map[string]Calendar, len(s.Calendars))
	for _, calendar := range s.Calendars {
		out[calendar.ID] = calendar
	}
	return out
}
