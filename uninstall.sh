#!/usr/bin/env bash
# Uninstalls apple-remote-kodi-connector.
#
#   sudo ./uninstall.sh [user] [--purge]     (default user: kodi)
#
# The installer also copies this script to
# /usr/local/lib/apple-remote-kodi-connector/uninstall.sh.
#
# --purge also deletes the configuration and the stored password.
# Kodi itself is not modified (its web server stays as you configured it).
set -euo pipefail

NAME=apple-remote-kodi-connector
TARGET=kodi
PURGE=0
for a in "$@"; do
  case $a in
    --purge) PURGE=1 ;;
    *) TARGET=$a ;;
  esac
done

die() { echo "ERROR: $*" >&2; exit 1; }
step() { printf '\n==> %s\n' "$*"; }

[ "$(id -u)" = 0 ] || die "run it with sudo: sudo ./uninstall.sh [user] [--purge]"
getent passwd "$TARGET" >/dev/null || die "user '$TARGET' does not exist"
TUID=$(id -u "$TARGET")
THOME=$(getent passwd "$TARGET" | cut -d: -f6)
UNITDIR=$THOME/.config/systemd/user
as_user() { sudo -u "$TARGET" HOME="$THOME" XDG_RUNTIME_DIR="/run/user/$TUID" "$@"; }

step "Stopping and removing the service"
as_user systemctl --user disable --now "$NAME" 2>/dev/null || true
pkill -u "$TARGET" -f "/$NAME( |\$)" 2>/dev/null || true
rm -f "$UNITDIR/$NAME.service" "$UNITDIR/default.target.wants/$NAME.service"
as_user systemctl --user daemon-reload 2>/dev/null || true

step "Removing the program"
rm -f "/usr/local/bin/$NAME"
rm -rf "/usr/local/lib/$NAME"

step "Removing the udev rule"
rm -f "/etc/udev/rules.d/71-$NAME.rules"
udevadm control --reload
for d in /sys/bus/usb/devices/* /sys/class/input/event*; do
  [ -e "$d" ] || continue
  v=$(cat "$d/idVendor" "$d/device/id/vendor" 2>/dev/null | head -1 || true)
  p=$(cat "$d/idProduct" "$d/device/id/product" 2>/dev/null | head -1 || true)
  if [ "$v" = 05ac ] && [ "$p" = 8242 ]; then
    if [ -w "$d/power/wakeup" ]; then echo disabled > "$d/power/wakeup"; fi
    udevadm trigger --action=change "$d"
  fi
done

if [ $PURGE = 1 ]; then
  step "Deleting configuration and password"
  rm -rf "$THOME/.config/$NAME"
else
  echo
  echo "Keeping $THOME/.config/$NAME (use --purge to delete it)."
fi

step "Done"
