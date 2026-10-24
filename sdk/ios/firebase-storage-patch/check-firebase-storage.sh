#!/bin/sh
# Copyright 2026 The Aruma Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails when the firebase-ios-sdk SwiftPM resolved is not the Aruma mirror's patched one. SwiftPM
# never refuses the official package (a missing mirrors.json, or the mirror URL named next to the
# official one both resolve it with exit 0), and Storage then still works through ArumaRedirect's
# fallback, with download URLs on Google. It also fails when the checkout's commit or tree is not
# one that mirror.lock, next to this script, pins (SR3-09): SwiftPM's own check of a tag's revision
# is trust on first use per machine, so a moved tag or a mirror serving other code would pass a
# fresh CI runner. This is the recommended CI gate, and it works as an Xcode "Run Script" build
# phase of the app target (with User Script Sandboxing off for that target):
#
#   "${SRCROOT}/<path to aruma>/sdk/ios/firebase-storage-patch/check-firebase-storage.sh"
#
#   check-firebase-storage.sh [<SourcePackages directory, or a firebase-ios-sdk checkout>]
#
#   ARUMA_MIRROR_PINS  the pin file (default: mirror.lock next to this script)
#
# Without an argument it reads ARUMA_SOURCE_PACKAGES_DIR (a build setting, for -clonedSourcePackagesDirPath),
# else Xcode's own <DerivedData>/<project>/SourcePackages, found from BUILD_DIR. Errors are printed
# as "error: …" so that Xcode shows them on the build.
set -eu

dir=${1:-${ARUMA_SOURCE_PACKAGES_DIR:-}}
if [ -z "$dir" ] && [ -n "${BUILD_DIR:-}" ]; then
  dir=${BUILD_DIR%/Build/*}/SourcePackages
fi
if [ -z "$dir" ]; then
  echo "error: check-firebase-storage.sh: give the SourcePackages directory (or set ARUMA_SOURCE_PACKAGES_DIR)" >&2
  exit 1
fi
checkout=$dir
[ -d "$dir/checkouts/firebase-ios-sdk" ] && checkout=$dir/checkouts/firebase-ios-sdk
if [ ! -d "$checkout/FirebaseStorage/Sources" ]; then
  echo "error: no firebase-ios-sdk checkout in $dir: resolve the packages first" >&2
  exit 1
fi
if [ ! -f "$checkout/ARUMA-NOTICE.md" ] || [ ! -f "$checkout/FirebaseStorage/Sources/Internal/ArumaStorageOrigin.swift" ]; then
  echo "error: $checkout is the OFFICIAL firebase-ios-sdk, not the Aruma mirror: its download URLs name Google." >&2
  echo "error: set the mirror (swift package config set-mirror --original https://github.com/firebase/firebase-ios-sdk --mirror <the Aruma mirror>)," >&2
  echo "error: and never name the mirror URL directly next to the official one (sdk/ios/firebase-storage-patch/README.md)." >&2
  exit 1
fi
pins=${ARUMA_MIRROR_PINS:-$(cd "$(dirname "$0")" && pwd)/mirror.lock}
if [ ! -f "$pins" ]; then
  echo "error: no pin file $pins: it lists the mirror's tags with their commit and tree" >&2
  exit 1
fi
commit=$(git -C "$checkout" rev-parse -q --verify 'HEAD^{commit}' 2>/dev/null) || commit=
tree=$(git -C "$checkout" rev-parse -q --verify 'HEAD^{tree}' 2>/dev/null) || tree=
tag=$(awk -v c="$commit" -v t="$tree" '!/^#/ && NF == 3 && $2 == c && $3 == t { print $1; exit }' "$pins")
if [ -z "$commit" ] || [ -z "$tag" ]; then
  echo "error: $checkout is at ${commit:-an unreadable revision} (tree ${tree:-?}), which $pins does not pin:" >&2
  echo "error: a moved tag, or a mirror that serves other code. Resolve from Package.resolved (-onlyUsePackageVersionsFromResolvedFile)," >&2
  echo "error: and see sdk/ios/firebase-storage-patch/README.md (Pinning)." >&2
  exit 1
fi
echo "note: firebase-ios-sdk in $checkout is the Aruma mirror's patched build ($tag, $commit)"
