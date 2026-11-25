#!/usr/bin/env bash
# Installs or updates the cell on this machine, its server, to a release tag
# of the platform repo: checks the tag out in the clone this script lives in,
# builds the platform image here and runs install.sh with it. No registry:
# the other machines of the cell get the image from this one (E-42). Run as
# root; the first time with the settings of install.sh, later without (it
# keeps them):
#
#   CELL_DOMAIN=cell.example.com CELL_ACME_EMAIL=ops@example.com deploy/release.sh v0.1.0
#   deploy/release.sh v0.2.0
#
# Use a clone for releases only: the update checks the tag out in it. Git
# runs as the clone's owner, with their access to the repo.
set -euo pipefail
# Read as a whole before running: the checkout below replaces this file.
{
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
source "$here/lib.sh"
[ "$(id -u)" -eq 0 ] || die "run as root"
tag=${1:?usage: release.sh <release tag>}

owner=$(stat -c %U "$repo")
as_owner() { sudo -u "$owner" -H "$@"; }
log "release $tag"
as_owner git -C "$repo" fetch --tags --quiet
as_owner git -C "$repo" -c advice.detachedHead=false checkout --quiet "refs/tags/$tag"

if ! command -v docker >/dev/null; then
  log "Docker (only to build the platform image)"
  apt-get update -q
  DEBIAN_FRONTEND=noninteractive apt-get install -yq docker.io
fi
image=registry.cell.internal/cell-operator:$tag
log "build $image"
docker build -q -t "$image" "$repo" >/dev/null
tar=$(mktemp)
trap 'rm -f "$tar"' EXIT
docker save "$image" -o "$tar"

OPERATOR_IMAGE=$image OPERATOR_IMAGE_TAR=$tar "$repo/deploy/install.sh"
log "cell runs $tag"
exit
}
