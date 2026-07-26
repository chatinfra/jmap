package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONFlagRejectedWithYAMLError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--json"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected --json to be rejected")
	}
	doc := requireYAMLError(t, stdout.String(), stderr.String(), "error.schema.yaml")
	envelope, ok := doc.(map[string]any)["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing: %#v", doc)
	}
	code, _ := envelope["code"].(string)
	message, _ := envelope["message"].(string)
	if code == "" || !strings.Contains(message, "--json") {
		t.Fatalf("unsupported --json envelope = %#v", envelope)
	}
}

func TestRootHelpHasHumanReadableSections(t *testing.T) {
	baselineStdout, baselineStderr, err := runCLI(t)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if baselineStderr != "" {
		t.Fatalf("stderr = %q; want empty stderr", baselineStderr)
	}

	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		stdout, stderr, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
		if stderr != "" {
			t.Fatalf("Run(%v) stderr = %q; want empty stderr", args, stderr)
		}
		if stdout != baselineStdout {
			t.Fatalf("Run(%v) stdout differs from bare help\n--- bare ---\n%s\n--- got ---\n%s", args, baselineStdout, stdout)
		}
	}

	for _, want := range []string{"USAGE", "COMMANDS", "FLAGS", "OUTPUT", "EXAMPLES", "SEE ALSO"} {
		if !strings.Contains(baselineStdout, "\n"+want+"\n") {
			t.Fatalf("root help missing section %q:\n%s", want, baselineStdout)
		}
	}
	for _, forbidden := range []string{"\ncommands:\n", "\nflags:\n", "\nschemas:\n"} {
		if strings.Contains(baselineStdout, forbidden) {
			t.Fatalf("root help still looks like YAML discovery; contains %q:\n%s", forbidden, baselineStdout)
		}
	}
}

func TestRootHelpListsCommandsDefaultsAndSchemasPointer(t *testing.T) {
	stdout, stderr, err := runCLI(t, "help")
	if err != nil {
		t.Fatalf("Run(help) error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q; want empty stderr", stderr)
	}

	for _, command := range []string{"check", "raw", "calendar", "event", "availability", "principal", "participant", "addressbook", "contact", "mailbox", "message", "hours", "slot", "appointment", "schemas"} {
		if !strings.Contains(stdout, "\n  "+command) {
			t.Fatalf("root help missing command %q:\n%s", command, stdout)
		}
	}
	for _, want := range []string{"--url URL", "JMAP_URL", "--user USER", "JMAP_USER", "--password PASS", "JMAP_PASSWORD", "--timeout DURATION", "JMAP_TIMEOUT", "--trace", "JMAP_TRACE", "--state-root DIR", "JMAP_STATE_ROOT", "--dry-run", "JMAP_DRY_RUN", "--force", "JMAP_FORCE"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("root help missing %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{"stdout:", "stderr:", "jmap schemas"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("root help missing output/discovery text %q:\n%s", want, stdout)
		}
	}
}

func TestCommandHelpDoesNotRequireConfig(t *testing.T) {
	clearJMAPEnv(t)
	for _, args := range [][]string{
		{"help", "calendar"},
		{"help", "calendar", "create"},
		{"help", "raw", "call"},
		{"event", "query", "--help"},
		{"contact", "create", "--help"},
		{"message", "create", "-h"},
		{"slot", "list", "--help"},
		{"appointment", "create", "--help"},
		{"appointment", "notification", "--help"},
	} {
		stdout, stderr, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("Run(%v) error = %v stderr=%s", args, err, stderr)
		}
		if stderr != "" {
			t.Fatalf("Run(%v) stderr = %q; want empty stderr", args, stderr)
		}
		for _, want := range []string{"USAGE", "OUTPUT", "EXAMPLES"} {
			if !strings.Contains(stdout, "\n"+want+"\n") {
				t.Fatalf("Run(%v) help missing %s:\n%s", args, want, stdout)
			}
		}
		if strings.Contains(stdout, "missing required configuration") || strings.Contains(stderr, "missing_config") {
			t.Fatalf("Run(%v) attempted configuration-dependent execution stdout=%q stderr=%q", args, stdout, stderr)
		}
	}
}

func TestCommandHelpPluralAliasesAndHelpForms(t *testing.T) {
	clearJMAPEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"calendars", "--help"}, want: "jmap calendar -"},
		{args: []string{"events", "-h"}, want: "jmap event -"},
		{args: []string{"contacts", "--help"}, want: "jmap contact -"},
		{args: []string{"mailboxes", "--help"}, want: "jmap mailbox -"},
		{args: []string{"messages", "--help"}, want: "jmap message -"},
		{args: []string{"slots", "--help"}, want: "jmap slot -"},
		{args: []string{"appointments", "--help"}, want: "jmap appointment -"},
	} {
		stdout, stderr, err := runCLI(t, tc.args...)
		if err != nil {
			t.Fatalf("Run(%v) error = %v stderr=%s", tc.args, err, stderr)
		}
		if stderr != "" {
			t.Fatalf("Run(%v) stderr = %q; want empty stderr", tc.args, stderr)
		}
		if !strings.Contains(stdout, tc.want) {
			t.Fatalf("Run(%v) stdout missing %q:\n%s", tc.args, tc.want, stdout)
		}
	}
}

func TestUnknownHelpTopicUsesYAMLErrorEnvelope(t *testing.T) {
	stdout, stderr, err := runCLI(t, "help", "bogus")
	if err == nil {
		t.Fatal("expected unknown help topic error")
	}
	doc := requireYAMLError(t, stdout, stderr, "error.schema.yaml")
	envelope := doc.(map[string]any)["error"].(map[string]any)
	if envelope["code"] != "unknown_help_topic" {
		t.Fatalf("error envelope = %#v", envelope)
	}
	message, _ := envelope["message"].(string)
	if !strings.Contains(message, "jmap help") {
		t.Fatalf("error message missing jmap help hint: %#v", envelope)
	}
}

func TestSchemasCommandRemainsStructuredDiscovery(t *testing.T) {
	stdout, stderr, err := runCLI(t, "schemas")
	if err != nil {
		t.Fatalf("Run(schemas) error = %v", err)
	}
	doc := requireYAMLStdout(t, stdout, stderr, "schemas.schema.yaml").(map[string]any)
	schemas, ok := doc["schemas"].([]any)
	if !ok {
		t.Fatalf("schemas payload = %#v", doc)
	}
	ids := map[string]bool{}
	for _, item := range schemas {
		schema, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("schema item = %#v", item)
		}
		id, _ := schema["id"].(string)
		ids[id] = true
	}
	requireSchemaDiscoveryPaths(t, doc)
	for _, want := range []string{"schemas", "error", "check", "raw", "calendar", "event", "availability", "principal", "participant", "addressbook", "contact", "mailbox", "message", "hours", "slot", "appointment"} {
		if !ids[want] {
			t.Fatalf("schemas discovery missing id %q: %#v", want, ids)
		}
	}
	if ids["help"] {
		t.Fatalf("schemas discovery advertises legacy default-help schema: %#v", ids)
	}
}

func TestYAMLErrorEnvelopeForMissingConfig(t *testing.T) {
	t.Setenv("JMAP_URL", "")
	t.Setenv("JMAP_USER", "")
	t.Setenv("JMAP_PASSWORD", "")
	var stdout, stderr bytes.Buffer
	err := Run([]string{"check"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	doc := requireYAMLError(t, stdout.String(), stderr.String(), "error.schema.yaml")
	envelope := doc.(map[string]any)["error"].(map[string]any)
	if envelope["code"] != "missing_config" {
		t.Fatalf("error envelope = %#v", envelope)
	}
}

func TestEnvFallbackAndCheckCommandRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "secret" {
			t.Fatalf("bad auth user=%q pass=%q ok=%t", user, pass, ok)
		}
		var req struct {
			MethodCalls []json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.MethodCalls) != 1 || !bytes.Contains(req.MethodCalls[0], []byte("Calendar/get")) {
			t.Fatalf("bad request: %#v", req.MethodCalls)
		}
		_, _ = w.Write([]byte(`{"methodResponses":[["Calendar/get",{"list":[]},"c1"]]}`))
	}))
	defer server.Close()
	t.Setenv("JMAP_URL", server.URL)
	t.Setenv("JMAP_USER", "alice")
	t.Setenv("JMAP_PASSWORD", "secret")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"check"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v stderr=%s", err, stderr.String())
	}
	result := requireYAMLStdout(t, stdout.String(), stderr.String(), "check.schema.yaml").(map[string]any)
	if result["connected"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestJMAPHTTPErrorRedactsPassword(t *testing.T) {
	const secret = "super-secret-password"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad password "+secret, http.StatusUnauthorized)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--url", server.URL, "--user", "alice", "--password", secret, "check"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	requireYAMLError(t, stdout.String(), stderr.String(), "error.schema.yaml")
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr leaked password: %s", stderr.String())
	}
}

func TestForceRequiredBeforeBulkDelete(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--url", "https://example.test", "--user", "alice", "--password", "secret", "event", "delete-all"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected force error")
	}
	doc := requireYAMLError(t, stdout.String(), stderr.String(), "error.schema.yaml")
	envelope := doc.(map[string]any)["error"].(map[string]any)
	if envelope["code"] != "force_required" {
		t.Fatalf("error envelope = %#v", envelope)
	}
}

func TestDryRunMutationEmitsPreviewWithoutNetwork(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--dry-run", "--url", "https://example.test", "--user", "alice", "--password", "secret", "calendar", "create", "--name", "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run() error = %v stderr=%s", err, stderr.String())
	}
	result := requireYAMLStdout(t, stdout.String(), stderr.String(), "calendar.schema.yaml").(map[string]any)
	if result["dryRun"] != true || result["name"] != "demo" {
		t.Fatalf("result = %#v", result)
	}
}

func TestAppointmentWaitingListUsesStateRoot(t *testing.T) {
	root := testTempRoot(t)
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--state-root", root, "appointment", "waiting-list", "--contact-id", "contact-1", "--date", "2026-01-05", "--time", "10:00", "--service", "service2"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run() error = %v stderr=%s", err, stderr.String())
	}
	result := requireYAMLStdout(t, stdout.String(), stderr.String(), "appointment.schema.yaml").(map[string]any)
	if result["persisted"] != true {
		t.Fatalf("result = %#v", result)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("state files = %d, want 1", len(entries))
	}
}

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func clearJMAPEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"JMAP_URL", "JMAP_USER", "JMAP_PASSWORD", "JMAP_TIMEOUT", "JMAP_TRACE", "JMAP_STATE_ROOT", "JMAP_DRY_RUN", "JMAP_FORCE"} {
		t.Setenv(name, "")
	}
}

func testTempRoot(t *testing.T) string {
	t.Helper()
	base := os.Getenv("SUPER_TMP_DIR")
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(filepath.Join(wd, "AGENTS.md")); err == nil {
				base = filepath.Join(wd, "tmp")
				break
			}
			parent := filepath.Dir(wd)
			if parent == wd {
				base = filepath.Join(".", "tmp")
				break
			}
			wd = parent
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "jmap-cli-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
