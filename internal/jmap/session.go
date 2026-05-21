package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type SessionInfo struct {
	Accounts        map[string]AccountInfo `json:"accounts,omitempty"`
	PrimaryAccounts map[string]string      `json:"primaryAccounts,omitempty"`
	APIURL          string                 `json:"apiUrl,omitempty"`
	DownloadURL     string                 `json:"downloadUrl,omitempty"`
	UploadURL       string                 `json:"uploadUrl,omitempty"`
	EventSourceURL  string                 `json:"eventSourceUrl,omitempty"`
	State           string                 `json:"state,omitempty"`
	Raw             map[string]any         `json:"-"`
}

type AccountInfo struct {
	Name         string         `json:"name,omitempty"`
	IsPersonal   bool           `json:"isPersonal,omitempty"`
	IsReadOnly   bool           `json:"isReadOnly,omitempty"`
	AccountCaps  map[string]any `json:"accountCapabilities,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}

func (c *Client) Session(ctx context.Context) (SessionInfo, error) {
	var firstErr error
	for _, endpoint := range c.sessionEndpointCandidates() {
		info, err := c.getSession(ctx, endpoint)
		if err == nil {
			return info, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !isSessionEndpointMiss(err) {
			return SessionInfo{}, err
		}
	}
	if firstErr == nil {
		firstErr = errors.New("no JMAP session endpoint candidates")
	}
	return SessionInfo{}, firstErr
}

func (c *Client) CalendarAccount(ctx context.Context) (string, SessionInfo, error) {
	info, err := c.Session(ctx)
	if err != nil {
		return c.config.Username, SessionInfo{}, err
	}
	if accountID := info.PrimaryAccounts[CapabilityCalendars]; strings.TrimSpace(accountID) != "" {
		return accountID, info, nil
	}
	for id, account := range info.Accounts {
		if account.Supports(CapabilityCalendars) {
			return id, info, nil
		}
	}
	if strings.TrimSpace(c.config.Username) != "" {
		return c.config.Username, info, nil
	}
	return "", info, errors.New("JMAP session did not advertise a calendar account")
}

func (a AccountInfo) Supports(capability string) bool {
	if capability == "" {
		return false
	}
	if _, ok := a.AccountCaps[capability]; ok {
		return true
	}
	if _, ok := a.Capabilities[capability]; ok {
		return true
	}
	return false
}

func NewProviderFromSession(ctx context.Context, client *Client) (Provider, SessionInfo, error) {
	accountID, info, err := client.CalendarAccount(ctx)
	if err != nil {
		return Provider{}, info, err
	}
	return Provider{Client: client, AccountID: accountID}, info, nil
}

func (c *Client) getSession(ctx context.Context, endpoint string) (SessionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return SessionInfo{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.config.Username, c.config.Password)
	resp, err := c.http.Do(req)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("jmap session request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SessionInfo{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SessionInfo{}, &sessionHTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: Redact(string(body), c.config.Password)}
	}
	var info SessionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return SessionInfo{}, fmt.Errorf("decode jmap session: %w", err)
	}
	_ = json.Unmarshal(body, &info.Raw)
	if info.Accounts == nil {
		info.Accounts = map[string]AccountInfo{}
	}
	if info.PrimaryAccounts == nil {
		info.PrimaryAccounts = map[string]string{}
	}
	return info, nil
}

func (c *Client) sessionEndpointCandidates() []string {
	base := strings.TrimRight(c.config.BaseURL, "/")
	var candidates []string
	if strings.HasSuffix(base, "/.well-known/jmap") || strings.HasSuffix(base, "/jmap/session") || strings.HasSuffix(base, "/session") {
		candidates = append(candidates, base)
	} else {
		candidates = append(candidates, base+"/.well-known/jmap")
		if strings.HasSuffix(base, "/jmap") {
			candidates = append(candidates, base+"/session")
		} else {
			candidates = append(candidates, base+"/jmap/session")
			candidates = append(candidates, base+"/session")
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := url.ParseRequestURI(candidate); err == nil && !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}

type sessionHTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *sessionHTTPError) Error() string {
	if e.Body == "" {
		return "jmap session HTTP " + e.Status
	}
	return "jmap session HTTP " + e.Status + ": " + e.Body
}

func isSessionEndpointMiss(err error) bool {
	var httpErr *sessionHTTPError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusMethodNotAllowed)
}
