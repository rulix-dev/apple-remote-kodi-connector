#!/usr/bin/env bash
# Installs, updates or uninstalls apple-remote-kodi-connector from the GitHub
# releases, without cloning the repository or installing Go.
#
#   curl -fsSL https://raw.githubusercontent.com/rulix-dev/apple-remote-kodi-connector/main/get.sh | sudo bash -s -- install [user]
#   curl -fsSL https://raw.githubusercontent.com/rulix-dev/apple-remote-kodi-connector/main/get.sh | sudo bash -s -- uninstall [user] [--purge]
#
# The user defaults to "kodi". Environment variables:
#   VERSION=v1.2.3     install a given release instead of the latest one
#                      (default: latest stable release, or newest pre-release)
#                      VERSION=nightly installs the latest development build
#   KODI_PASSWORD=...  Kodi web server password (otherwise asked on first install)
set -euo pipefail

GH_REPO=rulix-dev/apple-remote-kodi-connector
NAME=apple-remote-kodi-connector

die() { echo "ERROR: $*" >&2; exit 1; }

cmd=${1:-install}
[ $# -gt 0 ] && shift
case $cmd in
  install | uninstall) ;;
  *) die "usage: get.sh install|uninstall [user] [--purge]" ;;
esac
[ "$(id -u)" = 0 ] || die "run it with sudo (... | sudo bash -s -- $cmd)"

# Uninstalling does not need a download if the local uninstaller is present.
if [ "$cmd" = uninstall ] && [ -x "/usr/local/lib/$NAME/uninstall.sh" ]; then
  exec "/usr/local/lib/$NAME/uninstall.sh" "$@"
fi

for tool in curl tar sha256sum; do
  command -v "$tool" >/dev/null || die "$tool is required"
done
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

tag=${VERSION:-}
if [ -z "$tag" ]; then
  # Latest stable release; if there is none yet, the newest pre-release.
  latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$GH_REPO/releases/latest" 2>/dev/null || true)
  tag=${latest##*/}
  case $tag in
    v*) ;;
    *) tag=$(curl -fsSL "https://api.github.com/repos/$GH_REPO/releases?per_page=20" |
      grep -o '"tag_name": *"v[^"]*"' | head -1 | sed 's/.*"\(v[^"]*\)"/\1/') ;;
  esac
  [ -n "$tag" ] || die "no release found at https://github.com/$GH_REPO/releases"
fi

pkg=$NAME-$tag-linux-$arch
base=https://github.com/$GH_REPO/releases/download/$tag
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "==> Downloading $pkg"
curl -fsSL -o "$tmp/$pkg.tar.gz" "$base/$pkg.tar.gz"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
(cd "$tmp" && grep " $pkg.tar.gz\$" SHA256SUMS | sha256sum -c --quiet -) || die "checksum mismatch"
tar -xzf "$tmp/$pkg.tar.gz" -C "$tmp"

"$tmp/$pkg/$cmd.sh" "$@"
