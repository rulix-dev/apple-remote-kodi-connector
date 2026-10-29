package tv

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const arpHeader = "IP address       HW type     Flags       HW address            Mask     Device\n"

func arpTable(lines ...string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(arpHeader + strings.Join(lines, "\n") + "\n")), nil
	}
}

const (
	tvMAC   = "02:00:00:00:be:e4"
	tvEntry = "192.0.2.162    0x1         0x2         02:00:00:00:be:e4     *        wlp3s0"
)

func TestParseARP(t *testing.T) {
	table := arpHeader +
		"192.0.2.1      0x1         0x2         02:00:00:00:00:01     *        wlp3s0\n" +
		"192.0.2.99     0x1         0x0         02:00:00:00:be:e4     *        wlp3s0\n" + // incomplete
		tvEntry + "\n"
	if got := parseARP(strings.NewReader(table), "02:00:00:00:BE:E4"); got != "192.0.2.162" {
		t.Fatalf("parseARP() = %q, want 192.0.2.162", got)
	}
	if got := parseARP(strings.NewReader(arpHeader), tvMAC); got != "" {
		t.Fatalf("parseARP(empty) = %q, want empty", got)
	}
}

type fakeNet struct {
	up        map[string]bool // ip:port -> listening
	discovers int
	found     []string
}

func (f *fakeNet) dial(_ context.Context, addr string) error {
	if f.up[addr] {
		return nil
	}
	return errors.New("connection refused")
}

func (f *fakeNet) discover(context.Context) []string {
	f.discovers++
	return f.found
}

func newTestProber(n *fakeNet, arp func() (io.ReadCloser, error), now *time.Time) *Prober {
	p := NewProber(tvMAC)
	p.arp, p.dial, p.discover = arp, n.dial, n.discover
	p.now = func() time.Time { return *now }
	return p
}

func TestOnAndOff(t *testing.T) {
	now := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	n := &fakeNet{up: map[string]bool{"192.0.2.162:3000": true}}
	p := newTestProber(n, arpTable(tvEntry), &now)

	if !p.On(context.Background()) {
		t.Fatal("On() = false with the TV listening")
	}
	if p.IP() != "192.0.2.162" {
		t.Fatalf("IP() = %q", p.IP())
	}
	n.up["192.0.2.162:3000"] = false // TV turned off
	if p.On(context.Background()) {
		t.Fatal("On() = true with the TV off")
	}
	if n.discovers != 1 {
		t.Fatalf("discovers = %d, want 1", n.discovers)
	}
	// While off, discovery is rate limited.
	now = now.Add(5 * time.Second)
	p.On(context.Background())
	if n.discovers != 1 {
		t.Fatalf("discovers after 5s = %d, want 1 (rate limited)", n.discovers)
	}
}

func TestOffWithoutARPEntry(t *testing.T) {
	now := time.Now()
	n := &fakeNet{up: map[string]bool{}}
	p := newTestProber(n, arpTable(), &now)
	if p.On(context.Background()) {
		t.Fatal("On() = true for a TV never seen")
	}
}

func TestIPChangeFoundBySSDP(t *testing.T) {
	now := time.Now()
	n := &fakeNet{up: map[string]bool{"192.0.2.170:3000": true}, found: []string{"192.0.2.170"}}
	arp := tvEntry
	p := newTestProber(n, func() (io.ReadCloser, error) { return arpTable(arp)() }, &now)
	p.ip = "192.0.2.162"
	arp = "192.0.2.170    0x1         0x2         02:00:00:00:be:e4     *        wlp3s0"
	if !p.On(context.Background()) || p.IP() != "192.0.2.170" {
		t.Fatalf("On() = false or IP() = %q; want TV found at its new IP", p.IP())
	}
}

func TestSSDPIgnoresOtherTVs(t *testing.T) {
	now := time.Now()
	// Another webOS TV answers SSDP, but its MAC is not ours.
	n := &fakeNet{up: map[string]bool{"192.0.2.180:3000": true}, found: []string{"192.0.2.180"}}
	p := newTestProber(n, arpTable("192.0.2.180    0x1         0x2         aa:bb:cc:dd:ee:ff     *        wlp3s0"), &now)
	if p.On(context.Background()) {
		t.Fatal("On() = true for a different TV")
	}
}
