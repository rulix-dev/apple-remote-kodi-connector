// Package kodi talks to Kodi over HTTP JSON-RPC and manages its Flatpak process.
package kodi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// ErrUnauthorized means Kodi rejected the credentials (HTTP 401).
var ErrUnauthorized = errors.New("HTTP 401: Kodi rejected the credentials (check kodi.username and the password)")

// maxResponse bounds the size of a response body; JSONRPC.Introspect is the
// largest one and stays well below this.
const maxResponse = 16 << 20

// RPCError is an error object returned by Kodi.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// Client is a minimal Kodi JSON-RPC client. It never logs credentials.
type Client struct {
	url      string
	username string
	password string
	hc       *http.Client
	nextID   atomic.Int64
}

// NewClient returns a client for url (e.g. http://127.0.0.1:8080/jsonrpc).
// timeout bounds every request.
func NewClient(url, username, password string, timeout time.Duration) *Client {
	return &Client{url: url, username: username, password: password, hc: &http.Client{Timeout: timeout}}
}

// Call invokes method with params and decodes the result into result (if non-nil).
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method, "id": c.nextID.Add(1)}
	if params != nil {
		req["params"] = params
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.username != "" {
		hr.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s: %w", method, ErrUnauthorized)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s: unexpected HTTP status %s", method, resp.Status)
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&r); err != nil {
		return fmt.Errorf("%s: decode response: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("%s: %w", method, r.Error)
	}
	if result != nil {
		if err := json.Unmarshal(r.Result, result); err != nil {
			return fmt.Errorf("%s: decode result: %w", method, err)
		}
	}
	return nil
}

// Ping checks that the API answers and accepts the credentials.
func (c *Client) Ping(ctx context.Context) error {
	var s string
	if err := c.Call(ctx, "JSONRPC.Ping", nil, &s); err != nil {
		return err
	}
	if s != "pong" {
		return fmt.Errorf("JSONRPC.Ping: unexpected result %q", s)
	}
	return nil
}

// Back sends Input.Back.
func (c *Client) Back(ctx context.Context) error {
	return c.Call(ctx, "Input.Back", nil, nil)
}

// Quit asks Kodi to exit cleanly (Application.Quit).
func (c *Client) Quit(ctx context.Context) error {
	return c.Call(ctx, "Application.Quit", nil, nil)
}

// ExecuteAction runs a Kodi action by name (Input.ExecuteAction), e.g.
// "playpause", "select", "up".
func (c *Client) ExecuteAction(ctx context.Context, action string) error {
	return c.Call(ctx, "Input.ExecuteAction", map[string]string{"action": action}, nil)
}

// KeyPress sends a keyboard key through Kodi's keymaps (Input.ButtonEvent
// with keymap "KB"), exactly as if it had been typed: its effect depends on
// the current window, e.g. "left" moves in menus and seeks in fullscreen video.
// Key names are those of keyboard.xml: up, down, left, right, return, space...
func (c *Client) KeyPress(ctx context.Context, key string) error {
	return c.Call(ctx, "Input.ButtonEvent", map[string]string{"button": key, "keymap": "KB"}, nil)
}

// PausePlayback pauses every active player that is playing and returns how
// many were paused. It never resumes playback: Player.PlayPause is called
// with play=false and only for players whose speed is not 0.
func (c *Client) PausePlayback(ctx context.Context) (int, error) {
	var players []struct {
		PlayerID int `json:"playerid"`
	}
	if err := c.Call(ctx, "Player.GetActivePlayers", nil, &players); err != nil {
		return 0, err
	}
	paused := 0
	for _, p := range players {
		var props struct {
			Speed float64 `json:"speed"`
		}
		if err := c.Call(ctx, "Player.GetProperties", map[string]any{"playerid": p.PlayerID, "properties": []string{"speed"}}, &props); err != nil {
			return paused, err
		}
		if props.Speed == 0 {
			continue
		}
		if err := c.Call(ctx, "Player.PlayPause", map[string]any{"playerid": p.PlayerID, "play": false}, nil); err != nil {
			return paused, err
		}
		paused++
	}
	return paused, nil
}

// Playing reports whether any active player is playing (speed != 0).
func (c *Client) Playing(ctx context.Context) (bool, error) {
	var players []struct {
		PlayerID int `json:"playerid"`
	}
	if err := c.Call(ctx, "Player.GetActivePlayers", nil, &players); err != nil {
		return false, err
	}
	for _, p := range players {
		var props struct {
			Speed float64 `json:"speed"`
		}
		if err := c.Call(ctx, "Player.GetProperties", map[string]any{"playerid": p.PlayerID, "properties": []string{"speed"}}, &props); err != nil {
			return false, err
		}
		if props.Speed != 0 {
			return true, nil
		}
	}
	return false, nil
}

// Notify shows a notification in Kodi for the given time.
func (c *Client) Notify(ctx context.Context, title, message string, display time.Duration) error {
	return c.Call(ctx, "GUI.ShowNotification", map[string]any{
		"title": title, "message": message, "displaytime": int(display.Milliseconds()),
	}, nil)
}

// AdjustVolume changes Kodi's volume by delta percentage points, clamped to
// 0..100, and returns the new volume.
func (c *Client) AdjustVolume(ctx context.Context, delta int) (int, error) {
	var r struct {
		Volume int `json:"volume"`
	}
	if err := c.Call(ctx, "Application.GetProperties", map[string]any{"properties": []string{"volume"}}, &r); err != nil {
		return 0, err
	}
	v := min(max(r.Volume+delta, 0), 100)
	if err := c.Call(ctx, "Application.SetVolume", map[string]any{"volume": v}, nil); err != nil {
		return 0, err
	}
	return v, nil
}

// CurrentWindow returns the id of the active Kodi window (see WindowID).
func (c *Client) CurrentWindow(ctx context.Context) (int, error) {
	var r struct {
		CurrentWindow struct {
			ID int `json:"id"`
		} `json:"currentwindow"`
	}
	if err := c.Call(ctx, "GUI.GetProperties", map[string]any{"properties": []string{"currentwindow"}}, &r); err != nil {
		return 0, err
	}
	return r.CurrentWindow.ID, nil
}

// Version returns the Kodi version, e.g. "21.3 stable".
func (c *Client) Version(ctx context.Context) (string, error) {
	var r struct {
		Version struct {
			Major int    `json:"major"`
			Minor int    `json:"minor"`
			Tag   string `json:"tag"`
		} `json:"version"`
	}
	if err := c.Call(ctx, "Application.GetProperties", map[string]any{"properties": []string{"version"}}, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d %s", r.Version.Major, r.Version.Minor, r.Version.Tag), nil
}

// HasMethod reports whether the running Kodi exposes a JSON-RPC method.
func (c *Client) HasMethod(ctx context.Context, method string) (bool, error) {
	var r struct {
		Methods map[string]json.RawMessage `json:"methods"`
	}
	params := map[string]any{"filter": map[string]string{"id": method, "type": "method"}}
	if err := c.Call(ctx, "JSONRPC.Introspect", params, &r); err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			return false, nil // Kodi answers "invalid params" for unknown ids
		}
		return false, err
	}
	_, ok := r.Methods[method]
	return ok, nil
}
