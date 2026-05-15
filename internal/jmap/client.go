package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	BaseURL     string
	Username    string
	Password    string
	Timeout     time.Duration
	Trace       bool
	TraceWriter io.Writer
	HTTPClient  *http.Client
}

type Client struct {
	config Config
	http   *http.Client
}

func NewClient(config Config) *Client {
	hc := config.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: config.Timeout}
	} else if config.Timeout > 0 && hc.Timeout == 0 {
		clone := *hc
		clone.Timeout = config.Timeout
		hc = &clone
	}
	return &Client{config: config, http: hc}
}

func (c *Client) Username() string { return c.config.Username }
func (c *Client) BaseURL() string  { return c.config.BaseURL }

func (c *Client) Endpoint() string {
	base := strings.TrimRight(c.config.BaseURL, "/")
	if strings.HasSuffix(base, "/jmap") {
		return base
	}
	return base + "/jmap"
}

func (c *Client) Call(ctx context.Context, method string, params any, capabilities ...string) (MethodResponse, error) {
	responses, err := c.Multi(ctx, []MethodCall{{Name: method, Params: params, ID: "c1"}}, capabilities...)
	if err != nil {
		return MethodResponse{}, err
	}
	if len(responses) == 0 {
		return MethodResponse{}, fmt.Errorf("jmap response for %s had no method responses", method)
	}
	return responses[0], nil
}

func (c *Client) Multi(ctx context.Context, calls []MethodCall, capabilities ...string) ([]MethodResponse, error) {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("c%d", i+1)
		}
	}
	reqBody := Request{Using: UniqueCapabilities(capabilities), MethodCalls: calls}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.SetBasicAuth(c.config.Username, c.config.Password)
	c.tracef("> POST %s\n> Authorization: <redacted>\n> %s\n", c.Endpoint(), Redact(string(body), c.config.Password))

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("jmap request failed: %w", err)
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	c.tracef("< HTTP %d\n< %s\n", httpResp.StatusCode, Redact(string(respBody), c.config.Password))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("jmap HTTP %d: %s", httpResp.StatusCode, Redact(string(respBody), c.config.Password))
	}
	var response Response
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("decode jmap response: %w", err)
	}
	for _, methodResponse := range response.MethodResponses {
		if methodResponse.Name == "error" {
			var methodErr MethodError
			_ = methodResponse.Decode(&methodErr)
			return nil, methodErr
		}
	}
	return response.MethodResponses, nil
}

func (c *Client) tracef(format string, args ...any) {
	if !c.config.Trace {
		return
	}
	w := c.config.TraceWriter
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintf(w, format, args...)
}
