package config

import (
	"os"
	"testing"
)

// The shipped example must always be a valid configuration.
func TestExampleConfigIsValid(t *testing.T) {
	b, err := os.ReadFile("../../configs/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(config.example.yaml) error = %v", err)
	}
	if !c.Device.Grab || len(c.Buttons) != 7 {
		t.Fatalf("example: grab=%v buttons=%d, want grab and 7 buttons", c.Device.Grab, len(c.Buttons))
	}
}
