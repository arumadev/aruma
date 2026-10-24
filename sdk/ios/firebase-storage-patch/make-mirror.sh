#!/usr/bin/env bash
# Copyright 2026 The Aruma Authors
# SPDX-License-Identifier: Apache-2.0
#
# Builds one release of the FirebaseStorage mirror (ADR-022 §16.3): the official firebase-ios-sdk
# at <upstream-tag>, with storage-host.patch applied, as ONE commit with no parent, tagged with the
# official name itself (12.19.3, not 12.19.3-aruma.1), so that `from:` and `exact:` requirements
# resolve to it unchanged once SwiftPM is pointed at the mirror (`swift package config set-mirror`).
# The mirror never carries an unpatched commit or tag.
#
#   sdk/ios/firebase-storage-patch/make-mirror.sh <upstream-tag>
#
#   ARUMA_MIRROR        the mirror repository to push the tag to (default: a bare repository
#                       $ARUMA_MIRROR_WORK/firebase-ios-sdk.git, created if absent). Its last path
#                       component must stay firebase-ios-sdk: that is the package identity.
#   ARUMA_MIRROR_WORK   checkouts, DerivedData and SwiftPM caches (default <repo>/.cache/firebase-storage-mirror)
#   ARUMA_UPSTREAM      the official repository (default https://github.com/firebase/firebase-ios-sdk.git)
#   ARUMA_MIRROR_TESTS  macOS (default): run FirebaseStorage's unit tests (FirebaseStorageUnit, the
#                       official ones and the patch's) before tagging; off: skip them.
#
# It fails, and tags nothing, when the patch does not apply exactly, when a test fails, or when the
# mirror already has the tag on another commit. It needs git and Xcode.
set -euo pipefail

tag=${1:?usage: make-mirror.sh <upstream-tag>}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
work=${ARUMA_MIRROR_WORK:-$root/.cache/firebase-storage-mirror}
upstream=${ARUMA_UPSTREAM:-https://github.com/firebase/firebase-ios-sdk.git}
mirror=${ARUMA_MIRROR:-$work/firebase-ios-sdk.git}
tests=${ARUMA_MIRROR_TESTS:-macOS}
src=$work/src-$tag
patch=$here/storage-host.patch

log() { printf '\n==> %s\n' "$*"; }

[[ "$(basename "${mirror%.git}")" == firebase-ios-sdk ]] \
  || { echo "the mirror's last path component must be firebase-ios-sdk (the package identity): $mirror" >&2; exit 1; }
mkdir -p "$work"

log "fetching $upstream at $tag"
rm -rf "$src"
git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" "$upstream" "$src"
upstream_commit=$(git -C "$src" rev-parse HEAD)
echo "official $tag is $upstream_commit"

log "applying $(basename "$patch")"
# --check first: nothing is written unless every hunk applies exactly.
git -C "$src" apply --check --whitespace=error-all "$patch"
git -C "$src" apply --whitespace=error-all "$patch"
git -C "$src" diff --stat
cp "$here/MIRROR-NOTICE.md" "$src/ARUMA-NOTICE.md"

if [[ "$tests" != off ]]; then
  log "FirebaseStorage's unit tests on $tests (FirebaseStorageUnit: the official tests and the patch's)"
  # The test scheme, put in place as upstream's scripts/setup_spm_tests.sh does (not committed).
  mkdir -p "$src/.swiftpm/xcode/xcshareddata/xcschemes"
  cp "$src/scripts/spm_test_schemes/FirebaseStorageUnit.xcscheme" "$src/.swiftpm/xcode/xcshareddata/xcschemes/"
  status=0
  (cd "$src" && xcodebuild test -scheme FirebaseStorageUnit -destination "platform=$tests" \
    -derivedDataPath "$work/dd" -clonedSourcePackagesDirPath "$work/spm" -packageCachePath "$work/spm-cache" \
    -skipPackagePluginValidation -resultBundlePath "$work/FirebaseStorageUnit-$tag.xcresult" \
    >"$work/tests-$tag.log" 2>&1) || status=$?
  rm -rf "$work/FirebaseStorageUnit-$tag.xcresult"
  grep -E "Executed [0-9]+ tests?|error:|failed|\*\* TEST" "$work/tests-$tag.log" | grep -v "^ld: warning" | tail -8 || true
  [[ $status -eq 0 ]] || { echo "FirebaseStorage's tests failed; see $work/tests-$tag.log" >&2; exit 1; }
fi

log "committing the patched tree as one commit with no parent"
git -C "$src" add -- FirebaseStorage ARUMA-NOTICE.md
tree=$(git -C "$src" write-tree)
# Dated as the official commit, so that the same patch, tag and committer give the same revision
# (Package.resolved pins it) when the mirror is rebuilt.
upstream_date=$(git -C "$src" log -1 --format=%cI HEAD)
export GIT_AUTHOR_DATE=$upstream_date GIT_COMMITTER_DATE=$upstream_date
commit=$(git -C "$src" commit-tree "$tree" -F - <<EOF
FirebaseStorage $tag for Aruma

The official firebase-ios-sdk $tag ($upstream_commit) with
sdk/ios/firebase-storage-patch/storage-host.patch from https://github.com/arumadev/aruma:
FirebaseStorage reads its server from ArumaURL in Info.plist, and its port is optional.
Nothing else is changed. See ARUMA-NOTICE.md (Apache-2.0).
EOF
)
git -C "$src" tag -f -a "$tag" -m "FirebaseStorage $tag for Aruma (official $upstream_commit)" "$commit" >/dev/null
# The tag must name a patched commit without a parent: never the official history.
[[ -z "$(git -C "$src" rev-list --parents -n 1 "$commit" | cut -d' ' -f2-)" ]]
git -C "$src" cat-file -e "$commit:ARUMA-NOTICE.md"

log "pushing tag $tag to $mirror"
if [[ "$mirror" == "$work/firebase-ios-sdk.git" && ! -d "$mirror" ]]; then
  git init -q --bare "$mirror"
fi
existing=$(git ls-remote --tags "$mirror" "refs/tags/$tag^{}" | cut -f1)
if [[ -n "$existing" ]]; then
  existing_tree=$(git -C "$src" fetch -q "$mirror" "refs/tags/$tag" && git -C "$src" rev-parse "FETCH_HEAD^{tree}")
  if [[ "$existing_tree" == "$tree" ]]; then
    echo "the mirror already has $tag with this patched tree; nothing to push"
    exit 0
  fi
  echo "the mirror already has $tag on another tree ($existing); refusing to move a published tag" >&2
  exit 1
fi
git -C "$src" push -q "$mirror" "refs/tags/$tag"
echo "mirror tag $tag -> $commit (tree $tree)"
