package evdev

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned when no matching device is present.
var ErrNotFound = errors.New("input device not found")

// Match selects a physical input device by its USB vendor and product IDs
// (hex, as shown by lsusb, e.g. "05ac" and "8242").
type Match struct {
	Vendor  string
	Product string
}

// Find returns the /dev/input/eventN path of the first physical device that
// matches. Virtual devices (/sys/devices/virtual/...) are skipped: Input
// Remapper's "forwarded" device copies the vendor and product IDs of the
// device it mirrors.
func Find(m Match) (string, error) {
	return find("/sys/class/input", "/dev/input", m)
}

func find(sysClass, devDir string, m Match) (string, error) {
	entries, err := filepath.Glob(filepath.Join(sysClass, "event*"))
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	for _, e := range entries {
		real, err := filepath.EvalSymlinks(e)
		if err != nil || strings.Contains(real, "/devices/virtual/") {
			continue
		}
		vendor := readAttr(filepath.Join(e, "device", "id", "vendor"))
		product := readAttr(filepath.Join(e, "device", "id", "product"))
		if strings.EqualFold(vendor, m.Vendor) && strings.EqualFold(product, m.Product) {
			return filepath.Join(devDir, filepath.Base(e)), nil
		}
	}
	return "", fmt.Errorf("%w (vendor %s, product %s)", ErrNotFound, m.Vendor, m.Product)
}

func readAttr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
