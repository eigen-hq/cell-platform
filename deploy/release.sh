#!/usr/bin/env bash
# Signs and publishes a release of the platform (E-65):
#
#   make release VERSION=v0.3.0
#
# The tag must exist here and on GitHub, on the commit to release. The
# release carries cell-<version>.tar.gz (git archive of the tag),
# SHA256SUMS and SHA256SUMS.sig; get.sh installs it only when the
# signature matches its key. The private key never enters the repo:
# CELL_RELEASE_KEY, default ~/.config/cell/release-signing-key.
#
#   deploy/release.sh build <version> <dir>   only the files, nothing published
#   deploy/release.sh publish <version>       the files, then gh release create
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
key=${CELL_RELEASE_KEY:-$HOME/.config/cell/release-signing-key}
fail() { echo "error: $*" >&2; exit 1; }
sha256() { (sha256sum "$1" 2>/dev/null || shasum -a 256 "$1") | awk '{print $1}'; }

# build <version> <dir> writes the signed release files of the tag to dir.
build() {
  local v=$1 out=$2 file=cell-$1.tar.gz commit
  [[ $v =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || fail "VERSION=$v is no version (vX.Y.Z)"
  commit=$(git -C "$root" rev-parse -q --verify "refs/tags/$v^{commit}") || fail "tag $v is missing: tag the commit to release"
  # The key as the release carries it: a placeholder signs nothing.
  git -C "$root" show "$commit:deploy/release-signers" >"$out/signers" || fail "$v has no deploy/release-signers"
  ! grep -q PLACEHOLDER "$out/signers" || fail "deploy/release-signers at $v holds no key yet: commit the public key first"
  [ -f "$key" ] || fail "no signing key at $key (CELL_RELEASE_KEY)"
  git -C "$root" archive --format=tar.gz --prefix="cell-$v/" -o "$out/$file" "$commit"
  printf '%s  %s\n' "$(sha256 "$out/$file")" "$file" >"$out/SHA256SUMS"
  ssh-keygen -Y sign -q -f "$key" -n cell-release "$out/SHA256SUMS"
  # The check get.sh makes: a release it would refuse is not published.
  ssh-keygen -Y verify -f "$out/signers" -I cell-release -n cell-release -s "$out/SHA256SUMS.sig" <"$out/SHA256SUMS" >/dev/null ||
    fail "the signature does not match deploy/release-signers at $v: another key?"
  rm "$out/signers"
}

publish() {
  local v=$1 commit remote live
  # Not local: the EXIT trap runs after publish has returned.
  out=$(mktemp -d)
  trap 'rm -rf "$out"' EXIT
  build "$v" "$out"
  commit=$(git -C "$root" rev-parse "refs/tags/$v^{commit}")
  # On GitHub the tag points at the archived commit (with an annotated tag
  # the last line is the commit it points at).
  remote=$(git -C "$root" ls-remote origin "refs/tags/$v" "refs/tags/$v^{}" | awk 'END {print $1}')
  [ "$remote" = "$commit" ] || fail "tag $v on GitHub is ${remote:-missing}, here $commit: push the tag"
  # The installer customers run must know the key, or it refuses the release.
  live=$(curl -fsSL https://get.eigen-platform.com | sed -n "s/^signers='\(.*\)'$/\1/p")
  [ "$live" = "$(git -C "$root" show "$commit:deploy/release-signers" | grep -v '^#')" ] ||
    fail "get.eigen-platform.com has another release key: update its copy of get.sh (branch pages) first"
  gh release create "$v" --verify-tag --generate-notes "$out/cell-$v.tar.gz" "$out/SHA256SUMS" "$out/SHA256SUMS.sig"
}

case ${1:-} in
  build)
    [ $# -eq 3 ] || fail "usage: $0 build <version> <dir>"
    build "$2" "$3"
    ;;
  publish)
    [ $# -eq 2 ] || fail "usage: $0 publish <version>"
    publish "$2"
    ;;
  *) fail "usage: $0 build <version> <dir> | publish <version>" ;;
esac
