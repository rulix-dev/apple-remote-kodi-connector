// Package controller turns configured actions into Kodi requests, applying
// the launch, quit and de-duplication policies.
//
// Policies:
//   - Kodi is launched only when no instance exists (flatpak ps / kodi.bin)
//     and no launch is in progress. After a launch, presses that need Kodi
//     are ignored until it answers JSON-RPC or startup_grace elapses.
//   - If Kodi is running but JSON-RPC fails outside that window, the error is
//     reported and nothing is launched.
//   - Quit uses Application.Quit. While Kodi is quitting every press is
//     ignored, so a long press can never close Kodi twice nor be followed by
//     a launch of a second instance. If Kodi does not exit within
//     quit_timeout (or JSON-RPC is unavailable), SIGTERM is sent to kodi.bin
//     when signal_fallback is enabled.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/config"
)

// RPC is the subset of the Kodi JSON-RPC client used by the controller.
type RPC interface {
	Back(ctx context.Context) error
	Quit(ctx context.Context) error
	ExecuteAction(ctx context.Context, action string) error
	KeyPress(ctx context.Context, key string) error
	CurrentWindow(ctx context.Context) (int, error)
	AdjustVolume(ctx context.Context, delta int) (int, error)
	PausePlayback(ctx context.Context) (int, error)
}

// Process is the subset of the Kodi process manager used by the controller.
type Process interface {
	Running(ctx context.Context) bool
	Launch(ctx context.Context) error
	Terminate() (int, error)
	Raise(ctx context.Context, windowClass string) error
	Suspend(ctx context.Context) error
}

// Outcome describes what an action did.
type Outcome string

const (
	Sent            Outcome = "sent"
	Launched        Outcome = "launched"
	AlreadyRunning  Outcome = "ignored: Kodi is already running"
	QuitRequested   Outcome = "quit requested"
	IgnoredStarting Outcome = "ignored: Kodi is starting"
	IgnoredQuitting Outcome = "ignored: Kodi is quitting"
	NotRunning      Outcome = "ignored: Kodi is not running"
	NothingPlaying  Outcome = "ignored: nothing is playing"
	Suspended       Outcome = "suspended"
	Failed          Outcome = "failed"
)

// Options tunes the policies.
type Options struct {
	StartupGrace   time.Duration
	QuitTimeout    time.Duration
	PollInterval   time.Duration
	SignalFallback bool
	WindowClass    string // WM_CLASS of the Kodi window, for toggle_fullscreen
}

// Controller executes actions. Do must be called from a single goroutine;
// the background wait for Kodi to exit runs concurrently.
type Controller struct {
	rpc  RPC
	proc Process
	opts Options
	now  func() time.Time

	mu         sync.Mutex
	launchedAt time.Time // zero when no launch is in progress
	quitting   bool
	wg         sync.WaitGroup
}

// New returns a controller.
func New(rpc RPC, proc Process, opts Options) *Controller {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 500 * time.Millisecond
	}
	return &Controller{rpc: rpc, proc: proc, opts: opts, now: time.Now}
}

// Do executes a configured action.
func (c *Controller) Do(ctx context.Context, action string) (Outcome, error) {
	if c.isQuitting() {
		return IgnoredQuitting, nil
	}
	switch action {
	case config.ActionBackOrLaunch:
		if !c.proc.Running(ctx) {
			return c.launch(ctx)
		}
		return c.call(ctx, c.rpc.Back)
	case config.ActionLaunch:
		if c.proc.Running(ctx) {
			return AlreadyRunning, nil
		}
		return c.launch(ctx)
	case config.ActionBack:
		return c.requireRunning(ctx, c.rpc.Back)
	case config.ActionQuit:
		return c.quit(ctx)
	case config.ActionFullscreen:
		return c.requireRunning(ctx, func(ctx context.Context) error {
			// Raising is best effort: toggling still works if it fails.
			if err := c.proc.Raise(ctx, c.opts.WindowClass); err != nil {
				slog.Warn("cannot bring Kodi to the front", "err", err)
			}
			return c.rpc.ExecuteAction(ctx, "togglefullscreen")
		})
	case config.ActionSuspend:
		// Works whether Kodi is open or not; pausing first is best effort.
		if c.proc.Running(ctx) {
			if _, err := c.rpc.PausePlayback(ctx); err != nil {
				slog.Warn("cannot pause Kodi before suspending", "err", err)
			}
		}
		if err := c.proc.Suspend(ctx); err != nil {
			return Failed, err
		}
		return Suspended, nil
	case config.ActionPause:
		if !c.proc.Running(ctx) {
			return NotRunning, nil
		}
		n, err := c.rpc.PausePlayback(ctx)
		if err != nil {
			return Failed, fmt.Errorf("pause: %w", err)
		}
		if n == 0 {
			return NothingPlaying, nil
		}
		return Sent, nil
	}
	if name, ok := strings.CutPrefix(action, config.ActionPrefix); ok {
		return c.requireRunning(ctx, func(ctx context.Context) error { return c.rpc.ExecuteAction(ctx, name) })
	}
	if delta, ok := config.ParseVolume(action); ok {
		return c.requireRunning(ctx, func(ctx context.Context) error {
			_, err := c.rpc.AdjustVolume(ctx, delta)
			return err
		})
	}
	if key, ok := strings.CutPrefix(action, config.KeyPrefix); ok {
		return c.requireRunning(ctx, func(ctx context.Context) error { return c.rpc.KeyPress(ctx, key) })
	}
	return Failed, fmt.Errorf("unknown action %q", action)
}

// DoIn executes action, or the override for the active Kodi window if there
// is one. If the window cannot be determined (e.g. Kodi is closed), action is
// used. It returns the action actually executed.
func (c *Controller) DoIn(ctx context.Context, action string, inWindow map[int]string) (string, Outcome, error) {
	if len(inWindow) > 0 && !c.isQuitting() {
		if win, err := c.rpc.CurrentWindow(ctx); err == nil {
			if a, ok := inWindow[win]; ok {
				action = a
			}
		}
	}
	out, err := c.Do(ctx, action)
	return action, out, err
}

// Wait blocks until background work (waiting for Kodi to exit) finishes.
func (c *Controller) Wait() { c.wg.Wait() }

func (c *Controller) requireRunning(ctx context.Context, f func(context.Context) error) (Outcome, error) {
	if !c.proc.Running(ctx) {
		if c.starting() {
			return IgnoredStarting, nil
		}
		return NotRunning, nil
	}
	return c.call(ctx, f)
}

// call runs a JSON-RPC request against a running Kodi.
func (c *Controller) call(ctx context.Context, f func(context.Context) error) (Outcome, error) {
	if err := f(ctx); err != nil {
		if c.starting() {
			return IgnoredStarting, nil // the API is not up yet
		}
		return Failed, fmt.Errorf("Kodi is running but JSON-RPC failed: %w", err)
	}
	c.mu.Lock()
	c.launchedAt = time.Time{} // Kodi answers: the launch is complete
	c.mu.Unlock()
	return Sent, nil
}

func (c *Controller) launch(ctx context.Context) (Outcome, error) {
	if c.starting() {
		return IgnoredStarting, nil
	}
	if err := c.proc.Launch(ctx); err != nil {
		return Failed, fmt.Errorf("launch Kodi: %w", err)
	}
	c.mu.Lock()
	c.launchedAt = c.now()
	c.mu.Unlock()
	return Launched, nil
}

func (c *Controller) quit(ctx context.Context) (Outcome, error) {
	if !c.proc.Running(ctx) {
		if c.starting() {
			return IgnoredStarting, nil
		}
		return NotRunning, nil
	}
	c.setQuitting(true)
	signalled := false
	if err := c.rpc.Quit(ctx); err != nil {
		if !c.opts.SignalFallback {
			c.setQuitting(false)
			return Failed, fmt.Errorf("quit Kodi: %w", err)
		}
		slog.Warn("Application.Quit failed; sending SIGTERM to kodi.bin as fallback", "err", err)
		if _, terr := c.proc.Terminate(); terr != nil {
			c.setQuitting(false)
			return Failed, errors.Join(fmt.Errorf("quit Kodi: %w", err), terr)
		}
		signalled = true
	}
	c.wg.Add(1)
	go c.awaitExit(signalled)
	return QuitRequested, nil
}

// awaitExit waits for Kodi to exit, escalating to SIGTERM once if allowed.
func (c *Controller) awaitExit(signalled bool) {
	defer c.wg.Done()
	defer c.setQuitting(false)
	for {
		if c.waitGone(c.opts.QuitTimeout) {
			c.mu.Lock()
			c.launchedAt = time.Time{}
			c.mu.Unlock()
			slog.Info("Kodi has exited")
			return
		}
		if signalled || !c.opts.SignalFallback {
			slog.Error("Kodi is still running after quit", "waited", c.opts.QuitTimeout)
			return
		}
		slog.Warn("Kodi did not exit in time; sending SIGTERM to kodi.bin", "waited", c.opts.QuitTimeout)
		if _, err := c.proc.Terminate(); err != nil {
			slog.Error("SIGTERM to kodi.bin failed", "err", err)
			return
		}
		signalled = true
	}
}

func (c *Controller) waitGone(timeout time.Duration) bool {
	deadline := c.now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		running := c.proc.Running(ctx)
		cancel()
		if !running {
			return true
		}
		if !c.now().Before(deadline) {
			return false
		}
		time.Sleep(c.opts.PollInterval)
	}
}

func (c *Controller) starting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.launchedAt.IsZero() && c.now().Sub(c.launchedAt) < c.opts.StartupGrace
}

func (c *Controller) isQuitting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quitting
}

func (c *Controller) setQuitting(v bool) {
	c.mu.Lock()
	c.quitting = v
	c.mu.Unlock()
}
