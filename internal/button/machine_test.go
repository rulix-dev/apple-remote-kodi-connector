package button

import (
	"testing"
	"time"
)

const (
	menu  uint16 = 139
	enter uint16 = 28
	other uint16 = 115
)

var t0 = time.Date(2026, 10, 28, 22, 0, 0, 0, time.UTC)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

var timing = Timing{RepeatDelay: ms(400), RepeatInterval: ms(150)}

func newMachine() *Machine {
	return New(map[uint16]Spec{menu: {LongPress: ms(700)}, enter: {LongPress: ms(1500)}}, timing)
}

// accepted calls m.Press and reports only whether it was accepted.
func accepted(m *Machine, code uint16, at time.Time) bool {
	_, ok := m.Press(code, at)
	return ok
}

func TestShortPress(t *testing.T) {
	m := newMachine()
	if !accepted(m, menu, t0) {
		t.Fatal("Press() = false, want true")
	}
	if got := m.Tick(t0.Add(ms(131))); len(got) != 0 {
		t.Fatalf("Tick() before threshold = %v, want none", got)
	}
	got, ok := m.Release(menu, t0.Add(ms(131)))
	want := Event{Code: menu, Kind: Short, Held: ms(131)}
	if !ok || got != want {
		t.Fatalf("Release() = %v, %v; want %v, true", got, ok, want)
	}
}

func TestLongPressFiresOnceAndReleaseIsInformational(t *testing.T) {
	m := newMachine()
	m.Press(menu, t0)

	deadline, ok := m.NextDeadline()
	if !ok || !deadline.Equal(t0.Add(ms(700))) {
		t.Fatalf("NextDeadline() = %v, %v; want %v, true", deadline, ok, t0.Add(ms(700)))
	}

	got := m.Tick(t0.Add(ms(700)))
	if len(got) != 1 || got[0] != (Event{Code: menu, Kind: Long, Held: ms(700)}) {
		t.Fatalf("Tick() at threshold = %v, want one Long", got)
	}
	if again := m.Tick(t0.Add(ms(2000))); len(again) != 0 {
		t.Fatalf("second Tick() = %v, want none (long must fire once)", again)
	}
	if _, ok := m.NextDeadline(); ok {
		t.Fatal("NextDeadline() after Long fired: ok = true, want false")
	}

	rel, ok := m.Release(menu, t0.Add(ms(4723)))
	if !ok || rel.Kind != LongReleased {
		t.Fatalf("Release() after Long = %v, %v; want LongReleased", rel, ok)
	}
}

func TestReleaseAfterThresholdWithoutTickIsLong(t *testing.T) {
	m := newMachine()
	m.Press(menu, t0)
	got, ok := m.Release(menu, t0.Add(ms(828)))
	if !ok || got.Kind != Long {
		t.Fatalf("Release() = %v, %v; want Long", got, ok)
	}
}

func TestBoundary(t *testing.T) {
	tests := []struct {
		held time.Duration
		want Kind
	}{
		{ms(699), Short},
		{ms(700), Long},
	}
	for _, tt := range tests {
		m := newMachine()
		m.Press(menu, t0)
		got, _ := m.Release(menu, t0.Add(tt.held))
		if got.Kind != tt.want {
			t.Errorf("held %v: Kind = %v, want %v", tt.held, got.Kind, tt.want)
		}
	}
}

func TestDuplicatePressKeepsOriginal(t *testing.T) {
	m := newMachine()
	m.Press(menu, t0)
	if accepted(m, menu, t0.Add(ms(300))) {
		t.Fatal("duplicate Press() = true, want false")
	}
	got, _ := m.Release(menu, t0.Add(ms(800)))
	if got.Kind != Long || got.Held != ms(800) {
		t.Fatalf("Release() = %v, want Long held 800ms from the first press", got)
	}
}

func TestStrayReleaseIgnored(t *testing.T) {
	m := newMachine()
	if _, ok := m.Release(menu, t0); ok {
		t.Fatal("Release() without press: ok = true, want false")
	}
	m.Press(menu, t0)
	m.Release(menu, t0.Add(ms(100)))
	if _, ok := m.Release(menu, t0.Add(ms(200))); ok {
		t.Fatal("second Release(): ok = true, want false")
	}
}

func TestUntrackedCodeIgnored(t *testing.T) {
	m := newMachine()
	if accepted(m, other, t0) {
		t.Fatal("Press(untracked) = true, want false")
	}
	if _, ok := m.Release(other, t0.Add(ms(100))); ok {
		t.Fatal("Release(untracked): ok = true, want false")
	}
}

func TestPerButtonThresholdsAndDeadlineOrder(t *testing.T) {
	m := newMachine()
	m.Press(enter, t0)
	m.Press(menu, t0.Add(ms(100)))

	d, _ := m.NextDeadline()
	if want := t0.Add(ms(800)); !d.Equal(want) {
		t.Fatalf("NextDeadline() = %v, want %v (menu)", d, want)
	}
	got := m.Tick(t0.Add(ms(800)))
	if len(got) != 1 || got[0].Code != menu {
		t.Fatalf("Tick() at 800ms = %v, want only menu Long", got)
	}
	d, _ = m.NextDeadline()
	if want := t0.Add(ms(1500)); !d.Equal(want) {
		t.Fatalf("NextDeadline() = %v, want %v (enter)", d, want)
	}
	rel, _ := m.Release(enter, t0.Add(ms(1000)))
	if rel.Kind != Short {
		t.Fatalf("enter released at 1000ms = %v, want Short (threshold 1500ms)", rel.Kind)
	}
}

func TestConsecutiveShortPresses(t *testing.T) {
	m := newMachine()
	for i, start := range []int{0, 392, 800} {
		at := t0.Add(ms(start))
		m.Press(menu, at)
		got, ok := m.Release(menu, at.Add(ms(131)))
		if !ok || got.Kind != Short {
			t.Fatalf("press %d: Release() = %v, %v; want Short", i, got, ok)
		}
	}
}

func TestReset(t *testing.T) {
	m := newMachine()
	m.Press(menu, t0)
	m.Reset()
	if _, ok := m.NextDeadline(); ok {
		t.Fatal("NextDeadline() after Reset: ok = true, want false")
	}
	if got := m.Tick(t0.Add(ms(1000))); len(got) != 0 {
		t.Fatalf("Tick() after Reset = %v, want none", got)
	}
	if !accepted(m, menu, t0.Add(ms(1000))) {
		t.Fatal("Press() after Reset = false, want true")
	}
}

func TestNoLongPressThreshold(t *testing.T) {
	m := New(map[uint16]Spec{other: {}}, timing)
	m.Press(other, t0)
	if _, ok := m.NextDeadline(); ok {
		t.Fatal("NextDeadline() with long press disabled: ok = true, want false")
	}
	if got := m.Tick(t0.Add(ms(5000))); len(got) != 0 {
		t.Fatalf("Tick() = %v, want none", got)
	}
	got, ok := m.Release(other, t0.Add(ms(5000)))
	if !ok || got.Kind != Short {
		t.Fatalf("Release() = %v, %v; want Short", got, ok)
	}
}

func TestRepeatMode(t *testing.T) {
	m := New(map[uint16]Spec{other: {Repeat: true, LongPress: ms(700)}}, timing)

	ev, ok := m.Press(other, t0)
	if !ok || ev.Kind != Pressed {
		t.Fatalf("Press() = %v, %v; want Pressed", ev, ok)
	}
	if got := m.Tick(t0.Add(ms(399))); len(got) != 0 {
		t.Fatalf("Tick() before repeat delay = %v, want none", got)
	}
	d, _ := m.NextDeadline()
	if !d.Equal(t0.Add(ms(400))) {
		t.Fatalf("NextDeadline() = %v, want +400ms", d)
	}
	if got := m.Tick(t0.Add(ms(400))); len(got) != 1 || got[0].Kind != Repeat {
		t.Fatalf("Tick() at delay = %v, want one Repeat", got)
	}
	// A late tick (e.g. a busy loop) emits a single Repeat, never a burst,
	// and LongPress is ignored in repeat mode.
	if got := m.Tick(t0.Add(ms(2000))); len(got) != 1 || got[0].Kind != Repeat {
		t.Fatalf("late Tick() = %v, want exactly one Repeat", got)
	}
	d, _ = m.NextDeadline()
	if !d.Equal(t0.Add(ms(2150))) {
		t.Fatalf("NextDeadline() = %v, want +2150ms", d)
	}
	rel, ok := m.Release(other, t0.Add(ms(2100)))
	if !ok || rel.Kind != Released {
		t.Fatalf("Release() = %v, %v; want Released", rel, ok)
	}
	if _, ok := m.NextDeadline(); ok {
		t.Fatal("NextDeadline() after release: ok = true, want false")
	}
}

func TestNonRepeatPressEmitsNothing(t *testing.T) {
	m := newMachine()
	ev, ok := m.Press(menu, t0)
	if !ok || ev.Kind != None {
		t.Fatalf("Press() = %v, %v; want None, true", ev, ok)
	}
}

func TestExtraLongPress(t *testing.T) {
	spec := Spec{LongPress: ms(700), ExtraLong: ms(5000)}
	newM := func() *Machine { return New(map[uint16]Spec{other: spec}, timing) }

	// Short and long are resolved on release; no Long is emitted while held.
	m := newM()
	m.Press(other, t0)
	if got := m.Tick(t0.Add(ms(2000))); len(got) != 0 {
		t.Fatalf("Tick() at 2s = %v, want none (long is decided on release)", got)
	}
	if d, _ := m.NextDeadline(); !d.Equal(t0.Add(ms(5000))) {
		t.Fatalf("NextDeadline() = %v, want +5s", d)
	}
	if ev, _ := m.Release(other, t0.Add(ms(2000))); ev.Kind != Long {
		t.Fatalf("release at 2s = %v, want Long", ev.Kind)
	}
	m.Press(other, t0)
	if ev, _ := m.Release(other, t0.Add(ms(300))); ev.Kind != Short {
		t.Fatalf("release at 300ms = %v, want Short", ev.Kind)
	}

	// Very long fires while held, once, and the release is informational.
	m = newM()
	m.Press(other, t0)
	got := m.Tick(t0.Add(ms(5000)))
	if len(got) != 1 || got[0].Kind != ExtraLong {
		t.Fatalf("Tick() at 5s = %v, want one ExtraLong", got)
	}
	if again := m.Tick(t0.Add(ms(9000))); len(again) != 0 {
		t.Fatalf("Tick() at 9s = %v, want none", again)
	}
	if ev, _ := m.Release(other, t0.Add(ms(9000))); ev.Kind != LongReleased {
		t.Fatalf("release after extra long = %v, want LongReleased", ev.Kind)
	}

	// A release past the threshold before Tick ran still resolves as ExtraLong.
	m = newM()
	m.Press(other, t0)
	if ev, _ := m.Release(other, t0.Add(ms(5100))); ev.Kind != ExtraLong {
		t.Fatalf("late release = %v, want ExtraLong", ev.Kind)
	}
}

func TestLongStillImmediateWithoutExtraLong(t *testing.T) {
	m := newMachine() // menu: long 700ms, no extra long
	m.Press(menu, t0)
	if got := m.Tick(t0.Add(ms(700))); len(got) != 1 || got[0].Kind != Long {
		t.Fatalf("Tick() at 700ms = %v, want immediate Long", got)
	}
}
