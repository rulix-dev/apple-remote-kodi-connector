package evdev

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfs builds a /sys/class/input-like tree. Each device lives under
// root/devices/<parent>/inputN and is linked from root/class/eventN.
func fakeSysfs(t *testing.T, devs map[string][3]string) string {
	t.Helper()
	root := t.TempDir()
	class := filepath.Join(root, "class")
	if err := os.MkdirAll(class, 0o755); err != nil {
		t.Fatal(err)
	}
	for event, d := range devs {
		parent, vendor, product := d[0], d[1], d[2]
		input := filepath.Join(root, "devices", parent, "input-"+event)
		eventDir := filepath.Join(input, event)
		ids := filepath.Join(input, "id")
		for _, dir := range []string{eventDir, ids} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(input, filepath.Join(eventDir, "device")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(ids, "vendor"), vendor+"\n")
		write(t, filepath.Join(ids, "product"), product+"\n")
		if err := os.Symlink(eventDir, filepath.Join(class, event)); err != nil {
			t.Fatal(err)
		}
	}
	return class
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindSkipsVirtualDevices(t *testing.T) {
	class := fakeSysfs(t, map[string][3]string{
		"event17": {"virtual/input", "05ac", "8242"}, // Input Remapper "forwarded"
		"event8":  {"pci0000:00/usb3", "05ac", "0253"},
		"event9":  {"pci0000:00/usb2", "05ac", "8242"},
	})
	got, err := find(class, "/dev/input", Match{Vendor: "05AC", Product: "8242"})
	if err != nil {
		t.Fatalf("find() error = %v", err)
	}
	if want := "/dev/input/event9"; got != want {
		t.Fatalf("find() = %q, want %q", got, want)
	}
}

func TestFindNotFound(t *testing.T) {
	class := fakeSysfs(t, map[string][3]string{
		"event17": {"virtual/input", "05ac", "8242"},
	})
	if _, err := find(class, "/dev/input", Match{Vendor: "05ac", Product: "8242"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("find() error = %v, want ErrNotFound", err)
	}
}
