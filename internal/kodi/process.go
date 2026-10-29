package kodi

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// UnitName is the transient systemd user unit Kodi is launched in. Running
// Kodi in its own unit keeps it alive when this program's service restarts,
// and systemd refuses to start a second unit with the same name.
const UnitName = "apple-remote-kodi-connector-kodi"

// Process manages the Kodi Flatpak of the current user.
type Process struct {
	appID   string
	binary  string
	procDir string
	uid     int
}

// NewProcess returns a manager for the Flatpak appID whose main binary,
// as seen in /proc/<pid>/cmdline, is binary (e.g. /app/lib/kodi/kodi.bin).
func NewProcess(appID, binary string) *Process {
	return &Process{appID: appID, binary: binary, procDir: "/proc", uid: os.Getuid()}
}

// Running reports whether a Kodi instance of this user exists, including one
// that is still starting: it is listed by `flatpak ps` (which covers the
// sandbox before kodi.bin appears) or a kodi.bin process is present.
func (p *Process) Running(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "flatpak", "ps", "--columns=application").Output()
	if err != nil {
		slog.Warn("flatpak ps failed; checking /proc only", "err", err)
	} else {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == p.appID {
				return true
			}
		}
	}
	return len(p.pids()) > 0
}

// SessionEnv describes how Kodi will be connected to the user's session.
type SessionEnv struct {
	RuntimeDir string // XDG_RUNTIME_DIR, needed by systemd-run --user to reach the user bus
	Display    string // X11 display of the user's graphical session
	XAuthority string
	Wayland    string
}

// Session resolves the session variables, preferring this process's
// environment and falling back to what logind knows about the user's
// graphical session. This makes launching independent of where the program
// runs (a desktop terminal, `su - kodi`, or a systemd user service started
// before the desktop exported DISPLAY).
func (p *Process) Session(ctx context.Context) SessionEnv {
	e := SessionEnv{
		RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"),
		Display:    os.Getenv("DISPLAY"),
		XAuthority: os.Getenv("XAUTHORITY"),
		Wayland:    os.Getenv("WAYLAND_DISPLAY"),
	}
	uid := strconv.Itoa(p.uid)
	if e.RuntimeDir == "" {
		e.RuntimeDir = loginctl(ctx, "show-user", uid, "-p", "RuntimePath", "--value")
	}
	if e.Display == "" && e.Wayland == "" {
		if session := loginctl(ctx, "show-user", uid, "-p", "Display", "--value"); session != "" {
			e.Display = loginctl(ctx, "show-session", session, "-p", "Display", "--value")
		}
	}
	if e.XAuthority == "" && e.Display != "" {
		if home, err := os.UserHomeDir(); err == nil {
			if path := filepath.Join(home, ".Xauthority"); fileExists(path) {
				e.XAuthority = path
			}
		}
	}
	return e
}

// Launch starts Kodi in its own transient systemd user unit.
func (p *Process) Launch(ctx context.Context) error {
	e := p.Session(ctx)
	if e.Display == "" && e.Wayland == "" {
		return fmt.Errorf("no graphical session found for uid %d (DISPLAY unknown)", p.uid)
	}
	args := []string{"--user", "--collect", "--quiet", "--unit=" + UnitName, "--description=Kodi (apple-remote-kodi-connector)"}
	for k, v := range map[string]string{"DISPLAY": e.Display, "XAUTHORITY": e.XAuthority, "WAYLAND_DISPLAY": e.Wayland} {
		if v != "" {
			args = append(args, "--setenv="+k+"="+v)
		}
	}
	args = append(args, "flatpak", "run", p.appID)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	cmd.Env = os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" && e.RuntimeDir != "" {
		cmd.Env = append(cmd.Env, "XDG_RUNTIME_DIR="+e.RuntimeDir)
	}
	slog.Info("launching Kodi", "unit", UnitName, "display", e.Display, "xauthority", e.XAuthority, "wayland", e.Wayland)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func loginctl(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, "loginctl", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Raise brings the Kodi window to the front of the graphical session, using
// wmctrl (or xdotool as a fallback) with the session's DISPLAY/XAUTHORITY.
// windowClass is matched against WM_CLASS, e.g. "kodi.Kodi".
func (p *Process) Raise(ctx context.Context, windowClass string) error {
	e := p.Session(ctx)
	if e.Display == "" {
		return fmt.Errorf("no X11 display found for uid %d", p.uid)
	}
	env := append(os.Environ(), "DISPLAY="+e.Display)
	if e.XAuthority != "" {
		env = append(env, "XAUTHORITY="+e.XAuthority)
	}
	var cmd *exec.Cmd
	switch {
	case commandExists("wmctrl"):
		cmd = exec.CommandContext(ctx, "wmctrl", "-x", "-a", windowClass)
	case commandExists("xdotool"):
		class, _, _ := strings.Cut(windowClass, ".")
		cmd = exec.CommandContext(ctx, "xdotool", "search", "--onlyvisible", "--class", class, "windowactivate")
	default:
		return fmt.Errorf("neither wmctrl nor xdotool is installed")
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", cmd.Args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Suspend suspends the computer through logind (systemctl suspend). The
// user of the active local session is allowed to do it without a password.
func (p *Process) Suspend(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "suspend").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl suspend: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Terminate sends SIGTERM to this user's kodi.bin processes, matched exactly
// by their executable path, and returns how many were signalled. It is only
// a fallback when Application.Quit cannot be used: Kodi handles SIGTERM as a
// regular shutdown.
func (p *Process) Terminate() (int, error) {
	n := 0
	for _, pid := range p.pids() {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return n, fmt.Errorf("kill %d: %w", pid, err)
		}
		n++
	}
	return n, nil
}

// pids returns this user's processes whose argv[0] is exactly p.binary.
func (p *Process) pids() []int {
	entries, err := os.ReadDir(p.procDir)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(p.procDir, e.Name())
		info, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != p.uid {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		argv0, _, _ := bytes.Cut(cmdline, []byte{0})
		if string(argv0) == p.binary {
			pids = append(pids, pid)
		}
	}
	return pids
}
