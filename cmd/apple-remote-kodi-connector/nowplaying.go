//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rulix-dev/apple-remote-kodi-connector/internal/kodi"
	"github.com/rulix-dev/apple-remote-kodi-connector/internal/telemetry"
)

// formatWaitPolls is how many polls a new video waits for Kodi to report its
// stream format before the "now playing" event is sent without it.
const formatWaitPolls = 2

// nowPlaying polls Kodi for what is playing. When a new item starts it sends a
// Datadog event and logs it (with the video format and whether the GPU or the
// CPU decodes it); on every poll it updates the kodi.player.* gauges. Each poll
// is a trace that is only kept when something changed.
func nowPlaying(ctx context.Context, rpc *kodi.Client, proc *kodi.Process, interval time.Duration, hwCodecs []string) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var (
		last      *kodi.Item // item of the previous successful poll
		announced bool       // the "now playing" event of last was sent
		waited    int        // polls waited for the format of last
		lastTags  = []string{"media_type:none"}
	)
	for {
		pctx, cancel := context.WithTimeout(ctx, interval)
		span, pctx := telemetry.StartSpan(pctx, "kodi.now_playing", "poll")
		running := proc.Running(pctx)
		var item *kodi.Item
		var err error
		if running {
			item, err = rpc.NowPlaying(pctx)
		}
		cancel()

		tags := []string{"media_type:none"}
		if item != nil {
			tags = mediaTags(*item, hwCodecs)
		}
		if strings.Join(tags, ",") != strings.Join(lastTags, ",") {
			// Zero the previous series so a stale "1" does not linger.
			telemetry.Gauge("kodi.player.active", 0, lastTags...)
			telemetry.Gauge("kodi.player.playing", 0, lastTags...)
		}
		telemetry.Bool("kodi.running", running)
		telemetry.Bool("kodi.player.active", item != nil, tags...)
		telemetry.Bool("kodi.player.playing", item != nil && item.Playing, tags...)

		keep := false
		switch {
		case err != nil:
			// Typical while Kodi starts or quits; the RPC metrics count it.
			slog.Debug("now playing: Kodi did not answer", "err", err)
		case item == nil:
			if last != nil {
				slog.InfoContext(pctx, "playback stopped", "title", last.Display())
				telemetry.Count("kodi.player.stopped", "media_type:"+last.MediaType())
				keep = true
			}
			last, announced = nil, false
		default:
			if last == nil || itemKey(*item) != itemKey(*last) {
				announced, waited = false, 0
			}
			last = item
			if !announced {
				// A new video may not report its format on the first poll.
				if item.IsVideo() && item.VideoCodec == "" && waited < formatWaitPolls {
					waited++
				} else {
					announce(pctx, *item, tags, hwCodecs)
					span.SetTag("title", item.Display())
					announced, keep = true, true
				}
			}
		}
		if keep {
			span.Keep()
		} else {
			span.Drop()
		}
		span.Finish(err)
		lastTags = tags

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// announce logs "now playing" and sends the Datadog event.
func announce(ctx context.Context, it kodi.Item, tags, hwCodecs []string) {
	title := it.Display()
	attrs := []any{"title", title, "media_type", it.MediaType(), "player", it.Player}
	if it.VideoCodec != "" {
		attrs = append(attrs, "video_codec", it.VideoCodec, "width", it.Width, "height", it.Height,
			"resolution", it.Resolution(), "decode", it.Decode(hwCodecs))
	}
	if it.HDR != "" {
		attrs = append(attrs, "hdr", it.HDR)
	}
	if it.AudioCodec != "" {
		attrs = append(attrs, "audio_codec", it.AudioCodec, "audio_channels", it.AudioChannels)
	}
	slog.InfoContext(ctx, "now playing", attrs...)
	eventTags := append([]string{"player:" + it.Player}, tags...)
	if it.AudioCodec != "" {
		eventTags = append(eventTags, "audio_codec:"+it.AudioCodec)
	}
	telemetry.Event("Kodi: now playing "+title, itemDetails(it, hwCodecs), eventTags...)
	telemetry.Count("kodi.player.started", tags...)
}

// mediaTags are the low-cardinality tags of an item: media_type and, for
// videos with a known format, video_codec, resolution and decode.
func mediaTags(it kodi.Item, hwCodecs []string) []string {
	tags := []string{"media_type:" + it.MediaType()}
	if it.IsVideo() && it.VideoCodec != "" {
		tags = append(tags, "video_codec:"+it.VideoCodec, "decode:"+it.Decode(hwCodecs))
		if r := it.Resolution(); r != "" {
			tags = append(tags, "resolution:"+r)
		}
	}
	return tags
}

// itemKey identifies an item, to detect when a new one starts.
func itemKey(it kodi.Item) string {
	return strings.Join([]string{it.Player, it.Type, it.Label, it.Title, it.ShowTitle,
		fmt.Sprint(it.Season), fmt.Sprint(it.Episode)}, "|")
}

// formatLine is "HEVC · 1080p · CPU" (plus HDR and audio when known).
func formatLine(it kodi.Item, hwCodecs []string) string {
	var parts []string
	if it.VideoCodec != "" {
		parts = append(parts, strings.ToUpper(it.VideoCodec))
		if r := it.Resolution(); r != "" {
			parts = append(parts, r)
		}
		if it.HDR != "" {
			parts = append(parts, strings.ToUpper(it.HDR))
		}
		parts = append(parts, strings.ToUpper(it.Decode(hwCodecs)))
	}
	if it.AudioCodec != "" {
		a := strings.ToUpper(it.AudioCodec)
		if it.AudioChannels > 0 {
			a += fmt.Sprintf(" %dch", it.AudioChannels)
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " · ")
}

// itemDetails is the event text.
func itemDetails(it kodi.Item, hwCodecs []string) string {
	var lines []string
	if f := formatLine(it, hwCodecs); f != "" {
		lines = append(lines, f)
	}
	lines = append(lines, "Type: "+it.MediaType())
	if it.ShowTitle != "" {
		lines = append(lines, fmt.Sprintf("Show: %s, season %d, episode %d", it.ShowTitle, it.Season, it.Episode))
	}
	if it.Year > 0 {
		lines = append(lines, fmt.Sprintf("Year: %d", it.Year))
	}
	return strings.Join(lines, "\n")
}
