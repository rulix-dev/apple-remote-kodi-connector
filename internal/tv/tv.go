// Package tv tells whether a network TV is on, by checking that a TCP port it
// only listens on while powered (e.g. LG webOS's 3000) accepts connections.
//
// The TV is identified by its MAC address, which does not change, and its IP
// is looked up in the kernel neighbour table (/proc/net/arp) or, failing
// that, discovered with SSDP. The last known IP is remembered.
package tv

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// SSDPTargetWebOS is the SSDP search target answered by LG webOS TVs.
const SSDPTargetWebOS = "urn:lge-com:service:webos-second-screen:1"

// Prober checks whether the TV is on.
type Prober struct {
	MAC         string        // e.g. "02:00:00:00:be:e4"
	Port        string        // e.g. "3000"
	DialTimeout time.Duration // per connection attempt
	// Rediscover bounds how often SSDP discovery runs while the TV is off.
	Rediscover time.Duration

	// Hooks for tests.
	arp      func() (io.ReadCloser, error)
	dial     func(ctx context.Context, addr string) error
	discover func(ctx context.Context) []string
	now      func() time.Time

	ip           string
	lastDiscover time.Time
}

// NewProber returns a prober for an LG webOS TV.
func NewProber(mac string) *Prober {
	return &Prober{
		MAC:         strings.ToLower(mac),
		Port:        "3000",
		DialTimeout: time.Second,
		Rediscover:  30 * time.Second,
		arp:         func() (io.ReadCloser, error) { return os.Open("/proc/net/arp") },
		dial:        dialTCP,
		discover:    func(ctx context.Context) []string { return DiscoverSSDP(ctx, SSDPTargetWebOS, 2*time.Second) },
		now:         time.Now,
	}
}

// IP returns the last known IP of the TV, if any.
func (p *Prober) IP() string { return p.ip }

// On reports whether the TV accepts connections on its port.
func (p *Prober) On(ctx context.Context) bool {
	if ip := p.lookupARP(); ip != "" {
		p.ip = ip
	}
	if p.ip != "" && p.reachable(ctx, p.ip) {
		return true
	}
	// Not reachable at the known IP: it is off, or its IP changed. Look for
	// it again, but not on every poll since an off TV never answers.
	if p.now().Sub(p.lastDiscover) < p.Rediscover {
		return false
	}
	p.lastDiscover = p.now()
	for _, ip := range p.discover(ctx) {
		if ip == p.ip || !p.reachable(ctx, ip) {
			continue
		}
		// The connection populated the neighbour table: confirm it is our TV.
		if p.lookupARP() == ip {
			p.ip = ip
			return true
		}
	}
	return false
}

func (p *Prober) reachable(ctx context.Context, ip string) bool {
	ctx, cancel := context.WithTimeout(ctx, p.DialTimeout)
	defer cancel()
	return p.dial(ctx, net.JoinHostPort(ip, p.Port)) == nil
}

// lookupARP returns the IP whose complete neighbour entry has p.MAC.
func (p *Prober) lookupARP() string {
	f, err := p.arp()
	if err != nil {
		return ""
	}
	defer f.Close()
	return parseARP(f, p.MAC)
}

// parseARP scans /proc/net/arp:
// IP address  HW type  Flags  HW address  Mask  Device
func parseARP(r io.Reader, mac string) string {
	s := bufio.NewScanner(r)
	s.Scan() // header
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 4 {
			continue
		}
		const complete = 0x2
		flags, err := strconv.ParseInt(f[2], 0, 32)
		if err != nil || flags&complete == 0 {
			continue
		}
		if strings.EqualFold(f[3], mac) {
			return f[0]
		}
	}
	return ""
}

func dialTCP(ctx context.Context, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// DiscoverSSDP sends an SSDP M-SEARCH for target and returns the IPs that
// answer within wait.
func DiscoverSSDP(ctx context.Context, target string, wait time.Duration) []string {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil
	}
	defer conn.Close()
	msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: " + target + "\r\n\r\n"
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for range 2 {
		conn.WriteToUDP([]byte(msg), dst)
	}
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetReadDeadline(deadline)
	var ips []string
	seen := map[string]bool{}
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return ips
		}
		if ip := addr.IP.String(); !seen[ip] && strings.Contains(string(buf[:n]), target) {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
}
