#!/usr/bin/env bash
# Measures how SwiftPM treats the Aruma mirror of firebase-ios-sdk (ADR-022 §16.3), with the
# toolchain on this machine, by resolving small packages (no build):
#
#   from, exact, range   the official URL with `from:`, `exact:` and a range, and set-mirror:
#                        each must resolve to the mirror's patched commit for the official tag.
#   identity             an app that names the mirror URL directly, next to a package that names
#                        the official URL (as FlutterFire's firebase_storage does): the same
#                        identity firebase-ios-sdk from two URLs. Does SwiftPM refuse it?
#   identity+mirror      the same two packages, both on the official URL, with set-mirror.
#   stale-pin            a Package.resolved that pins the official revision of the tag, then
#                        set-mirror: what resolution does with a revision the mirror lacks.
#
#   test/e2e/ios-storage/measure-mirror.sh
#
#   ARUMA_FIREBASE_VERSION, ARUMA_IOS_STORAGE_CACHE, ARUMA_MIRROR  as in run.sh (the mirror must
#   already have the tag: run.sh or make-mirror.sh builds it).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
version=${ARUMA_FIREBASE_VERSION:-12.19.3}
cache=${ARUMA_IOS_STORAGE_CACHE:-$root/.cache/ios-storage}
mirror=${ARUMA_MIRROR:-$cache/mirror/firebase-ios-sdk.git}
official=https://github.com/firebase/firebase-ios-sdk
work=$here/.generated/measure
mirror_url=$mirror
[[ "$mirror_url" == *://* ]] || mirror_url=file://$mirror_url
mirror_commit=$(git ls-remote --tags "$mirror" "refs/tags/$version^{}" | cut -f1)
official_commit=$(git ls-remote --tags "$official" "refs/tags/$version^{}" "refs/tags/$version" | cut -f1 | tail -1)
[[ -n "$mirror_commit" ]] || { echo "the mirror has no tag $version" >&2; exit 1; }
echo "official $version: $official_commit; mirror $version: $mirror_commit"
rm -rf "$work"
mkdir -p "$work"

# package <dir> <name> <dependencies...> writes a Package.swift whose one target uses FirebaseStorage.
package() {
  local dir=$1 name=$2
  shift 2
  mkdir -p "$dir/Sources/$name"
  echo "public let marker = 1" >"$dir/Sources/$name/$name.swift"
  local deps=() products=()
  for d in "$@"; do
    deps+=("    $d,")
  done
  cat >"$dir/Package.swift" <<EOF
// swift-tools-version:5.9
import PackageDescription
let package = Package(
  name: "$name",
  platforms: [.iOS(.v15)],
  products: [.library(name: "$name", targets: ["$name"])],
  dependencies: [
$(printf '%s\n' "${deps[@]}")
  ],
  targets: [.target(name: "$name", dependencies: [$(
    if [[ "$*" == *firebase-ios-sdk* ]]; then echo '.product(name: "FirebaseStorage", package: "firebase-ios-sdk")'; fi
    for d in "$@"; do [[ "$d" == *'path:'* ]] && echo ", .product(name: \"Plugin\", package: \"plugin\")"; done
  )])]
)
EOF
}

set_mirror() {
  swift package --package-path "$1" config set-mirror --original "$official" --mirror "$mirror_url"
}

# resolve <case> <dir>: prints the outcome and the firebase-ios-sdk pin.
resolve() {
  local name=$1 dir=$2 status=0
  swift package --package-path "$dir" --scratch-path "$dir/.build" --cache-path "$cache/spm-cache" resolve \
    >"$dir/resolve.log" 2>&1 || status=$?
  local pin=none
  if [[ -f "$dir/Package.resolved" ]]; then
    pin=$(python3 -c 'import json, sys
pins = json.load(open(sys.argv[1]))["pins"]
p = [p for p in pins if p["identity"] == "firebase-ios-sdk"]
print("%s %s %s" % (p[0]["location"], p[0]["state"].get("version"), p[0]["state"]["revision"]) if p else "none")' "$dir/Package.resolved")
  fi
  local where=official
  [[ "$pin" == *"$mirror_commit"* ]] && where=MIRROR
  [[ "$pin" == none ]] && where=-
  printf '%-16s exit=%d  firebase-ios-sdk -> %s (%s)\n' "$name" "$status" "$where" "$pin"
  grep -iE "error|warning|conflict|identity" "$dir/resolve.log" | grep -v '^Fetching\|^Fetched\|^Computing\|^Computed\|^Creating\|^Working' \
    | head -4 | sed 's/^/                   /' || true
  rm -rf "$dir/.build"
}

echo
for kind in from exact range; do
  case $kind in
    from) req="from: \"$version\"" ;;
    exact) req="exact: \"$version\"" ;;
    range) req="\"$version\"..<\"13.0.0\"" ;;
  esac
  package "$work/$kind" App ".package(url: \"$official\", $req)"
  set_mirror "$work/$kind"
  resolve "$kind" "$work/$kind"
done

# FlutterFire's firebase_storage names the official URL with exact: (Package.swift:22).
package "$work/identity/plugin" Plugin ".package(url: \"$official\", exact: \"$version\")"
package "$work/identity/app" App ".package(url: \"$mirror_url\", exact: \"$version\")" ".package(path: \"../plugin\")"
resolve identity "$work/identity/app"

package "$work/identity-mirror/plugin" Plugin ".package(url: \"$official\", exact: \"$version\")"
package "$work/identity-mirror/app" App ".package(url: \"$official\", exact: \"$version\")" ".package(path: \"../plugin\")"
set_mirror "$work/identity-mirror/app"
resolve identity+mirror "$work/identity-mirror/app"

package "$work/stale-pin" App ".package(url: \"$official\", exact: \"$version\")"
cat >"$work/stale-pin/Package.resolved" <<EOF
{
  "pins" : [
    {
      "identity" : "firebase-ios-sdk",
      "kind" : "remoteSourceControl",
      "location" : "$official",
      "state" : { "revision" : "$official_commit", "version" : "$version" }
    }
  ],
  "version" : 2
}
EOF
set_mirror "$work/stale-pin"
resolve stale-pin "$work/stale-pin"
