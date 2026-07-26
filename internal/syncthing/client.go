// SPDX-License-Identifier: MIT

package syncthing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a thin REST client for our supervised syncthing instance.
type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

func NewClient(guiPort int, apiKey string) *Client {
	return &Client{
		base:   fmt.Sprintf("http://127.0.0.1:%d", guiPort),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("syncthing %s %s: %s: %s", method, path, resp.Status, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Ping() error {
	return c.do(http.MethodGet, "/rest/system/ping", nil, nil)
}

// MyID returns this machine's syncthing device ID.
func (c *Client) MyID() (string, error) {
	var st SystemStatus
	if err := c.do(http.MethodGet, "/rest/system/status", nil, &st); err != nil {
		return "", err
	}
	return st.MyID, nil
}

func (c *Client) Restart() error {
	return c.do(http.MethodPost, "/rest/system/restart", nil, nil)
}

// --- Config section read-modify-write ---
//
// Raw sections are fetched as json.RawMessage lists, edited via our thin
// structs merged over the raw JSON, and PUT back, so unknown fields survive.

func (c *Client) RawFolders() ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := c.do(http.MethodGet, "/rest/config/folders", nil, &out)
	return out, err
}

func (c *Client) RawDevices() ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := c.do(http.MethodGet, "/rest/config/devices", nil, &out)
	return out, err
}

// PutFolderOne creates or replaces a single folder config.
func (c *Client) PutFolderOne(id string, folder any) error {
	return c.do(http.MethodPut, "/rest/config/folders/"+url.PathEscape(id), folder, nil)
}

func (c *Client) DeleteFolder(id string) error {
	return c.do(http.MethodDelete, "/rest/config/folders/"+url.PathEscape(id), nil, nil)
}

func (c *Client) PutDeviceOne(id string, dev any) error {
	return c.do(http.MethodPut, "/rest/config/devices/"+url.PathEscape(id), dev, nil)
}

func (c *Client) DeleteDevice(id string) error {
	return c.do(http.MethodDelete, "/rest/config/devices/"+url.PathEscape(id), nil, nil)
}

func (c *Client) PatchOptions(patch map[string]any) error {
	return c.do(http.MethodPatch, "/rest/config/options", patch, nil)
}

func (c *Client) GetOptions() (map[string]any, error) {
	var out map[string]any
	err := c.do(http.MethodGet, "/rest/config/options", nil, &out)
	return out, err
}

// ConfigInSync reports whether syncthing needs a restart to apply config.
func (c *Client) ConfigInSync() (bool, error) {
	var out struct {
		RequiresRestart bool `json:"requiresRestart"`
	}
	err := c.do(http.MethodGet, "/rest/config/restart-required", nil, &out)
	return !out.RequiresRestart, err
}

// --- Ignores ---

func (c *Client) SetIgnores(folderID string, lines []string) error {
	body := map[string][]string{"ignore": lines}
	return c.do(http.MethodPost, "/rest/db/ignores?folder="+url.QueryEscape(folderID), body, nil)
}

// --- Status ---

func (c *Client) Connections() (*Connections, error) {
	var out Connections
	err := c.do(http.MethodGet, "/rest/system/connections", nil, &out)
	return &out, err
}

func (c *Client) DeviceStats() (map[string]DeviceStats, error) {
	var out map[string]DeviceStats
	err := c.do(http.MethodGet, "/rest/stats/device", nil, &out)
	return out, err
}

func (c *Client) FolderStatus(folderID string) (*FolderStatus, error) {
	var out FolderStatus
	err := c.do(http.MethodGet, "/rest/db/status?folder="+url.QueryEscape(folderID), nil, &out)
	return &out, err
}

func (c *Client) Completion(folderID, deviceID string) (*Completion, error) {
	var out Completion
	err := c.do(http.MethodGet,
		"/rest/db/completion?folder="+url.QueryEscape(folderID)+"&device="+url.QueryEscape(deviceID), nil, &out)
	return &out, err
}

// --- Pairing ---

func (c *Client) PendingDevices() (PendingDevices, error) {
	var out PendingDevices
	err := c.do(http.MethodGet, "/rest/cluster/pending/devices", nil, &out)
	return out, err
}

func (c *Client) PendingFolders() (PendingFolders, error) {
	var out PendingFolders
	err := c.do(http.MethodGet, "/rest/cluster/pending/folders", nil, &out)
	return out, err
}

// --- Repair ---

// Revert discards local modifications on a receive-only folder.
func (c *Client) Revert(folderID string) error {
	return c.do(http.MethodPost, "/rest/db/revert?folder="+url.QueryEscape(folderID), nil, nil)
}

// Override forces a send-only folder's state onto the cluster.
func (c *Client) Override(folderID string) error {
	return c.do(http.MethodPost, "/rest/db/override?folder="+url.QueryEscape(folderID), nil, nil)
}

// --- Events ---

// Events long-polls for events after sinceID. Returns an empty slice on
// timeout. The passed client timeout must exceed timeoutSec.
func (c *Client) Events(sinceID int64, timeoutSec int) ([]Event, error) {
	var out []Event
	path := fmt.Sprintf("/rest/events?since=%d&timeout=%d", sinceID, timeoutSec)
	err := c.do(http.MethodGet, path, nil, &out)
	return out, err
}
