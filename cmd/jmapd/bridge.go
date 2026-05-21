package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
	"github.com/chatinfra/jmap/internal/opencode"
)

var (
	sessionRetryInitialDelay = time.Second
	sessionRetryMaxElapsed   = 2 * time.Minute
)

type calendarClient interface {
	Connect(context.Context) (bool, error)
	Snapshot(context.Context) (jmap.CalendarSnapshot, error)
	SubscribeStateChanges(context.Context) (<-chan jmap.StateChange, <-chan error, func(), error)
	Close() error
}

type opencodeClient interface {
	CreateSession(context.Context) (opencode.Session, error)
	Prompt(context.Context, string, string) (opencode.AssistantResponse, error)
}

type Bridge struct {
	cfg      Config
	logger   *log.Logger
	jmap     calendarClient
	opencode opencodeClient
	store    *StateStore

	mu       sync.Mutex
	sessions map[string]string
	workers  map[string]*sessionWorker
	timers   []*time.Timer
	status   StatusFile
	snapshot jmap.CalendarSnapshot
	runCtx   context.Context
	wg       sync.WaitGroup
}

type sessionWorker struct {
	calendarID string
	ch         chan jmap.AlarmOccurrence
}

func NewBridge(cfg Config, logger *log.Logger) (*Bridge, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	opencodeClient, err := opencode.New(opencode.Config{
		BaseURL:       cfg.OpencodeBaseURL,
		Directory:     cfg.OpencodeDirectory,
		Agent:         cfg.AgentID,
		PromptTimeout: cfg.PromptTimeout,
	})
	if err != nil {
		return nil, err
	}
	return NewBridgeWithClients(cfg, logger, newRealCalendarClient(cfg), opencodeClient), nil
}

func NewBridgeWithClients(cfg Config, logger *log.Logger, jmapClient calendarClient, opencodeClient opencodeClient) *Bridge {
	if logger == nil {
		logger = log.Default()
	}
	return &Bridge{
		cfg:      cfg,
		logger:   logger,
		jmap:     jmapClient,
		opencode: opencodeClient,
		store:    NewStateStore(cfg.StateDir),
		workers:  map[string]*sessionWorker{},
		status: StatusFile{
			DaemonStartedAt:        time.Now().UTC(),
			RegisteredListenerKeys: calendarListenerKeys(),
		},
	}
}

func (b *Bridge) Run(ctx context.Context) error {
	sessions, err := b.store.LoadSessions()
	if err != nil {
		return fmt.Errorf("load sessions: %w", err)
	}
	persistedSnapshot, err := b.store.LoadEvents()
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}
	b.mu.Lock()
	b.runCtx = ctx
	b.sessions = sessions
	b.status.CalendarSessionCount = len(sessions)
	b.snapshot = persistedSnapshot
	b.mu.Unlock()
	b.flushStatus()
	if !persistedSnapshot.RefreshedAt.IsZero() {
		b.scheduleSnapshot(persistedSnapshot)
	}

	pushAvailable, err := b.jmap.Connect(ctx)
	if err != nil {
		b.recordError(fmt.Errorf("jmap connect: %w", err))
		return err
	}
	b.setConnected(true)
	defer func() {
		b.setConnected(false)
		b.setPushState("closed")
		b.stopTimers()
		b.shutdownWorkers()
		_ = b.jmap.Close()
		b.flushEvents()
		b.flushSessions()
		b.flushStatus()
	}()

	if err := b.refresh(ctx); err != nil {
		b.recordError(fmt.Errorf("jmap refresh: %w", err))
		return err
	}
	if pushAvailable {
		b.setPushState("eventsource")
		return b.runPush(ctx)
	}
	b.setPushState("polling")
	return b.runPolling(ctx)
}

func (b *Bridge) runPush(ctx context.Context) error {
	changes, errs, stop, err := b.jmap.SubscribeStateChanges(ctx)
	if err != nil {
		b.recordError(fmt.Errorf("jmap push subscribe: %w", err))
		return err
	}
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errs:
			if ok && err != nil {
				b.recordError(fmt.Errorf("jmap push: %w", err))
				return err
			}
			errs = nil
		case _, ok := <-changes:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				err := errors.New("JMAP push channel closed")
				b.recordError(err)
				return err
			}
			if err := b.refresh(ctx); err != nil {
				b.recordError(fmt.Errorf("jmap refresh: %w", err))
				return err
			}
		}
	}
}

func (b *Bridge) runPolling(ctx context.Context) error {
	ticker := time.NewTicker(b.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := b.refresh(ctx); err != nil {
				b.logOnlyError("jmap polling refresh", err)
			}
		}
	}
}

func (b *Bridge) refresh(ctx context.Context) error {
	snapshot, err := b.jmap.Snapshot(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	b.mu.Lock()
	b.snapshot = snapshot
	b.status.LastSnapshotRefreshAt = &now
	b.mu.Unlock()
	if err := b.store.SaveEvents(snapshot); err != nil {
		return err
	}
	b.flushStatus()
	b.scheduleSnapshot(snapshot)
	return nil
}

func (b *Bridge) scheduleSnapshot(snapshot jmap.CalendarSnapshot) {
	b.stopTimers()
	now := time.Now().UTC()
	calendarByID := snapshot.CalendarByID()
	var timers []*time.Timer
	for _, event := range snapshot.Events {
		calendarIDs := event.ActiveCalendarIDs()
		if len(calendarIDs) == 0 {
			for _, calendar := range snapshot.Calendars {
				calendarIDs = append(calendarIDs, calendar.ID)
			}
		}
		for _, calendarID := range calendarIDs {
			calendar := calendarByID[calendarID]
			occurrences, errs := jmap.AlarmOccurrences(event, calendarID, calendar.Name, now, b.cfg.AlarmWindow)
			for _, err := range errs {
				b.logger.Printf("VALARM parse skipped: %v", err)
			}
			for _, occurrence := range occurrences {
				if !occurrence.TriggerTime.After(now) {
					continue
				}
				occurrence := occurrence
				timer := time.AfterFunc(time.Until(occurrence.TriggerTime), func() { b.enqueueAlarm(occurrence) })
				timers = append(timers, timer)
			}
		}
	}
	b.mu.Lock()
	b.timers = timers
	b.mu.Unlock()
}

func (b *Bridge) enqueueAlarm(alarm jmap.AlarmOccurrence) {
	now := time.Now().UTC()
	b.mu.Lock()
	b.status.LastVALARMFiredAt = &now
	b.mu.Unlock()
	b.flushStatus()
	worker := b.workerFor(alarm.CalendarID)
	select {
	case worker.ch <- alarm:
	case <-b.runContext().Done():
	}
}

func (b *Bridge) workerFor(calendarID string) *sessionWorker {
	b.mu.Lock()
	defer b.mu.Unlock()
	if worker := b.workers[calendarID]; worker != nil {
		return worker
	}
	worker := &sessionWorker{calendarID: calendarID, ch: make(chan jmap.AlarmOccurrence, 64)}
	b.workers[calendarID] = worker
	b.wg.Add(1)
	ctx := b.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go b.runWorker(ctx, worker)
	return worker
}

func (b *Bridge) runWorker(ctx context.Context, worker *sessionWorker) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case alarm, ok := <-worker.ch:
			if !ok {
				return
			}
			b.processAlarm(ctx, alarm)
		}
	}
}

func (b *Bridge) processAlarm(ctx context.Context, alarm jmap.AlarmOccurrence) {
	sessionID, err := b.sessionForWithRetry(ctx, alarm.CalendarID)
	if err != nil {
		b.logOnlyError("create opencode session", err)
		return
	}
	prompt := FormatPrompt(alarm)
	_, err = b.opencode.Prompt(ctx, sessionID, prompt)
	if opencode.IsStaleSession(err) {
		b.logger.Printf("recreating stale opencode session calendar_id=%s", alarm.CalendarID)
		sessionID, err = b.recreateSession(ctx, alarm.CalendarID)
		if err == nil {
			_, err = b.opencode.Prompt(ctx, sessionID, prompt)
		}
	}
	if err != nil {
		b.logOnlyError("opencode prompt", err)
		return
	}
	now := time.Now().UTC()
	b.mu.Lock()
	b.status.LastPromptCompletedAt = &now
	b.mu.Unlock()
	b.flushStatus()
}

func (b *Bridge) sessionFor(ctx context.Context, calendarID string) (string, error) {
	b.mu.Lock()
	sessionID := b.sessions[calendarID]
	b.mu.Unlock()
	if sessionID != "" {
		return sessionID, nil
	}
	return b.recreateSession(ctx, calendarID)
}

func (b *Bridge) sessionForWithRetry(ctx context.Context, calendarID string) (string, error) {
	startedAt := time.Now()
	delay := sessionRetryInitialDelay
	attempt := 0
	for {
		attempt++
		sessionID, err := b.sessionFor(ctx, calendarID)
		if err == nil {
			return sessionID, nil
		}
		if !opencode.IsRetryable(err) || time.Since(startedAt) >= sessionRetryMaxElapsed {
			return "", err
		}
		b.logger.Printf("create session transient failure calendar_id=%s attempt=%d retry_in=%s: %v", calendarID, attempt, delay, err)
		b.recordError(err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
}

func (b *Bridge) recreateSession(ctx context.Context, calendarID string) (string, error) {
	session, err := b.opencode.CreateSession(ctx)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = map[string]string{}
	}
	b.sessions[calendarID] = session.ID
	b.status.CalendarSessionCount = len(b.sessions)
	b.mu.Unlock()
	if err := b.flushSessions(); err != nil {
		return "", err
	}
	b.flushStatus()
	return session.ID, nil
}

func (b *Bridge) stopTimers() {
	b.mu.Lock()
	timers := b.timers
	b.timers = nil
	b.mu.Unlock()
	for _, timer := range timers {
		timer.Stop()
	}
}

func (b *Bridge) shutdownWorkers() {
	b.mu.Lock()
	workers := make([]*sessionWorker, 0, len(b.workers))
	for _, worker := range b.workers {
		workers = append(workers, worker)
	}
	b.workers = map[string]*sessionWorker{}
	b.mu.Unlock()
	for _, worker := range workers {
		close(worker.ch)
	}
	b.wg.Wait()
}

func (b *Bridge) setConnected(connected bool) {
	b.mu.Lock()
	b.status.JMAPConnected = connected
	b.mu.Unlock()
	b.flushStatus()
}

func (b *Bridge) setPushState(state string) {
	b.mu.Lock()
	b.status.PushState = state
	b.mu.Unlock()
	b.flushStatus()
}

func (b *Bridge) recordError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.status.LastError = err.Error()
	b.mu.Unlock()
	b.flushStatus()
}

func (b *Bridge) logOnlyError(action string, err error) {
	if err == nil {
		return
	}
	b.logger.Printf("%s failed: %v", action, err)
	b.recordError(err)
}

func (b *Bridge) flushSessions() error {
	b.mu.Lock()
	sessions := make(map[string]string, len(b.sessions))
	for key, value := range b.sessions {
		sessions[key] = value
	}
	b.mu.Unlock()
	return b.store.SaveSessions(sessions)
}

func (b *Bridge) flushEvents() {
	b.mu.Lock()
	snapshot := b.snapshot
	b.mu.Unlock()
	if snapshot.RefreshedAt.IsZero() {
		return
	}
	if err := b.store.SaveEvents(snapshot); err != nil {
		b.logger.Printf("write events failed: %v", err)
	}
}

func (b *Bridge) flushStatus() {
	b.mu.Lock()
	status := b.status
	b.mu.Unlock()
	if err := b.store.SaveStatus(status); err != nil {
		b.logger.Printf("write status failed: %v", err)
	}
}

func (b *Bridge) runContext() context.Context {
	b.mu.Lock()
	ctx := b.runCtx
	b.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func calendarListenerDataTypes() []string { return []string{"Calendar", "CalendarEvent"} }

func calendarListenerKeys() []string { return []string{"calendar-valarm"} }

func registeredListenerKeys() []string { return calendarListenerKeys() }

func registeredSubscriptionDataTypes() []string { return calendarListenerDataTypes() }

func hasOnlyCalendarListener() bool {
	keys := registeredListenerKeys()
	return len(keys) == 1 && keys[0] == "calendar-valarm" && !containsString(registeredSubscriptionDataTypes(), "Email") && !containsString(registeredSubscriptionDataTypes(), "ContactCard")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
