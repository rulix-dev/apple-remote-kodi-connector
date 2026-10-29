# apple-remote-kodi-connector

Control **Kodi** with the **Apple Remote** on a **MacBook running Linux** that is connected to the TV.

It reads the Mac's built-in infrared receiver, tells short, long and extra-long presses apart, and sends the commands to Kodi through its API (JSON-RPC). It opens and closes Kodi, pauses playback on its own when nobody is watching, and can suspend and wake up the computer with the remote.

> This is not a generic solution: it is built and tested for the setup described below.

## Setup

### Hardware

| Item | Details |
|---|---|
| Computer | MacBook Pro with a built-in infrared receiver (USB `05ac:8242`). Tested on a MacBookPro8,1 (early 2011). |
| Remote | Apple Remote (white or aluminium). |
| TV | Connected over HDMI (on this Mac, through a Mini DisplayPort → HDMI adapter). |
| Optional | An LG webOS TV on the same Wi-Fi network, to pause when it is turned off. |

### Software

| Item | Details |
|---|---|
| OS | Linux Mint 22 (Ubuntu 24.04 based), X11 session with Cinnamon. |
| Kodi | Kodi 21 installed with **Flatpak** (`tv.kodi.Kodi`). |
| User | A dedicated user (e.g. `kodi`) with automatic login. The program runs as that user, without root. |
| Install | `curl` and `sudo`. Go ≥ 1.22 only to build from source. |
| Input Remapper | Not needed. If installed, it must not have a preset loaded for the IR receiver, or it would block the remote. |

## Features

| Button | Short press | Long (0.7 s) | Extra long (5 s) |
|---|---|---|---|
| **Menu** | Back. Opens Kodi if it is closed | Closes Kodi | — |
| **Center** | Select | Context menu | — |
| **Play/Pause** | Play / pause | Brings Kodi to the front and toggles windowed / fullscreen (on release) | Pauses and **suspends the computer** |
| **Up / Down** | Navigate (repeats while held). In fullscreen video: volume ±5 | — | — |
| **Left / Right** | Navigate (repeats while held). In video: rewind / fast-forward | — | — |

Also:

- **Wake up:** while the computer is suspended, any button on the remote wakes it up.
- **"Are you still there?":** if Kodi has been playing for 60 minutes without any button press, it shows a warning and pauses if nobody answers within 20 s.
- **Auto pause** with the laptop lid closed:
  - when the external display is disconnected;
  - when the TV is turned off (optional, LG webOS TVs on the same network only).
- While the program runs, the remote only controls Kodi: the desktop does not receive its buttons (e.g. the system volume does not change).

All buttons, timings and features are set in a configuration file, without rebuilding.

## Preparing Kodi

In Kodi, as the `kodi` user:

1. **Settings → Services → Control**
   - Enable **Allow remote control via HTTP**.
   - Port `8080`, username `kodi` and a **password** (the installer asks for it).
   - Disable **SSL**.
2. **Settings → System → Display → Monitor**: choose the TV output so that Kodi always opens on it.

Recommended in Cinnamon (as `kodi`): closing the lid should not suspend the computer (*System Settings → Power Management*).

## Installation

Run it on the computer itself, as a user with `sudo`, ideally with Kodi open so that the final check is complete.

**From the latest release** (no Go needed):

```bash
curl -fsSL https://raw.githubusercontent.com/rulix-dev/apple-remote-kodi-connector/main/get.sh | sudo bash -s -- install kodi
```

**From source** (needs Go ≥ 1.22):

```bash
sudo apt install golang-go git
git clone https://github.com/rulix-dev/apple-remote-kodi-connector.git
cd apple-remote-kodi-connector
sudo ./install.sh kodi
```

The only input needed is the **Kodi web server password**, asked once on the first install (or passed in the `KODI_PASSWORD` environment variable).

The installer:

1. Installs the program to `/usr/local/bin/`.
2. Gives the user of the active session access to **the IR receiver only**, and lets the remote wake the computer up (udev rule).
3. Creates the configuration in `~kodi/.config/apple-remote-kodi-connector/` and stores the Kodi password in a file readable by `kodi` only.
4. Installs and starts a systemd user service that starts with the `kodi` session and restarts if it fails.
5. Checks the installation.

To **update**, run the same command again (`get.sh install`, or `git pull` + `install.sh`). The configuration and the password are kept. To install a given release, set `VERSION`, e.g. `... | sudo VERSION=v0.1.0 bash -s -- install kodi`.

## Configuration

File: `~/.config/apple-remote-kodi-connector/config.yaml` (of the `kodi` user). It is commented; the most common settings:

| What | Where |
|---|---|
| Action of each button (short, long, extra long) | `buttons` |
| Duration of the long and extra-long presses | `long_press`, `extra_long_press` |
| Volume step in video | `in_window: {fullscreenvideo: "volume:+5"}` |
| "Are you still there?" warning | `idle_pause` (`after_minutes`, `warning_seconds`) |
| Pause when an LG TV is turned off | `auto_pause.tv`: `enabled: true` and the TV's physical address (MAC), shown by `ip neigh` with the TV on |

After changing it, restart the service (as `kodi`):

```bash
systemctl --user restart apple-remote-kodi-connector
```

## Troubleshooting

As the `kodi` user:

```bash
systemctl --user status apple-remote-kodi-connector     # is it running?
journalctl --user -u apple-remote-kodi-connector -f      # live log
apple-remote-kodi-connector -check                       # checks remote, Kodi, display and TV
```

To see the codes the remote sends, stop the service and run `apple-remote-kodi-connector -diag`.

| Problem | Usual cause |
|---|---|
| The remote does nothing | The service is not running, or Input Remapper holds the receiver (`-check` tells). |
| 401 errors in the log | The password does not match Kodi's: store it again in `kodi-password`. |
| Kodi does not open | The `kodi` graphical session is not found: check that the user is logged in. |

## Uninstallation

```bash
sudo /usr/local/lib/apple-remote-kodi-connector/uninstall.sh kodi           # program, service and udev rule
sudo /usr/local/lib/apple-remote-kodi-connector/uninstall.sh kodi --purge   # also the configuration and password
```

The same with the one-liner: `curl -fsSL .../get.sh | sudo bash -s -- uninstall kodi [--purge]`. No input is needed.

Kodi is not modified: its web server stays as you configured it.

## Limitations

- When turned off with its remote, the TV still shows up as connected over HDMI, so turning it off can only be detected over the network (LG webOS TVs). A network outage on the TV side may pause playback, and switching the TV to another HDMI input is not detected. Ideas in [docs/IMPROVEMENTS.md](docs/IMPROVEMENTS.md).
- While the program is stopped, the remote controls the desktop again.

## License

[MIT](LICENSE)
