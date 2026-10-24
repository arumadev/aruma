#!/usr/bin/env bash
# Measures where the build-time Storage rewrite works beyond run.sh's bundlers, with the same app,
# server and guard (ADR-022 §16.1 asks S6-W to measure these and write the result down):
#
#   test/e2e/web-storage/measure.sh [target…]
#
#   vite5 vite6 vite7 vite8  `vite build` and the dev server (its dependency optimizer: esbuild's
#                            before Vite 8, Rolldown's from 8), each in the browser
#   next                     Next.js: next build --webpack, next build (Turbopack) and next dev
#                            (Turbopack), each with its server under the netguard
#   expo                     an Expo app's web export through Metro, in the browser, and its
#                            Android bundle, checked for the rewritten host
#   flutter                  a Flutter web app after `aruma-web flutter-web`, in the browser
#                            (needs Flutter, and PUB_CACHE with firebase_storage 14.0.1)
#
# Default: all. (The run-time fallback is in run.sh.) The apps are copied from apps/ and installed under ARUMA_E2E_APPS_DIR (default
# <repo>/.cache/web-storage-apps), outside the repository. Results: one line per target and app,
# then the Playwright output. It exits 1 when a line says FAILED.
#
#   test/e2e/web-storage/measure.sh --install [target…]   only install the apps, then exit (CI
#                                                        measures after it with the network off)
#
# Under test/ci/egress-guard.sh (ARUMA_EGRESS_GUARD) it installs nothing and fails when an app is
# not installed. Every measured value is a check: an ok line, or a FAILED one.
#
# The versions are the ones measured (docs/compat/storage-v0.3.md, S6-W), and every other package
# resolves as the registry stood then (npm install --before), so each run installs the same tree.
set -euo pipefail

npm_before=2026-10-24T12:00:00Z
firebase=13.1.0
vite_versions=(5.4.21 6.4.5 7.3.7 8.3.5) # Vite 5, 6, 7, 8
next=16.4.3 react=19.3.1
expo=58.0.19 svg_transformer=1.5.3 metro=0.87.1

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
apps=${ARUMA_E2E_APPS_DIR:-$root/.cache/web-storage-apps}
sdk=$root/sdk/web/aruma-web
# shellcheck source=lib.sh
. "$here/lib.sh"
trap 'stop_dev; stop_all' EXIT
dev_port=${ARUMA_E2E_DEV_PORT:-9426}
export NEXT_TELEMETRY_DISABLED=1

install_only=
[[ ${1:-} == --install ]] && install_only=1 && shift
targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(vite5 vite6 vite7 vite8 next expo flutter)
guarded=${ARUMA_EGRESS_GUARD:-}
if [[ -n $install_only && -n $guarded ]]; then
  echo "measure.sh --install under the egress guard: install before turning it on" >&2
  exit 2
fi

# not_installed <what>: under the guard nothing is installed, so a missing install ends the run.
not_installed() {
  echo "$1 is not installed: run measure.sh --install before the egress guard" >&2
  exit 1
}

if [[ -n $install_only ]]; then
  mkdir -p "$gen"
else
  start_aruma
fi
cd "$here"
if [[ ! -d node_modules ]]; then
  [[ -z $guarded ]] || not_installed "$here"
  npm ci --no-audit --no-fund --silent
fi
mkdir -p "$apps"

results=()
note() {
  results+=("$*")
  printf '  %s\n' "$*"
}

# playwright <apps> [roots]: the bundlers spec on those apps.
playwright() {
  if ARUMA_E2E_APPS=$1 ARUMA_E2E_ROOTS=${2:-} npx playwright test tests/bundlers.spec.mjs >"$gen/pw-$3.log" 2>&1; then
    note "ok      $3"
  else
    note "FAILED  $3 (see $gen/pw-$3.log)"
    grep -E '✗|⚠|Error|expect\(' "$gen/pw-$3.log" | head -20 | sed 's/^/          /'
  fi
}

# package <dir> <json> [fields]: writes dir/package.json from a JSON fragment of dependencies (and
# of other fields).
package() {
  node -e '
    const [dir, deps, sdk, fields] = process.argv.slice(1);
    const d = JSON.parse(deps);
    d["@aruma/web"] = "file:" + sdk;
    const p = { name: "aruma-measure", private: true, type: "module", ...JSON.parse(fields || "{}"), dependencies: d };
    require("fs").writeFileSync(dir + "/package.json", JSON.stringify(p, null, 2));
  ' "$1" "$2" "$sdk" "${3:-}"
}

# install_app <dir>: npm install, with @aruma/web copied in (not linked: Turbopack and Metro do not
# follow a link out of the project) fresh from sdk/web/aruma-web.
install_app() {
  if [[ -n $guarded ]]; then
    [[ -d $1/node_modules/@aruma/web ]] || not_installed "$1"
    return 0
  fi
  rm -rf "$1/node_modules/@aruma"
  (cd "$1" && npm install --no-audit --no-fund --silent --install-links --before "$npm_before")
}

# pkg_version <dir> <package>: the version of the package installed in dir.
pkg_version() { (cd "$1" && node -p "require('$2/package.json').version"); }

# expect_version <what> <got> <want> [more]: ok when the installed version is the measured one.
expect_version() {
  if [[ $2 == "$3" ]]; then note "ok      $1 $2${4:+, $4}"; else note "FAILED  $1 $2, not the measured $3"; fi
}

vite_app() {
  local major=$1 name=vite$1 dir=$apps/vite$1
  log "Vite $major: build and dev server"
  rm -rf "$dir/src" "$dir/dist"
  mkdir -p "$dir/src"
  package "$dir" "{\"firebase\": \"$firebase\", \"vite\": \"${vite_versions[major - 5]}\"}"
  cp index.html "$dir/"
  cp src/main.js src/ops.js src/worker.js "$dir/src/"
  cp apps/vite/vite.config.mjs "$dir/"
  install_app "$dir" && rm -rf "$dir/node_modules/.vite"
  [[ -z $install_only ]] || return 0
  expect_version "$name: vite" "$(cd "$dir" && node -p 'require("vite/package.json").version')" "${vite_versions[major - 5]}"
  if (cd "$dir" && ARUMA_E2E_NAME=$name npx vite build >"$gen/build-$name.log" 2>&1); then
    playwright "$name" "$name=$dir/dist" "$name-build"
  else
    note "FAILED  $name-build: vite build failed (see $gen/build-$name.log)"
  fi
  if port_free "$name-dev"; then
    (cd "$dir" && ARUMA_E2E_NAME=$name exec npx vite --port "$dev_port") >"$gen/dev-$name.log" 2>&1 &
    dev_pid=$!
    if started "http://127.0.0.1:$dev_port/$name/" "$name-dev" "$gen/dev-$name.log"; then
      playwright "$name@http://127.0.0.1:$dev_port/$name/" "" "$name-dev"
    fi
    stop_dev
  fi
}

# The servers measure.sh starts take $dev_port one after the other. A server measured must be the
# one just started, never one an earlier stop left behind: port_free before the start, started
# after it, and stop_dev ends the whole process tree and waits until the port is free.

# answers: whether anything answers HTTP on $dev_port.
answers() { curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$dev_port/"; }

# port_free <name>: true when nothing answers on $dev_port, else a FAILED line.
port_free() {
  answers || return 0
  note "FAILED  $1: something already answers on :$dev_port, so the server was not started"
  return 1
}

# started <url> <name> <log>: true when the server just started answers on url and is still
# running (a server that lost the port to another exits), else a FAILED line.
started() {
  if wait_http "$1" && kill -0 "$dev_pid" 2>/dev/null; then return 0; fi
  note "FAILED  $2: the server did not start (see $3)"
  return 1
}

# kill_tree <pid>: the process and everything under it, the deepest first (npx, npm exec, the
# framework's CLI and the server it forks).
kill_tree() {
  local child
  for child in $(pgrep -P "$1" 2>/dev/null); do kill_tree "$child"; done
  kill "$1" 2>/dev/null || true
}

# stop_dev stops the server and what it started, and waits until the port is free.
dev_pid=
stop_dev() {
  [[ -n "$dev_pid" ]] || return 0
  kill_tree "$dev_pid"
  wait "$dev_pid" 2>/dev/null || true
  dev_pid=
  local _
  for _ in $(seq 1 60); do
    answers || return 0
    sleep 0.25
  done
  note "FAILED  a stopped server still answers on :$dev_port"
}

# netguarded <log> <command…>: runs a Node server under test/experiments/netguard.cjs.
netguarded() {
  local logf=$1
  shift
  ARUMA_NETGUARD_LOG=$logf NODE_OPTIONS="--require $root/test/experiments/netguard.cjs" "$@"
}

google='googleapis\.com|gstatic\.com|google\.com|firebaseio\.com|firebasedatabase\.app|firebaseapp\.com'

# blocked <log> [pattern]: the distinct targets the netguard blocked (matching pattern), on a line.
blocked() { grep BLOCK "$1" 2>/dev/null | grep -E "${2:-.}" | cut -d' ' -f2 | sort -u | head -3 | tr '\n' ' ' || true; }

# guard_clean <log> <what>: ok when the server's netguard saw its connections to this machine and
# nothing to Google. The server's Pages Router page reads a link from Aruma, so a netguard that
# saw no connection was not loaded (or another server answered), and that fails too.
guard_clean() {
  local allowed
  allowed=$(grep -c ALLOW "$1" 2>/dev/null || true)
  if [[ -n $(blocked "$1" "$google") ]]; then
    note "FAILED  $2: the server tried to reach Google: $(blocked "$1" "$google")"
  elif [[ ${allowed:-0} -eq 0 ]]; then
    note "FAILED  $2: its netguard saw no connection ($1): it was not loaded, or another server answered"
  elif [[ -n $(blocked "$1") ]]; then
    note "ok      $2: connected to $allowed local addresses, nothing to Google; blocked elsewhere: $(blocked "$1")"
  else
    note "ok      $2: the server connected to $allowed local addresses and nothing else"
  fi
}

next_app() {
  local dir=$apps/next
  log "Next.js: webpack and Turbopack"
  rm -rf "$dir/app" "$dir"/.next*
  mkdir -p "$dir/app"
  package "$dir" "{\"firebase\": \"$firebase\", \"next\": \"$next\", \"react\": \"$react\", \"react-dom\": \"$react\"}"
  cp apps/next/next.config.mjs "$dir/"
  cp apps/next/app/*.js "$dir/app/"
  rm -rf "$dir/pages"
  cp -R apps/next/pages "$dir/pages"
  cp src/ops.js "$dir/app/ops.js"
  install_app "$dir"
  [[ -z $install_only ]] || return 0
  expect_version "next:" "$(cd "$dir" && node -p 'require("next/package.json").version')" "$next"
  local b flag none chunks leak pages
  # turbopack-bare: the same build without the transpilePackages the line adds (the leak it closes).
  for b in webpack turbopack turbopack-bare; do
    flag=
    none=
    [[ $b == webpack ]] && flag=--webpack
    [[ $b == turbopack-bare ]] && none=1
    rm -f "$gen/netguard-next-$b.log"
    if ! (cd "$dir" && ARUMA_E2E_DIST=.next-$b ARUMA_E2E_NO_TRANSPILE=$none npx next build $flag >"$gen/build-next-$b.log" 2>&1); then
      note "FAILED  next-$b: next build failed (see $gen/build-next-$b.log)"
      grep -m5 -i "aruma\|error" "$gen/build-next-$b.log" | sed 's/^/          /'
      continue
    fi
    # What the build left: the host literal must be gone from every chunk (the bare build's leak
    # is the SDK the server loads from node_modules, not a chunk).
    if [[ $b != turbopack-bare ]]; then
      chunks=$( (grep -rl '"firebasestorage.googleapis.com"' "$dir/.next-$b" --include='*.js' 2>/dev/null || true) | wc -l | tr -d ' ')
      if [[ $chunks -eq 0 ]]; then note "ok      next-$b: no chunk names Google's host"; else note "FAILED  next-$b: $chunks chunks still name Google's host"; fi
    fi
    port_free "next-$b" || continue
    (cd "$dir" && ARUMA_E2E_DIST=.next-$b ARUMA_E2E_NAME=next-$b ARUMA_E2E_NO_TRANSPILE=$none netguarded "$gen/netguard-next-$b.log" \
      exec npx next start -p "$dev_port" -H 127.0.0.1) >"$gen/start-next-$b.log" 2>&1 &
    dev_pid=$!
    if ! started "http://127.0.0.1:$dev_port/" "next-$b" "$gen/start-next-$b.log"; then
      stop_dev
      continue
    fi
    if [[ $b == turbopack-bare ]]; then
      # The control: without transpilePackages the Pages Router's server loads the SDK unrewritten
      # and reaches for Google's host, which its netguard blocks (docs/compat/storage-v0.3.md).
      pages=$(pages_check "http://127.0.0.1:$dev_port/pages-ssr")
      stop_dev
      leak=$(blocked "$gen/netguard-next-$b.log" 'firebasestorage\.googleapis\.com')
      if [[ -n $leak && $pages != ok ]]; then
        note "ok      next-$b: without transpilePackages the Pages Router's server reached for $leak(blocked)"
      else
        note "FAILED  next-$b: the leak transpilePackages closes did not show (Pages Router: $pages; blocked: ${leak:-nothing})"
      fi
      continue
    fi
    playwright "next-$b@http://127.0.0.1:$dev_port/" "" "next-$b"
    pages_note "http://127.0.0.1:$dev_port/pages-ssr" "next-$b"
    stop_dev
    guard_clean "$gen/netguard-next-$b.log" "next-$b server"
  done
  # The dev server compiles with Turbopack too, page by page.
  rm -f "$gen/netguard-next-dev.log"
  port_free next-dev || return 0
  (cd "$dir" && ARUMA_E2E_DIST=.next-dev ARUMA_E2E_NAME=next-dev netguarded "$gen/netguard-next-dev.log" \
    exec npx next dev -p "$dev_port" -H 127.0.0.1) >"$gen/dev-next.log" 2>&1 &
  dev_pid=$!
  if started "http://127.0.0.1:$dev_port/" next-dev "$gen/dev-next.log"; then
    playwright "next-dev@http://127.0.0.1:$dev_port/" "" "next-dev"
    pages_note "http://127.0.0.1:$dev_port/pages-ssr" next-dev
    stop_dev
    guard_clean "$gen/netguard-next-dev.log" "next-dev server"
  else
    stop_dev
  fi
}

# pages_note <url> <name>: a note on the Pages Router page.
pages_note() {
  local r
  r=$(pages_check "$1")
  if [[ $r == ok ]]; then note "ok      $2 Pages Router (getServerSideProps)"; else note "FAILED  $2 Pages Router: $r"; fi
}

# pages_check <url>: "ok" when the page the server rendered names Aruma and links the unified URL,
# else "FAILED" and what it rendered.
pages_check() {
  local html
  html=$(curl -sS "$1" 2>/dev/null || true)
  if grep -q "<p id=\"ssr-host\">$ARUMA_URL</p>" <<<"$html" && grep -q "<img id=\"ssr\" src=\"$ARUMA_URL/v0/b/" <<<"$html"; then
    echo ok
  else
    echo "FAILED $(grep -o '<p id="ssr-host">[^<]*' <<<"$html" | head -1 | sed 's/.*>//') $(grep -o 'Internal Server Error\|Application error[^<]*' <<<"$html" | head -1)"
  fi
}

# literals <files…>: "<n> with Google's host literal, <m> with Aruma's" over the given files.
literals() {
  local google aruma
  google=$(grep -l -a -E "[\"']firebasestorage\.googleapis\.com[\"']" "$@" 2>/dev/null | wc -l | tr -d ' ')
  aruma=$(grep -l -a -E "[\"']${addr//./\\.}[\"']" "$@" 2>/dev/null | wc -l | tr -d ' ')
  echo "$google with Google's host literal, $aruma with Aruma's"
}

# rewritten <name> <files…>: ok when no file names Google's host and at least one names Aruma's.
rewritten() {
  local name=$1 counts
  shift
  counts=$(literals "$@")
  if [[ $counts =~ ^0\ with\ Google.*,\ ([0-9]+)\ with\ Aruma ]] && [[ ${BASH_REMATCH[1]} -ge 1 ]]; then
    note "ok      $name: $counts"
  else
    note "FAILED  $name: $counts (want 0 with Google's, at least 1 with Aruma's)"
  fi
}

expo_app() {
  local dir=$apps/expo
  log "Expo: Metro, the web export in the browser, the Android bundle, react-native-svg-transformer"
  rm -rf "$dir/dist" "$dir/dist-"* "$dir/.expo"
  mkdir -p "$dir"
  package "$dir" "{\"expo\": \"$expo\", \"firebase\": \"$firebase\", \"react-native-svg-transformer\": \"$svg_transformer\"}" '{"main": "index.js", "type": "commonjs"}'
  cp apps/expo/metro.config.js apps/expo/app.json apps/expo/index.js "$dir/"
  cp src/ops.js "$dir/ops.js"
  install_app "$dir"
  # React, React Native and the web packages at the versions this Expo bundles (what `expo install`
  # would pick, without asking Expo's servers).
  local peers
  if [[ -z $guarded ]]; then
    peers=$(cd "$dir" && node -p 'const b = require("expo/bundledNativeModules.json"); ["react", "react-dom", "react-native", "react-native-web", "@expo/metro-runtime", "react-native-svg"].map((k) => `${k}@${b[k]}`).join(" ")')
    # shellcheck disable=SC2086
    (cd "$dir" && npm install --no-audit --no-fund --silent --install-links --before "$npm_before" $peers)
  fi
  [[ -z $install_only ]] || return 0
  expect_version "expo:" "$(pkg_version "$dir" expo)" "$expo" "react-native $(pkg_version "$dir" react-native)"
  expect_version "expo: metro" "$(pkg_version "$dir" metro)" "$metro"
  expect_version "expo: react-native-svg-transformer" "$(pkg_version "$dir" react-native-svg-transformer)" "$svg_transformer"
  local svg platform out
  # web and android; the SVG transformer not at all, before @aruma/web (supported) and after it (refused).
  for svg in none before after; do
    for platform in web android; do
      out=dist-$platform-$svg
      if (cd "$dir" && ARUMA_E2E_SVG=$svg EXPO_OFFLINE=1 EXPO_NO_TELEMETRY=1 CI=1 npx expo export -p "$platform" --output-dir "$out" --no-bytecode --clear >"$gen/build-expo-$platform-$svg.log" 2>&1); then
        if [[ $svg == after ]]; then
          note "FAILED  expo-$platform-svg-after: the bundle was written without the rewrite: $(literals $(find "$dir/$out" -name '*.js'))"
        else
          # shellcheck disable=SC2046
          rewritten "expo-$platform-svg-$svg bundle" $(find "$dir/$out" -name '*.js')
          if [[ $platform == web ]]; then playwright expo "expo=$dir/$out" "expo-web-svg-$svg"; fi
        fi
      elif [[ $svg == after ]] && grep -q 'was bundled without the Storage rewrite' "$gen/build-expo-$platform-$svg.log"; then
        note "ok      expo-$platform-svg-after: refused: $(grep -o '@aruma/web/metro: [^,]*' "$gen/build-expo-$platform-$svg.log" | head -1 | cut -c1-110)"
      else
        note "FAILED  expo-$platform-svg-$svg: expo export failed (see $gen/build-expo-$platform-$svg.log)"
        grep -m5 -i "aruma\|error" "$gen/build-expo-$platform-$svg.log" | sed 's/^/          /'
      fi
    done
  done
}

flutter_app() {
  local dir=$apps/flutter
  log "Flutter web: aruma-web flutter-web, then flutter build web"
  local offline=--offline
  [[ -z $install_only ]] || offline=
  if [[ ! -f "$dir/web/index.html.orig" ]]; then
    [[ -z $guarded ]] || not_installed "$dir"
    rm -rf "$dir"
    flutter create $offline --platforms web --project-name aruma_e2e_web_storage "$dir" >"$gen/flutter-create.log" 2>&1
    cp "$dir/web/index.html" "$dir/web/index.html.orig"
  fi
  # The index.html flutter create wrote, before the tool patches it.
  cp "$dir/web/index.html.orig" "$dir/web/index.html"
  rm -rf "$dir/web/aruma" "$dir/test"
  cp apps/flutter/pubspec.yaml "$dir/"
  cp apps/flutter/lib/main.dart "$dir/lib/main.dart"
  cp apps/flutter/web/flutter_bootstrap.js "$dir/web/"
  # Under the guard the packages resolved by --install stay: pubspec.yaml is the same file.
  if [[ -z $guarded ]]; then
    (cd "$dir" && flutter pub get $offline >"$gen/flutter-pub.log" 2>&1)
  else
    [[ -f "$dir/.dart_tool/package_config.json" ]] || not_installed "$dir"
  fi
  [[ -z $install_only ]] || return 0
  # Flutter is the job's toolchain (ci.yml pins 3.44.2), not measure.sh's: shown, not checked.
  note "info    flutter $(flutter --version 2>/dev/null | head -1 | cut -d' ' -f2), firebase_core_web $(grep -o 'firebase_core_web-[0-9.]*' "$dir/.dart_tool/package_config.json" | head -1 | cut -d- -f2)"
  # The tool, twice: the second run must replace its own block, not add one.
  local out
  out=$(node "$sdk/src/cli.js" flutter-web --url "$ARUMA_URL" --project "$dir" --firebase "$here/node_modules/firebase")
  node "$sdk/src/cli.js" flutter-web --url "$ARUMA_URL" --project "$dir" --firebase "$here/node_modules/firebase" >/dev/null
  local version blocks
  version=$(sed -n 's/.*Firebase JS SDK \([0-9.]*\),.*/\1/p' <<<"$out")
  expect_version "flutter-web tool: Firebase JS SDK" "$version" "$firebase"
  blocks=$(grep -c '<!-- @aruma/web flutter-web' "$dir/web/index.html" || true)
  if [[ $blocks -eq 1 ]]; then
    note "ok      flutter-web tool: run twice, one block in index.html"
  else
    note "FAILED  flutter-web tool: run twice, $blocks blocks in index.html (want 1)"
  fi
  rewritten "flutter-web tool: firebase-storage-$version.js" "$dir/web/aruma/firebase-storage-$version.js"
  if ! (cd "$dir" && flutter build web --no-web-resources-cdn --base-href /flutter/ --no-pub >"$gen/build-flutter.log" 2>&1); then
    note "FAILED  flutter: flutter build web failed (see $gen/build-flutter.log)"
    return
  fi
  # The spec fails when firebase-storage.js comes from www.gstatic.com; it lists what did (the
  # SDK files firebase_core_web imports, answered from npm).
  ARUMA_E2E_MIRROR=$version ARUMA_E2E_SIGNED_OUT=1 playwright flutter "flutter=$dir/build/web" flutter
  note "info    flutter: $(grep -m1 'from www.gstatic.com' "$gen/pw-flutter.log" | sed 's/^ *//')"
}

for t in "${targets[@]}"; do
  case $t in
    expo) expo_app ;;
    flutter) flutter_app ;;
    vite5 | vite6 | vite7 | vite8) vite_app "${t#vite}" ;;
    next) next_app ;;
    *) echo "unknown target $t" >&2; exit 2 ;;
  esac
done

if [[ -n $install_only ]]; then
  log "installed ${targets[*]} in $apps"
  exit 0
fi
log "summary"
printf '  %s\n' "${results[@]}"
if printf '%s\n' "${results[@]}" | grep -q '^FAILED'; then
  exit 1
fi
