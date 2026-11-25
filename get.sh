#!/usr/bin/env bash
# Installs a cell on this machine, or updates it, from a release of the
# platform:
#
#   curl -fsSL https://raw.githubusercontent.com/marcelheidebrecht/cell-platform/main/get.sh | sudo bash
#
# CELL_VERSION picks a release (default: the latest), for example
# ... | sudo CELL_VERSION=v0.2.0 bash. Other settings pass through to
# deploy/install.sh the same way. The release lands in /opt/cell; settings
# and data live elsewhere, so an update only replaces that folder.
set -euo pipefail
# Read as a whole before running: a download cut short runs nothing.
{
repo=${CELL_REPO:-marcelheidebrecht/cell-platform}
dir=/opt/cell
fail() { printf '\n  error: %s\n\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "run as root: curl -fsSL <this URL> | sudo bash"
for c in curl tar; do command -v "$c" >/dev/null || fail "$c is missing (apt-get install $c)"; done
[ ! -d "$dir/.git" ] || fail "$dir is a git clone: run deploy/install.sh there"

version=${CELL_VERSION:-}
if [ -z "$version" ]; then
  # The latest release, from GitHub's redirect: no API call, no rate limit.
  url=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
    fail "could not reach github.com to find the latest release"
  version=${url##*/}
fi
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || fail "no release found (got \"$version\"); set CELL_VERSION=v<x.y.z>"

new=$(mktemp -d "$dir.XXXXXX")
trap 'rm -rf "$new"' EXIT
curl -fsSL "https://github.com/$repo/archive/refs/tags/$version.tar.gz" | tar xz -C "$new" --strip-components=1 ||
  fail "could not download release $version"
echo "$version" >"$new/VERSION"
chmod 755 "$new"
rm -rf "$dir.old"
[ ! -d "$dir" ] || mv "$dir" "$dir.old"
mv "$new" "$dir"
rm -rf "$dir.old"
trap - EXIT

# Piped into bash, stdin is this script: the installer asks on the terminal.
if { true </dev/tty; } 2>/dev/null; then
  exec "$dir/deploy/install.sh" </dev/tty
fi
exec "$dir/deploy/install.sh"
}
