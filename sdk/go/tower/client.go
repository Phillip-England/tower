package tower

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{
		BaseURL: baseURL,
		Key:     key,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Decision represents Tower's escalation decision for an IP.
type Decision struct {
	Action     string `json:"action"`      // ALLOW, FLAG, THROTTLE, BAN
	IP         string `json:"ip"`
	Reason     string `json:"reason,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

// Inspect checks an IP against Tower without recording a request.
func (c *Client) Inspect(ctx context.Context, ip string) (Decision, error) {
	var d Decision
	err := c.post(ctx, "/api/v1/inspect", map[string]string{"ip": ip}, &d)
	return d, err
}

// LogRequest reports a request to Tower for rate limiting and returns the decision.
func (c *Client) LogRequest(ctx context.Context, method, path, ip string) (Decision, error) {
	var d Decision
	payload := map[string]string{
		"method": method,
		"path":   path,
		"ip":     ip,
	}
	err := c.post(ctx, "/api/v1/log", payload, &d)
	return d, err
}

// RegisterCallback registers a URL to receive security event notifications.
func (c *Client) RegisterCallback(ctx context.Context, callbackURL string) error {
	return c.post(ctx, "/api/v1/callbacks", map[string]string{"url": callbackURL}, nil)
}

func (c *Client) post(ctx context.Context, p string, payload interface{}, out interface{}) error {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+p, bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.applyAuth(req)
	return c.do(req, out)
}

func (c *Client) get(ctx context.Context, p string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+p, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out interface{}) error {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != "" {
			return fmt.Errorf("tower error: %s", e.Error)
		}
		return fmt.Errorf("tower error: %s", resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) applyAuth(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tower-Key", c.Key)
}

func NormalizeBaseURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	return parsed.String()
}
