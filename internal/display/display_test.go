package display

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func fakeSystem(t *testing.T, lid string, connectors map[string]string) *Reader {
	t.Helper()
	root := t.TempDir()
	if lid != "" {
		dir := filepath.Join(root, "lid", "LID0")
		mustWrite(t, filepath.Join(dir, "state"), "state:      "+lid+"\n")
	}
	for name, status := range connectors {
		mustWrite(t, filepath.Join(root, "drm", name, "status"), status+"\n")
	}
	return &Reader{
		LidGlob: filepath.Join(root, "lid", "*", "state"),
		DRMGlob: filepath.Join(root, "drm", "card*-*"),
	}
}

func mustWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Connectors as seen on the MacBookPro8,1 of this project.
func macConnectors(hdmi string) map[string]string {
	return map[string]string{
		"card1-LVDS-1":   "connected", // built-in panel: always connected
		"card1-HDMI-A-1": hdmi,        // TV through the Mini DisplayPort adapter
		"card1-DP-1":     "disconnected",
		"card1-VGA-1":    "disconnected",
	}
}

func TestRead(t *testing.T) {
	tests := []struct {
		name      string
		lid, hdmi string
		unwatched bool
	}{
		{"lid closed, TV connected", "closed", "connected", false},
		{"lid closed, TV gone", "closed", "disconnected", true},
		{"lid open, TV gone", "open", "disconnected", false},
		{"lid open, TV connected", "open", "connected", false},
	}
	for _, tt := range tests {
		s, ok := fakeSystem(t, tt.lid, macConnectors(tt.hdmi)).Read()
		if !ok {
			t.Fatalf("%s: Read() ok = false", tt.name)
		}
		if s.Unwatched() != tt.unwatched {
			t.Errorf("%s: Unwatched() = %v, want %v (state %+v)", tt.name, s.Unwatched(), tt.unwatched, s)
		}
	}
	s, _ := fakeSystem(t, "closed", macConnectors("connected")).Read()
	if !slices.Equal(s.External, []string{"card1-HDMI-A-1"}) {
		t.Errorf("External = %v, want [card1-HDMI-A-1] (LVDS must be ignored)", s.External)
	}
}

func TestReadWithoutLid(t *testing.T) {
	if _, ok := fakeSystem(t, "", macConnectors("disconnected")).Read(); ok {
		t.Fatal("Read() ok = true without a lid switch")
	}
}

func TestTrigger(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	tr := &Trigger{Debounce: 3 * time.Second}

	steps := []struct {
		at   float64
		cond bool
		want bool
	}{
		{0, true, false},   // condition starts
		{1, false, false},  // brief blip (TV renegotiating): resets
		{2, true, false},   // starts again
		{4.9, true, false}, // not yet 3 s
		{5, true, true},    // fires
		{8, true, false},   // fires only once
		{9, false, false},  // TV back
		{10, true, false},  // gone again
		{13, true, true},   // re-armed: fires again
	}
	for _, s := range steps {
		if got := tr.Update(s.cond, at(s.at)); got != s.want {
			t.Fatalf("t=%vs cond=%v: Update() = %v, want %v", s.at, s.cond, got, s.want)
		}
	}
}

func TestTVOff(t *testing.T) {
	tests := []struct {
		lid, hdmi string
		tvOff     bool
		want      bool
	}{
		{"closed", "connected", true, true},     // TV turned off with its remote
		{"closed", "connected", false, false},   // TV on
		{"open", "connected", true, false},      // someone may watch on the laptop
		{"closed", "disconnected", true, false}, // covered by Unwatched instead
	}
	for _, tt := range tests {
		s, _ := fakeSystem(t, tt.lid, macConnectors(tt.hdmi)).Read()
		if got := s.TVOff(tt.tvOff); got != tt.want {
			t.Errorf("lid=%s hdmi=%s tvOff=%v: TVOff() = %v, want %v", tt.lid, tt.hdmi, tt.tvOff, got, tt.want)
		}
	}
}
