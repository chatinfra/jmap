package jmap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const DefaultPollingInterval = 60 * time.Second

type StateChange struct {
	AccountID string          `json:"accountId,omitempty"`
	Types     []string        `json:"types,omitempty"`
	State     map[string]any  `json:"state,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

func (c *Client) ProbePush(ctx context.Context) (SessionInfo, bool, error) {
	info, err := c.Session(ctx)
	if err != nil {
		return SessionInfo{}, false, err
	}
	return info, strings.TrimSpace(info.EventSourceURL) != "", nil
}

func (c *Client) SubscribeStateChanges(ctx context.Context, info SessionInfo, accountID string, dataTypes []string) (<-chan StateChange, <-chan error, func(), error) {
	target, err := c.eventSourceURL(info, dataTypes)
	if err != nil {
		return nil, nil, func() {}, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, target, nil)
	if err != nil {
		cancel()
		return nil, nil, func() {}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.SetBasicAuth(c.config.Username, c.config.Password)
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, nil, func() {}, fmt.Errorf("connect to JMAP EventSource: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		return nil, nil, func() {}, fmt.Errorf("JMAP EventSource HTTP %s: %s", resp.Status, Redact(string(body), c.config.Password))
	}
	out := make(chan StateChange, 16)
	errs := make(chan error, 1)
	allowed := map[string]bool{}
	for _, dataType := range dataTypes {
		allowed[dataType] = true
	}
	go func() {
		defer close(out)
		defer close(errs)
		defer resp.Body.Close()
		err := scanEventSource(resp.Body, func(data []byte) error {
			change, ok, err := parseStateChange(data, accountID, allowed)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			select {
			case <-streamCtx.Done():
				return streamCtx.Err()
			case out <- change:
				return nil
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			errs <- err
			return
		}
		if ctxErr := streamCtx.Err(); ctxErr != nil && !errors.Is(ctxErr, context.Canceled) {
			errs <- ctxErr
		}
	}()
	return out, errs, cancel, nil
}

func (c *Client) eventSourceURL(info SessionInfo, dataTypes []string) (string, error) {
	raw := strings.TrimSpace(info.EventSourceURL)
	if raw == "" {
		return "", errors.New("JMAP session does not advertise eventSourceUrl")
	}
	if strings.Contains(raw, "{types}") {
		raw = strings.ReplaceAll(raw, "{types}", url.QueryEscape(strings.Join(dataTypes, ",")))
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() {
		base, err := url.Parse(strings.TrimRight(c.config.BaseURL, "/") + "/")
		if err != nil {
			return "", err
		}
		parsed = base.ResolveReference(parsed)
	}
	q := parsed.Query()
	if len(dataTypes) > 0 && q.Get("types") == "" && !strings.Contains(info.EventSourceURL, "{types}") {
		q.Set("types", strings.Join(dataTypes, ","))
	}
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func scanEventSource(r io.Reader, fn func([]byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := []byte(strings.Join(dataLines, "\n"))
		dataLines = nil
		if fn == nil {
			return nil
		}
		return fn(data)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		if ok && field == "data" {
			dataLines = append(dataLines, value)
		}
	}
	if err := dispatch(); err != nil {
		return err
	}
	return scanner.Err()
}

func parseStateChange(data []byte, accountID string, allowed map[string]bool) (StateChange, bool, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return StateChange{}, false, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return StateChange{}, false, fmt.Errorf("decode JMAP StateChange: %w", err)
	}
	if nested, ok := objectMap(obj["data"]); ok {
		obj = nested
	}
	change := StateChange{State: map[string]any{}, Raw: append([]byte(nil), data...)}
	if id, ok := obj["accountId"].(string); ok {
		change.AccountID = id
	}
	state := map[string]any{}
	if changed, ok := objectMap(obj["changed"]); ok {
		if accountID != "" {
			if accountState, ok := objectMap(changed[accountID]); ok {
				change.AccountID = accountID
				state = accountState
			}
		}
		if len(state) == 0 {
			for id, value := range changed {
				if accountState, ok := objectMap(value); ok {
					change.AccountID = id
					state = accountState
					break
				}
			}
		}
	}
	if len(state) == 0 {
		if stateObj, ok := objectMap(obj["state"]); ok {
			state = stateObj
		}
	}
	if len(state) == 0 {
		if dataTypes, ok := stringSlice(obj["types"]); ok {
			for _, dataType := range dataTypes {
				state[dataType] = true
			}
		}
	}
	if len(state) == 0 {
		return StateChange{}, false, nil
	}
	for dataType, value := range state {
		if len(allowed) > 0 && !allowed[dataType] {
			continue
		}
		change.Types = append(change.Types, dataType)
		change.State[dataType] = value
	}
	if len(change.Types) == 0 {
		return StateChange{}, false, nil
	}
	sort.Strings(change.Types)
	return change, true, nil
}

func objectMap(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	return obj, ok
}

func stringSlice(value any) ([]string, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if ok && text != "" {
			out = append(out, text)
		}
	}
	return out, true
}
