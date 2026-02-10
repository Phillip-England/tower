package tower

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	BaseURL string
	UserID  string
	Key     string
	HTTP    *http.Client
}

type Message struct {
	ID        int64      `json:"ID"`
	UserID    string     `json:"UserID"`
	Body      string     `json:"Body"`
	CreatedAt time.Time  `json:"CreatedAt"`
	ReadAt    *time.Time `json:"ReadAt"`
}

func New(baseURL, userID, key string) *Client {
	return &Client{
		BaseURL: baseURL,
		UserID:  userID,
		Key:     key,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) LogRequest(ctx context.Context, method, path, ip string) error {
	payload := map[string]string{
		"method": method,
		"path":   path,
		"ip":     ip,
	}
	return c.post(ctx, "/api/v1/log", payload, nil)
}

func (c *Client) SendMessage(ctx context.Context, body string) (int64, error) {
	var resp struct {
		ID int64 `json:"id"`
	}
	err := c.post(ctx, "/api/v1/messages", map[string]string{"body": body}, &resp)
	return resp.ID, err
}

func (c *Client) ListMessages(ctx context.Context, limit int) ([]Message, error) {
	endpoint := "/api/v1/messages"
	if limit > 0 {
		endpoint += "?limit=" + strconv.Itoa(limit)
	}
	var msgs []Message
	err := c.get(ctx, endpoint, &msgs)
	return msgs, err
}

func (c *Client) DeleteMessage(ctx context.Context, id int64) error {
	return c.del(ctx, fmt.Sprintf("/api/v1/messages/%d", id))
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

func (c *Client) del(ctx context.Context, p string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+p, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	return c.do(req, nil)
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
	req.Header.Set("X-Tower-User", c.UserID)
	req.Header.Set("X-Tower-Key", c.Key)
}

func NormalizeBaseURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	return parsed.String()
}
