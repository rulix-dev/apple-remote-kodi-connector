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

// nowPlaying polls Kodi for what is playing. When a new item starts it sends a
// Datadog event and logs it; on every poll it updates the kodi.player.* gauges.
// Each poll is a trace that is only kept when something changed.
func nowPlaying(ctx context.Context, rpc *kodi.Client, proc *kodi.Process, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var last *kodi.Item
	lastType := "none"
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

		mediaType := "none"
		if item != nil {
			mediaType = item.MediaType()
		}
		if mediaType != lastType {
			// Zero the previous series so a stale "1" does not linger.
			telemetry.Gauge("kodi.player.active", 0, "media_type:"+lastType)
			telemetry.Gauge("kodi.player.playing", 0, "media_type:"+lastType)
		}
		telemetry.Bool("kodi.running", running)
		telemetry.Bool("kodi.player.active", item != nil, "media_type:"+mediaType)
		telemetry.Bool("kodi.player.playing", item != nil && item.Playing, "media_type:"+mediaType)

		switch {
		case err != nil:
			// Typical while Kodi starts or quits; the RPC metrics count it.
			slog.Debug("now playing: Kodi did not answer", "err", err)
			span.Drop()
		case item != nil && (last == nil || itemKey(*item) != itemKey(*last)):
			title := item.Display()
			slog.InfoContext(pctx, "now playing", "title", title, "media_type", mediaType, "player", item.Player)
			telemetry.Event("Kodi: now playing "+title, itemDetails(*item),
				"media_type:"+mediaType, "player:"+item.Player)
			telemetry.Count("kodi.player.started", "media_type:"+mediaType)
			span.SetTag("title", title)
			span.Keep()
		case item == nil && last != nil:
			slog.InfoContext(pctx, "playback stopped", "title", last.Display())
			telemetry.Count("kodi.player.stopped", "media_type:"+last.MediaType())
			span.Keep()
		default:
			span.Drop()
		}
		span.Finish(err)
		if err == nil {
			last = item
		}
		lastType = mediaType

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// itemKey identifies an item, to detect when a new one starts.
func itemKey(it kodi.Item) string {
	return strings.Join([]string{it.Player, it.Type, it.Label, it.Title, it.ShowTitle,
		fmt.Sprint(it.Season), fmt.Sprint(it.Episode)}, "|")
}

// itemDetails is the event text.
func itemDetails(it kodi.Item) string {
	lines := []string{"Type: " + it.MediaType()}
	if it.ShowTitle != "" {
		lines = append(lines, fmt.Sprintf("Show: %s, season %d, episode %d", it.ShowTitle, it.Season, it.Episode))
	}
	if it.Year > 0 {
		lines = append(lines, fmt.Sprintf("Year: %d", it.Year))
	}
	return strings.Join(lines, "\n")
}
