package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSendsBasicAuthEnvelopeAndRedactedTrace(t *testing.T) {
	var got Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jmap" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "secret" {
			t.Fatalf("bad basic auth user=%q pass=%q ok=%t", user, pass, ok)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"methodResponses":[["Calendar/get",{"list":[]},"c1"]]}`))
	}))
	defer server.Close()

	var trace bytes.Buffer
	client := NewClient(Config{BaseURL: server.URL, Username: "alice", Password: "secret", Timeout: time.Second, Trace: true, TraceWriter: &trace})
	resp, err := client.Call(context.Background(), "Calendar/get", map[string]any{"ids": nil}, CapabilityCalendars)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if resp.Name != "Calendar/get" {
		t.Fatalf("response name = %s", resp.Name)
	}
	if len(got.MethodCalls) != 1 || got.MethodCalls[0].Name != "Calendar/get" {
		t.Fatalf("method calls = %#v", got.MethodCalls)
	}
	if !contains(got.Using, CapabilityCore) || !contains(got.Using, CapabilityCalendars) {
		t.Fatalf("capabilities = %#v", got.Using)
	}
	if strings.Contains(trace.String(), "secret") {
		t.Fatalf("trace leaked password: %s", trace.String())
	}
	if !strings.Contains(trace.String(), "Authorization: <redacted>") {
		t.Fatalf("trace did not redact authorization: %s", trace.String())
	}
}

func TestClientReturnsJMAPMethodError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"methodResponses":[["error",{"type":"invalidArguments","description":"bad params"},"c1"]]}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Username: "alice", Password: "secret", Timeout: time.Second})
	_, err := client.Call(context.Background(), "Calendar/get", map[string]any{"ids": nil})
	if err == nil {
		t.Fatal("expected method error")
	}
	if !strings.Contains(err.Error(), "invalidArguments") {
		t.Fatalf("error = %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
