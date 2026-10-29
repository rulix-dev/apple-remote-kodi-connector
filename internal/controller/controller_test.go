package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeRPC struct {
	mu        sync.Mutex
	err       error
	quitErr   error
	calls     []string
	onQuit    func()
	actionOf  []string
	window    int
	windowErr error
	playing   int
}

func (f *fakeRPC) record(m string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, m)
	return f.err
}

func (f *fakeRPC) Back(context.Context) error { return f.record("Input.Back") }

func (f *fakeRPC) Quit(context.Context) error {
	f.mu.Lock()
	f.calls = append(f.calls, "Application.Quit")
	err, onQuit := f.quitErr, f.onQuit
	f.mu.Unlock()
	if err == nil && onQuit != nil {
		onQuit()
	}
	return err
}

func (f *fakeRPC) ExecuteAction(_ context.Context, a string) error {
	f.mu.Lock()
	f.actionOf = append(f.actionOf, a)
	f.mu.Unlock()
	return f.record("Input.ExecuteAction")
}

func (f *fakeRPC) KeyPress(_ context.Context, k string) error {
	f.mu.Lock()
	f.actionOf = append(f.actionOf, "key:"+k)
	f.mu.Unlock()
	return f.record("Input.ButtonEvent")
}

func (f *fakeRPC) CurrentWindow(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.window, f.windowErr
}

func (f *fakeRPC) AdjustVolume(_ context.Context, d int) (int, error) {
	f.mu.Lock()
	f.actionOf = append(f.actionOf, fmt.Sprintf("volume:%+d", d))
	f.mu.Unlock()
	return 0, f.record("Application.SetVolume")
}

func (f *fakeRPC) PausePlayback(context.Context) (int, error) {
	f.mu.Lock()
	n := f.playing
	f.playing = 0
	f.mu.Unlock()
	return n, f.record("Player.PlayPause")
}

func (f *fakeRPC) count(m string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == m {
			n++
		}
	}
	return n
}

type fakeProc struct {
	mu          sync.Mutex
	running     bool
	launches    int
	terminates  int
	launchErr   error
	exitOnTerm  bool
	showOnStart bool // the instance becomes visible as soon as it is launched
	raised      []string
	raiseErr    error
	suspends    int
}

func (f *fakeProc) Running(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeProc) Launch(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.launchErr != nil {
		return f.launchErr
	}
	f.launches++
	if f.showOnStart {
		f.running = true
	}
	return nil
}

func (f *fakeProc) Terminate() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminates++
	if f.exitOnTerm {
		f.running = false
	}
	return 1, nil
}

func (f *fakeProc) Raise(_ context.Context, class string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raised = append(f.raised, class)
	return f.raiseErr
}

func (f *fakeProc) Suspend(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspends++
	return nil
}

func (f *fakeProc) set(running bool) {
	f.mu.Lock()
	f.running = running
	f.mu.Unlock()
}

func (f *fakeProc) stats() (launches, terminates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.launches, f.terminates
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTest(rpc *fakeRPC, proc *fakeProc, fallback bool) (*Controller, *clock) {
	c := New(rpc, proc, Options{
		StartupGrace:   60 * time.Second,
		QuitTimeout:    50 * time.Millisecond,
		PollInterval:   2 * time.Millisecond,
		SignalFallback: fallback,
	})
	clk := &clock{t: time.Date(2026, 10, 28, 22, 0, 0, 0, time.UTC)}
	c.now = clk.now
	return c, clk
}

func expect(t *testing.T, c *Controller, action string, want Outcome) {
	t.Helper()
	got, err := c.Do(context.Background(), action)
	if got != want {
		t.Fatalf("Do(%q) = %q (err %v), want %q", action, got, err, want)
	}
	if want == Failed && err == nil {
		t.Fatalf("Do(%q) returned Failed without error", action)
	}
	if want != Failed && err != nil {
		t.Fatalf("Do(%q) error = %v", action, err)
	}
}

func TestShortWhenClosedLaunchesOnce(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{}
	c, clk := newTest(rpc, proc, true)

	expect(t, c, "back_or_launch", Launched)
	// More presses while the sandbox is not visible yet: no second instance.
	clk.advance(time.Second)
	expect(t, c, "back_or_launch", IgnoredStarting)
	// Instance visible but the API is not up yet.
	proc.set(true)
	rpc.err = errors.New("connection refused")
	clk.advance(5 * time.Second)
	expect(t, c, "back_or_launch", IgnoredStarting)

	if l, _ := proc.stats(); l != 1 {
		t.Fatalf("launches = %d, want 1", l)
	}
}

func TestLaunchRetriedAfterGraceIfKodiNeverAppeared(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{}
	c, clk := newTest(rpc, proc, true)
	expect(t, c, "back_or_launch", Launched)
	clk.advance(61 * time.Second)
	expect(t, c, "back_or_launch", Launched)
	if l, _ := proc.stats(); l != 2 {
		t.Fatalf("launches = %d, want 2", l)
	}
}

func TestLaunchFailureIsReportedAndRetryable(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{launchErr: errors.New("systemd-run: boom")}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "back_or_launch", Failed)
	proc.launchErr = nil
	expect(t, c, "back_or_launch", Launched)
}

func TestShortWhenRunningSendsBack(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "back_or_launch", Sent)
	if n := rpc.count("Input.Back"); n != 1 {
		t.Fatalf("Input.Back calls = %d, want 1", n)
	}
}

func TestRunningButAPIDownReportsErrorWithoutLaunching(t *testing.T) {
	rpc, proc := &fakeRPC{err: errors.New("timeout")}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "back_or_launch", Failed)
	if l, _ := proc.stats(); l != 0 {
		t.Fatalf("launches = %d, want 0", l)
	}
}

func TestSuccessfulCallEndsStartupWindow(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{showOnStart: true}
	c, clk := newTest(rpc, proc, true)
	expect(t, c, "back_or_launch", Launched)
	expect(t, c, "back_or_launch", Sent) // Kodi answers: launch complete
	proc.set(false)                      // user closes Kodi from its own menu
	clk.advance(time.Second)
	expect(t, c, "back_or_launch", Launched) // no need to wait for the grace period
}

func TestLongPressQuitsOnceAndIgnoresPressesWhileQuitting(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, true)
	exited := make(chan struct{})
	rpc.onQuit = func() {
		go func() {
			<-exited
			proc.set(false)
		}()
	}

	expect(t, c, "quit", QuitRequested)
	expect(t, c, "quit", IgnoredQuitting)
	expect(t, c, "back_or_launch", IgnoredQuitting)
	close(exited)
	c.Wait()

	if n := rpc.count("Application.Quit"); n != 1 {
		t.Fatalf("Application.Quit calls = %d, want 1", n)
	}
	if l, term := proc.stats(); l != 0 || term != 0 {
		t.Fatalf("launches, terminates = %d, %d; want 0, 0", l, term)
	}
	expect(t, c, "back_or_launch", Launched)
}

func TestQuitFallsBackToSIGTERMWhenRPCFails(t *testing.T) {
	rpc, proc := &fakeRPC{quitErr: errors.New("connection refused")}, &fakeProc{running: true, exitOnTerm: true}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "quit", QuitRequested)
	c.Wait()
	if _, term := proc.stats(); term != 1 {
		t.Fatalf("terminates = %d, want 1", term)
	}
}

func TestQuitWithoutFallbackReportsFailure(t *testing.T) {
	rpc, proc := &fakeRPC{quitErr: errors.New("connection refused")}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, false)
	expect(t, c, "quit", Failed)
	if _, term := proc.stats(); term != 0 {
		t.Fatalf("terminates = %d, want 0", term)
	}
	if c.isQuitting() {
		t.Fatal("still quitting after a failed quit")
	}
}

func TestQuitTimeoutEscalatesToSIGTERMOnce(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{running: true, exitOnTerm: true}
	c, _ := newTest(rpc, proc, true)
	c.now = time.Now // waitGone measures the timeout with the clock
	expect(t, c, "quit", QuitRequested)
	c.Wait()
	if _, term := proc.stats(); term != 1 {
		t.Fatalf("terminates = %d, want 1", term)
	}
}

func TestQuitWhenNotRunning(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "quit", NotRunning)
	if n := rpc.count("Application.Quit"); n != 0 {
		t.Fatalf("Application.Quit calls = %d, want 0", n)
	}
}

func TestKodiActions(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "action:playpause", NotRunning)
	proc.set(true)
	expect(t, c, "action:playpause", Sent)
	if len(rpc.actionOf) != 1 || rpc.actionOf[0] != "playpause" {
		t.Fatalf("ExecuteAction args = %v, want [playpause]", rpc.actionOf)
	}
	expect(t, c, "key:left", Sent)
	if len(rpc.actionOf) != 2 || rpc.actionOf[1] != "key:left" {
		t.Fatalf("KeyPress args = %v, want [... key:left]", rpc.actionOf)
	}
	expect(t, c, "launch", AlreadyRunning)
}

func TestDoInUsesWindowOverride(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, true)
	over := map[int]string{12005: "action:volumeup"}

	rpc.window = 10025 // videos library: default action
	if a, out, err := c.DoIn(context.Background(), "key:up", over); a != "key:up" || out != Sent || err != nil {
		t.Fatalf("DoIn() in library = %q, %q, %v; want key:up", a, out, err)
	}
	rpc.window = 12005 // fullscreen video: override
	if a, out, err := c.DoIn(context.Background(), "key:up", over); a != "action:volumeup" || out != Sent || err != nil {
		t.Fatalf("DoIn() in fullscreen video = %q, %q, %v; want action:volumeup", a, out, err)
	}
	rpc.windowErr = errors.New("connection refused") // unknown window: default
	if a, _, _ := c.DoIn(context.Background(), "key:up", over); a != "key:up" {
		t.Fatalf("DoIn() with unknown window = %q, want key:up", a)
	}
	if got := rpc.actionOf; len(got) != 3 || got[1] != "volumeup" {
		t.Fatalf("calls = %v, want [key:up volumeup key:up]", got)
	}
}

func TestVolumeAction(t *testing.T) {
	rpc, proc := &fakeRPC{window: 12005}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, true)
	a, out, err := c.DoIn(context.Background(), "key:up", map[int]string{12005: "volume:+5"})
	if a != "volume:+5" || out != Sent || err != nil {
		t.Fatalf("DoIn() = %q, %q, %v; want volume:+5 sent", a, out, err)
	}
	if got := rpc.actionOf; len(got) != 1 || got[0] != "volume:+5" {
		t.Fatalf("calls = %v, want [volume:+5]", got)
	}
}

func TestPauseAction(t *testing.T) {
	rpc, proc := &fakeRPC{playing: 1}, &fakeProc{}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "pause", NotRunning)
	proc.set(true)
	expect(t, c, "pause", Sent)
	expect(t, c, "pause", NothingPlaying)
}

func TestToggleFullscreenRaisesAndToggles(t *testing.T) {
	rpc, proc := &fakeRPC{}, &fakeProc{running: true, raiseErr: errors.New("no wmctrl")}
	c, _ := newTest(rpc, proc, true)
	c.opts.WindowClass = "kodi.Kodi"
	// Raising fails, but the toggle is still sent.
	expect(t, c, "toggle_fullscreen", Sent)
	if len(proc.raised) != 1 || proc.raised[0] != "kodi.Kodi" {
		t.Fatalf("raised = %v, want [kodi.Kodi]", proc.raised)
	}
	if len(rpc.actionOf) != 1 || rpc.actionOf[0] != "togglefullscreen" {
		t.Fatalf("actions = %v, want [togglefullscreen]", rpc.actionOf)
	}
	proc.set(false)
	expect(t, c, "toggle_fullscreen", NotRunning)
}

func TestSuspendPausesThenSuspends(t *testing.T) {
	rpc, proc := &fakeRPC{playing: 1}, &fakeProc{running: true}
	c, _ := newTest(rpc, proc, true)
	expect(t, c, "suspend", Suspended)
	if rpc.count("Player.PlayPause") != 1 || proc.suspends != 1 {
		t.Fatalf("pauses=%d suspends=%d, want 1 and 1", rpc.count("Player.PlayPause"), proc.suspends)
	}
	// Kodi closed: suspends without talking to Kodi.
	proc.set(false)
	expect(t, c, "suspend", Suspended)
	if rpc.count("Player.PlayPause") != 1 || proc.suspends != 2 {
		t.Fatalf("pauses=%d suspends=%d, want 1 and 2", rpc.count("Player.PlayPause"), proc.suspends)
	}
}
