package kodi

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPidsMatchesExactBinaryOnly(t *testing.T) {
	proc := t.TempDir()
	procs := map[string]string{
		"100":  "/app/lib/kodi/kodi.bin\x00--standalone\x00",
		"101":  "bwrap\x00--args\x0041\x00--\x00kodi\x00",
		"102":  "/usr/bin/python3\x00/app/lib/kodi/kodi.bin\x00", // mentions kodi.bin but is not it
		"103":  "/app/lib/kodi/kodi.bin.old\x00",
		"self": "/app/lib/kodi/kodi.bin\x00", // not a pid
	}
	for pid, cmdline := range procs {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := NewProcess("tv.kodi.Kodi", "/app/lib/kodi/kodi.bin")
	p.procDir = proc
	if got := p.pids(); !slices.Equal(got, []int{100}) {
		t.Fatalf("pids() = %v, want [100]", got)
	}

	p.uid = os.Getuid() + 1 // another user's Kodi must be ignored
	if got := p.pids(); len(got) != 0 {
		t.Fatalf("pids() for another uid = %v, want none", got)
	}
}
