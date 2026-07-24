// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// Client is the CLI side of the CLI↔daemon localhost API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// Connect returns a Client for the running daemon, or an error if no daemon
// is reachable.
func Connect() (*Client, error) {
	state, err := config.LoadState()
	if err != nil {
		return nil, err
	}
	if state.IPCToken == "" || state.DashboardPort == 0 {
		return nil, fmt.Errorf("daemon has never run; start it with: backup-maker daemon")
	}
	c := &Client{
		base:  fmt.Sprintf("http://127.0.0.1:%d", state.DashboardPort),
		token: state.IPCToken,
		http:  &http.Client{Timeout: 5 * time.Second},
	}
	if err := c.Ping(); err != nil {
		return nil, fmt.Errorf("daemon not running (start it with: backup-maker daemon): %w", err)
	}
	return c, nil
}

func (c *Client) do(method, path string, out any) error {
	req, err := http.NewRequest(method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// post sends a JSON body and decodes the JSON reply into out (out may be nil).
func (c *Client) post(path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s", strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Ping() error {
	return c.do(http.MethodGet, "/api/ping", nil)
}

// Wake asks the daemon to send a Wake-on-LAN packet to target immediately,
// bypassing the background rate limit. A nil error means the packet was sent,
// NOT that the target is awake.
func (c *Client) Wake(target string) (string, error) {
	var out struct {
		Message string `json:"message"`
	}
	if err := c.post("/api/wake", map[string]string{"target": target}, &out); err != nil {
		return "", err
	}
	return out.Message, nil
}

// Status decodes the daemon's status model into out.
func (c *Client) Status(out any) error {
	return c.do(http.MethodGet, "/api/status", out)
}
