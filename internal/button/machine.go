// Package button turns raw key press/release events into short, long and
// repeated presses.
//
// The machine is pure: callers pass the current time explicitly and poll Tick
// at NextDeadline, which makes it deterministic and easy to test.
package button

import (
	"maps"
	"time"
)

// Kind classifies an event emitted by the machine.
type Kind int

const (
	// None means nothing to report.
	None Kind = iota
	// Short is emitted on release when the button was held less than its threshold.
	Short
	// Long is emitted as soon as the button has been held for its threshold,
	// without waiting for the release.
	Long
	// LongReleased is emitted on the release that follows a Long. It is
	// informational only and must not trigger an action.
	LongReleased
	// Pressed is emitted on key-down by buttons in repeat mode.
	Pressed
	// Repeat is emitted periodically while a repeat-mode button is held.
	Repeat
	// Released is emitted on key-up by buttons in repeat mode. Informational.
	Released
	// ExtraLong is emitted as soon as a button with a ExtraLong threshold has
	// been held that long. The following release emits LongReleased.
	ExtraLong
)

func (k Kind) String() string {
	switch k {
	case Short:
		return "short"
	case Long:
		return "long"
	case LongReleased:
		return "long-released"
	case Pressed:
		return "pressed"
	case Repeat:
		return "repeat"
	case Released:
		return "released"
	case ExtraLong:
		return "extra-long"
	default:
		return "none"
	}
}

// Event is the result of resolving a press.
type Event struct {
	Code uint16
	Kind Kind
	Held time.Duration
}

// Spec configures how a button is resolved.
type Spec struct {
	// LongPress enables long presses when > 0: the press resolves as Long
	// once held this long, otherwise as Short on release.
	LongPress time.Duration
	// ExtraLong enables a third level when > 0: ExtraLong is emitted as soon
	// as the button is held this long. Because a held press may still become
	// ExtraLong, Long is then resolved on release instead of at LongPress.
	ExtraLong time.Duration
	// Repeat makes the button act on key-down (Pressed) and then
	// periodically while held (Repeat), like a keyboard. LongPress and
	// ExtraLong are ignored for repeat buttons.
	Repeat bool
}

// Timing configures repeat mode.
type Timing struct {
	RepeatDelay    time.Duration // from key-down to the first Repeat
	RepeatInterval time.Duration // between Repeats
}

type press struct {
	at         time.Time
	longFired  bool
	nextRepeat time.Time
}

// Machine tracks the state of a set of buttons, identified by their key code.
type Machine struct {
	specs   map[uint16]Spec
	timing  Timing
	pressed map[uint16]*press
}

// New returns a machine that tracks only the codes present in specs.
func New(specs map[uint16]Spec, timing Timing) *Machine {
	return &Machine{specs: maps.Clone(specs), timing: timing, pressed: make(map[uint16]*press)}
}

// Tracks reports whether the machine handles code.
func (m *Machine) Tracks(code uint16) bool {
	_, ok := m.specs[code]
	return ok
}

// Press records a key-down. accepted is false when the press is ignored:
// the code is not tracked, or the button is already down (duplicate press
// or a lost release), in which case the original press is kept. For repeat
// buttons it returns a Pressed event.
func (m *Machine) Press(code uint16, now time.Time) (ev Event, accepted bool) {
	spec, ok := m.specs[code]
	if !ok {
		return Event{}, false
	}
	if _, down := m.pressed[code]; down {
		return Event{}, false
	}
	p := &press{at: now}
	m.pressed[code] = p
	if spec.Repeat {
		p.nextRepeat = now.Add(m.timing.RepeatDelay)
		return Event{Code: code, Kind: Pressed}, true
	}
	return Event{}, true
}

// Release records a key-up. It returns false for releases without a matching
// press. If the threshold has elapsed but Tick has not run yet, the press is
// still resolved as Long so the classification never depends on polling.
func (m *Machine) Release(code uint16, now time.Time) (Event, bool) {
	p, down := m.pressed[code]
	if !down {
		return Event{}, false
	}
	delete(m.pressed, code)
	held := now.Sub(p.at)
	spec := m.specs[code]
	switch {
	case spec.Repeat:
		return Event{Code: code, Kind: Released, Held: held}, true
	case p.longFired:
		return Event{Code: code, Kind: LongReleased, Held: held}, true
	case spec.ExtraLong > 0 && held >= spec.ExtraLong:
		return Event{Code: code, Kind: ExtraLong, Held: held}, true
	case spec.LongPress > 0 && held >= spec.LongPress:
		return Event{Code: code, Kind: Long, Held: held}, true
	default:
		return Event{Code: code, Kind: Short, Held: held}, true
	}
}

// Tick emits the events that are due: a Long, at most once per press, for
// buttons held past their threshold, and at most one Repeat per held repeat
// button (a late Tick never produces a burst).
func (m *Machine) Tick(now time.Time) []Event {
	var out []Event
	for code, p := range m.pressed {
		spec := m.specs[code]
		held := now.Sub(p.at)
		switch {
		case spec.Repeat:
			if !now.Before(p.nextRepeat) {
				p.nextRepeat = now.Add(m.timing.RepeatInterval)
				out = append(out, Event{Code: code, Kind: Repeat, Held: held})
			}
		case p.longFired:
		case spec.ExtraLong > 0:
			if held >= spec.ExtraLong {
				p.longFired = true
				out = append(out, Event{Code: code, Kind: ExtraLong, Held: held})
			}
		case spec.LongPress > 0 && held >= spec.LongPress:
			p.longFired = true
			out = append(out, Event{Code: code, Kind: Long, Held: held})
		}
	}
	return out
}

// NextDeadline returns the earliest time at which Tick may emit an event.
func (m *Machine) NextDeadline() (time.Time, bool) {
	var next time.Time
	found := false
	for code, p := range m.pressed {
		spec := m.specs[code]
		var d time.Time
		switch {
		case spec.Repeat:
			d = p.nextRepeat
		case p.longFired:
			continue
		case spec.ExtraLong > 0:
			d = p.at.Add(spec.ExtraLong)
		case spec.LongPress > 0:
			d = p.at.Add(spec.LongPress)
		default:
			continue
		}
		if !found || d.Before(next) {
			next, found = d, true
		}
	}
	return next, found
}

// Reset forgets all pressed buttons, e.g. after the device disconnects.
func (m *Machine) Reset() {
	clear(m.pressed)
}
