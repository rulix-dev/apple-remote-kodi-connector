package idle

import (
	"context"
	"testing"
	"time"
)

type fakeKodi struct {
	playing  bool
	notified int
}

func (f *fakeKodi) Playing(context.Context) (bool, error) { return f.playing, nil }

func (f *fakeKodi) Notify(context.Context, string, string, time.Duration) error {
	f.notified++
	return nil
}

type harness struct {
	w      *Watcher
	kodi   *fakeKodi
	now    time.Time
	pauses int
}

func newHarness() *harness {
	h := &harness{kodi: &fakeKodi{playing: true}, now: time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)}
	h.w = New(60*time.Minute, 20*time.Second, h.kodi, func() { h.pauses++ })
	h.w.now = func() time.Time { return h.now }
	h.w.last = h.now
	return h
}

func (h *harness) at(d time.Duration) {
	h.now = h.now.Add(d)
	h.w.Step(context.Background())
}

func TestWarnsThenPauses(t *testing.T) {
	h := newHarness()
	h.at(59 * time.Minute)
	if h.kodi.notified != 0 {
		t.Fatal("warned before 60 min")
	}
	h.at(time.Minute)
	if h.kodi.notified != 1 {
		t.Fatalf("notified = %d at 60 min, want 1", h.kodi.notified)
	}
	h.at(19 * time.Second)
	if h.pauses != 0 {
		t.Fatal("paused before the 20 s grace")
	}
	h.at(time.Second)
	if h.pauses != 1 {
		t.Fatalf("pauses = %d after grace, want 1", h.pauses)
	}
	// A new period starts: no second warning right away.
	h.at(time.Minute)
	if h.kodi.notified != 1 {
		t.Fatalf("notified = %d right after pausing, want 1", h.kodi.notified)
	}
}

func TestPressDuringGraceCancels(t *testing.T) {
	h := newHarness()
	h.at(60 * time.Minute)
	h.now = h.now.Add(5 * time.Second)
	h.w.Activity()
	h.at(30 * time.Second)
	if h.pauses != 0 {
		t.Fatal("paused although the warning was answered")
	}
	h.at(59 * time.Minute)
	if h.kodi.notified != 1 {
		t.Fatalf("notified = %d, want 1 (period restarted at the press)", h.kodi.notified)
	}
}

func TestActivityResetsPeriod(t *testing.T) {
	h := newHarness()
	h.at(50 * time.Minute)
	h.w.Activity()
	h.at(50 * time.Minute)
	if h.kodi.notified != 0 {
		t.Fatal("warned although there was activity 50 min ago")
	}
}

func TestNoWarningWhenNotPlaying(t *testing.T) {
	h := newHarness()
	h.kodi.playing = false
	h.at(2 * time.Hour)
	if h.kodi.notified != 0 || h.pauses != 0 {
		t.Fatal("acted while nothing was playing")
	}
	// Playback starts later (e.g. next episode) with no activity: warn.
	h.kodi.playing = true
	h.at(time.Second)
	if h.kodi.notified != 1 {
		t.Fatalf("notified = %d, want 1", h.kodi.notified)
	}
}
