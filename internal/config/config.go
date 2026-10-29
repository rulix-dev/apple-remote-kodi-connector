// Package config loads ~/.config/apple-remote-kodi-connector/config.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/button"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/evdev"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/kodi"
)

// PasswordEnv overrides kodi.password_file when set.
const PasswordEnv = "KODI_PASSWORD"

// Actions understood by the controller, besides "action:<kodi action>" and
// "key:<keyboard key>".
const (
	ActionBackOrLaunch = "back_or_launch"
	ActionBack         = "back"
	ActionLaunch       = "launch"
	ActionQuit         = "quit"
	ActionPause        = "pause"
	// ActionFullscreen brings Kodi to the front and toggles between windowed
	// and fullscreen mode.
	ActionFullscreen = "toggle_fullscreen"
	// ActionSuspend pauses Kodi (if playing) and suspends the computer.
	ActionSuspend = "suspend"
	ActionPrefix  = "action:"
	KeyPrefix     = "key:"
	VolumePrefix  = "volume:"
)

// ParseVolume parses the step of a "volume:<+N|-N>" action.
func ParseVolume(a string) (int, bool) {
	arg, ok := strings.CutPrefix(a, VolumePrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n == 0 || n < -100 || n > 100 {
		return 0, false
	}
	return n, true
}

// Config is the whole configuration file.
type Config struct {
	Device         Device            `yaml:"device"`
	LongPress      time.Duration     `yaml:"long_press"`
	ExtraLongPress time.Duration     `yaml:"extra_long_press"`
	RepeatDelay    time.Duration     `yaml:"repeat_delay"`
	RepeatInterval time.Duration     `yaml:"repeat_interval"`
	Kodi           Kodi              `yaml:"kodi"`
	AutoPause      AutoPause         `yaml:"auto_pause"`
	IdlePause      IdlePause         `yaml:"idle_pause"`
	Buttons        map[string]Button `yaml:"buttons"`
}

// AutoPause pauses Kodi when nobody can be watching: the lid is closed and
// no external display is connected (e.g. the TV was turned off).
type AutoPause struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Debounce     time.Duration `yaml:"debounce"`
	TV           TV            `yaml:"tv"`
}

// IdlePause warns in Kodi after AfterMinutes without any press on the remote
// while Kodi is playing, and pauses if nobody presses a button within
// WarningSeconds.
type IdlePause struct {
	Enabled        bool `yaml:"enabled"`
	AfterMinutes   int  `yaml:"after_minutes"`
	WarningSeconds int  `yaml:"warning_seconds"`
}

// TV pauses Kodi when the lid is closed, the external display is still
// connected and the network TV is off (it stops answering on Port).
type TV struct {
	Enabled      bool          `yaml:"enabled"`
	MAC          string        `yaml:"mac"`
	Port         int           `yaml:"port"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Debounce     time.Duration `yaml:"debounce"`
}

// Device selects the IR receiver.
type Device struct {
	Path    string `yaml:"path"`
	Vendor  string `yaml:"vendor"`
	Product string `yaml:"product"`
	Grab    bool   `yaml:"grab"`
}

// Kodi configures JSON-RPC and process management.
type Kodi struct {
	URL            string        `yaml:"url"`
	Username       string        `yaml:"username"`
	PasswordFile   string        `yaml:"password_file"`
	Timeout        time.Duration `yaml:"timeout"`
	FlatpakID      string        `yaml:"flatpak_id"`
	WindowClass    string        `yaml:"window_class"`
	Binary         string        `yaml:"binary"`
	StartupGrace   time.Duration `yaml:"startup_grace"`
	QuitTimeout    time.Duration `yaml:"quit_timeout"`
	SignalFallback bool          `yaml:"signal_fallback"`
}

// Button maps a key to the actions for a short and a long press.
// An empty Long disables long presses: any hold resolves as Short.
// With Repeat, Short runs on key-down and again periodically while the button
// is held (for navigation); Long must then be empty.
type Button struct {
	Short     string        `yaml:"short"`
	Long      string        `yaml:"long"`
	LongPress time.Duration `yaml:"long_press"`
	// ExtraLong runs as soon as the button has been held ExtraLongPress. On a
	// button with ExtraLong, Long runs on release instead of at LongPress.
	ExtraLong      string        `yaml:"extra_long"`
	ExtraLongPress time.Duration `yaml:"extra_long_press"`
	Repeat         bool          `yaml:"repeat"`
	// InWindow overrides Short while a given Kodi window is active, keyed by
	// window name ("fullscreenvideo") or numeric id.
	InWindow map[string]string `yaml:"in_window"`
}

// ShortOverrides returns InWindow keyed by Kodi window id.
func (b Button) ShortOverrides() map[int]string {
	if len(b.InWindow) == 0 {
		return nil
	}
	out := make(map[int]string, len(b.InWindow))
	for name, a := range b.InWindow {
		id, _ := kodi.WindowID(name) // validated in Parse
		out[id] = a
	}
	return out
}

// DefaultPath returns $XDG_CONFIG_HOME/apple-remote-kodi-connector/config.yaml.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "apple-remote-kodi-connector", "config.yaml")
}

func defaults() Config {
	return Config{
		Device:         Device{Vendor: "05ac", Product: "8242"},
		LongPress:      700 * time.Millisecond,
		ExtraLongPress: 5 * time.Second,
		RepeatDelay:    400 * time.Millisecond,
		RepeatInterval: 150 * time.Millisecond,
		Kodi: Kodi{
			URL:            "http://127.0.0.1:8080/jsonrpc",
			Username:       "kodi",
			PasswordFile:   "~/.config/apple-remote-kodi-connector/kodi-password",
			Timeout:        3 * time.Second,
			FlatpakID:      "tv.kodi.Kodi",
			WindowClass:    "kodi.Kodi",
			Binary:         "/app/lib/kodi/kodi.bin",
			StartupGrace:   60 * time.Second,
			QuitTimeout:    20 * time.Second,
			SignalFallback: true,
		},
		IdlePause: IdlePause{Enabled: true, AfterMinutes: 60, WarningSeconds: 20},
		AutoPause: AutoPause{
			Enabled: true, PollInterval: 250 * time.Millisecond, Debounce: time.Second,
			TV: TV{Port: 3000, PollInterval: 2 * time.Second, Debounce: 3 * time.Second},
		},
	}
}

// Load reads and validates the configuration file. Missing fields keep their
// defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes and validates a configuration document. Unknown fields are
// rejected so that typos do not silently fall back to defaults.
func Parse(b []byte) (*Config, error) {
	c := defaults()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(c.Buttons) == 0 {
		c.Buttons = map[string]Button{"KEY_MENU": {Short: ActionBackOrLaunch, Long: ActionQuit}}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	var errs []error
	if c.LongPress <= 0 || c.RepeatDelay <= 0 || c.RepeatInterval <= 0 {
		errs = append(errs, errors.New("long_press, repeat_delay and repeat_interval must be > 0"))
	}
	if c.Kodi.URL == "" {
		errs = append(errs, errors.New("kodi.url is required"))
	}
	if c.Kodi.Timeout <= 0 || c.Kodi.StartupGrace <= 0 || c.Kodi.QuitTimeout <= 0 {
		errs = append(errs, errors.New("kodi.timeout, kodi.startup_grace and kodi.quit_timeout must be > 0"))
	}
	if c.AutoPause.Enabled && (c.AutoPause.PollInterval <= 0 || c.AutoPause.Debounce < 0) {
		errs = append(errs, errors.New("auto_pause.poll_interval must be > 0 and auto_pause.debounce >= 0"))
	}
	if tv := c.AutoPause.TV; c.AutoPause.Enabled && tv.Enabled {
		if _, err := net.ParseMAC(tv.MAC); err != nil {
			errs = append(errs, fmt.Errorf("auto_pause.tv.mac: %w", err))
		}
		if tv.Port <= 0 || tv.Port > 65535 || tv.PollInterval <= 0 || tv.Debounce < 0 {
			errs = append(errs, errors.New("auto_pause.tv: port must be 1-65535, poll_interval > 0, debounce >= 0"))
		}
	}
	if ip := c.IdlePause; ip.Enabled && (ip.AfterMinutes <= 0 || ip.WarningSeconds <= 0) {
		errs = append(errs, errors.New("idle_pause: after_minutes and warning_seconds must be > 0"))
	}
	if c.Device.Path == "" && (c.Device.Vendor == "" || c.Device.Product == "") {
		errs = append(errs, errors.New("device.path or device.vendor+device.product is required"))
	}
	for key, b := range c.Buttons {
		if _, err := evdev.ParseKey(key); err != nil {
			errs = append(errs, fmt.Errorf("buttons: %w", err))
		}
		for _, a := range []string{b.Short, b.Long, b.ExtraLong} {
			if err := ValidateAction(a); err != nil {
				errs = append(errs, fmt.Errorf("buttons.%s: %w", key, err))
			}
		}
		if b.LongPress < 0 {
			errs = append(errs, fmt.Errorf("buttons.%s.long_press must be >= 0", key))
		}
		for win, a := range b.InWindow {
			if _, err := kodi.WindowID(win); err != nil {
				errs = append(errs, fmt.Errorf("buttons.%s.in_window: %w", key, err))
			}
			if a == "" {
				errs = append(errs, fmt.Errorf("buttons.%s.in_window.%s: empty action", key, win))
			} else if err := ValidateAction(a); err != nil {
				errs = append(errs, fmt.Errorf("buttons.%s.in_window.%s: %w", key, win, err))
			}
		}
		if b.Repeat && (b.Long != "" || b.ExtraLong != "") {
			errs = append(errs, fmt.Errorf("buttons.%s: repeat buttons cannot have long or extra_long actions", key))
		}
		if b.ExtraLong != "" {
			lp, elp := b.LongPress, b.ExtraLongPress
			if lp <= 0 {
				lp = c.LongPress
			}
			if elp <= 0 {
				elp = c.ExtraLongPress
			}
			if elp <= lp {
				errs = append(errs, fmt.Errorf("buttons.%s: extra_long_press (%v) must be longer than long_press (%v)", key, elp, lp))
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateAction accepts "" (no action), a built-in action, "action:<name>"
// or "key:<name>".
func ValidateAction(a string) error {
	switch a {
	case "", ActionBackOrLaunch, ActionBack, ActionLaunch, ActionQuit, ActionPause, ActionFullscreen, ActionSuspend:
		return nil
	}
	if strings.HasPrefix(a, VolumePrefix) {
		if _, ok := ParseVolume(a); !ok {
			return fmt.Errorf("invalid action %q: want volume:+N or volume:-N with N in 1..100", a)
		}
		return nil
	}
	for _, prefix := range []string{ActionPrefix, KeyPrefix} {
		if name, ok := strings.CutPrefix(a, prefix); ok && name != "" {
			return nil
		}
	}
	return fmt.Errorf("unknown action %q", a)
}

// Specs returns how every configured key must be resolved, as expected by
// button.New.
func (c *Config) Specs() map[uint16]button.Spec {
	out := make(map[uint16]button.Spec, len(c.Buttons))
	for key, b := range c.Buttons {
		code, _ := evdev.ParseKey(key) // validated in Parse
		spec := button.Spec{Repeat: b.Repeat}
		switch {
		case b.Repeat || b.Long == "":
		case b.LongPress > 0:
			spec.LongPress = b.LongPress
		default:
			spec.LongPress = c.LongPress
		}
		switch {
		case b.Repeat || b.ExtraLong == "":
		case b.ExtraLongPress > 0:
			spec.ExtraLong = b.ExtraLongPress
		default:
			spec.ExtraLong = c.ExtraLongPress
		}
		out[code] = spec
	}
	return out
}

// Timing returns the repeat timing, as expected by button.New.
func (c *Config) Timing() button.Timing {
	return button.Timing{RepeatDelay: c.RepeatDelay, RepeatInterval: c.RepeatInterval}
}

// ButtonFor returns the configuration of a key code.
func (c *Config) ButtonFor(code uint16) (Button, bool) {
	for key, b := range c.Buttons {
		if k, _ := evdev.ParseKey(key); k == code {
			return b, true
		}
	}
	return Button{}, false
}

// Password returns the Kodi password and where it came from. It reads
// $KODI_PASSWORD, or else kodi.password_file, which must not be accessible
// by group or others.
func (k Kodi) Password() (password, source string, err error) {
	if p, ok := os.LookupEnv(PasswordEnv); ok {
		return p, "$" + PasswordEnv, nil
	}
	path, err := expandHome(k.PasswordFile)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("password file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", "", fmt.Errorf("password file %s has permissions %04o; run: chmod 600 %s", path, perm, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("password file: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), path, nil
}

func expandHome(p string) (string, error) {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}
