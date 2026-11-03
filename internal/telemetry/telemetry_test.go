package telemetry

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDisabledIsNoOp(t *testing.T) {
	// Nothing started: every call must be safe and cheap.
	Count("x", "a:b")
	Gauge("x", 1)
	Bool("x", true)
	Duration("x", time.Second)
	Event("t", "x")
	s, ctx := StartSpan(context.Background(), "op", "res")
	if s != nil {
		t.Fatal("StartSpan() returned a span while tracing is disabled")
	}
	s.SetTag("k", "v")
	s.Keep()
	s.Drop()
	s.Finish(errors.New("boom"))
	if c, _ := StartChildSpan(ctx, "op", "res"); c != nil {
		t.Fatal("StartChildSpan() returned a span while tracing is disabled")
	}
}

func TestResolveEnvOverrides(t *testing.T) {
	t.Setenv("DD_ENV", "home")
	t.Setenv("DD_AGENT_HOST", "")
	t.Setenv("DD_DOGSTATSD_PORT", "9125")
	t.Setenv("DD_TRACE_AGENT_PORT", "")
	got := Config{Service: "svc", Env: "dev", StatsdPort: 1, TracePort: 0}.Resolve()
	if got.Env != "home" || got.AgentHost != "localhost" || got.StatsdPort != 9125 || got.TracePort != 8126 {
		t.Fatalf("Resolve() = %+v", got)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"ignored: Kodi is starting": "ignored_kodi_is_starting",
		"quit requested":            "quit_requested",
		"sent":                      "sent",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStartSendsToDogStatsD(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	for _, k := range []string{"DD_SERVICE", "DD_ENV", "DD_AGENT_HOST", "DD_DOGSTATSD_PORT"} {
		t.Setenv(k, "")
	}

	stop, err := Start(Config{Service: "svc", Env: "home", Version: "v1", AgentHost: "127.0.0.1", StatsdPort: port})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	Count("apple_remote.button.press", "key:KEY_MENU", "press:long")
	Event("Kodi: now playing X", "text", "media_type:movie")
	stop() // flushes
	defer func() { stats = noop() }()

	var got strings.Builder
	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for !strings.Contains(got.String(), "_e{") || !strings.Contains(got.String(), "|c") {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		got.Write(buf[:n])
	}
	out := got.String()
	for _, want := range []string{"apple_remote.button.press:1|c", "key:KEY_MENU", "press:long", "service:svc", "env:home", "version:v1", "Kodi: now playing X"} {
		if !strings.Contains(out, want) {
			t.Errorf("DogStatsD payload missing %q:\n%s", want, out)
		}
	}
}
