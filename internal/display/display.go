// Package display detects the "lid closed and no external display" condition
// from sysfs/procfs, readable by any user.
package display

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// internalConnectors are DRM connector types of a laptop's built-in panel.
var internalConnectors = []string{"-LVDS-", "-eDP-", "-DSI-"}

// State is a snapshot of the lid and the external outputs.
type State struct {
	LidClosed bool
	External  []string // connected external connectors, e.g. "card1-HDMI-A-1"
}

// Unwatched reports whether nobody can be watching: the lid is closed and no
// external display is connected.
func (s State) Unwatched() bool {
	return s.LidClosed && len(s.External) == 0
}

// TVOff reports the "TV turned off with its remote" case: the lid is closed,
// an external display is still reported as connected (TVs keep the HDMI
// link up in standby) and the TV itself says it is off (tvOff).
func (s State) TVOff(tvOff bool) bool {
	return s.LidClosed && len(s.External) > 0 && tvOff
}

// Reader reads State. Paths are configurable for tests.
type Reader struct {
	LidGlob string // default /proc/acpi/button/lid/*/state
	DRMGlob string // default /sys/class/drm/card*-*
}

// NewReader returns a Reader for the real system.
func NewReader() *Reader {
	return &Reader{LidGlob: "/proc/acpi/button/lid/*/state", DRMGlob: "/sys/class/drm/card*-*"}
}

// Read returns the current state. ok is false when the machine has no lid
// switch, in which case the condition can never be detected.
func (r *Reader) Read() (s State, ok bool) {
	lids, _ := filepath.Glob(r.LidGlob)
	for _, l := range lids {
		b, err := os.ReadFile(l)
		if err != nil {
			continue
		}
		ok = true
		if strings.Contains(string(b), "closed") {
			s.LidClosed = true
		}
	}
	conns, _ := filepath.Glob(r.DRMGlob)
	for _, c := range conns {
		name := filepath.Base(c)
		if isInternal(name) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c, "status"))
		if err == nil && strings.TrimSpace(string(b)) == "connected" {
			s.External = append(s.External, name)
		}
	}
	return s, ok
}

func isInternal(connector string) bool {
	for _, t := range internalConnectors {
		if strings.Contains(connector, t) {
			return true
		}
	}
	return false
}

// Trigger fires once when a condition has held continuously for a debounce
// period, and re-arms only after the condition becomes false. This ignores
// the brief disconnections that happen while a TV renegotiates its mode.
type Trigger struct {
	Debounce time.Duration
	since    time.Time
	fired    bool
}

// Update feeds the current condition and returns true when the trigger fires.
func (t *Trigger) Update(cond bool, now time.Time) bool {
	if !cond {
		t.since, t.fired = time.Time{}, false
		return false
	}
	if t.since.IsZero() {
		t.since = now
	}
	if !t.fired && now.Sub(t.since) >= t.Debounce {
		t.fired = true
		return true
	}
	return false
}
