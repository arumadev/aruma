#!/bin/sh
# Xcode build phase "Aruma URL" (after "Thin Binary"): writes ArumaURL into the app's built
# Info.plist from aruma.json, the one place the URL is written. ArumaRedirect (Auth) and the
# patched FirebaseStorage read it at launch. With "useAruma": false the keys are removed, and the
# app is a plain FlutterFire app. Flutter's own build phase edits the built Info.plist the same way.
set -eu
json="$SRCROOT/../aruma.json"
plist="$TARGET_BUILD_DIR/$INFOPLIST_PATH"
use=$(plutil -extract useAruma raw -o - "$json")
if [ "$use" = "true" ]; then
  url=$(plutil -extract arumaUrl raw -o - "$json")
  plutil -replace ArumaURL -string "${url%/}" "$plist"
  if [ "$CONFIGURATION" = "Release" ]; then
    # Release: an app that resolved the official FirebaseStorage stops at launch instead of
    # sending download links to Google (docs/guides/mobile-apps.md, Release builds).
    plutil -replace ArumaStorageRedirect -bool NO "$plist"
  fi
  echo "ArumaURL is ${url%/}"
else
  plutil -remove ArumaURL "$plist" 2>/dev/null || true
  plutil -remove ArumaStorageRedirect "$plist" 2>/dev/null || true
  echo "aruma.json: useAruma is false; no ArumaURL"
fi
