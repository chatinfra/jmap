package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"github.com/chatinfra/jmap/internal/appointment"
	"github.com/chatinfra/jmap/internal/jmap"
	"github.com/chatinfra/jmap/internal/schedule"
)

type options struct {
	url       string
	user      string
	password  string
	json      bool
	timeout   time.Duration
	trace     bool
	stateRoot string
	dryRun    bool
	force     bool
}

type commandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e commandError) Error() string { return e.Message }

type errorEnvelope struct {
	Error commandError `json:"error"`
}

func Run(args []string, stdout, stderr io.Writer) error {
	if err := rejectJSONFlag(args); err != nil {
		emitError(options{}, stdout, stderr, err)
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return printUsage(stdout)
	}
	opts, rest, err := parseGlobal(args, os.Getenv)
	if err != nil {
		emitError(opts, stdout, stderr, err)
		return err
	}
	if len(rest) == 0 {
		err := coded("missing_command", "missing command")
		emitError(opts, stdout, stderr, err)
		return err
	}
	if rest[0] == "schemas" || rest[0] == "schema" {
		return writeYAML(stdout, schemaDiscovery())
	}
	ctx := context.Background()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	if err := run(ctx, opts, rest, stdout, stderr); err != nil {
		emitError(opts, stdout, stderr, err)
		return err
	}
	return nil
}

func rejectJSONFlag(args []string) error {
	for _, arg := range args {
		if arg == "--json" || strings.HasPrefix(arg, "--json=") {
			return coded("unsupported_flag", "--json is not supported; jmap emits YAML output by default")
		}
	}
	return nil
}

func schemaDiscovery() map[string]any {
	ids := []string{"help", "schemas", "error", "check", "raw", "calendar", "event", "availability", "principal", "participant", "addressbook", "contact", "mailbox", "message", "hours", "slot", "appointment"}
	schemas := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		schemas = append(schemas, map[string]string{"id": id, "path": "spec/outputs/" + id + ".schema.yaml"})
	}
	return map[string]any{"tool": "jmap", "schemas": schemas}
}

func parseGlobal(args []string, getenv func(string) string) (options, []string, error) {
	opts := options{timeout: 30 * time.Second}
	normalized, err := normalizeGlobalFlags(args)
	if err != nil {
		return opts, nil, err
	}
	fs := flag.NewFlagSet("jmap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.url, "url", "", "JMAP server base URL; default JMAP_URL")
	fs.StringVar(&opts.user, "user", "", "JMAP username/account id; default JMAP_USER")
	fs.StringVar(&opts.password, "password", "", "JMAP password; default JMAP_PASSWORD")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "request timeout; default JMAP_TIMEOUT or 30s; 0 disables client timeout")
	fs.BoolVar(&opts.trace, "trace", false, "write redacted HTTP traces to stderr")
	fs.StringVar(&opts.stateRoot, "state-root", "", "appointment state root; default JMAP_STATE_ROOT or user config")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "preview mutating/destructive commands")
	fs.BoolVar(&opts.force, "force", false, "confirm destructive bulk commands")
	if err := fs.Parse(normalized); err != nil {
		return opts, nil, err
	}
	if opts.url == "" {
		opts.url = getenv("JMAP_URL")
	}
	if opts.user == "" {
		opts.user = getenv("JMAP_USER")
	}
	if opts.password == "" {
		opts.password = getenv("JMAP_PASSWORD")
	}
	if opts.stateRoot == "" {
		opts.stateRoot = appointment.DefaultStateRoot(getenv)
	}
	if value := getenv("JMAP_TIMEOUT"); value != "" && opts.timeout == 30*time.Second {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return opts, nil, fmt.Errorf("invalid JMAP_TIMEOUT: %w", err)
		}
		opts.timeout = parsed
	}
	if boolEnv(getenv("JMAP_TRACE")) {
		opts.trace = true
	}
	if boolEnv(getenv("JMAP_DRY_RUN")) {
		opts.dryRun = true
	}
	if boolEnv(getenv("JMAP_FORCE")) {
		opts.force = true
	}
	return opts, fs.Args(), nil
}

func normalizeGlobalFlags(args []string) ([]string, error) {
	boolFlags := map[string]bool{"trace": true, "dry-run": true, "force": true}
	valueFlags := map[string]bool{"url": true, "user": true, "password": true, "timeout": true, "state-root": true}
	globals := make([]string, 0, len(args))
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			rest = append(rest, arg)
			continue
		}
		nameValue := strings.TrimPrefix(arg, "--")
		name, value, hasValue := strings.Cut(nameValue, "=")
		if boolFlags[name] {
			if hasValue {
				globals = append(globals, "--"+name+"="+value)
			} else {
				globals = append(globals, "--"+name)
			}
			continue
		}
		if valueFlags[name] {
			globals = append(globals, "--"+name)
			if hasValue {
				globals = append(globals, value)
				continue
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s requires a value", name)
			}
			i++
			globals = append(globals, args[i])
			continue
		}
		rest = append(rest, arg)
	}
	return append(globals, rest...), nil
}

func run(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	cmd, cmdArgs := args[0], args[1:]
	switch cmd {
	case "check":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		result, err := provider.Check(ctx)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, result, fmt.Sprintf("connected account=%s\n", opts.user))
	case "raw":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runRaw(ctx, provider, opts, cmdArgs, stdout)
	case "calendar", "calendars":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runCalendar(ctx, provider, opts, cmdArgs, stdout)
	case "event", "events":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runEvent(ctx, provider, opts, cmdArgs, stdout)
	case "availability":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runAvailability(ctx, provider, opts, cmdArgs, stdout)
	case "principal", "principals":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runPrincipal(ctx, provider, opts, cmdArgs, stdout)
	case "participant", "participants":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runParticipant(ctx, provider, opts, cmdArgs, stdout)
	case "addressbook", "addressbooks":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runAddressBook(ctx, provider, opts, cmdArgs, stdout)
	case "contact", "contacts":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runContact(ctx, provider, opts, cmdArgs, stdout)
	case "mailbox", "mailboxes":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runMailbox(ctx, provider, opts, cmdArgs, stdout)
	case "message", "messages":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runMessage(ctx, provider, opts, cmdArgs, stdout)
	case "hours":
		provider, err := optionalProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runHours(ctx, provider, opts, cmdArgs, stdout)
	case "slot", "slots":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		return runSlot(ctx, provider, opts, cmdArgs, stdout)
	case "appointment", "appointments":
		return runAppointment(ctx, opts, cmdArgs, stdout, stderr)
	default:
		return coded("unknown_command", fmt.Sprintf("unknown command %q", cmd))
	}
}

func newProvider(opts options, trace io.Writer) (jmap.Provider, error) {
	missing := []string{}
	if opts.url == "" {
		missing = append(missing, "--url/JMAP_URL")
	}
	if opts.user == "" {
		missing = append(missing, "--user/JMAP_USER")
	}
	if opts.password == "" {
		missing = append(missing, "--password/JMAP_PASSWORD")
	}
	if len(missing) > 0 {
		return jmap.Provider{}, coded("missing_config", "missing required configuration: "+strings.Join(missing, ", "))
	}
	client := jmap.NewClient(jmap.Config{BaseURL: opts.url, Username: opts.user, Password: opts.password, Timeout: opts.timeout, Trace: opts.trace, TraceWriter: trace})
	return jmap.NewProvider(client), nil
}

func optionalProvider(opts options, trace io.Writer) (*jmap.Provider, error) {
	if opts.url == "" || opts.user == "" || opts.password == "" {
		return nil, nil
	}
	provider, err := newProvider(opts, trace)
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

func runRaw(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "call" {
		return coded("usage", "usage: jmap raw call <method> --params JSON [--capability URN]")
	}
	fs := newFlagSet("jmap raw call")
	params := fs.String("params", "{}", "JSON params object")
	var capabilities stringListFlag
	fs.Var(&capabilities, "capability", "JMAP capability URN; repeatable")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return coded("usage", "raw call requires a method name")
	}
	resp, err := provider.RawCall(ctx, fs.Arg(0), json.RawMessage(*params), capabilities)
	if err != nil {
		return err
	}
	result := map[string]any{"method": resp.Name, "id": resp.ID, "params": json.RawMessage(resp.Params)}
	return write(stdout, opts.json, result, fmt.Sprintf("method=%s id=%s\n", resp.Name, resp.ID))
}

func runCalendar(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap calendar <list|create|get|get-or-create|delete|delete-all>")
	}
	switch args[0] {
	case "list", "ls":
		calendars, err := provider.Calendars(ctx)
		if err != nil {
			return err
		}
		jmap.SortCalendars(calendars)
		return write(stdout, opts.json, calendars, renderCalendars(calendars))
	case "create":
		fs := newFlagSet("jmap calendar create")
		name := fs.String("name", "", "calendar name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" && fs.NArg() > 0 {
			*name = fs.Arg(0)
		}
		if *name == "" {
			return coded("usage", "calendar create requires --name")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "create": "calendar", "name": *name}, fmt.Sprintf("would create calendar name=%s\n", *name))
		}
		calendar, err := provider.CreateCalendar(ctx, *name)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, calendar, fmt.Sprintf("created calendar id=%s name=%s\n", calendar.ID, calendar.Name))
	case "get":
		fs := newFlagSet("jmap calendar get")
		name := fs.String("name", "", "calendar name")
		id := fs.String("id", "", "calendar id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" && *id == "" && fs.NArg() > 0 {
			*name = fs.Arg(0)
		}
		calendars, err := provider.Calendars(ctx)
		if err != nil {
			return err
		}
		for _, calendar := range calendars {
			if (*id != "" && calendar.ID == *id) || (*name != "" && calendar.Name == *name) {
				return write(stdout, opts.json, calendar, fmt.Sprintf("calendar id=%s name=%s\n", calendar.ID, calendar.Name))
			}
		}
		return coded("not_found", "calendar not found")
	case "get-or-create", "ensure":
		fs := newFlagSet("jmap calendar get-or-create")
		name := fs.String("name", "", "calendar name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" && fs.NArg() > 0 {
			*name = fs.Arg(0)
		}
		if *name == "" {
			return coded("usage", "calendar get-or-create requires --name")
		}
		calendar, created, err := provider.GetOrCreateCalendar(ctx, *name)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"calendar": calendar, "created": created}, fmt.Sprintf("calendar id=%s name=%s created=%t\n", calendar.ID, calendar.Name, created))
	case "delete", "rm":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "calendar delete requires <calendar-id>")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": []string{id}}, fmt.Sprintf("would delete calendar id=%s\n", id))
		}
		if err := provider.DeleteCalendar(ctx, id); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"deleted": id}, fmt.Sprintf("deleted calendar id=%s\n", id))
	case "delete-all", "reset":
		if err := requireForce(opts, "calendar delete-all requires --force"); err != nil {
			return err
		}
		calendars, err := provider.Calendars(ctx)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(calendars))
		for _, calendar := range calendars {
			ids = append(ids, calendar.ID)
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": ids}, fmt.Sprintf("would delete %d calendars\n", len(ids)))
		}
		for _, id := range ids {
			if err := provider.DeleteCalendar(ctx, id); err != nil {
				return err
			}
		}
		return write(stdout, opts.json, map[string]any{"deleted": ids}, fmt.Sprintf("deleted %d calendars\n", len(ids)))
	default:
		return coded("usage", "unknown calendar action "+args[0])
	}
}

func runEvent(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap event <list|get|create|query|update|delete|delete-all>")
	}
	switch args[0] {
	case "list", "ls":
		fs := newFlagSet("jmap event list")
		var calendarIDs stringListFlag
		fs.Var(&calendarIDs, "calendar-id", "calendar id; repeatable")
		raw := fs.Bool("raw", false, "emit raw event JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *raw {
			list, err := provider.EventsRaw(ctx, calendarIDs)
			if err != nil {
				return err
			}
			return write(stdout, opts.json, list, fmt.Sprintf("events=%d\n", len(list)))
		}
		events, err := provider.Events(ctx, calendarIDs)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, events, renderEvents(events))
	case "list-json":
		list, err := provider.EventsRaw(ctx, nil)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, list, fmt.Sprintf("events=%d\n", len(list)))
	case "get", "get-json":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "event get requires <event-id>")
		}
		if args[0] == "get-json" {
			raw, err := provider.GetEventRaw(ctx, id)
			if err != nil {
				return err
			}
			return write(stdout, opts.json, raw, fmt.Sprintf("event id=%s\n", id))
		}
		event, err := provider.GetEvent(ctx, id)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, event, renderEvent(event))
	case "create":
		fs := newFlagSet("jmap event create")
		title := fs.String("title", "", "event title")
		startValue := fs.String("start", "", "event start RFC3339")
		durationValue := fs.String("duration", "30m", "event duration")
		description := fs.String("description", "", "event description")
		weekly := fs.Bool("weekly", false, "create a weekly recurrence rule")
		var calendarIDs stringListFlag
		fs.Var(&calendarIDs, "calendar-id", "calendar id; repeatable")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *title == "" {
			return coded("usage", "event create requires --title")
		}
		start, err := parseTime(*startValue)
		if err != nil {
			return err
		}
		duration, err := parseDuration(*durationValue)
		if err != nil {
			return err
		}
		recurrence := ""
		if *weekly {
			recurrence = "weekly"
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "title": *title, "start": start, "duration": duration.String(), "calendarIds": calendarIDs}, fmt.Sprintf("would create event title=%s\n", *title))
		}
		id, err := provider.CreateEvent(ctx, *title, start, duration, *description, calendarIDs, recurrence)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"id": id}, fmt.Sprintf("created event id=%s\n", id))
	case "create-json":
		fs := newFlagSet("jmap event create-json")
		body := fs.String("body", "", "event JSON object")
		var calendarIDs stringListFlag
		fs.Var(&calendarIDs, "calendar-id", "calendar id; repeatable")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		raw := jsonBody(*body, fs.Args())
		if len(raw) == 0 {
			return coded("usage", "event create-json requires --body JSON or positional JSON")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "event": json.RawMessage(raw)}, "would create event JSON\n")
		}
		id, err := provider.CreateEventRaw(ctx, json.RawMessage(raw), calendarIDs)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"id": id}, fmt.Sprintf("created event id=%s\n", id))
	case "update", "update-json":
		fs := newFlagSet("jmap event update")
		id := fs.String("id", "", "event id")
		body := fs.String("body", "", "event update JSON object")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" && fs.NArg() > 0 {
			*id = fs.Arg(0)
		}
		raw := jsonBody(*body, fs.Args()[min(1, fs.NArg()):])
		if *id == "" || len(raw) == 0 {
			return coded("usage", "event update requires --id and --body JSON")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "update": *id}, fmt.Sprintf("would update event id=%s\n", *id))
		}
		if err := provider.UpdateEventRaw(ctx, *id, json.RawMessage(raw)); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"updated": *id}, fmt.Sprintf("updated event id=%s\n", *id))
	case "delete", "rm":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "event delete requires <event-id>")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": id}, fmt.Sprintf("would delete event id=%s\n", id))
		}
		if err := provider.DeleteEvent(ctx, id); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"deleted": id}, fmt.Sprintf("deleted event id=%s\n", id))
	case "delete-all", "reset":
		if err := requireForce(opts, "event delete-all requires --force"); err != nil {
			return err
		}
		fs := newFlagSet("jmap event delete-all")
		var calendarIDs stringListFlag
		fs.Var(&calendarIDs, "calendar-id", "calendar id; repeatable")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		events, err := provider.Events(ctx, calendarIDs)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": ids}, fmt.Sprintf("would delete %d events\n", len(ids)))
		}
		for _, id := range ids {
			if err := provider.DeleteEvent(ctx, id); err != nil {
				return err
			}
		}
		return write(stdout, opts.json, map[string]any{"deleted": ids}, fmt.Sprintf("deleted %d events\n", len(ids)))
	case "query":
		fs := newFlagSet("jmap event query")
		afterValue := fs.String("after", "", "inclusive lower bound RFC3339")
		beforeValue := fs.String("before", "", "exclusive upper bound RFC3339")
		var calendarIDs stringListFlag
		fs.Var(&calendarIDs, "calendar-id", "calendar id; repeatable")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var after, before *time.Time
		if *afterValue != "" {
			parsed, err := parseTime(*afterValue)
			if err != nil {
				return err
			}
			after = &parsed
		}
		if *beforeValue != "" {
			parsed, err := parseTime(*beforeValue)
			if err != nil {
				return err
			}
			before = &parsed
		}
		result, err := provider.QueryEvents(ctx, after, before, calendarIDs)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, result, fmt.Sprintf("event query ids=%d total=%d\n", len(result.IDs), result.Total))
	default:
		return coded("usage", "unknown event action "+args[0])
	}
}

func runAvailability(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) > 0 && (args[0] == "check" || args[0] == "list") {
		args = args[1:]
	}
	fs := newFlagSet("jmap availability")
	startValue := fs.String("start", "", "start RFC3339")
	endValue := fs.String("end", "", "end RFC3339")
	durationValue := fs.String("duration", "", "duration used when --end is omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	start, err := parseTime(*startValue)
	if err != nil {
		return err
	}
	var end time.Time
	if *endValue != "" {
		end, err = parseTime(*endValue)
	} else {
		var d time.Duration
		d, err = parseDuration(*durationValue)
		end = start.Add(d)
	}
	if err != nil {
		return err
	}
	periods, err := provider.Availability(ctx, start, end)
	if err != nil {
		return err
	}
	return write(stdout, opts.json, periods, fmt.Sprintf("busyPeriods=%d\n", len(periods)))
}

func runPrincipal(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] != "list" && args[0] != "query" {
		return coded("usage", "usage: jmap principal list")
	}
	result, err := provider.Principals(ctx)
	if err != nil {
		return err
	}
	return write(stdout, opts.json, result, fmt.Sprintf("principals=%d\n", len(result.IDs)))
}

func runParticipant(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		participants, err := provider.Participants(ctx)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, participants, fmt.Sprintf("participants=%d\n", len(participants)))
	}
	if args[0] != "create" {
		return coded("usage", "usage: jmap participant <list|create>")
	}
	fs := newFlagSet("jmap participant create")
	name := fs.String("name", "", "participant name")
	scheduleID := fs.String("schedule-id", "", "schedule id")
	sendTo := fs.String("send-to", "", "sendTo mailto URI")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *name == "" {
		return coded("usage", "participant create requires --name")
	}
	sendToMap := map[string]string{}
	if *sendTo != "" {
		sendToMap["imip"] = *sendTo
	}
	if opts.dryRun {
		return write(stdout, opts.json, map[string]any{"dryRun": true, "name": *name}, fmt.Sprintf("would create participant name=%s\n", *name))
	}
	id, err := provider.CreateParticipant(ctx, *name, *scheduleID, sendToMap)
	if err != nil {
		return err
	}
	return write(stdout, opts.json, map[string]any{"id": id}, fmt.Sprintf("created participant id=%s\n", id))
}

func runAddressBook(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] != "list" && args[0] != "ls" {
		return coded("usage", "usage: jmap addressbook list")
	}
	books, err := provider.AddressBooks(ctx)
	if err != nil {
		return err
	}
	return write(stdout, opts.json, books, fmt.Sprintf("addressBooks=%d\n", len(books)))
}

func runContact(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap contact <list|get|create|update|delete|delete-all|search|get-or-create-...>")
	}
	switch args[0] {
	case "list", "ls":
		contacts, err := provider.Contacts(ctx)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, contacts, renderContacts(contacts))
	case "list-json":
		raw, err := provider.ContactsRaw(ctx)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, raw, fmt.Sprintf("contacts=%d\n", len(raw)))
	case "get", "get-json":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "contact get requires <contact-id>")
		}
		if args[0] == "get-json" {
			raw, err := provider.GetContactRaw(ctx, id)
			if err != nil {
				return err
			}
			return write(stdout, opts.json, raw, fmt.Sprintf("contact id=%s\n", id))
		}
		contact, err := provider.GetContact(ctx, id)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, contact, renderContact(contact))
	case "create":
		contact, err := parseContactFlags("jmap contact create", args[1:])
		if err != nil {
			return err
		}
		if contact.FirstName == "" && contact.LastName == "" && contact.Company == "" {
			return coded("usage", "contact create requires a name or company")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "contact": contact}, fmt.Sprintf("would create contact name=%s\n", contact.DisplayName()))
		}
		id, err := provider.CreateContact(ctx, contact)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"id": id}, fmt.Sprintf("created contact id=%s\n", id))
	case "create-json":
		fs := newFlagSet("jmap contact create-json")
		body := fs.String("body", "", "contact JSON object")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		raw := jsonBody(*body, fs.Args())
		if len(raw) == 0 {
			return coded("usage", "contact create-json requires --body JSON or positional JSON")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "contact": json.RawMessage(raw)}, "would create contact JSON\n")
		}
		id, err := provider.CreateContactRaw(ctx, json.RawMessage(raw))
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"id": id}, fmt.Sprintf("created contact id=%s\n", id))
	case "update", "update-json":
		fs := newFlagSet("jmap contact update")
		id := fs.String("id", "", "contact id")
		body := fs.String("body", "", "contact update JSON object")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" && fs.NArg() > 0 {
			*id = fs.Arg(0)
		}
		raw := jsonBody(*body, fs.Args()[min(1, fs.NArg()):])
		if *id == "" || len(raw) == 0 {
			return coded("usage", "contact update requires --id and --body JSON")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "update": *id}, fmt.Sprintf("would update contact id=%s\n", *id))
		}
		if err := provider.UpdateContactRaw(ctx, *id, json.RawMessage(raw)); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"updated": *id}, fmt.Sprintf("updated contact id=%s\n", *id))
	case "delete", "rm":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "contact delete requires <contact-id>")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": id}, fmt.Sprintf("would delete contact id=%s\n", id))
		}
		if err := provider.DeleteContact(ctx, id); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"deleted": id}, fmt.Sprintf("deleted contact id=%s\n", id))
	case "delete-all", "reset":
		if err := requireForce(opts, "contact delete-all requires --force"); err != nil {
			return err
		}
		contacts, err := provider.Contacts(ctx)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(contacts))
		for _, contact := range contacts {
			ids = append(ids, contact.ID)
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": ids}, fmt.Sprintf("would delete %d contacts\n", len(ids)))
		}
		for _, id := range ids {
			if err := provider.DeleteContact(ctx, id); err != nil {
				return err
			}
		}
		return write(stdout, opts.json, map[string]any{"deleted": ids}, fmt.Sprintf("deleted %d contacts\n", len(ids)))
	case "search":
		query := strings.Join(args[1:], " ")
		if query == "" {
			return coded("usage", "contact search requires a query")
		}
		result, err := provider.SearchContacts(ctx, query)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, result, renderContactSearch(result))
	case "get-or-create-phone", "get-or-create-email", "get-or-create-name":
		contact, key, err := parseContactGetOrCreate(args[0], args[1:])
		if err != nil {
			return err
		}
		var result jmap.Contact
		var created bool
		switch args[0] {
		case "get-or-create-phone":
			result, created, err = provider.GetOrCreateContactByPhone(ctx, key, contact)
		case "get-or-create-email":
			result, created, err = provider.GetOrCreateContactByEmail(ctx, key, contact)
		case "get-or-create-name":
			result, created, err = provider.GetOrCreateContactByName(ctx, key, contact)
		}
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"contact": result, "created": created}, fmt.Sprintf("contact id=%s created=%t\n", result.ID, created))
	default:
		return coded("usage", "unknown contact action "+args[0])
	}
}

func runMailbox(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] != "list" && args[0] != "ls" {
		return coded("usage", "usage: jmap mailbox list")
	}
	mailboxes, err := provider.Mailboxes(ctx)
	if err != nil {
		return err
	}
	return write(stdout, opts.json, mailboxes, renderMailboxes(mailboxes))
}

func runMessage(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap message <query|query-ids|get|create|delete>")
	}
	switch args[0] {
	case "query", "query-ids", "ids":
		fs := newFlagSet("jmap message query")
		mailboxID := fs.String("mailbox-id", "", "mailbox id filter")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ids, err := provider.QueryMessageIDs(ctx, *mailboxID)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"ids": ids}, fmt.Sprintf("messageIds=%d\n", len(ids)))
	case "query-by-mailbox":
		mailboxID := firstArg(args[1:])
		if mailboxID == "" {
			return coded("usage", "message query-by-mailbox requires <mailbox-id>")
		}
		ids, err := provider.QueryMessageIDs(ctx, mailboxID)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"ids": ids, "mailboxId": mailboxID}, fmt.Sprintf("messageIds=%d mailbox=%s\n", len(ids), mailboxID))
	case "get":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "message get requires <message-id>")
		}
		message, err := provider.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, message, fmt.Sprintf("message id=%s subject=%s\n", message.ID, message.Subject))
	case "create":
		fs := newFlagSet("jmap message create")
		mailboxID := fs.String("mailbox-id", "", "mailbox id")
		from := fs.String("from", "", "sender address")
		subject := fs.String("subject", "", "subject")
		messageID := fs.String("message-id", "", "message id")
		body := fs.String("body", "", "plain text body")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *mailboxID == "" || *from == "" || *subject == "" {
			return coded("usage", "message create requires --mailbox-id, --from, and --subject")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "mailboxId": *mailboxID, "subject": *subject}, fmt.Sprintf("would create message subject=%s\n", *subject))
		}
		id, err := provider.CreateMessage(ctx, *mailboxID, *from, *subject, *messageID, *body)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"id": id}, fmt.Sprintf("created message id=%s\n", id))
	case "delete", "rm":
		id := firstArg(args[1:])
		if id == "" {
			return coded("usage", "message delete requires <message-id>")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "delete": id}, fmt.Sprintf("would delete message id=%s\n", id))
		}
		if err := provider.DeleteMessage(ctx, id); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"deleted": id}, fmt.Sprintf("deleted message id=%s\n", id))
	default:
		return coded("usage", "unknown message action "+args[0])
	}
}

func runHours(ctx context.Context, provider *jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap hours <json|ensure>")
	}
	action := args[0]
	config, rest, err := parseScheduleConfig("jmap hours "+action, args[1:])
	if err != nil {
		return err
	}
	_ = rest
	switch action {
	case "json", "list", "show":
		return write(stdout, opts.json, config, renderHours(config))
	case "ensure":
		if provider == nil {
			return coded("missing_config", "hours ensure requires JMAP connection configuration")
		}
		result, err := ensureHours(ctx, *provider, opts, config)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, result, fmt.Sprintf("ensured calendars=%d hoursCreated=%d\n", len(result.Calendars), len(result.CreatedEventIDs)))
	default:
		return coded("usage", "unknown hours action "+action)
	}
}

type ensureHoursResult struct {
	Calendars       []jmap.Calendar `json:"calendars"`
	CreatedEventIDs []string        `json:"createdEventIds"`
	DryRun          bool            `json:"dryRun,omitempty"`
}

func ensureHours(ctx context.Context, provider jmap.Provider, opts options, config schedule.Config) (ensureHoursResult, error) {
	config = config.Normalize()
	var result ensureHoursResult
	for _, name := range append(config.AppointmentCalendars, config.HoursCalendars...) {
		calendar, created, err := provider.GetOrCreateCalendar(ctx, name)
		if err != nil {
			return result, err
		}
		_ = created
		result.Calendars = append(result.Calendars, calendar)
	}
	hoursCal, ok, err := provider.GetCalendarByName(ctx, config.HoursCalendarName)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, coded("not_found", "hours calendar was not created")
	}
	events, err := provider.Events(ctx, []string{hoursCal.ID})
	if err != nil {
		return result, err
	}
	if len(events) > 0 {
		return result, nil
	}
	if opts.dryRun {
		result.DryRun = true
		return result, nil
	}
	loc := config.Location()
	now := time.Now().In(loc)
	for _, window := range config.WeeklyHours {
		start := schedule.NextWeeklyWindowStart(now, window, loc)
		end := schedule.AtClock(start, window.Close, loc)
		if !end.After(start) {
			continue
		}
		title := strings.ToLower(window.Day.String()) + "-hours"
		id, err := provider.CreateEvent(ctx, title, start, end.Sub(start), "business hours", []string{hoursCal.ID}, "weekly")
		if err != nil {
			return result, err
		}
		result.CreatedEventIDs = append(result.CreatedEventIDs, id)
	}
	return result, nil
}

func runSlot(ctx context.Context, provider jmap.Provider, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap slot <list|date|busy|within-hours|conflicts>")
	}
	action := args[0]
	config, rest, err := parseScheduleConfig("jmap slot "+action, args[1:])
	if err != nil {
		return err
	}
	switch action {
	case "list":
		fs := newFlagSet("jmap slot list")
		fromValue := fs.String("from", "", "range start RFC3339")
		toValue := fs.String("to", "", "range end RFC3339")
		durationValue := fs.String("duration", "30m", "slot duration")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		from, err := parseTime(*fromValue)
		if err != nil {
			return err
		}
		to, err := parseTime(*toValue)
		if err != nil {
			return err
		}
		duration, err := parseDuration(*durationValue)
		if err != nil {
			return err
		}
		periods, hoursKeys, apptKeys, err := slotPeriods(ctx, provider, config, from, to)
		if err != nil {
			return err
		}
		slots := schedule.AvailableSlotsRange(from, to, duration, periods, hoursKeys, apptKeys)
		return write(stdout, opts.json, slots, renderSlots(slots))
	case "date", "for-date":
		fs := newFlagSet("jmap slot date")
		date := fs.String("date", "", "local date YYYY-MM-DD")
		durationValue := fs.String("duration", "30m", "slot duration")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *date == "" {
			return coded("usage", "slot date requires --date")
		}
		duration, err := parseDuration(*durationValue)
		if err != nil {
			return err
		}
		loc := config.Location()
		day, err := time.ParseInLocation("2006-01-02", *date, loc)
		if err != nil {
			return err
		}
		periods, hoursKeys, apptKeys, err := slotPeriods(ctx, provider, config, day, day.AddDate(0, 0, 1))
		if err != nil {
			return err
		}
		slots := schedule.AvailableSlotsRange(day, day.AddDate(0, 0, 1), duration, periods, hoursKeys, apptKeys)
		return write(stdout, opts.json, slots, renderSlots(slots))
	case "busy", "is-busy", "within-hours", "conflicts":
		fs := newFlagSet("jmap slot " + action)
		startValue := fs.String("start", "", "start RFC3339")
		durationValue := fs.String("duration", "30m", "duration")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		start, err := parseTime(*startValue)
		if err != nil {
			return err
		}
		duration, err := parseDuration(*durationValue)
		if err != nil {
			return err
		}
		end := start.Add(duration)
		periods, hoursKeys, apptKeys, err := slotPeriods(ctx, provider, config, start, end)
		if err != nil {
			return err
		}
		if action == "conflicts" {
			conflicts := schedule.ConflictingAppointments(periods, apptKeys, start, end)
			return write(stdout, opts.json, conflicts, fmt.Sprintf("conflicts=%d\n", len(conflicts)))
		}
		if action == "within-hours" {
			ok := schedule.IsWithinBusinessHours(periods, hoursKeys, start, end)
			return write(stdout, opts.json, map[string]any{"withinHours": ok}, fmt.Sprintf("withinHours=%t\n", ok))
		}
		busy := schedule.IsBusy(periods, apptKeys, start, end)
		return write(stdout, opts.json, map[string]any{"busy": busy}, fmt.Sprintf("busy=%t\n", busy))
	default:
		return coded("usage", "unknown slot action "+action)
	}
}

func slotPeriods(ctx context.Context, provider jmap.Provider, config schedule.Config, from, to time.Time) ([]schedule.Period, map[string]bool, map[string]bool, error) {
	config = config.Normalize()
	calendars, err := provider.Calendars(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	nameToID := map[string]string{}
	for _, calendar := range calendars {
		nameToID[calendar.Name] = calendar.ID
	}
	hoursIDs := []string{}
	for _, name := range config.HoursCalendars {
		if id := nameToID[name]; id != "" {
			hoursIDs = append(hoursIDs, id)
		}
	}
	apptIDs := []string{}
	for _, name := range config.AppointmentCalendars {
		if id := nameToID[name]; id != "" {
			apptIDs = append(apptIDs, id)
		}
	}
	var hourEvents []jmap.Event
	if len(hoursIDs) > 0 {
		hourEvents, err = provider.Events(ctx, hoursIDs)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	var apptEvents []jmap.Event
	if len(apptIDs) > 0 {
		apptEvents, err = provider.Events(ctx, apptIDs)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	hoursKeys := eventKeys(hourEvents)
	apptKeys := eventKeys(apptEvents)
	busy, err := provider.Availability(ctx, from, to)
	if err != nil {
		return nil, nil, nil, err
	}
	periods := make([]schedule.Period, 0, len(busy))
	for _, item := range busy {
		start, err := item.StartTime()
		if err != nil {
			continue
		}
		end, err := item.EndTime()
		if err != nil {
			continue
		}
		key := item.EventKey()
		kind := "busy"
		if hoursKeys[key] {
			kind = "hours"
		} else if apptKeys[key] {
			kind = "appointment"
		}
		periods = append(periods, schedule.Period{Start: start, End: end, EventID: item.Event.ID, EventUID: item.Event.UID, Title: item.Event.Title, Kind: kind, BusyState: item.BusyStatus})
	}
	return periods, hoursKeys, apptKeys, nil
}

func eventKeys(events []jmap.Event) map[string]bool {
	keys := map[string]bool{}
	for _, event := range events {
		if event.UID != "" {
			keys[event.UID] = true
		}
		if event.ID != "" {
			keys[event.ID] = true
		}
	}
	return keys
}

func runAppointment(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap appointment <services|create|list|get|cancel|dates|times|next|query-exact|waiting-list|notification>")
	}
	store, err := appointmentStore(opts)
	if err != nil {
		return err
	}
	switch args[0] {
	case "services":
		services := appointment.DefaultServices()
		return write(stdout, opts.json, services, renderServices(services))
	case "default-services":
		fs := newFlagSet("jmap appointment default-services")
		contactID := fs.String("contact-id", "", "contact id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		services, err := store.DefaultServicesForContact(*contactID)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, services, renderServices(services))
	case "create":
		return runAppointmentCreate(ctx, opts, store, args[1:], stdout, stderr)
	case "list", "ls":
		fs := newFlagSet("jmap appointment list")
		future := fs.Bool("future", false, "only future appointments")
		contactID := fs.String("contact-id", "", "contact id filter")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var entries []appointment.Entry
		if *future {
			entries, err = store.ListFuture(time.Now(), *contactID)
		} else {
			entries, err = store.ListEntries()
			if *contactID != "" {
				entries = filterEntriesByContact(entries, *contactID)
			}
		}
		if err != nil {
			return err
		}
		return write(stdout, opts.json, entries, renderAppointments(entries))
	case "get":
		eventID := firstArg(args[1:])
		if eventID == "" {
			return coded("usage", "appointment get requires <event-id>")
		}
		entry, ok, err := store.GetEntry(eventID)
		if err != nil {
			return err
		}
		if !ok {
			return coded("not_found", "appointment not found: "+eventID)
		}
		return write(stdout, opts.json, entry, renderAppointments([]appointment.Entry{entry}))
	case "cancel":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		eventID := firstArg(args[1:])
		if eventID == "" {
			return coded("usage", "appointment cancel requires <event-id>")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "cancel": eventID}, fmt.Sprintf("would cancel appointment event=%s\n", eventID))
		}
		if err := provider.DeleteEvent(ctx, eventID); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"cancelled": []string{eventID}}, fmt.Sprintf("cancelled appointment event=%s\n", eventID))
	case "cancel-multiple", "cancel-many":
		provider, err := newProvider(opts, stderr)
		if err != nil {
			return err
		}
		ids := splitCSV(args[1:])
		if len(ids) == 0 {
			return coded("usage", "appointment cancel-multiple requires event ids")
		}
		if opts.dryRun {
			return write(stdout, opts.json, map[string]any{"dryRun": true, "cancel": ids}, fmt.Sprintf("would cancel %d appointments\n", len(ids)))
		}
		for _, id := range ids {
			if err := provider.DeleteEvent(ctx, id); err != nil {
				return err
			}
		}
		return write(stdout, opts.json, map[string]any{"cancelled": ids}, fmt.Sprintf("cancelled %d appointments\n", len(ids)))
	case "update-contact-name":
		fs := newFlagSet("jmap appointment update-contact-name")
		contactID := fs.String("contact-id", "", "contact id")
		name := fs.String("name", "", "new contact display name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *contactID == "" || *name == "" {
			return coded("usage", "update-contact-name requires --contact-id and --name")
		}
		updated, err := store.UpdateContactName(*contactID, *name)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, updated, fmt.Sprintf("updated appointments=%d\n", len(updated)))
	case "dates", "available-dates", "times", "available-times", "times-for-date", "next", "next-available", "query-exact":
		return runAppointmentAvailability(opts, args, stdout)
	case "waiting-list":
		fs := newFlagSet("jmap appointment waiting-list")
		contactID := fs.String("contact-id", "", "contact id")
		var days, times, services stringListFlag
		fs.Var(&days, "date", "requested date YYYY-MM-DD; repeatable")
		fs.Var(&times, "time", "requested local time HH:MM; repeatable")
		fs.Var(&services, "service", "service name; repeatable")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *contactID == "" {
			return coded("usage", "waiting-list requires --contact-id")
		}
		req := appointment.WaitingListRequest{ContactID: *contactID, Days: days, Times: times, ServiceIDs: services, CreatedAt: time.Now().UTC()}
		if err := store.AddWaitingList(req); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"persisted": true, "request": req}, "waiting-list request persisted\n")
	case "notification":
		return runAppointmentNotification(store, opts, args[1:], stdout)
	default:
		return coded("usage", "unknown appointment action "+args[0])
	}
}

func runAppointmentCreate(ctx context.Context, opts options, store *appointment.Store, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("jmap appointment create")
	contactID := fs.String("contact-id", "", "contact id")
	contactName := fs.String("contact-name", "", "contact display name")
	title := fs.String("title", "", "appointment title")
	startValue := fs.String("start", "", "appointment start RFC3339")
	durationValue := fs.String("duration", "", "override duration")
	calendarID := fs.String("calendar-id", "", "appointment calendar id")
	var serviceNames stringListFlag
	fs.Var(&serviceNames, "service", "service name; repeatable")
	config, rest, err := parseScheduleConfig("jmap appointment create", args)
	if err != nil {
		return err
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *contactID == "" {
		return coded("usage", "appointment create requires --contact-id")
	}
	services, err := appointment.SelectServices(serviceNames)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return coded("no_services", "no services selected")
	}
	start, err := parseTime(*startValue)
	if err != nil {
		return err
	}
	duration, err := appointment.ServicesDuration(services)
	if err != nil {
		return err
	}
	if *durationValue != "" {
		duration, err = parseDuration(*durationValue)
		if err != nil {
			return err
		}
	}
	if start.Before(time.Now().Add(-10 * time.Second)) {
		return coded("time_in_past", "appointment start time is in the past")
	}
	if !schedule.IsWithinWeeklyHours(config, start, start.Add(duration)) {
		return coded("not_business_hours", "appointment is outside configured business hours")
	}
	if *title == "" {
		*title = strings.TrimSpace(*contactName + " " + strings.Join(serviceNames, ","))
		if *title == "" {
			*title = "Appointment"
		}
	}
	if opts.dryRun {
		return write(stdout, opts.json, map[string]any{"dryRun": true, "title": *title, "contactId": *contactID, "start": start, "duration": duration.String(), "services": services}, fmt.Sprintf("would book appointment contact=%s start=%s\n", *contactID, start.Format(time.RFC3339)))
	}
	provider, err := newProvider(opts, stderr)
	if err != nil {
		return err
	}
	calendarIDs := []string{}
	if *calendarID != "" {
		calendarIDs = []string{*calendarID}
	} else if cal, ok, err := provider.GetCalendarByName(ctx, config.AppointmentCalendarName); err != nil {
		return err
	} else if ok {
		calendarIDs = []string{cal.ID}
	}
	periods, hoursKeys, apptKeys, err := slotPeriods(ctx, provider, config, start, start.Add(duration))
	if err != nil {
		return err
	}
	if schedule.IsBusy(periods, apptKeys, start, start.Add(duration)) {
		return coded("other_appointment_conflict", "appointment conflicts with an existing appointment")
	}
	if len(hoursKeys) > 0 && !schedule.IsWithinBusinessHours(periods, hoursKeys, start, start.Add(duration)) {
		return coded("not_business_hours", "appointment is outside configured JMAP business hours")
	}
	eventID, err := provider.CreateEvent(ctx, *title, start, duration, "appointment", calendarIDs, "")
	if err != nil {
		return err
	}
	entry := appointment.Entry{EventID: eventID, ContactID: *contactID, ContactName: *contactName, Title: *title, Start: start, Duration: duration.String(), Services: services, CreatedAt: time.Now().UTC()}
	if err := store.PutEntry(entry); err != nil {
		return err
	}
	return write(stdout, opts.json, map[string]any{"status": "booked", "appointment": entry}, fmt.Sprintf("booked appointment event=%s\n", eventID))
}

func runAppointmentAvailability(opts options, args []string, stdout io.Writer) error {
	config, rest, err := parseScheduleConfig("jmap appointment "+args[0], args[1:])
	if err != nil {
		return err
	}
	action := args[0]
	fs := newFlagSet("jmap appointment " + action)
	durationValue := fs.String("duration", "30m", "duration")
	fromValue := fs.String("from", time.Now().Add(20*time.Minute).Format(time.RFC3339), "range start")
	toValue := fs.String("to", time.Now().AddDate(0, 0, config.Normalize().SlotDays).Format(time.RFC3339), "range end")
	date := fs.String("date", "", "local date YYYY-MM-DD")
	limit := fs.Int("limit", 10, "maximum slots")
	var dates, times stringListFlag
	fs.Var(&dates, "exact-date", "requested date for query-exact; repeatable")
	fs.Var(&times, "time", "requested local time HH:MM; repeatable")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	duration, err := parseDuration(*durationValue)
	if err != nil {
		return err
	}
	now := time.Now()
	switch action {
	case "dates", "available-dates":
		result := schedule.AvailableDates(config, now, duration, nil)
		return write(stdout, opts.json, result, fmt.Sprintf("availableDates=%d\n", len(result)))
	case "times", "available-times":
		from, err := parseTime(*fromValue)
		if err != nil {
			return err
		}
		to, err := parseTime(*toValue)
		if err != nil {
			return err
		}
		slots := schedule.AvailableSlots(config, from, to, duration, nil)
		return write(stdout, opts.json, slots, renderSlots(slots))
	case "times-for-date":
		if *date == "" {
			return coded("usage", "times-for-date requires --date")
		}
		slots, err := schedule.AvailableSlotsForLocalDate(config, *date, duration, nil)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, slots, renderSlots(slots))
	case "next", "next-available":
		slots := schedule.NextAvailable(config, now.Add(20*time.Minute), duration, nil, *limit)
		return write(stdout, opts.json, slots, renderSlots(slots))
	case "query-exact":
		if len(dates) == 0 && *date != "" {
			dates = append(dates, *date)
		}
		result := exactAvailability(config, dates, times, duration, *limit)
		return write(stdout, opts.json, result, fmt.Sprintf("available=%d alternates=%d\n", len(result["available"].([]schedule.Slot)), len(result["alternates"].([]schedule.Slot))))
	default:
		return coded("usage", "unknown appointment availability action "+action)
	}
}

func exactAvailability(config schedule.Config, dates, times []string, duration time.Duration, limit int) map[string]any {
	loc := config.Normalize().Location()
	var available []schedule.Slot
	if len(dates) == 0 || len(times) == 0 {
		available = schedule.NextAvailable(config, time.Now().Add(20*time.Minute), duration, nil, limit)
	} else {
		for _, date := range dates {
			for _, tm := range times {
				start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+tm, loc)
				if err != nil {
					continue
				}
				end := start.Add(duration)
				if start.After(time.Now().Add(20*time.Minute)) && schedule.IsWithinWeeklyHours(config, start, end) {
					available = append(available, schedule.Slot{Start: start, End: end})
				}
			}
		}
	}
	alternates := []schedule.Slot{}
	if len(available) == 0 {
		alternates = schedule.NextAvailable(config, time.Now().Add(20*time.Minute), duration, nil, limit)
	}
	return map[string]any{"available": available, "alternates": alternates}
}

func runAppointmentNotification(store *appointment.Store, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return coded("usage", "usage: jmap appointment notification <mark-sent|mark-failed|notify-due|list>")
	}
	switch args[0] {
	case "mark-sent":
		eventID := firstArg(args[1:])
		if eventID == "" {
			return coded("usage", "mark-sent requires <event-id>")
		}
		if err := store.MarkNotificationSent(eventID, time.Now().UTC()); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"eventId": eventID, "marked": "sent"}, fmt.Sprintf("marked sent event=%s\n", eventID))
	case "mark-failed":
		fs := newFlagSet("jmap appointment notification mark-failed")
		eventID := fs.String("event-id", "", "event id")
		message := fs.String("error", "", "failure message")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *eventID == "" && fs.NArg() > 0 {
			*eventID = fs.Arg(0)
		}
		if *eventID == "" {
			return coded("usage", "mark-failed requires <event-id>")
		}
		if err := store.MarkNotificationFailed(*eventID, *message, time.Now().UTC()); err != nil {
			return err
		}
		return write(stdout, opts.json, map[string]any{"eventId": *eventID, "marked": "failed"}, fmt.Sprintf("marked failed event=%s\n", *eventID))
	case "notify-due":
		fs := newFlagSet("jmap appointment notification notify-due")
		withinValue := fs.String("within", "24h", "duration window")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		within, err := parseDuration(*withinValue)
		if err != nil {
			return err
		}
		due, err := store.NotifyDue(time.Now(), within)
		if err != nil {
			return err
		}
		return write(stdout, opts.json, due, fmt.Sprintf("notifyDue=%d\n", len(due)))
	case "list":
		notifications, err := store.Notifications()
		if err != nil {
			return err
		}
		return write(stdout, opts.json, notifications, fmt.Sprintf("notifications=%d\n", len(notifications)))
	default:
		return coded("usage", "unknown notification action "+args[0])
	}
}

func parseScheduleConfig(name string, args []string) (schedule.Config, []string, error) {
	_ = name
	config := schedule.DefaultConfig()
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			rest = append(rest, arg)
			continue
		}
		nameValue := strings.TrimPrefix(arg, "--")
		flagName, value, hasValue := strings.Cut(nameValue, "=")
		consumeValue := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag --%s requires a value", flagName)
			}
			i++
			return args[i], nil
		}
		switch flagName {
		case "appt-calendar":
			v, err := consumeValue()
			if err != nil {
				return config, nil, err
			}
			config.AppointmentCalendarName = v
			config.AppointmentCalendars = []string{v}
		case "hours-calendar":
			v, err := consumeValue()
			if err != nil {
				return config, nil, err
			}
			config.HoursCalendarName = v
			config.HoursCalendars = []string{v}
		case "slot-days":
			v, err := consumeValue()
			if err != nil {
				return config, nil, err
			}
			parsed, err := strconv.Atoi(v)
			if err != nil {
				return config, nil, fmt.Errorf("invalid --slot-days: %w", err)
			}
			config.SlotDays = parsed
		case "timezone":
			v, err := consumeValue()
			if err != nil {
				return config, nil, err
			}
			config.TimeZone = v
		case "hours":
			v, err := consumeValue()
			if err != nil {
				return config, nil, err
			}
			weekly, err := schedule.ParseWeeklyHours(v)
			if err != nil {
				return config, nil, err
			}
			config.WeeklyHours = weekly
		default:
			rest = append(rest, arg)
		}
	}
	return config.Normalize(), rest, nil
}

func appointmentStore(opts options) (*appointment.Store, error) {
	return appointment.NewStore(opts.stateRoot, appointment.AccountKey(opts.url, opts.user))
}

func parseContactFlags(name string, args []string) (jmap.Contact, error) {
	fs := newFlagSet(name)
	first := fs.String("first-name", "", "first name")
	last := fs.String("last-name", "", "last name")
	company := fs.String("company", "", "company")
	var emails, phones, addresses stringListFlag
	fs.Var(&emails, "email", "email value; repeatable")
	fs.Var(&phones, "phone", "phone value; repeatable")
	fs.Var(&addresses, "address", "address value; repeatable")
	if err := fs.Parse(args); err != nil {
		return jmap.Contact{}, err
	}
	contact := jmap.Contact{FirstName: *first, LastName: *last, Company: *company}
	for _, email := range emails {
		contact.Emails = append(contact.Emails, jmap.Email{Type: "personal", Value: email})
	}
	for _, phone := range phones {
		contact.Phones = append(contact.Phones, jmap.Phone{Type: "home", Value: phone})
	}
	for _, address := range addresses {
		contact.Addresses = append(contact.Addresses, jmap.Address{Type: "home", Value: address})
	}
	return contact, nil
}

func parseContactGetOrCreate(action string, args []string) (jmap.Contact, string, error) {
	fs := newFlagSet("jmap contact " + action)
	value := fs.String("value", "", "lookup value")
	first := fs.String("first-name", "", "first name")
	last := fs.String("last-name", "", "last name")
	company := fs.String("company", "", "company")
	var emails, phones stringListFlag
	fs.Var(&emails, "email", "email value; repeatable")
	fs.Var(&phones, "phone", "phone value; repeatable")
	if err := fs.Parse(args); err != nil {
		return jmap.Contact{}, "", err
	}
	if *value == "" && fs.NArg() > 0 {
		*value = fs.Arg(0)
	}
	if *value == "" {
		return jmap.Contact{}, "", coded("usage", action+" requires --value")
	}
	contact := jmap.Contact{FirstName: *first, LastName: *last, Company: *company}
	for _, email := range emails {
		contact.Emails = append(contact.Emails, jmap.Email{Type: "personal", Value: email})
	}
	for _, phone := range phones {
		contact.Phones = append(contact.Phones, jmap.Phone{Type: "home", Value: phone})
	}
	return contact, *value, nil
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, coded("usage", "time value is required")
	}
	return jmap.ParseTime(value, time.UTC)
}

func parseDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, coded("usage", "duration value is required")
	}
	return jmap.ParseDuration(value)
}

type stringListFlag []string

func (s *stringListFlag) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			*s = append(*s, item)
		}
	}
	return nil
}

func (s *stringListFlag) String() string { return strings.Join(*s, ",") }

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func write(out io.Writer, asJSON bool, value any, human string) error {
	return writeYAML(out, value)
}

func emitError(opts options, stdout, stderr io.Writer, err error) {
	if err == nil {
		return
	}
	msg := jmap.Redact(err.Error(), opts.password)
	ce := commandError{Code: "command_failed", Message: msg}
	var codedErr commandError
	if errors.As(err, &codedErr) {
		ce = commandError{Code: codedErr.Code, Message: jmap.Redact(codedErr.Message, opts.password)}
	}
	_ = writeYAML(stderr, errorEnvelope{Error: ce})
}

func writeYAML(out io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	data, err = yaml.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

func coded(code, message string) error { return commandError{Code: code, Message: message} }

func requireForce(opts options, message string) error {
	if opts.force {
		return nil
	}
	return coded("force_required", message)
}

func boolEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func firstArg(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func jsonBody(flagBody string, args []string) string {
	if strings.TrimSpace(flagBody) != "" {
		return flagBody
	}
	if len(args) > 0 {
		return strings.Join(args, " ")
	}
	return ""
}

func splitCSV(args []string) []string {
	var out []string
	for _, arg := range args {
		for _, item := range strings.Split(arg, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func filterEntriesByContact(entries []appointment.Entry, contactID string) []appointment.Entry {
	var out []appointment.Entry
	for _, entry := range entries {
		if entry.ContactID == contactID {
			out = append(out, entry)
		}
	}
	return out
}

func renderCalendars(calendars []jmap.Calendar) string {
	if len(calendars) == 0 {
		return "no calendars\n"
	}
	var b strings.Builder
	for _, calendar := range calendars {
		fmt.Fprintf(&b, "calendar id=%s name=%s\n", calendar.ID, calendar.Name)
	}
	return b.String()
}

func renderEvents(events []jmap.Event) string {
	if len(events) == 0 {
		return "no events\n"
	}
	var b strings.Builder
	for _, event := range events {
		fmt.Fprintf(&b, "event id=%s title=%s start=%s duration=%s\n", event.ID, event.Title, event.UTCStart, event.Duration)
	}
	return b.String()
}

func renderEvent(event jmap.Event) string {
	return fmt.Sprintf("event id=%s title=%s start=%s duration=%s\n", event.ID, event.Title, event.UTCStart, event.Duration)
}

func renderContacts(contacts []jmap.Contact) string {
	if len(contacts) == 0 {
		return "no contacts\n"
	}
	var b strings.Builder
	for _, contact := range contacts {
		fmt.Fprintf(&b, "contact id=%s name=%s\n", contact.ID, contact.DisplayName())
	}
	return b.String()
}

func renderContact(contact jmap.Contact) string {
	return fmt.Sprintf("contact id=%s name=%s\n", contact.ID, contact.DisplayName())
}

func renderContactSearch(result jmap.ContactSearchResult) string {
	switch result.Status {
	case "exact":
		return fmt.Sprintf("exact contact id=%s\n", result.Contact.ID)
	case "multiple":
		return fmt.Sprintf("multiple contacts=%d\n", len(result.Contacts))
	default:
		return "no matches\n"
	}
}

func renderMailboxes(mailboxes []jmap.Mailbox) string {
	if len(mailboxes) == 0 {
		return "no mailboxes\n"
	}
	var b strings.Builder
	for _, mailbox := range mailboxes {
		fmt.Fprintf(&b, "mailbox id=%s name=%s total=%d unread=%d\n", mailbox.ID, mailbox.Name, mailbox.TotalEmails, mailbox.UnreadEmails)
	}
	return b.String()
}

func renderHours(config schedule.Config) string {
	config = config.Normalize()
	var b strings.Builder
	fmt.Fprintf(&b, "appointmentCalendar=%s hoursCalendar=%s slotDays=%d timezone=%s\n", config.AppointmentCalendarName, config.HoursCalendarName, config.SlotDays, config.TimeZone)
	for _, window := range config.WeeklyHours {
		fmt.Fprintf(&b, "%s %02d:%02d-%02d:%02d\n", strings.ToLower(window.Day.String()), window.Open.Hour, window.Open.Minute, window.Close.Hour, window.Close.Minute)
	}
	return b.String()
}

func renderSlots(slots []schedule.Slot) string {
	if len(slots) == 0 {
		return "no slots\n"
	}
	var b strings.Builder
	for _, slot := range slots {
		fmt.Fprintf(&b, "slot start=%s end=%s\n", slot.Start.Format(time.RFC3339), slot.End.Format(time.RFC3339))
	}
	return b.String()
}

func renderServices(services []appointment.Service) string {
	var b strings.Builder
	for _, service := range services {
		fmt.Fprintf(&b, "service name=%s duration=%s cost=%d display=%t\n", service.Name, service.Duration, service.CostDollars, service.Display)
	}
	return b.String()
}

func renderAppointments(entries []appointment.Entry) string {
	if len(entries) == 0 {
		return "no appointments\n"
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Start.Before(entries[j].Start) })
	var b strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&b, "appointment event=%s contact=%s start=%s title=%s\n", entry.EventID, entry.ContactID, entry.Start.Format(time.RFC3339), entry.Title)
	}
	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func printUsage(out io.Writer) error {
	return writeYAML(out, map[string]any{
		"tool":    "jmap",
		"summary": "Pure Go JMAP/Cyrus CLI for calendars, contacts, mail, scheduling, and appointments",
		"usage":   "jmap [global flags] <command> [command args]",
		"flags": []map[string]string{
			{"name": "--url URL", "summary": "JMAP base URL; default JMAP_URL"},
			{"name": "--user USER", "summary": "JMAP account/username; default JMAP_USER"},
			{"name": "--password PASS", "summary": "JMAP password; default JMAP_PASSWORD"},
			{"name": "--timeout D", "summary": "request timeout"},
			{"name": "--trace", "summary": "write redacted HTTP traces to stderr"},
			{"name": "--state-root DIR", "summary": "local appointment state root"},
			{"name": "--dry-run", "summary": "preview mutating commands where possible"},
			{"name": "--force", "summary": "confirm destructive bulk commands"},
		},
		"commands": []map[string]string{
			{"name": "check", "summary": "verify JMAP connection", "schema": "check"},
			{"name": "raw", "summary": "send a raw JMAP method call; --params remains JSON input", "schema": "raw"},
			{"name": "calendar", "summary": "calendar commands", "schema": "calendar"},
			{"name": "event", "summary": "event commands", "schema": "event"},
			{"name": "contact", "summary": "contact commands", "schema": "contact"},
			{"name": "mailbox", "summary": "mailbox commands", "schema": "mailbox"},
			{"name": "message", "summary": "message commands", "schema": "message"},
			{"name": "hours", "summary": "business-hours helpers", "schema": "hours"},
			{"name": "slot", "summary": "availability slot helpers", "schema": "slot"},
			{"name": "appointment", "summary": "appointment workflow helpers", "schema": "appointment"},
			{"name": "schemas", "summary": "list output schemas", "schema": "schemas"},
		},
		"schemas": []string{"help", "schemas", "error", "check", "raw", "calendar", "event", "contact", "mailbox", "message", "hours", "slot", "appointment"},
	})
}
