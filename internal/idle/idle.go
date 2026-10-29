// Package idle implements the "are you still watching?" safeguard: after a
// long time without any press on the remote while Kodi is playing, show a
// warning in Kodi and pause if nobody presses a button within a grace period.
package idle

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Kodi is what the watcher needs from Kodi.
type Kodi interface {
	Playing(ctx context.Context) (bool, error)
	Notify(ctx context.Context, title, message string, display time.Duration) error
}

// Watcher tracks remote activity. Step must be called periodically (e.g.
// every second) from a single goroutine; Activity may be called from any.
type Watcher struct {
	After time.Duration // inactivity before warning
	Grace time.Duration // time to answer the warning
	Kodi  Kodi
	Pause func() // called when nobody answered the warning

	now func() time.Time

	mu       sync.Mutex
	last     time.Time // last remote activity
	warnedAt time.Time // zero when no warning is pending
}

// New returns a watcher whose inactivity period starts now.
func New(after, grace time.Duration, kodi Kodi, pause func()) *Watcher {
	w := &Watcher{After: after, Grace: grace, Kodi: kodi, Pause: pause, now: time.Now}
	w.last = w.now()
	return w
}

// Activity records a press on the remote.
func (w *Watcher) Activity() {
	w.mu.Lock()
	w.last = w.now()
	w.mu.Unlock()
}

// Step checks the timers and acts when due.
func (w *Watcher) Step(ctx context.Context) {
	w.mu.Lock()
	now, last, warnedAt := w.now(), w.last, w.warnedAt
	w.mu.Unlock()

	if !warnedAt.IsZero() {
		switch {
		case last.After(warnedAt):
			slog.Info("idle warning answered")
			w.setWarned(time.Time{})
		case now.Sub(warnedAt) >= w.Grace:
			slog.Info("idle warning not answered: pausing Kodi", "grace", w.Grace)
			w.Pause()
			w.mu.Lock()
			w.warnedAt, w.last = time.Time{}, now // start a new period
			w.mu.Unlock()
		}
		return
	}
	if now.Sub(last) < w.After {
		return
	}
	playing, err := w.Kodi.Playing(ctx)
	if err != nil || !playing {
		return // nothing to protect; check again on the next step
	}
	msg := "Press any button on the remote to keep watching"
	if err := w.Kodi.Notify(ctx, "Are you still there?", msg, w.Grace); err != nil {
		slog.Warn("cannot show idle warning in Kodi", "err", err)
	}
	slog.Info("no remote activity: idle warning shown", "idle_for", now.Sub(last).Round(time.Second), "grace", w.Grace)
	w.setWarned(now)
}

func (w *Watcher) setWarned(t time.Time) {
	w.mu.Lock()
	w.warnedAt = t
	w.mu.Unlock()
}
