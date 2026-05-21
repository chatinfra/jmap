package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCreatesSessionSubmitsPromptAndWaitsForCompletion(t *testing.T) {
	prompted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			if r.Method != http.MethodPost {
				t.Fatalf("create method=%s", r.Method)
			}
			if got := r.URL.Query().Get("directory"); got != "/repo" {
				t.Fatalf("create directory=%q", got)
			}
			respondJSON(t, w, map[string]string{"id": "ses-1"})
		case "/api/session/ses-1/prompt":
			if r.Method != http.MethodPost {
				t.Fatalf("prompt method=%s", r.Method)
			}
			if got := r.URL.Query().Get("directory"); got != "/repo" {
				t.Fatalf("prompt directory=%q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode prompt body: %v", err)
			}
			prompt := body["prompt"].(map[string]any)
			if prompt["text"] != "hello" {
				t.Fatalf("prompt=%v", prompt)
			}
			agents := prompt["agents"].([]any)
			agent := agents[0].(map[string]any)
			if agent["name"] != "agent-1" {
				t.Fatalf("agents=%v", agents)
			}
			if body["delivery"] != "immediate" {
				t.Fatalf("delivery=%v", body["delivery"])
			}
			close(prompted)
			respondJSON(t, w, map[string]bool{"queued": true})
		case "/event":
			if got := r.URL.Query().Get("directory"); got != "/repo" {
				t.Fatalf("event directory=%q", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			w.WriteHeader(http.StatusOK)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-prompted:
			case <-r.Context().Done():
				return
			}
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"message.part.updated\",\"properties\":{\"sessionID\":\"ses-1\",\"part\":{\"id\":\"prt-1\",\"sessionID\":\"ses-1\",\"messageID\":\"msg-1\",\"type\":\"text\",\"text\":\"answer\"},\"time\":1}}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"ses-1\",\"info\":{\"id\":\"msg-1\",\"sessionID\":\"ses-1\",\"role\":\"assistant\",\"time\":{\"created\":1,\"completed\":2}}}}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Directory: "/repo", Agent: "agent-1", PromptTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "ses-1" {
		t.Fatalf("session=%+v", session)
	}
	response, err := client.Prompt(context.Background(), session.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if response.SessionID != "ses-1" || response.MessageID != "msg-1" || response.Text != "answer" {
		t.Fatalf("response=%+v", response)
	}
}

func TestClientFallsBackToLegacySessionAndMessageEndpoints(t *testing.T) {
	prompted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case "/session":
			if got := r.URL.Query().Get("directory"); got != "/repo" {
				t.Fatalf("legacy create directory=%q", got)
			}
			respondJSON(t, w, map[string]string{"id": "ses-1"})
		case "/api/session/ses-1/prompt":
			http.NotFound(w, r)
		case "/session/ses-1/message":
			if got := r.URL.Query().Get("directory"); got != "/repo" {
				t.Fatalf("legacy prompt directory=%q", got)
			}
			close(prompted)
			respondJSON(t, w, map[string]bool{"queued": true})
		case "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-prompted:
			case <-r.Context().Done():
				return
			}
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"session.next.text.ended\",\"properties\":{\"sessionID\":\"ses-1\",\"text\":\"answer\"}}\n\n")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Directory: "/repo", Agent: "agent-1", PromptTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Prompt(context.Background(), session.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "answer" {
		t.Fatalf("text=%q", response.Text)
	}
}

func TestClientHandlesVersionedSessionNextTextEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/session/ses-1/prompt":
			respondJSON(t, w, map[string]bool{"queued": true})
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"session.next.text.delta.1\",\"properties\":{\"sessionID\":\"other\",\"delta\":\"skip\"}}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"session.next.text.delta.1\",\"properties\":{\"sessionID\":\"ses-1\",\"delta\":\"hel\"}}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"session.next.text.ended.1\",\"properties\":{\"sessionID\":\"ses-1\",\"text\":\"hello\"}}\n\n")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Directory: "/repo", PromptTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Prompt(context.Background(), "ses-1", "go")
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "hello" {
		t.Fatalf("text=%q", response.Text)
	}
}

func TestClientSurfacesStaleSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ses-old/prompt" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		http.Error(w, `{"message":"session not found"}`, http.StatusNotFound)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Directory: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	err = client.SubmitPrompt(context.Background(), "ses-old", "hello")
	if !IsStaleSession(err) {
		t.Fatalf("IsStaleSession=false err=%v", err)
	}
	var stale *StaleSessionError
	if !errors.As(err, &stale) || stale.SessionID != "ses-old" {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
}

func TestClientReturnsEventErrorAndTimeout(t *testing.T) {
	t.Run("event error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/session/ses-1/prompt":
				respondJSON(t, w, map[string]bool{"queued": true})
			case "/event":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"session.error\",\"properties\":{\"sessionID\":\"ses-1\",\"error\":{\"message\":\"boom\"}}}\n\n")
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Directory: "/repo", PromptTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Prompt(context.Background(), "ses-1", "hello")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/session/ses-1/prompt":
				respondJSON(t, w, map[string]bool{"queued": true})
			case "/event":
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-r.Context().Done()
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Directory: "/repo", PromptTimeout: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Prompt(context.Background(), "ses-1", "hello")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
}

func respondJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
