package evdev

import (
	"fmt"
	"strconv"
	"strings"
)

// Key codes emitted by the Apple Remote through the hid_appleir driver.
const (
	KeyEnter      uint16 = 28
	KeyVolumeDown uint16 = 114
	KeyVolumeUp   uint16 = 115
	KeyMenu       uint16 = 139
	KeyBack       uint16 = 158
	KeyForward    uint16 = 159
	KeyPlayPause  uint16 = 164
)

var keyNames = map[uint16]string{
	KeyEnter:      "KEY_ENTER",
	KeyVolumeDown: "KEY_VOLUMEDOWN",
	KeyVolumeUp:   "KEY_VOLUMEUP",
	KeyMenu:       "KEY_MENU",
	KeyBack:       "KEY_BACK",
	KeyForward:    "KEY_FORWARD",
	KeyPlayPause:  "KEY_PLAYPAUSE",
}

// KeyName returns the Linux name of a key code, or its number if unknown.
func KeyName(code uint16) string {
	if n, ok := keyNames[code]; ok {
		return n
	}
	return fmt.Sprintf("KEY_%d", code)
}

// ParseKey accepts a Linux key name ("KEY_MENU") or a numeric code ("139").
func ParseKey(s string) (uint16, error) {
	for code, name := range keyNames {
		if strings.EqualFold(name, s) {
			return code, nil
		}
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(s), "KEY_"), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("unknown key %q", s)
	}
	return uint16(n), nil
}
