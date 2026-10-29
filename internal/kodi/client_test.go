package kodi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type rpcRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// fakeKodi answers JSON-RPC requests with handler and records them.
func fakeKodi(t *testing.T, handler func(req rpcRequest) string) (*Client, *[]rpcRequest) {
	t.Helper()
	var got []rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "kodi" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		got = append(got, req)
		w.Write([]byte(handler(req)))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "kodi", "secret", time.Second), &got
}

func TestPingAndActions(t *testing.T) {
	c, got := fakeKodi(t, func(req rpcRequest) string {
		if req.Method == "JSONRPC.Ping" {
			return `{"id":1,"jsonrpc":"2.0","result":"pong"}`
		}
		return `{"id":1,"jsonrpc":"2.0","result":"OK"}`
	})
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if err := c.Back(ctx); err != nil {
		t.Fatalf("Back() error = %v", err)
	}
	if err := c.Quit(ctx); err != nil {
		t.Fatalf("Quit() error = %v", err)
	}
	if err := c.ExecuteAction(ctx, "playpause"); err != nil {
		t.Fatalf("ExecuteAction() error = %v", err)
	}
	if err := c.KeyPress(ctx, "left"); err != nil {
		t.Fatalf("KeyPress() error = %v", err)
	}
	methods := []string{}
	for _, r := range *got {
		methods = append(methods, r.Method)
	}
	if want := "JSONRPC.Ping Input.Back Application.Quit Input.ExecuteAction Input.ButtonEvent"; strings.Join(methods, " ") != want {
		t.Fatalf("methods = %v, want %s", methods, want)
	}
	if p := string((*got)[3].Params); p != `{"action":"playpause"}` {
		t.Fatalf("ExecuteAction params = %s", p)
	}
	if p := string((*got)[4].Params); p != `{"button":"left","keymap":"KB"}` {
		t.Fatalf("KeyPress params = %s", p)
	}
}

func TestUnauthorized(t *testing.T) {
	c, _ := fakeKodi(t, func(rpcRequest) string { return "" })
	c.password = "wrong"
	err := c.Ping(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Ping() error = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestRPCError(t *testing.T) {
	c, _ := fakeKodi(t, func(rpcRequest) string {
		return `{"id":1,"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found."}}`
	})
	err := c.Back(context.Background())
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32601 {
		t.Fatalf("Back() error = %v, want RPCError -32601", err)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "kodi", "secret", 100*time.Millisecond)
	start := time.Now()
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping() error = nil, want timeout")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Ping() took %v, want it bounded by the 100ms timeout", d)
	}
}

func TestHasMethod(t *testing.T) {
	c, _ := fakeKodi(t, func(req rpcRequest) string {
		if strings.Contains(string(req.Params), "Application.Quit") {
			return `{"id":1,"jsonrpc":"2.0","result":{"methods":{"Application.Quit":{}}}}`
		}
		return `{"id":1,"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params."}}`
	})
	ctx := context.Background()
	if ok, err := c.HasMethod(ctx, "Application.Quit"); err != nil || !ok {
		t.Fatalf("HasMethod(Application.Quit) = %v, %v; want true", ok, err)
	}
	if ok, err := c.HasMethod(ctx, "Nope.Nope"); err != nil || ok {
		t.Fatalf("HasMethod(Nope.Nope) = %v, %v; want false, nil", ok, err)
	}
}

func TestCurrentWindow(t *testing.T) {
	c, got := fakeKodi(t, func(rpcRequest) string {
		return `{"id":1,"jsonrpc":"2.0","result":{"currentwindow":{"id":12005,"label":"Fullscreen video"}}}`
	})
	id, err := c.CurrentWindow(context.Background())
	if err != nil || id != 12005 {
		t.Fatalf("CurrentWindow() = %d, %v; want 12005", id, err)
	}
	if (*got)[0].Method != "GUI.GetProperties" {
		t.Fatalf("method = %s, want GUI.GetProperties", (*got)[0].Method)
	}
}

func TestWindowID(t *testing.T) {
	for name, want := range map[string]int{"fullscreenvideo": 12005, "FullscreenVideo": 12005, "10025": 10025} {
		if got, err := WindowID(name); err != nil || got != want {
			t.Errorf("WindowID(%q) = %d, %v; want %d", name, got, err, want)
		}
	}
	if _, err := WindowID("nope"); err == nil {
		t.Error("WindowID(nope) error = nil, want error")
	}
}

func TestAdjustVolumeClamps(t *testing.T) {
	tests := []struct{ current, delta, want int }{
		{50, 5, 55},
		{97, 5, 100},
		{3, -5, 0},
	}
	for _, tt := range tests {
		var set string
		c, _ := fakeKodi(t, func(req rpcRequest) string {
			if req.Method == "Application.SetVolume" {
				set = string(req.Params)
				return `{"id":1,"jsonrpc":"2.0","result":0}`
			}
			return fmt.Sprintf(`{"id":1,"jsonrpc":"2.0","result":{"volume":%d}}`, tt.current)
		})
		got, err := c.AdjustVolume(context.Background(), tt.delta)
		if err != nil || got != tt.want {
			t.Errorf("AdjustVolume(%d) from %d = %d, %v; want %d", tt.delta, tt.current, got, err, tt.want)
		}
		if want := fmt.Sprintf(`{"volume":%d}`, tt.want); set != want {
			t.Errorf("SetVolume params = %s, want %s", set, want)
		}
	}
}

func TestPausePlaybackOnlyPausesPlayingPlayers(t *testing.T) {
	var paused []string
	c, _ := fakeKodi(t, func(req rpcRequest) string {
		switch req.Method {
		case "Player.GetActivePlayers":
			return `{"id":1,"jsonrpc":"2.0","result":[{"playerid":0,"type":"audio"},{"playerid":1,"type":"video"}]}`
		case "Player.GetProperties":
			if strings.Contains(string(req.Params), `"playerid":1`) {
				return `{"id":1,"jsonrpc":"2.0","result":{"speed":1}}`
			}
			return `{"id":1,"jsonrpc":"2.0","result":{"speed":0}}` // already paused
		case "Player.PlayPause":
			paused = append(paused, string(req.Params))
			return `{"id":1,"jsonrpc":"2.0","result":{"speed":0}}`
		}
		return `{"id":1,"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found."}}`
	})
	n, err := c.PausePlayback(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("PausePlayback() = %d, %v; want 1", n, err)
	}
	if len(paused) != 1 || paused[0] != `{"play":false,"playerid":1}` {
		t.Fatalf("Player.PlayPause params = %v, want only player 1 with play=false", paused)
	}
}

func TestPlayingAndNotify(t *testing.T) {
	var notify string
	c, _ := fakeKodi(t, func(req rpcRequest) string {
		switch req.Method {
		case "Player.GetActivePlayers":
			return `{"id":1,"jsonrpc":"2.0","result":[{"playerid":1,"type":"video"}]}`
		case "Player.GetProperties":
			return `{"id":1,"jsonrpc":"2.0","result":{"speed":1}}`
		case "GUI.ShowNotification":
			notify = string(req.Params)
			return `{"id":1,"jsonrpc":"2.0","result":"OK"}`
		}
		return ""
	})
	ctx := context.Background()
	if ok, err := c.Playing(ctx); err != nil || !ok {
		t.Fatalf("Playing() = %v, %v; want true", ok, err)
	}
	if err := c.Notify(ctx, "T", "M", 20*time.Second); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if notify != `{"displaytime":20000,"message":"M","title":"T"}` {
		t.Fatalf("GUI.ShowNotification params = %s", notify)
	}
}
