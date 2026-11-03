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

func TestNowPlaying(t *testing.T) {
	c, _ := fakeKodi(t, func(req rpcRequest) string {
		switch req.Method {
		case "Player.GetActivePlayers":
			return `{"id":1,"jsonrpc":"2.0","result":[{"playerid":1,"type":"video"}]}`
		case "Player.GetItem":
			return `{"id":1,"jsonrpc":"2.0","result":{"item":{"type":"episode","label":"Pilot","title":"Pilot","showtitle":"Some Show","season":1,"episode":2,"year":2008}}}`
		case "Player.GetProperties":
			return `{"id":1,"jsonrpc":"2.0","result":{"speed":0}}`
		}
		return ""
	})
	it, err := c.NowPlaying(context.Background())
	if err != nil || it == nil {
		t.Fatalf("NowPlaying() = %v, %v", it, err)
	}
	if it.MediaType() != "episode" || it.Playing || it.Display() != "Some Show S01E02 · Pilot" {
		t.Fatalf("NowPlaying() = %+v, display %q", it, it.Display())
	}
}

func TestNowPlayingNothing(t *testing.T) {
	c, _ := fakeKodi(t, func(rpcRequest) string { return `{"id":1,"jsonrpc":"2.0","result":[]}` })
	if it, err := c.NowPlaying(context.Background()); err != nil || it != nil {
		t.Fatalf("NowPlaying() = %v, %v; want nil, nil", it, err)
	}
}

func TestItemDisplay(t *testing.T) {
	tests := map[string]Item{
		"Movie (2001)": {Type: "movie", Title: "Movie", Year: 2001},
		"Song":         {Type: "song", Title: "Song"},
		"label only":   {Player: "video", Type: "unknown", Label: "label only"},
	}
	for want, it := range tests {
		if got := it.Display(); got != want {
			t.Errorf("Display() = %q, want %q", got, want)
		}
	}
	if (Item{Player: "audio", Type: "unknown"}).MediaType() != "audio" {
		t.Error("MediaType() should fall back to the player type")
	}
}

func TestErrorClass(t *testing.T) {
	if got := errorClass(fmt.Errorf("x: %w", ErrUnauthorized)); got != "unauthorized" {
		t.Errorf("errorClass(401) = %q", got)
	}
	if got := errorClass(&RPCError{Code: -1}); got != "rpc" {
		t.Errorf("errorClass(rpc) = %q", got)
	}
	if got := errorClass(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("errorClass(deadline) = %q", got)
	}
}

func TestNowPlayingStreamFormat(t *testing.T) {
	c, _ := fakeKodi(t, func(req rpcRequest) string {
		switch {
		case req.Method == "Player.GetActivePlayers":
			return `{"id":1,"jsonrpc":"2.0","result":[{"playerid":1,"type":"video"}]}`
		case req.Method == "Player.GetItem":
			return `{"id":1,"jsonrpc":"2.0","result":{"item":{"type":"movie","label":"M","title":"M","year":2001}}}`
		case strings.Contains(string(req.Params), "currentvideostream"):
			return `{"id":1,"jsonrpc":"2.0","result":{"currentvideostream":{"codec":"hevc","width":1920,"height":800,"hdrtype":""},"currentaudiostream":{"codec":"eac3","channels":6}}}`
		default:
			return `{"id":1,"jsonrpc":"2.0","result":{"speed":1}}`
		}
	})
	it, err := c.NowPlaying(context.Background())
	if err != nil || it == nil {
		t.Fatalf("NowPlaying() = %v, %v", it, err)
	}
	if it.VideoCodec != "hevc" || it.Width != 1920 || it.Height != 800 || it.AudioCodec != "eac3" || it.AudioChannels != 6 {
		t.Fatalf("stream format = %+v", it)
	}
	if it.Resolution() != "1080p" {
		t.Errorf("Resolution() = %q, want 1080p (cropped 1920x800)", it.Resolution())
	}
	hw := []string{"h264", "mpeg2video", "vc1", "wmv3"}
	if it.Decode(hw) != "cpu" {
		t.Errorf("Decode(hevc) = %q, want cpu", it.Decode(hw))
	}
	it.VideoCodec = "H264"
	if it.Decode(hw) != "hardware" {
		t.Errorf("Decode(H264) = %q, want hardware", it.Decode(hw))
	}
}

func TestNowPlayingWithoutStreamFormat(t *testing.T) {
	// If the stream properties fail, the item is still returned.
	c, _ := fakeKodi(t, func(req rpcRequest) string {
		switch {
		case req.Method == "Player.GetActivePlayers":
			return `{"id":1,"jsonrpc":"2.0","result":[{"playerid":1,"type":"video"}]}`
		case req.Method == "Player.GetItem":
			return `{"id":1,"jsonrpc":"2.0","result":{"item":{"type":"movie","title":"M"}}}`
		case strings.Contains(string(req.Params), "currentvideostream"):
			return `{"id":1,"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params."}}`
		default:
			return `{"id":1,"jsonrpc":"2.0","result":{"speed":1}}`
		}
	})
	it, err := c.NowPlaying(context.Background())
	if err != nil || it == nil || it.VideoCodec != "" {
		t.Fatalf("NowPlaying() = %+v, %v; want the item without format", it, err)
	}
}

func TestResolution(t *testing.T) {
	for want, wh := range map[string][2]int{
		"2160p": {3840, 2160}, "1080p": {1920, 1080}, "720p": {1280, 720},
		"480p": {720, 480}, "sd": {480, 360}, "": {0, 0},
	} {
		if got := (Item{Width: wh[0], Height: wh[1]}).Resolution(); got != want {
			t.Errorf("Resolution(%dx%d) = %q, want %q", wh[0], wh[1], got, want)
		}
	}
	if (Item{Player: "audio", AudioCodec: "flac"}).Decode([]string{"flac"}) != "" {
		t.Error("Decode() must be empty for non-video items")
	}
}
