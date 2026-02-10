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
	ID        int64      `json:"id"`
	UserID    string     `json:"user_id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
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

func (c *Client) ListMessages(ctx context.Context, limit, offset int) ([]Message, error) {
	endpoint := "/api/v1/messages"
	params := url.Values{}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	var msgs []Message
	err := c.get(ctx, endpoint, &msgs)
	return msgs, err
}

func (c *Client) GetMessage(ctx context.Context, id int64) (Message, error) {
	var msg Message
	err := c.get(ctx, fmt.Sprintf("/api/v1/messages/%d", id), &msg)
	return msg, err
}

func (c *Client) MarkMessageRead(ctx context.Context, id int64) error {
	return c.patch(ctx, fmt.Sprintf("/api/v1/messages/%d", id))
}

func (c *Client) UnreadCount(ctx context.Context) (int, error) {
	var resp struct {
		UnreadCount int `json:"unread_count"`
	}
	err := c.get(ctx, "/api/v1/messages/unread-count", &resp)
	return resp.UnreadCount, err
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

func (c *Client) patch(ctx context.Context, p string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+p, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	return c.do(req, nil)
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
