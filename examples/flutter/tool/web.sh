#!/usr/bin/env bash
# The web build step: points the Firebase JS SDK that firebase_core_web loads at the Aruma server of
# aruma.json, or takes every Aruma line out again when "useAruma" is false. Run it once after
# cloning, after changing aruma.json and after upgrading firebase_core; then `flutter run -d chrome`
# or `flutter build web --no-web-resources-cdn` as usual.
#
#   tool/web.sh
#
# With "useAruma": true it
#   1. installs @aruma/web from this repository and the firebase npm package of the version
#      firebase_core_web loads (package.json; nothing is sent to Google, the files come from npm);
#   2. runs `npx aruma-web flutter-web --url <url> --products storage,firestore`, the documented
#      step (docs/guides/storage.md, docs/guides/firestore.md): it writes web/aruma/ with
#      firebase-storage.js and firebase-firestore.js rewritten for Aruma, and puts its block in
#      web/index.html in place of the flutter_bootstrap.js script;
#   3. adds a second block before it, for Auth: @aruma/web's install() (docs/guides/auth-web.md),
#      copied from the package to web/aruma/auth/, which sends Firebase Auth's requests to Aruma.
#      The flutter-web step covers Storage and Firestore only.
# With "useAruma": false it removes both blocks and web/aruma/, so the page is the plain FlutterFire
# one again.
#
# web/aruma/ is generated (it names your server): it is in .gitignore. web/index.html is changed in
# place, as the flutter-web step documents.
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
cd "$here"

read -r use url < <(node -e '
  const c = JSON.parse(require("fs").readFileSync("aruma.json", "utf8"));
  process.stdout.write(`${c.useAruma === true} ${c.useAruma === true ? String(c.arumaUrl ?? "").trim() : "-"}\n`);
')

# The Auth block of this script, which it replaces on every run.
auth_begin='<!-- tool/web.sh: Firebase Auth on Aruma (@aruma/web install). Generated; run tool/web.sh again to change it. -->'
auth_end='<!-- /tool/web.sh -->'

# edit_index <node program>: runs the program on web/index.html's text (as `html`) and writes back
# what it returns.
edit_index() {
  AUTH_BEGIN=$auth_begin AUTH_END=$auth_end ARUMA_URL=$url node -e '
    const fs = require("fs");
    const path = "web/index.html";
    const html = fs.readFileSync(path, "utf8");
    const { AUTH_BEGIN: begin, AUTH_END: end, ARUMA_URL: url } = process.env;
    const withoutAuth = (h) => {
      const b = h.indexOf(begin);
      if (b === -1) return h;
      const e = h.indexOf(end, b);
      if (e === -1) throw new Error("web/index.html has the start of the Auth block but not its end");
      return h.slice(0, b) + h.slice(e + end.length).replace(/^\n\s*/, "");
    };
    fs.writeFileSync(path, ('"$1"')(html));
  '
}

if [[ $use != true ]]; then
  echo "aruma.json: useAruma is false, so the web build is the plain FlutterFire one"
  rm -rf web/aruma
  edit_index '(html) => {
    html = withoutAuth(html);
    const b = html.indexOf("<!-- @aruma/web flutter-web");
    if (b === -1) return html;
    const close = "<!-- /@aruma/web flutter-web -->";
    const e = html.indexOf(close, b);
    return html.slice(0, b) + "<script src=\"flutter_bootstrap.js\" async></script>" + html.slice(e + close.length);
  }'
  exit 0
fi

[[ -n $url ]] || { echo "aruma.json: arumaUrl is missing" >&2; exit 1; }
echo "aruma.json: the web build goes to $url"

# firebase_core_web's pinned version must be in .dart_tool/package_config.json. (The e2e runs this
# with TOOL_WEB_NO_INSTALL=1 when everything was installed before the network was turned off.)
if [[ -z ${TOOL_WEB_NO_INSTALL:-} ]]; then
  flutter pub get >/dev/null
  npm install --no-audit --no-fund --silent
fi

# http to a host that is not this machine is for development only, and has to be asked for.
insecure=()
if [[ $url == http://* ]] && ! [[ $url =~ ^http://(localhost|127\.[0-9.]+|\[::1\])(:[0-9]+)?/?$ ]]; then
  insecure=(--allow-insecure)
  echo "warning: $url is http to another machine: development only" >&2
fi

npx aruma-web flutter-web --url "$url" --products storage,firestore "${insecure[@]}"

mkdir -p web/aruma/auth
cp node_modules/@aruma/web/src/index.js node_modules/@aruma/web/src/nodeguard.js web/aruma/auth/
cp node_modules/@aruma/web/LICENSE web/aruma/auth/LICENSE

allow=false
[[ ${#insecure[@]} -eq 0 ]] || allow=true
ALLOW=$allow edit_index '(html) => {
  html = withoutAuth(html);
  const at = html.indexOf("<!-- @aruma/web flutter-web");
  if (at === -1) throw new Error("web/index.html has no @aruma/web flutter-web block");
  // A module script runs before the module scripts after it, and the flutter-web block loads
  // flutter_bootstrap.js only after its imports: Dart never runs before Auth is redirected.
  const block = `${begin}\n<script type="module">\n  import { install } from "./aruma/auth/index.js";\n` +
    `  install({ url: ${JSON.stringify(url)}, allowInsecure: ${process.env.ALLOW} });\n</script>\n${end}\n  `;
  return html.slice(0, at) + block + html.slice(at);
}'
echo "  wrote web/aruma/auth/ (@aruma/web install, for Auth)"
echo "  patched web/index.html"
