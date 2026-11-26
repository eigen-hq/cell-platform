#!/usr/bin/env bash
# Installs a cell on this machine, or updates it, from a release of the
# platform:
#
#   curl -fsSL https://get.eigen-platform.com | sudo bash
#
# That address serves a copy of this file (GitHub Pages, branch pages):
# update the copy whenever this file changes.
#
# CELL_VERSION picks a release (default: the latest), for example
# ... | sudo CELL_VERSION=v0.2.0 bash. Other settings pass through to
# deploy/install.sh the same way. The release lands in /opt/cell; settings
# and data live elsewhere, so an update only replaces that folder.
set -euo pipefail
# Read as a whole before running: a download cut short runs nothing.
{
repo=${CELL_REPO:-eigen-hq/cell-platform}
dir=/opt/cell
fail() { printf '\n  error: %s\n\n' "$*" >&2; exit 1; }

# The key that signs the releases (deploy/release-signers, E-65). Every
# release after v0.2.5 carries SHA256SUMS and its signature; this script
# installs one only when both match.
signers='cell-release namespaces="cell-release" ssh-ed25519 PLACEHOLDER-no-release-key-yet'
# The releases from before signing, each by its commit: a moved tag changes
# nothing.
legacy() {
  case $1 in
    v0.1.0) echo 2217f8cad299de5a8eb5abf39f65cdbf07b2da15 ;;
    v0.2.0) echo 5eac9cec0a9703bf5975b09b8eb1e81ea01b02d0 ;;
    v0.2.1) echo bf7694e3b90d7e677fa5885af76813a211f7ff14 ;;
    v0.2.2) echo c9505385d58911a77837104d2cb8327aae7a0a4c ;;
    v0.2.3) echo aff97de61d32f2f6080f3d2eae300cdce2c9a528 ;;
    v0.2.4) echo 18640548d9c9bb949bc47708d3667d522b40900a ;;
    v0.2.5) echo 8ee23ea2f8b6c24f2ac59ec5e7ca26c578061dcc ;;
    *) return 1 ;;
  esac
}
sha256() { (sha256sum "$1" 2>/dev/null || shasum -a 256 "$1") | awk '{print $1}'; }

# release <version> <download dir> <dir> unpacks the release into dir once
# its signature and checksum match, then removes the download dir; dir stays
# empty otherwise.
release() {
  local v=$1 dl=$2 to=$3 commit file=cell-$1.tar.gz sum a
  if commit=$(legacy "$v"); then
    curl -fsSL "https://github.com/$repo/archive/$commit.tar.gz" | tar xz -C "$to" --strip-components=1 ||
      fail "could not download release $v"
    rm -rf "$dl"
    return
  fi
  [[ $signers != *PLACEHOLDER* ]] || fail "this installer has no release key yet and installs only v0.2.5 or older (CELL_VERSION=v0.2.5)"
  command -v ssh-keygen >/dev/null || fail "ssh-keygen is missing, it checks the release (apt-get install openssh-client)"
  for a in SHA256SUMS SHA256SUMS.sig "$file"; do
    curl -fsSL -o "$dl/$a" "https://github.com/$repo/releases/download/$v/$a" ||
      fail "could not download $a of release $v (every release after v0.2.5 is signed)"
  done
  printf '%s\n' "$signers" >"$dl/signers"
  ssh-keygen -Y verify -f "$dl/signers" -I cell-release -n cell-release -s "$dl/SHA256SUMS.sig" <"$dl/SHA256SUMS" >/dev/null 2>&1 ||
    fail "the signature of release $v does not match the release key: refusing it"
  sum=$(awk -v f="$file" '$2 == f {print $1}' "$dl/SHA256SUMS")
  [ -n "$sum" ] && [ "$(sha256 "$dl/$file")" = "$sum" ] ||
    fail "$file does not match the signed checksums of release $v: refusing it"
  tar xzf "$dl/$file" -C "$to" --strip-components=1 || fail "could not unpack release $v"
  rm -rf "$dl"
}

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
dl=$(mktemp -d)
trap 'rm -rf "$new" "$dl"' EXIT
release "$version" "$dl" "$new"
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
