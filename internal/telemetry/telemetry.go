// Package telemetry sends metrics and events to DogStatsD and traces to the
// Datadog trace agent.
//
// Until Start is called (or when it is disabled) every function is a cheap
// no-op, so the rest of the program can call it unconditionally. If the agent
// is not running nothing blocks: DogStatsD is UDP and the tracer only logs
// its connection errors at debug level.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/ext"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
)

// Config selects where telemetry goes. Environment variables of the Datadog
// unified service tagging convention override it: DD_SERVICE, DD_ENV,
// DD_AGENT_HOST, DD_DOGSTATSD_PORT and DD_TRACE_AGENT_PORT.
type Config struct {
	Service    string
	Env        string
	Version    string
	AgentHost  string
	StatsdPort int
	TracePort  int
	Tracing    bool
}

var (
	stats   statsd.ClientInterface = noop()
	tracing bool
)

func noop() statsd.ClientInterface { return &statsd.NoOpClient{} }

// Resolve applies the environment overrides and defaults.
func (c Config) Resolve() Config {
	c.Service = envOr("DD_SERVICE", c.Service)
	c.Env = envOr("DD_ENV", c.Env)
	c.AgentHost = envOr("DD_AGENT_HOST", c.AgentHost)
	if c.AgentHost == "" {
		c.AgentHost = "localhost"
	}
	c.StatsdPort = envIntOr("DD_DOGSTATSD_PORT", c.StatsdPort, 8125)
	c.TracePort = envIntOr("DD_TRACE_AGENT_PORT", c.TracePort, 8126)
	return c
}

// Start enables telemetry. The returned function flushes and stops it.
func Start(cfg Config) (stop func(), err error) {
	cfg = cfg.Resolve()
	tags := []string{"service:" + cfg.Service, "version:" + cfg.Version}
	if cfg.Env != "" {
		tags = append(tags, "env:"+cfg.Env)
	}
	statsdAddr := net.JoinHostPort(cfg.AgentHost, strconv.Itoa(cfg.StatsdPort))
	client, err := statsd.New(statsdAddr, statsd.WithTags(tags))
	if err != nil {
		return func() {}, fmt.Errorf("dogstatsd %s: %w", statsdAddr, err)
	}
	stats = client

	if cfg.Tracing {
		opts := []tracer.StartOption{
			tracer.WithService(cfg.Service),
			tracer.WithServiceVersion(cfg.Version),
			tracer.WithAgentAddr(net.JoinHostPort(cfg.AgentHost, strconv.Itoa(cfg.TracePort))),
			tracer.WithDogstatsdAddr(statsdAddr),
			tracer.WithRuntimeMetrics(),
			tracer.WithLogStartup(false),
			tracer.WithLogger(debugLogger{}),
		}
		if cfg.Env != "" {
			opts = append(opts, tracer.WithEnv(cfg.Env))
		}
		if err := tracer.Start(opts...); err != nil {
			slog.Warn("datadog tracer not started", "err", err)
		} else {
			tracing = true
		}
	}
	slog.Info("datadog telemetry enabled", "service", cfg.Service, "env", cfg.Env,
		"dogstatsd", statsdAddr, "tracing", tracing)
	return func() {
		if tracing {
			tracer.Stop()
		}
		client.Close()
	}, nil
}

// debugLogger sends the tracer's own messages to slog at debug level, so a
// missing agent does not flood the log.
type debugLogger struct{}

func (debugLogger) Log(msg string) { slog.Debug("datadog tracer", "msg", msg) }

// Count adds 1 to a counter.
func Count(name string, tags ...string) { _ = stats.Incr(name, tags, 1) }

// Gauge sets a gauge.
func Gauge(name string, value float64, tags ...string) { _ = stats.Gauge(name, value, tags, 1) }

// Bool sets a 0/1 gauge.
func Bool(name string, value bool, tags ...string) {
	v := 0.0
	if value {
		v = 1
	}
	Gauge(name, v, tags...)
}

// Duration records a duration in milliseconds as a distribution.
func Duration(name string, d time.Duration, tags ...string) {
	_ = stats.Distribution(name, float64(d.Microseconds())/1000, tags, 1)
}

// Event sends a Datadog event.
func Event(title, text string, tags ...string) {
	_ = stats.Event(&statsd.Event{Title: title, Text: text, Tags: tags, SourceTypeName: "kodi"})
}

// Span wraps a tracer span; a nil *Span is a valid no-op span.
type Span struct{ s *tracer.Span }

// StartSpan starts a span, as a child of the span in ctx if there is one.
func StartSpan(ctx context.Context, operation, resource string, tags ...Tag) (*Span, context.Context) {
	if !tracing {
		return nil, ctx
	}
	opts := []tracer.StartSpanOption{tracer.ResourceName(resource)}
	for _, t := range tags {
		opts = append(opts, tracer.Tag(t.Key, t.Value))
	}
	s, ctx := tracer.StartSpanFromContext(ctx, operation, opts...)
	return &Span{s: s}, ctx
}

// StartChildSpan starts a span only if ctx already carries one, so that
// background work (polling) does not create a trace for every call.
func StartChildSpan(ctx context.Context, operation, resource string, tags ...Tag) (*Span, context.Context) {
	if !tracing {
		return nil, ctx
	}
	if _, ok := tracer.SpanFromContext(ctx); !ok {
		return nil, ctx
	}
	return StartSpan(ctx, operation, resource, tags...)
}

// ContextWithSpan returns ctx carrying s, so that spans started from it are
// children of s.
func ContextWithSpan(ctx context.Context, s *Span) context.Context {
	if s == nil {
		return ctx
	}
	return tracer.ContextWithSpan(ctx, s.s)
}

// Tag is a span tag.
type Tag struct {
	Key   string
	Value any
}

// T builds a Tag.
func T(key string, value any) Tag { return Tag{key, value} }

// SetTag sets a tag on the span.
func (s *Span) SetTag(key string, value any) {
	if s != nil {
		s.s.SetTag(key, value)
	}
}

// Keep forces the trace to be kept (e.g. a poll that found a change).
func (s *Span) Keep() { s.SetTag(ext.ManualKeep, true) }

// Drop discards the trace (e.g. a poll where nothing changed).
func (s *Span) Drop() { s.SetTag(ext.ManualDrop, true) }

// Finish ends the span, recording err if not nil.
func (s *Span) Finish(err error) {
	if s == nil {
		return
	}
	if err != nil {
		s.s.Finish(tracer.WithError(err))
		return
	}
	s.s.Finish()
}

// Slug turns free text into a tag-friendly value: "ignored: Kodi is
// starting" -> "ignored_kodi_is_starting".
func Slug(s string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			underscore = false
		} else if !underscore && b.Len() > 0 {
			b.WriteByte('_')
			underscore = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, cfg, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	if cfg > 0 {
		return cfg
	}
	return def
}
