//go:build linux

package main

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"time"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/config"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/display"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/evdev"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/kodi"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/tv"
)

// runCheck verifies the setup step by step and prints a human-readable
// report. It never prints the password. It returns false if a required
// check failed.
func runCheck(ctx context.Context, cfg *config.Config, path string) bool {
	ok := true
	pass := func(format string, a ...any) { fmt.Printf("  OK    "+format+"\n", a...) }
	warn := func(format string, a ...any) { fmt.Printf("  WARN  "+format+"\n", a...) }
	fail := func(format string, a ...any) { fmt.Printf("  FAIL  "+format+"\n", a...); ok = false }

	fmt.Println("Configuration")
	pass("loaded %s", path)
	keys := make([]string, 0, len(cfg.Buttons))
	for k := range cfg.Buttons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := cfg.Buttons[k]
		pass("%s: short=%q long=%q extra_long=%q repeat=%v in_window=%v", k, b.Short, b.Long, b.ExtraLong, b.Repeat, b.InWindow)
	}

	fmt.Println("Input device")
	r := &reader{cfg: cfg}
	if devPath, err := r.devicePath(); err != nil {
		fail("%v", err)
	} else if dev, err := evdev.Open(devPath); err != nil {
		fail("%v", err)
	} else {
		pass("%s (%s) readable", devPath, dev.Name)
		if cfg.Device.Grab {
			if err := dev.Grab(); err != nil {
				fail("exclusive access: %v (is Input Remapper using the remote?)", err)
			} else {
				pass("exclusive access (grab) available")
			}
		}
		dev.Close()
	}

	fmt.Println("Kodi JSON-RPC")
	password, source, err := cfg.Kodi.Password()
	if err != nil {
		fail("password: %v", err)
	} else {
		pass("password read from %s (not shown)", source)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		c := kodi.NewClient(cfg.Kodi.URL, cfg.Kodi.Username, password, cfg.Kodi.Timeout)
		if err := c.Ping(cctx); err != nil {
			warn("%s: %v (Kodi closed? web server disabled? SSL enabled?)", cfg.Kodi.URL, err)
		} else {
			pass("%s answers JSONRPC.Ping", cfg.Kodi.URL)
			if v, err := c.Version(cctx); err == nil {
				pass("Kodi %s", v)
			}
			for _, m := range []string{"Input.Back", "Application.Quit", "Input.ExecuteAction", "Input.ButtonEvent", "GUI.GetProperties", "Application.SetVolume", "Player.PlayPause", "GUI.ShowNotification"} {
				if has, err := c.HasMethod(cctx, m); err != nil {
					warn("%s: %v", m, err)
				} else if has {
					pass("method %s available", m)
				} else {
					fail("method %s NOT available", m)
				}
			}
		}
	}

	fmt.Println("Kodi process")
	proc := kodi.NewProcess(cfg.Kodi.FlatpakID, cfg.Kodi.Binary)
	if proc.Running(ctx) {
		pass("%s is running", cfg.Kodi.FlatpakID)
	} else {
		pass("%s is not running", cfg.Kodi.FlatpakID)
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		fail("systemd-run not found: %v", err)
	} else {
		pass("systemd-run available")
	}

	fmt.Println("Graphical session (needed to launch Kodi)")
	sess := proc.Session(ctx)
	if sess.RuntimeDir != "" {
		pass("XDG_RUNTIME_DIR=%s", sess.RuntimeDir)
	} else {
		fail("XDG_RUNTIME_DIR unknown: systemd-run --user cannot reach the user bus")
	}
	switch {
	case sess.Display != "":
		pass("DISPLAY=%s", sess.Display)
	case sess.Wayland != "":
		pass("WAYLAND_DISPLAY=%s", sess.Wayland)
	default:
		fail("no graphical session found (DISPLAY unknown): Kodi cannot be launched")
	}
	if sess.XAuthority != "" {
		pass("XAUTHORITY=%s", sess.XAuthority)
	} else if sess.Display != "" {
		warn("XAUTHORITY unknown (~/.Xauthority not found); Kodi may fail to open the display")
	}

	fmt.Println("Idle pause (no remote activity)")
	if ip := cfg.IdlePause; ip.Enabled {
		pass("warn after %d min without presses, pause %d s later", ip.AfterMinutes, ip.WarningSeconds)
	} else {
		pass("disabled in configuration")
	}

	fmt.Println("Auto pause (lid closed + no external display)")
	if !cfg.AutoPause.Enabled {
		pass("disabled in configuration")
	} else if st, found := display.NewReader().Read(); !found {
		warn("no lid switch found: auto pause cannot work")
	} else {
		pass("enabled; now: lid_closed=%v external=%v -> no-display rule would pause: %v",
			st.LidClosed, st.External, st.Unwatched())
		if t := cfg.AutoPause.TV; t.Enabled {
			p := tv.NewProber(t.MAC)
			p.Port = strconv.Itoa(t.Port)
			on := p.On(ctx)
			if p.IP() == "" {
				warn("TV %s not found on the network (off, or on another network?)", t.MAC)
			} else {
				pass("TV %s at %s: on=%v", t.MAC, p.IP(), on)
			}
			pass("TV rule would pause now: %v", st.TVOff(!on))
		}
	}
	if ok {
		fmt.Println("\nAll required checks passed.")
	} else {
		fmt.Println("\nSome required checks FAILED.")
	}
	return ok
}
