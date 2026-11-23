#!/usr/bin/env bash
# Points SwiftPM at your mirror of firebase-ios-sdk with the patched FirebaseStorage
# (sdk/ios/firebase-storage-patch: build it with make-mirror.sh 12.19.3 and host it yourself), for
# this Xcode workspace only: it writes Runner.xcworkspace/xcshareddata/swiftpm/configuration/
# mirrors.json, where Xcode reads mirrors (docs/guides/mobile-apps.md). Commit that file.
#
#   ios/set_mirror.sh <mirror URL>      for example git@git.example.com:you/firebase-ios-sdk.git
#
# Then check what SwiftPM resolved, as the guide says:
#   <aruma>/sdk/ios/firebase-storage-patch/check-firebase-storage.sh
set -euo pipefail
[[ $# -eq 1 ]] || { echo "usage: $0 <mirror URL>" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf '// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: "MirrorConfig")\n' >"$work/Package.swift"
swift package --package-path "$work" config set-mirror \
  --original https://github.com/firebase/firebase-ios-sdk --mirror "$1"
mkdir -p "$here/Runner.xcworkspace/xcshareddata/swiftpm/configuration"
cp "$work/.swiftpm/configuration/mirrors.json" "$here/Runner.xcworkspace/xcshareddata/swiftpm/configuration/mirrors.json"
echo "wrote ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration/mirrors.json"
