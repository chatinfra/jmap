package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/chatinfra/jmap/internal/jmap"
	"github.com/chatinfra/jmap/internal/opencode"
)

var (
	sessionRetryInitialDelay = time.Second
	sessionRetryMaxElapsed   = 2 * time.Minute
	errJMAPPushChannelClosed = errors.New("JMAP push channel closed")
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
	mail     *emailListener
	contacts *contactDirectory

	mu       sync.Mutex
	sessions map[string]string
	workers  map[string]*sessionWorker
	timers   []*time.Timer
	status   StatusFile
	snapshot jmap.CalendarSnapshot
	runCtx   context.Context
	wg       sync.WaitGroup
}

// submission is one unit of work for a session worker: the opencode session key
// (a calendarId for the calendar listener, or a mail-namespaced key for the
// email listener) and the plain-text prompt to submit.
type submission struct {
	sessionKey string
	prompt     string
}

type sessionWorker struct {
	sessionKey string
	ch         chan submission
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
	b := &Bridge{
		cfg:      cfg,
		logger:   logger,
		jmap:     jmapClient,
		opencode: opencodeClient,
		store:    NewStateStore(cfg.StateDir),
		workers:  map[string]*sessionWorker{},
		status: StatusFile{
			DaemonStartedAt: time.Now().UTC(),
		},
	}
	// The calendar VALARM listener is always active. The email and contact
	// modules attach whenever the JMAP client exposes the corresponding
	// provider operations (the real client always does); narrow test clients
	// that implement only the calendar interface leave them unwired.
	if source, ok := jmapClient.(contactSource); ok {
		b.contacts = newContactDirectory(source, b.store, logger, b.markContactRefreshed)
	}
	if source, ok := jmapClient.(mailSource); ok {
		b.mail = newEmailListener(source, b.contacts, b.store, logger, b.submitEmail)
	}
	b.status.RegisteredListenerKeys = b.registeredListenerKeys()
	return b
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
	if b.contacts != nil {
		if err := b.contacts.Load(); err != nil {
			return fmt.Errorf("load contacts: %w", err)
		}
	}
	if b.mail != nil {
		if err := b.mail.Load(); err != nil {
			return fmt.Errorf("load messages: %w", err)
		}
	}
	calendarSessions, mailSessions := sessionCounts(sessions)
	b.mu.Lock()
	b.runCtx = ctx
	b.sessions = sessions
	b.status.CalendarSessionCount = calendarSessions
	b.status.MailSessionCount = mailSessions
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
		if err := b.runPush(ctx); err != nil {
			if ctx.Err() == nil {
				b.logger.Printf("JMAP push unavailable (%v); falling back to polling every %s", err, b.cfg.PollInterval)
				b.setPushState("polling")
				return b.runPolling(ctx)
			}
			return err
		}
		return nil
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
				b.recordError(errJMAPPushChannelClosed)
				return errJMAPPushChannelClosed
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
	b.refreshModules(ctx)
	return nil
}

// refreshModules drives the non-calendar listener modules on the same refresh
// cadence as the calendar snapshot (push notification or polling tick). A
// module error is log-only so a transient mail or contact failure never tears
// down the calendar listener or the daemon. Contacts refresh first so the
// directory is warm when the email listener resolves senders.
func (b *Bridge) refreshModules(ctx context.Context) {
	if b.contacts != nil {
		if err := b.contacts.Refresh(ctx); err != nil {
			b.logOnlyError("contact directory refresh", err)
		}
	}
	if b.mail != nil {
		if err := b.mail.Refresh(ctx); err != nil {
			b.logOnlyError("email listener refresh", err)
		}
	}
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
	b.enqueue(submission{sessionKey: alarm.CalendarID, prompt: FormatPrompt(alarm)})
}

// submitEmail is the email listener's hook into the shared prompt submitter: it
// records the inbound-mail timestamp and enqueues the message prompt onto the
// mailbox's session worker so mail prompts serialize within one mail session.
func (b *Bridge) submitEmail(sessionKey, prompt string, receivedAt time.Time) {
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	b.mu.Lock()
	b.status.LastEmailReceivedAt = &receivedAt
	b.mu.Unlock()
	b.flushStatus()
	b.enqueue(submission{sessionKey: sessionKey, prompt: prompt})
}

// markContactRefreshed records the last time the contact directory reloaded the
// tenant's shared contact records.
func (b *Bridge) markContactRefreshed(at time.Time) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	b.mu.Lock()
	b.status.LastContactRefreshAt = &at
	b.mu.Unlock()
	b.flushStatus()
}

func (b *Bridge) enqueue(sub submission) {
	worker := b.workerFor(sub.sessionKey)
	select {
	case worker.ch <- sub:
	case <-b.runContext().Done():
	}
}

func (b *Bridge) workerFor(sessionKey string) *sessionWorker {
	b.mu.Lock()
	defer b.mu.Unlock()
	if worker := b.workers[sessionKey]; worker != nil {
		return worker
	}
	worker := &sessionWorker{sessionKey: sessionKey, ch: make(chan submission, 64)}
	b.workers[sessionKey] = worker
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
		case sub, ok := <-worker.ch:
			if !ok {
				return
			}
			b.processSubmission(ctx, sub)
		}
	}
}

func (b *Bridge) processSubmission(ctx context.Context, sub submission) {
	sessionID, err := b.sessionForWithRetry(ctx, sub.sessionKey)
	if err != nil {
		b.logOnlyError("create opencode session", err)
		return
	}
	_, err = b.opencode.Prompt(ctx, sessionID, sub.prompt)
	if opencode.IsStaleSession(err) {
		b.logger.Printf("recreating stale opencode session session_key=%s", sub.sessionKey)
		sessionID, err = b.recreateSession(ctx, sub.sessionKey)
		if err == nil {
			_, err = b.opencode.Prompt(ctx, sessionID, sub.prompt)
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

func (b *Bridge) sessionFor(ctx context.Context, sessionKey string) (string, error) {
	b.mu.Lock()
	sessionID := b.sessions[sessionKey]
	b.mu.Unlock()
	if sessionID != "" {
		return sessionID, nil
	}
	return b.recreateSession(ctx, sessionKey)
}

func (b *Bridge) sessionForWithRetry(ctx context.Context, sessionKey string) (string, error) {
	startedAt := time.Now()
	delay := sessionRetryInitialDelay
	attempt := 0
	for {
		attempt++
		sessionID, err := b.sessionFor(ctx, sessionKey)
		if err == nil {
			return sessionID, nil
		}
		if !opencode.IsRetryable(err) || time.Since(startedAt) >= sessionRetryMaxElapsed {
			return "", err
		}
		b.logger.Printf("create session transient failure session_key=%s attempt=%d retry_in=%s: %v", sessionKey, attempt, delay, err)
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

func (b *Bridge) recreateSession(ctx context.Context, sessionKey string) (string, error) {
	session, err := b.opencode.CreateSession(ctx)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = map[string]string{}
	}
	b.sessions[sessionKey] = session.ID
	calendarSessions, mailSessions := sessionCounts(b.sessions)
	b.status.CalendarSessionCount = calendarSessions
	b.status.MailSessionCount = mailSessions
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

func emailListenerKeys() []string { return []string{"email-inbox"} }

func contactListenerKeys() []string { return []string{"contact-directory"} }

// registeredListenerKeys reports the listener modules active on this bridge.
// The calendar VALARM listener is always active; the email and contact modules
// are reported when they are wired (the real client always wires them).
func (b *Bridge) registeredListenerKeys() []string {
	keys := calendarListenerKeys()
	if b.mail != nil {
		keys = append(keys, emailListenerKeys()...)
	}
	if b.contacts != nil {
		keys = append(keys, contactListenerKeys()...)
	}
	return keys
}

// subscriptionDataTypes reports the JMAP data types this bridge subscribes to
// across its active listener modules.
func (b *Bridge) subscriptionDataTypes() []string {
	types := calendarListenerDataTypes()
	if b.mail != nil {
		types = append(types, "Email")
	}
	if b.contacts != nil {
		types = append(types, "ContactCard")
	}
	return types
}

// allSubscriptionDataTypes is the full set of JMAP data types the daemon's
// listener modules consume. The real JMAP client subscribes to this set so a
// change to any active data type refreshes the corresponding module.
func allSubscriptionDataTypes() []string {
	return []string{"Calendar", "CalendarEvent", "Email", "ContactCard"}
}

// sessionCounts partitions the persisted session map into per-calendar and
// per-mailbox session counts for status reporting.
func sessionCounts(sessions map[string]string) (calendar, mail int) {
	for key := range sessions {
		if isMailSessionKey(key) {
			mail++
			continue
		}
		calendar++
	}
	return calendar, mail
}
