package kodi

import (
	"fmt"
	"strconv"
	"strings"
)

// Window ids of Kodi (xbmc/guilib/WindowIDs.h) that are useful as contexts.
var windowIDs = map[string]int{
	"home":            10000,
	"fullscreenvideo": 12005,
	"visualisation":   12006,
	"slideshow":       12007,
	"videoosd":        12901,
}

// WindowID accepts a known window name ("fullscreenvideo") or a numeric id.
func WindowID(name string) (int, error) {
	if id, ok := windowIDs[strings.ToLower(name)]; ok {
		return id, nil
	}
	id, err := strconv.Atoi(name)
	if err != nil {
		return 0, fmt.Errorf("unknown Kodi window %q", name)
	}
	return id, nil
}
