package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/button"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte("{}"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if c.LongPress != 700*time.Millisecond {
		t.Errorf("LongPress = %v, want 700ms", c.LongPress)
	}
	if c.Kodi.URL != "http://127.0.0.1:8080/jsonrpc" || c.Kodi.FlatpakID != "tv.kodi.Kodi" {
		t.Errorf("Kodi defaults = %+v", c.Kodi)
	}
	b, ok := c.ButtonFor(139)
	if !ok || b.Short != ActionBackOrLaunch || b.Long != ActionQuit {
		t.Errorf("default Menu button = %+v, %v", b, ok)
	}
}

func TestParseButtonsAndSpecs(t *testing.T) {
	c, err := Parse([]byte(`
long_press: 800ms
buttons:
  KEY_MENU: {short: back_or_launch, long: quit}
  KEY_ENTER: {short: "key:return", long: "action:contextmenu", long_press: 1500ms}
  "164": {short: "action:playpause"}
  KEY_VOLUMEUP: {short: "key:up", repeat: true}
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	got := c.Specs()
	want := map[uint16]button.Spec{
		139: {LongPress: 800 * time.Millisecond},
		28:  {LongPress: 1500 * time.Millisecond},
		164: {},
		115: {Repeat: true},
	}
	if len(got) != len(want) {
		t.Fatalf("Specs() = %v, want %v", got, want)
	}
	for code, spec := range want {
		if got[code] != spec {
			t.Errorf("Specs()[%d] = %+v, want %+v", code, got[code], spec)
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	tests := map[string]string{
		"unknown action":    "buttons: {KEY_MENU: {short: explode}}",
		"unknown key":       "buttons: {KEY_NOPE: {short: back}}",
		"empty action":      `buttons: {KEY_MENU: {short: "action:"}}`,
		"bad duration":      "long_press: 0s",
		"unknown field":     "long_pres: 1s",
		"repeat + long":     `buttons: {KEY_MENU: {short: "key:up", long: quit, repeat: true}}`,
		"empty key":         `buttons: {KEY_MENU: {short: "key:"}}`,
		"bad volume":        `buttons: {KEY_MENU: {short: "volume:x"}}`,
		"zero volume":       `buttons: {KEY_MENU: {short: "volume:0"}}`,
		"big volume":        `buttons: {KEY_MENU: {short: "volume:+101"}}`,
		"extra_long < long": "buttons: {KEY_MENU: {short: back, long: quit, extra_long: suspend, extra_long_press: 500ms}}",
		"repeat+extra_long": `buttons: {KEY_MENU: {short: "key:up", repeat: true, extra_long: suspend}}`,
		"idle zero":         "idle_pause: {enabled: true, after_minutes: 0}",
		"bad tv mac":        "auto_pause: {tv: {enabled: true, mac: nope}}",
		"bad window":        `buttons: {KEY_MENU: {short: back, in_window: {nope: back}}}`,
		"bad override":      `buttons: {KEY_MENU: {short: back, in_window: {fullscreenvideo: explode}}}`,
	}
	for name, doc := range tests {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: Parse() error = nil, want error", name)
		}
	}
}

func TestPasswordFromEnv(t *testing.T) {
	t.Setenv(PasswordEnv, "s3cret")
	p, src, err := Kodi{PasswordFile: "/nonexistent"}.Password()
	if err != nil || p != "s3cret" || src != "$KODI_PASSWORD" {
		t.Fatalf("Password() = %q, %q, %v", p, src, err)
	}
}

func TestPasswordFilePermissions(t *testing.T) {
	os.Unsetenv(PasswordEnv)
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Kodi{PasswordFile: path}).Password(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("Password() with 0644 error = %v, want permission error", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	p, _, err := Kodi{PasswordFile: path}.Password()
	if err != nil || p != "s3cret" {
		t.Fatalf("Password() = %q, %v; want s3cret (trailing newline trimmed)", p, err)
	}
}

func TestShortOverrides(t *testing.T) {
	c, err := Parse([]byte(`
buttons:
  KEY_VOLUMEUP:
    short: "key:up"
    repeat: true
    in_window: {fullscreenvideo: "action:volumeup", "10025": "key:pageup"}
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	b, _ := c.ButtonFor(115)
	got := b.ShortOverrides()
	if len(got) != 2 || got[12005] != "action:volumeup" || got[10025] != "key:pageup" {
		t.Fatalf("ShortOverrides() = %v", got)
	}
}

func TestParseVolume(t *testing.T) {
	for a, want := range map[string]int{"volume:+5": 5, "volume:-10": -10, "volume:3": 3} {
		if got, ok := ParseVolume(a); !ok || got != want {
			t.Errorf("ParseVolume(%q) = %d, %v; want %d", a, got, ok, want)
		}
	}
}

func TestExtraLongSpec(t *testing.T) {
	c, err := Parse([]byte(`
buttons:
  KEY_PLAYPAUSE: {short: "action:playpause", long: toggle_fullscreen, extra_long: suspend}
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := button.Spec{LongPress: 700 * time.Millisecond, ExtraLong: 5 * time.Second}
	if got := c.Specs()[164]; got != want {
		t.Fatalf("Specs()[KEY_PLAYPAUSE] = %+v, want %+v", got, want)
	}
}
