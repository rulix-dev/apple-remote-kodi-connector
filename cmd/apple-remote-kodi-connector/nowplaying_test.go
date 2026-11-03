//go:build linux

package main

import (
	"strings"
	"testing"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/kodi"
)

var hw = []string{"h264", "mpeg2video", "vc1", "wmv3"}

func TestFormatLineAndTags(t *testing.T) {
	movie := kodi.Item{Player: "video", Type: "movie", VideoCodec: "hevc", Width: 1920, Height: 1080, AudioCodec: "ac3", AudioChannels: 6}
	if got, want := formatLine(movie, hw), "HEVC · 1080p · CPU · AC3 6ch"; got != want {
		t.Errorf("formatLine() = %q, want %q", got, want)
	}
	if got, want := strings.Join(mediaTags(movie, hw), ","), "media_type:movie,video_codec:hevc,decode:cpu,resolution:1080p"; got != want {
		t.Errorf("mediaTags() = %q, want %q", got, want)
	}
	song := kodi.Item{Player: "audio", Type: "song", AudioCodec: "flac", AudioChannels: 2}
	if got := formatLine(song, hw); got != "FLAC 2ch" {
		t.Errorf("formatLine(song) = %q", got)
	}
	if got := strings.Join(mediaTags(song, hw), ","); got != "media_type:song" {
		t.Errorf("mediaTags(song) = %q, want only media_type", got)
	}
}
