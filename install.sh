#!/usr/bin/env bash
# Installs apple-remote-kodi-connector for the user who watches Kodi on the TV.
#
#   sudo ./install.sh [user]        (default: kodi)
#
# Safe to run again to update: the existing configuration and password are kept.
# To uninstall: sudo ./uninstall.sh [user]
set -euo pipefail

NAME=apple-remote-kodi-connector
REPO=$(cd "$(dirname "$0")" && pwd)
TARGET=${1:-kodi}
BIN=/usr/local/bin/$NAME
RULE=/etc/udev/rules.d/71-$NAME.rules

die() { echo "ERROR: $*" >&2; exit 1; }
step() { printf '\n==> %s\n' "$*"; }

[ "$(id -u)" = 0 ] || die "run it with sudo: sudo ./install.sh [user]"
getent passwd "$TARGET" >/dev/null || die "user '$TARGET' does not exist"
TUID=$(id -u "$TARGET")
TGROUP=$(id -gn "$TARGET")
THOME=$(getent passwd "$TARGET" | cut -d: -f6)
CONF=$THOME/.config/$NAME
UNITDIR=$THOME/.config/systemd/user
as_user() { sudo -u "$TARGET" HOME="$THOME" XDG_RUNTIME_DIR="/run/user/$TUID" "$@"; }

# Runs "udevadm trigger" on the Apple IR receiver devices only.
trigger_ir_receiver() {
  for d in /sys/bus/usb/devices/* /sys/class/input/event*; do
    [ -e "$d" ] || continue
    v=$(cat "$d/idVendor" "$d/device/id/vendor" 2>/dev/null | head -1 || true)
    p=$(cat "$d/idProduct" "$d/device/id/product" 2>/dev/null | head -1 || true)
    if [ "$v" = 05ac ] && [ "$p" = 8242 ]; then
      udevadm trigger --action=change "$d"
    fi
  done
  udevadm settle
}

step "Building"
if [ -x "$REPO/bin/$NAME" ]; then
  echo "Using the already built binary: bin/$NAME"
else
  command -v go >/dev/null || die "Go is needed to build: sudo apt install golang-go"
  builder=${SUDO_USER:-root}
  sudo -u "$builder" sh -c "cd '$REPO' && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o bin/$NAME ./cmd/$NAME"
fi

step "Stopping any running instance"
as_user systemctl --user stop "$NAME" 2>/dev/null || true
pkill -u "$TARGET" -f "/$NAME( |\$)" 2>/dev/null || true

step "Installing the program to $BIN"
install -m 0755 "$REPO/bin/$NAME" "$BIN"

step "IR receiver permissions (udev rule)"
install -m 0644 "$REPO/packaging/udev/71-$NAME.rules" "$RULE"
udevadm control --reload
trigger_ir_receiver
found=0
for d in /sys/bus/usb/devices/*; do
  if [ "$(cat "$d/idVendor" 2>/dev/null)" = 05ac ] && [ "$(cat "$d/idProduct" 2>/dev/null)" = 8242 ]; then
    found=1
    echo "IR receiver found; wake up with the remote: $(cat "$d/power/wakeup")"
  fi
done
[ $found = 1 ] || echo "WARNING: Apple IR receiver (USB 05ac:8242) not found."

step "Configuration in $CONF"
install -d -m 0700 -o "$TARGET" -g "$TGROUP" "$CONF"
if [ -f "$CONF/config.yaml" ]; then
  echo "Keeping the existing config.yaml"
else
  install -m 0600 -o "$TARGET" -g "$TGROUP" "$REPO/configs/config.example.yaml" "$CONF/config.yaml"
  echo "Created config.yaml from the example"
fi
if [ -f "$CONF/kodi-password" ]; then
  echo "Keeping the existing Kodi password"
else
  read -rsp "Kodi web server password (not shown): " p1; echo
  read -rsp "Repeat it: " p2; echo
  [ "$p1" = "$p2" ] || die "passwords do not match"
  (umask 077; printf '%s' "$p1" > "$CONF/kodi-password")
  unset p1 p2
  echo "Saved to kodi-password (readable by $TARGET only)"
fi
chown "$TARGET:$TGROUP" "$CONF"/*
chmod 0600 "$CONF/kodi-password"

ir_cfg=$THOME/.config/input-remapper-2/config.json
if [ -f "$ir_cfg" ] && grep -q "IR Receiver" "$ir_cfg"; then
  echo "WARNING: Input Remapper loads a preset for the IR receiver at login, which"
  echo "         would block the remote. Disable its autoload in Input Remapper."
fi

step "User service (systemd --user)"
install -d -o "$TARGET" -g "$TGROUP" "$THOME/.config/systemd" "$UNITDIR"
install -m 0644 -o "$TARGET" -g "$TGROUP" "$REPO/packaging/systemd/$NAME.service" "$UNITDIR/$NAME.service"
if as_user systemctl --user daemon-reload 2>/dev/null; then
  as_user systemctl --user enable "$NAME" >/dev/null 2>&1
  as_user "$BIN" -check || echo "WARNING: review the FAIL items above (with Kodi open)."
  as_user systemctl --user restart "$NAME"
  sleep 2
  as_user systemctl --user --no-pager status "$NAME" | head -5 || true
else
  install -d -o "$TARGET" -g "$TGROUP" "$UNITDIR/default.target.wants"
  ln -sf "../$NAME.service" "$UNITDIR/default.target.wants/$NAME.service"
  echo "User $TARGET is not logged in: the service will start with their session."
fi

step "Done"
echo "Logs (as $TARGET):  journalctl --user -u $NAME -f"
