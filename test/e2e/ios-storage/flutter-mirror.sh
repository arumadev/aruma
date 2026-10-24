#!/usr/bin/env bash
# Measures whether `flutter build ios`, in Swift Package Manager mode, honours the SwiftPM mirror
# of firebase-ios-sdk (ADR-022 §16.3): a new Flutter app with firebase_core 4.15.2 and
# firebase_storage 13.6.1 (FlutterFire's last line on Firebase iOS 12.x: its Package.swift names
# the official URL with exact: "12.19.3"; the 5.x line needs firebase-ios-sdk 13, which needs
# Xcode 26), the mirror set in ios/Runner.xcworkspace's xcshareddata/swiftpm/configuration, and a
# simulator build. It then reports the pin in Package.resolved and whether the firebase-ios-sdk
# checkout Flutter built from is the patched one.
#
#   test/e2e/ios-storage/flutter-mirror.sh
#
#   ARUMA_FIREBASE_VERSION, ARUMA_IOS_STORAGE_CACHE, ARUMA_MIRROR  as in run.sh (the mirror must
#   already have the tag). The app is made under $ARUMA_IOS_STORAGE_CACHE/flutter-app.
#
# It needs Flutter (with PUB_CACHE where you want it) and Xcode.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
version=${ARUMA_FIREBASE_VERSION:-12.19.3}
cache=${ARUMA_IOS_STORAGE_CACHE:-$root/.cache/ios-storage}
mirror=${ARUMA_MIRROR:-$cache/mirror/firebase-ios-sdk.git}
official=https://github.com/firebase/firebase-ios-sdk
app=$cache/flutter-app
mirror_url=$mirror
[[ "$mirror_url" == *://* ]] || mirror_url=file://$mirror_url
mirror_commit=$(git ls-remote --tags "$mirror" "refs/tags/$version^{}" | cut -f1)
[[ -n "$mirror_commit" ]] || { echo "the mirror has no tag $version" >&2; exit 1; }

log() { printf '\n==> %s\n' "$*"; }

log "a Flutter app with firebase_core 4.15.2 and firebase_storage 13.6.1, Swift Package Manager on"
rm -rf "$app"
flutter create --platforms ios --org com.aruma --project-name aruma_mirror "$app" >/dev/null
python3 - "$app/pubspec.yaml" <<'EOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
s = s.replace("dependencies:\n  flutter:\n    sdk: flutter\n",
              "dependencies:\n  flutter:\n    sdk: flutter\n  firebase_core: 4.15.2\n  firebase_storage: 13.6.1\n", 1)
s = re.sub(r"\nflutter:\n", "\nflutter:\n  config:\n    enable-swift-package-manager: true\n", s, count=1)
open(p, "w").write(s)
EOF
(cd "$app" && flutter pub get >/dev/null)
# FlutterFire's packages need iOS 15; `flutter create` writes 13.0.
sed -i '' 's/IPHONEOS_DEPLOYMENT_TARGET = 13.0;/IPHONEOS_DEPLOYMENT_TARGET = 15.0;/' "$app/ios/Runner.xcodeproj/project.pbxproj"
grep -h 'firebaseSdkVersion: Version\|firebase-ios-sdk' \
  "${PUB_CACHE:-$HOME/.pub-cache}/hosted/pub.dev/firebase_core-4.15.2/ios/firebase_core/Package.swift" \
  | sed 's/^ */firebase_core 4.15.2: /' || true

log "swift package config set-mirror, into ios/Runner.xcworkspace"
cfg=$app/.mirror-config
mkdir -p "$cfg"
printf '// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: "MirrorConfig")\n' >"$cfg/Package.swift"
swift package --package-path "$cfg" config set-mirror --original "$official" --mirror "$mirror_url"
mkdir -p "$app/ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration"
cp "$cfg/.swiftpm/configuration/mirrors.json" "$app/ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration/"

log "flutter build ios --simulator --debug"
# Intermediates next to the app rather than in ~/Library/Developer/Xcode/DerivedData.
status=0
(cd "$app" && FLUTTER_XCODE_OBJROOT=$app/build/ios/obj flutter build ios --simulator --debug) >"$app/build.log" 2>&1 || status=$?
tail -3 "$app/build.log"

resolved=$(find "$app/ios" -name Package.resolved -path '*Runner.xcworkspace*' | head -1)
checkout=$(find "$app/build" -type d -path '*checkouts/firebase-ios-sdk' -prune 2>/dev/null | head -1)
pin=$( [[ -n "$resolved" ]] && python3 -c 'import json, sys
p = [p for p in json.load(open(sys.argv[1]))["pins"] if p["identity"] == "firebase-ios-sdk"]
print("%s %s %s" % (p[0]["location"], p[0]["state"].get("version"), p[0]["state"]["revision"]) if p else "none")' "$resolved" || echo none)
echo "flutter build ios exit=$status"
echo "Package.resolved ($resolved): $pin"
if [[ "$pin" == *"$mirror_commit"* ]]; then echo "pin: the MIRROR's $version"; else echo "pin: NOT the mirror ($mirror_commit)"; fi
if [[ -n "$checkout" && -f "$checkout/ARUMA-NOTICE.md" ]]; then
  echo "checkout $checkout: PATCHED"
elif [[ -n "$checkout" ]]; then
  echo "checkout $checkout: official"
else
  echo "checkout: not found under $app/build"
fi
