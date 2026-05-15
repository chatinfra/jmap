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

func TestHelpOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	requireYAMLStdout(t, stdout.String(), stderr.String(), "help.schema.yaml")
	stdout.Reset()
	stderr.Reset()
	if err := Run([]string{"schemas"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	requireYAMLStdout(t, stdout.String(), stderr.String(), "schemas.schema.yaml")
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
