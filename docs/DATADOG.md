# Datadog

Enabled by default (`datadog.enabled: true`, `env: prod`; see `configs/config.example.yaml`). Every metric, event and span carries `service`, `env` and `version` (the binary version).

## Metrics

| Metric | Type | Tags | Meaning |
|---|---|---|---|
| `apple_remote.up` | gauge | — | 1 every minute while the daemon runs |
| `apple_remote.button.press` | count | `key`, `press:short\|long\|extralong\|repeat` | Button presses (`repeat`: auto-repeat while a navigation button is held) |
| `apple_remote.action` | count | `action`, `outcome` | Actions run (`outcome`: `sent`, `launched`, `failed`, `ignored_kodi_is_starting`…) |
| `apple_remote.action.duration` | distribution (ms) | `action` | Time to run an action |
| `apple_remote.device.connected` | gauge | — | 1 while the IR receiver is open |
| `apple_remote.device.connect` | count | — | Times the IR receiver was opened (reconnections) |
| `apple_remote.device.errors` | count | `error:not_found\|permission\|busy\|read` | Failures to open or read the IR receiver (`busy`: another program, e.g. Input Remapper, holds it) |
| `apple_remote.idle_pause` | count | `result:warned\|answered\|paused` | "Are you still there?" warnings and their outcome |
| `apple_remote.auto_pause` | count | `reason:no_display\|tv_off` | Automatic pauses |
| `apple_remote.display.lid_closed` | gauge | — | 1 while the lid is closed |
| `apple_remote.display.external` | gauge | — | Connected external displays |
| `apple_remote.tv.on` | gauge | — | 1 while the network TV answers (if `auto_pause.tv` is enabled) |
| `apple_remote.tv.state_change` | count | `state:on\|off` | TV turned on / off |
| `kodi.rpc.duration` | distribution (ms) | `method`, `status:ok\|error` | Kodi JSON-RPC latency |
| `kodi.rpc.errors` | count | `method`, `error:unauthorized\|rpc\|timeout\|connection\|other` | Kodi JSON-RPC errors |
| `kodi.running` | gauge | — | 1 while Kodi is running |
| `kodi.player.active` | gauge | `media_type` | 1 while something is loaded in a player (`media_type`: `movie`, `episode`, `song`, `musicvideo`, `channel`, `video`, `audio`…; `none` when idle) |
| `kodi.player.playing` | gauge | `media_type` | 1 while it is playing (0 when paused) |
| `kodi.player.started` / `kodi.player.stopped` | count | `media_type` | Items started / playback stopped |
| `kodi.launch` | count | `status:ok\|error` | Kodi launched from the remote |
| `kodi.quit` | count | `via:rpc` | Kodi closed from the remote |
| `kodi.quit.sigterm` | count | `reason:rpc_failed\|timeout` | Fallback SIGTERM to Kodi |

Go runtime metrics (`runtime.go.*`) are also sent by the tracer.

## Events

| Title | Tags | When |
|---|---|---|
| `Kodi: now playing <title>` | `media_type`, `player` (source type: `kodi`) | A new item starts playing. `<title>` is `Show S01E02 · Episode`, `Movie (2001)` or the song title |

## Traces (APM)

| Operation | Resource | Children |
|---|---|---|
| `button.press` | `<KEY> <press>` (e.g. `KEY_MENU long`) | `controller.action` > `kodi.rpc` (resource: JSON-RPC method), `kodi.process` (`running`, `launch`, `raise`), `system` (`suspend`) |
| `auto_pause`, `idle_pause` | reason | Same as above |
| `kodi.now_playing` | `poll` | `kodi.process`, `kodi.rpc`. Kept only when the playing item changes |
| `tv.probe` | `webos` | Kept only when the TV turns on or off |

Repeated presses while a button is held are counted but not traced.

## Logs

The daemon logs to stderr, so systemd stores it in the journal of the user unit `apple-remote-kodi-connector.service`. With `log.format: json` every record is a JSON object (`time`, `level`, `msg` and attributes); records logged during a traced press also carry `dd.trace_id` and `dd.span_id`.
