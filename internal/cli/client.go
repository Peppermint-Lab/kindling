package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kindlingvm/kindling/internal/auth"
)

// Client calls the Kindling control-plane REST API.
type Client struct {
	BaseURL      string
	APIKey       string
	SessionToken string // hex-encoded session token for Cookie header
	HTTP         *http.Client
}

// NewClient builds an HTTP client from a resolved profile.
func NewClient(p Profile) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base URL is empty")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	c := &http.Client{Timeout: 120 * time.Second}
	return &Client{
		BaseURL:      base,
		APIKey:       strings.TrimSpace(p.APIKey),
		SessionToken: strings.TrimSpace(p.SessionCookie),
		HTTP:         c,
	}, nil
}

// streamHTTP returns a client suitable for long-lived streams (no total timeout).
func (c *Client) streamHTTP() *http.Client {
	return &http.Client{
		Timeout:   0,
		Transport: c.HTTP.Transport,
	}
}

// APIError is returned for non-2xx JSON error bodies.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("API %d (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("API %d: %s", e.Status, e.Message)
}

// Do sends a request to path (e.g. /api/projects). If body is non-nil it is JSON-encoded.
func (c *Client) Do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.BaseURL + path
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(c.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	} else if strings.TrimSpace(c.SessionToken) != "" {
		req.Header.Set("Cookie", auth.SessionCookieName+"="+strings.TrimSpace(c.SessionToken))
	}
	return c.HTTP.Do(req)
}

// DoStream is like Do but uses a no-timeout client for SSE/long polls.
func (c *Client) DoStream(ctx context.Context, method, path string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	} else if strings.TrimSpace(c.SessionToken) != "" {
		req.Header.Set("Cookie", auth.SessionCookieName+"="+strings.TrimSpace(c.SessionToken))
	}
	return c.streamHTTP().Do(req)
}

// DoJSON decodes a JSON body on success (2xx). For error responses, decodes apiError shape.
func (c *Client) DoJSON(ctx context.Context, method, path string, body any, out any) error {
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(b, &ae)
		return &APIError{Status: resp.StatusCode, Code: ae.Code, Message: ae.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// SessionHexFromLoginResponse extracts kindling_session from Set-Cookie headers.
func SessionHexFromLoginResponse(resp *http.Response) (string, error) {
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.SessionCookieName && strings.TrimSpace(ck.Value) != "" {
			return strings.TrimSpace(ck.Value), nil
		}
	}
	return "", fmt.Errorf("no %s cookie in response", auth.SessionCookieName)
}
