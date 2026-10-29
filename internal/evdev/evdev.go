//go:build linux

// Package evdev reads key events from Linux input devices (/dev/input/eventN).
package evdev

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Event types and key values used by this program (linux/input-event-codes.h).
const (
	EvSyn uint16 = 0x00
	EvKey uint16 = 0x01

	ValueRelease int32 = 0
	ValuePress   int32 = 1
	ValueRepeat  int32 = 2
)

// ioctl numbers (linux/input.h).
const (
	eviocgrab     = 0x40044590 // _IOW('E', 0x90, int)
	eviocgnameLen = 256
	eviocgname    = 0x80000000 | eviocgnameLen<<16 | 'E'<<8 | 0x06 // _IOC(_IOC_READ, 'E', 0x06, len)
)

// Event is a single input_event.
type Event struct {
	Time  time.Time // kernel timestamp
	Type  uint16
	Code  uint16
	Value int32
}

// rawEvent mirrors struct input_event on 64-bit Linux.
type rawEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

const rawEventSize = int(unsafe.Sizeof(rawEvent{}))

// Device is an open input device.
type Device struct {
	f    *os.File
	buf  []byte
	Path string
	Name string
}

// Open opens an input device for reading.
func Open(path string) (*Device, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	d := &Device{f: f, buf: make([]byte, rawEventSize*64), Path: path}
	if name, err := d.ioctlName(); err == nil {
		d.Name = name
	}
	return d, nil
}

func (d *Device) ioctlName() (string, error) {
	buf := make([]byte, eviocgnameLen)
	if err := d.ioctl(eviocgname, uintptr(unsafe.Pointer(&buf[0]))); err != nil {
		return "", err
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf), nil
}

// Grab takes exclusive access to the device: while grabbed, no other reader
// (X11, Cinnamon, Kodi) receives its events. The grab ends on Close.
func (d *Device) Grab() error {
	if err := d.ioctl(eviocgrab, 1); err != nil {
		return fmt.Errorf("EVIOCGRAB %s: %w", d.Path, err)
	}
	return nil
}

func (d *Device) ioctl(req, arg uintptr) error {
	conn, err := d.f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// Read blocks until at least one event is available. It returns an error when
// the device is closed or disconnected.
func (d *Device) Read() ([]Event, error) {
	n, err := d.f.Read(d.buf)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, n/rawEventSize)
	r := bytes.NewReader(d.buf[:n-n%rawEventSize])
	for r.Len() > 0 {
		var raw rawEvent
		if err := binary.Read(r, binary.NativeEndian, &raw); err != nil {
			return nil, err
		}
		events = append(events, Event{
			Time:  time.Unix(raw.Sec, raw.Usec*1000),
			Type:  raw.Type,
			Code:  raw.Code,
			Value: raw.Value,
		})
	}
	return events, nil
}

// Close releases the device (and the grab, if any).
func (d *Device) Close() error {
	return d.f.Close()
}
