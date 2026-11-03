//go:build linux

// Command apple-remote-kodi-connector reads the Apple Remote IR receiver, resolves short
// and long presses and controls Kodi through JSON-RPC.
//
// Usage:
//
//	apple-remote-kodi-connector [-config path]          run
//	apple-remote-kodi-connector -diag                   also log every key event
//	apple-remote-kodi-connector -check                  verify setup and exit
//	apple-remote-kodi-connector -version                print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	slogtrace "github.com/DataDog/dd-trace-go/contrib/log/slog/v2"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/button"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/config"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/controller"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/display"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/evdev"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/idle"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/kodi"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/telemetry"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/tv"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

const (
	retryDelay    = 2 * time.Second
	actionTimeout = 15 * time.Second
	actionQueue   = 8
	// gaugeRefresh re-sends state gauges that otherwise only change rarely.
	gaugeRefresh = time.Minute
)

var errQueueFull = errors.New("action queue full")

func main() {
	configPath := flag.String("config", config.DefaultPath(), "configuration file")
	diag := flag.Bool("diag", false, "log every key event of the device (to discover button codes)")
	check := flag.Bool("check", false, "verify configuration, device access and Kodi JSON-RPC, then exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("cannot load configuration", "path", *configPath, "err", err)
		os.Exit(1)
	}
	setupLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ensureRuntimeDir(ctx, kodi.NewProcess(cfg.Kodi.FlatpakID, cfg.Kodi.Binary))

	if *check {
		if !runCheck(ctx, cfg, *configPath) {
			os.Exit(1)
		}
		return
	}

	password, source, err := cfg.Kodi.Password()
	if err != nil {
		slog.Error("cannot read Kodi password", "err", err)
		os.Exit(1)
	}
	slog.Info("starting", "version", version, "config", *configPath, "password_from", source, "kodi_url", cfg.Kodi.URL)

	if dd := cfg.Datadog; dd.Enabled {
		stopTelemetry, err := telemetry.Start(telemetry.Config{
			Service: dd.Service, Env: dd.Env, Version: version, AgentHost: dd.AgentHost,
			StatsdPort: dd.StatsdPort, TracePort: dd.TracePort, Tracing: dd.Tracing,
		})
		if err != nil {
			slog.Warn("datadog telemetry disabled", "err", err)
		}
		defer stopTelemetry()
		go heartbeat(ctx)
	}

	rpc := kodi.NewClient(cfg.Kodi.URL, cfg.Kodi.Username, password, cfg.Kodi.Timeout)
	proc := kodi.NewProcess(cfg.Kodi.FlatpakID, cfg.Kodi.Binary)
	ctl := controller.New(rpc, proc, controller.Options{
		StartupGrace:   cfg.Kodi.StartupGrace,
		QuitTimeout:    cfg.Kodi.QuitTimeout,
		SignalFallback: cfg.Kodi.SignalFallback,
		WindowClass:    cfg.Kodi.WindowClass,
	})

	actions := make(chan request, actionQueue)
	go worker(ctx, ctl, actions)

	if cfg.AutoPause.Enabled {
		go autoPause(ctx, cfg.AutoPause, actions)
	}

	var idleW *idle.Watcher
	if ip := cfg.IdlePause; ip.Enabled {
		idleW = idle.New(time.Duration(ip.AfterMinutes)*time.Minute, time.Duration(ip.WarningSeconds)*time.Second, rpc, func() {
			enqueue(ctx, actions, config.ActionPause, "idle_pause", "no_remote_activity")
		})
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
					idleW.Step(sctx)
					cancel()
				}
			}
		}()
		slog.Info("idle pause enabled", "after_minutes", ip.AfterMinutes, "warning_seconds", ip.WarningSeconds)
	}

	if cfg.Datadog.Enabled {
		go nowPlaying(ctx, rpc, proc, cfg.Datadog.NowPlayingInterval)
	}

	r := &reader{cfg: cfg, diag: *diag, actions: actions, idle: idleW}
	r.run(ctx)
	slog.Info("stopped")
}

// ensureRuntimeDir sets XDG_RUNTIME_DIR when it is missing (e.g. after
// `su - kodi`), so that flatpak and systemd-run --user can reach the user's
// instances and bus.
func ensureRuntimeDir(ctx context.Context, proc *kodi.Process) {
	if os.Getenv("XDG_RUNTIME_DIR") != "" {
		return
	}
	if dir := proc.Session(ctx).RuntimeDir; dir != "" {
		os.Setenv("XDG_RUNTIME_DIR", dir)
		slog.Info("XDG_RUNTIME_DIR was not set; using the user's runtime directory", "dir", dir)
	}
}

// setupLogger writes the log to stderr (the journal) as text or JSON. With
// Datadog enabled, records logged with a traced context carry dd.trace_id and
// dd.span_id, to correlate logs and traces.
func setupLogger(cfg *config.Config) {
	var h slog.Handler = slog.NewTextHandler(os.Stderr, nil)
	if cfg.Log.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, nil)
	}
	if cfg.Datadog.Enabled {
		h = slogtrace.WrapHandler(h)
	}
	slog.SetDefault(slog.New(h))
}

// heartbeat sends apple_remote.up periodically, to alert if the daemon stops.
func heartbeat(ctx context.Context) {
	tick := time.NewTicker(gaugeRefresh)
	defer tick.Stop()
	for {
		telemetry.Gauge("apple_remote.up", 1)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// request is an action to execute, with optional per-window overrides.
type request struct {
	action   string
	inWindow map[int]string
	repeat   bool            // repeated while the button is held: logged at debug level
	span     *telemetry.Span // root span of the press (or automatic action), finished by the worker
}

// enqueue queues an automatic action (not triggered by a button) in its own trace.
func enqueue(ctx context.Context, actions chan<- request, action, operation, reason string) {
	span, _ := telemetry.StartSpan(ctx, operation, reason, telemetry.T("action", action))
	select {
	case actions <- request{action: action, span: span}:
	default:
		slog.Warn("action queue full; automatic action dropped", "action", action, "reason", reason)
		span.Finish(errQueueFull)
	}
}

// autoPause polls the lid, the display outputs and (optionally) the network
// TV, and queues a pause, once per occurrence, when the lid is closed and
// either no external display is connected or the TV is off.
func autoPause(ctx context.Context, cfg config.AutoPause, actions chan<- request) {
	rd := display.NewReader()
	if _, ok := rd.Read(); !ok {
		slog.Warn("auto_pause disabled: no lid switch found")
		return
	}
	var tvOff atomic.Bool
	if cfg.TV.Enabled {
		go watchTV(ctx, cfg.TV, &tvOff)
	}
	noDisplay := &display.Trigger{Debounce: cfg.Debounce}
	tvGone := &display.Trigger{Debounce: cfg.TV.Debounce}
	tick := time.NewTicker(cfg.PollInterval)
	defer tick.Stop()
	var last string
	var lastGauge time.Time
	for {
		st, _ := rd.Read()
		now := time.Now()
		desc := fmt.Sprintf("lid_closed=%v external=%v", st.LidClosed, st.External)
		changed := desc != last
		if changed {
			slog.Info("display state", "lid_closed", st.LidClosed, "external", st.External)
			last = desc
		}
		if changed || now.Sub(lastGauge) >= gaugeRefresh {
			telemetry.Bool("apple_remote.display.lid_closed", st.LidClosed)
			telemetry.Gauge("apple_remote.display.external", float64(len(st.External)))
			lastGauge = now
		}
		reason, tag := "", ""
		if noDisplay.Update(st.Unwatched(), now) {
			reason, tag = "lid closed and no external display", "no_display"
		}
		if tvGone.Update(cfg.TV.Enabled && st.TVOff(tvOff.Load()), now) {
			reason, tag = "lid closed and TV turned off", "tv_off"
		}
		if reason != "" {
			slog.Info("pausing Kodi: " + reason)
			telemetry.Count("apple_remote.auto_pause", "reason:"+tag)
			enqueue(ctx, actions, config.ActionPause, "auto_pause", tag)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// watchTV polls the network TV and stores whether it is off.
func watchTV(ctx context.Context, cfg config.TV, off *atomic.Bool) {
	p := tv.NewProber(cfg.MAC)
	p.Port = strconv.Itoa(cfg.Port)
	tick := time.NewTicker(cfg.PollInterval)
	defer tick.Stop()
	first := true
	for {
		// One trace per probe, kept only when the state changes.
		span, sctx := telemetry.StartSpan(ctx, "tv.probe", "webos")
		on := p.On(sctx)
		span.SetTag("tv.on", on)
		if changed := off.Load() == on; first || changed {
			slog.InfoContext(sctx, "TV state", "on", on, "ip", p.IP(), "mac", cfg.MAC)
			if !first {
				state := "off"
				if on {
					state = "on"
				}
				telemetry.Count("apple_remote.tv.state_change", "state:"+state)
			}
			span.Keep()
			first = false
		} else {
			span.Drop()
		}
		span.Finish(nil)
		telemetry.Bool("apple_remote.tv.on", on)
		off.Store(!on)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// worker executes actions one at a time, so requests to Kodi never overlap.
func worker(ctx context.Context, ctl *controller.Controller, actions <-chan request) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-actions:
			actx, cancel := context.WithTimeout(telemetry.ContextWithSpan(ctx, req.span), actionTimeout)
			span, actx := telemetry.StartChildSpan(actx, "controller.action", req.action)
			start := time.Now()
			action, out, err := ctl.DoIn(actx, req.action, req.inWindow)
			took := time.Since(start)
			outcome := telemetry.Slug(string(out))
			span.SetTag("action", action)
			span.SetTag("outcome", outcome)
			span.Finish(err)
			req.span.SetTag("outcome", outcome)
			req.span.Finish(err)
			telemetry.Count("apple_remote.action", "action:"+action, "outcome:"+outcome)
			telemetry.Duration("apple_remote.action.duration", took, "action:"+action)
			took = took.Round(time.Millisecond)
			switch {
			case err != nil:
				slog.ErrorContext(actx, "action failed", "action", action, "err", err, "took", took)
			case req.repeat:
				slog.DebugContext(actx, "action done", "action", action, "outcome", out, "took", took)
			default:
				slog.InfoContext(actx, "action done", "action", action, "outcome", out, "took", took)
			}
			cancel()
		}
	}
}

type reader struct {
	cfg     *config.Config
	diag    bool
	actions chan<- request
	idle    *idle.Watcher // nil when idle_pause is disabled
}

// run keeps (re)connecting to the device until ctx is cancelled.
func (r *reader) run(ctx context.Context) {
	var lastErr string
	for {
		err := r.listen(ctx)
		telemetry.Bool("apple_remote.device.connected", false)
		if ctx.Err() != nil {
			return
		}
		telemetry.Count("apple_remote.device.errors", "error:"+deviceErrorClass(err))
		// Log each distinct error once so a missing device does not flood the log.
		if msg := err.Error(); msg != lastErr {
			slog.Warn("device unavailable, retrying", "err", err, "every", retryDelay)
			lastErr = msg
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

func (r *reader) devicePath() (string, error) {
	if r.cfg.Device.Path != "" {
		return r.cfg.Device.Path, nil
	}
	return evdev.Find(evdev.Match{Vendor: r.cfg.Device.Vendor, Product: r.cfg.Device.Product})
}

func (r *reader) listen(ctx context.Context) error {
	path, err := r.devicePath()
	if err != nil {
		return err
	}
	dev, err := evdev.Open(path)
	if err != nil {
		return err
	}
	defer dev.Close()
	if r.cfg.Device.Grab {
		if err := dev.Grab(); err != nil {
			return fmt.Errorf("%w (is Input Remapper using the remote?)", err)
		}
	}
	slog.Info("listening", "device", dev.Path, "name", dev.Name, "grab", r.cfg.Device.Grab)
	telemetry.Bool("apple_remote.device.connected", true)
	telemetry.Count("apple_remote.device.connect")

	type readResult struct {
		events []evdev.Event
		err    error
	}
	reads := make(chan readResult)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			events, err := dev.Read()
			select {
			case reads <- readResult{events, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	m := button.New(r.cfg.Specs(), r.cfg.Timing())
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	armTimer := func() {
		timer.Stop()
		if d, ok := m.NextDeadline(); ok {
			timer.Reset(time.Until(d))
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			for _, ev := range m.Tick(time.Now()) {
				r.resolved(ev)
			}
			armTimer()
		case res := <-reads:
			if res.err != nil {
				m.Reset()
				return fmt.Errorf("read %s: %w", dev.Path, res.err)
			}
			for _, ev := range res.events {
				r.handle(m, ev)
			}
			armTimer()
		}
	}
}

func (r *reader) handle(m *button.Machine, ev evdev.Event) {
	if ev.Type != evdev.EvKey || ev.Value == evdev.ValueRepeat {
		return // autorepeat while held is not a new press; not logged to avoid flooding
	}
	if ev.Value == evdev.ValuePress && r.idle != nil {
		r.idle.Activity() // any button counts, even unassigned ones
	}
	key := evdev.KeyName(ev.Code)
	if r.diag {
		slog.Info("key event", "key", key, "code", ev.Code, "value", ev.Value)
	}
	if !m.Tracks(ev.Code) {
		return
	}
	now := time.Now()
	switch ev.Value {
	case evdev.ValuePress:
		res, ok := m.Press(ev.Code, now)
		if !ok {
			slog.Warn("duplicate press ignored", "key", key)
		} else if res.Kind != button.None {
			r.resolved(res)
		}
	case evdev.ValueRelease:
		if res, ok := m.Release(ev.Code, now); ok {
			r.resolved(res)
		} else {
			slog.Warn("release without press ignored", "key", key)
		}
	}
}

func (r *reader) resolved(ev button.Event) {
	key := evdev.KeyName(ev.Code)
	held := ev.Held.Round(time.Millisecond)
	b, _ := r.cfg.ButtonFor(ev.Code)
	var req request
	switch ev.Kind {
	case button.Short, button.Pressed:
		req = request{action: b.Short, inWindow: b.ShortOverrides()}
	case button.Long:
		req = request{action: b.Long}
	case button.ExtraLong:
		req = request{action: b.ExtraLong}
	case button.Repeat:
		telemetry.Count("apple_remote.button.press", "key:"+key, "press:repeat")
		// While held, only queue a repeat if Kodi has caught up, so that
		// releasing the button stops the movement immediately.
		if b.Short != "" && len(r.actions) == 0 {
			r.actions <- request{action: b.Short, inWindow: b.ShortOverrides(), repeat: true}
		}
		return
	case button.LongReleased, button.Released:
		slog.Info("release", "key", key, "kind", ev.Kind, "held", held)
		return
	}
	press := pressTag(ev.Kind)
	telemetry.Count("apple_remote.button.press", "key:"+key, "press:"+press)
	if req.action == "" {
		slog.Info("press", "key", key, "kind", ev.Kind, "held", held, "action", "")
		return
	}
	// One trace per press: button.press > controller.action > kodi.rpc / kodi.process.
	span, sctx := telemetry.StartSpan(context.Background(), "button.press", key+" "+press,
		telemetry.T("key", key), telemetry.T("press", press), telemetry.T("action", req.action),
		telemetry.T("held_ms", held.Milliseconds()))
	req.span = span
	slog.InfoContext(sctx, "press", "key", key, "kind", ev.Kind, "held", held, "action", req.action)
	select {
	case r.actions <- req:
	default:
		slog.WarnContext(sctx, "action queue full; press dropped", "key", key, "action", req.action)
		span.Finish(errQueueFull)
	}
}

// pressTag is the "press" metric tag of a button event.
func pressTag(k button.Kind) string {
	switch k {
	case button.Long:
		return "long"
	case button.ExtraLong:
		return "extralong"
	case button.Repeat:
		return "repeat"
	default:
		return "short"
	}
}

// deviceErrorClass classifies why the IR receiver could not be used.
func deviceErrorClass(err error) string {
	switch {
	case errors.Is(err, evdev.ErrNotFound):
		return "not_found"
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	case errors.Is(err, syscall.EBUSY):
		return "busy"
	default:
		return "read"
	}
}
