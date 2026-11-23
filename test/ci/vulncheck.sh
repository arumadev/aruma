#!/usr/bin/env bash
# Known vulnerabilities in the Go code we ship and the Go libraries we publish (govulncheck).
#
# govulncheck's source mode cannot load a module whose go directive is older than the toolchain's
# standard library uses (go 1.26.0 here, built with the go1.27 toolchain of go.mod): it reports
# "requires go1.27 or later" in GOROOT and stops. So each module is scanned as a binary, which
# is also what runs: aruma and arumactl for the root module, and the test binary of each nested
# module (the library and the code its tests call), built with this checkout's toolchain. A
# vulnerable symbol that the linker keeps fails the run; a module that is required but not
# linked (golang.org/x/crypto/openpgp, for one) does not.
#
#   test/ci/vulncheck.sh            from anywhere in the checkout
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
version=v1.10.0
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
toolchain=$(go env GOVERSION)
GOBIN=$bin GOTOOLCHAIN=$toolchain go install "golang.org/x/vuln/cmd/govulncheck@$version"
scan() { echo "== $1"; "$bin/govulncheck" -mode=binary "$2"; }

go build -o "$bin/aruma" ./cmd/aruma
go build -o "$bin/arumactl" ./cmd/arumactl
status=0
scan aruma "$bin/aruma" || status=1
scan arumactl "$bin/arumactl" || status=1

# The nested modules, with the root's toolchain (they declare an older minimum).
for mod in sdk/go/adminauth sdk/go/adminstorage test/smoke/admin-go; do
  for pkg in $(cd "$mod" && GOTOOLCHAIN=$toolchain go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...); do
    out="$bin/$(echo "$pkg" | tr '/' '_').test"
    (cd "$mod" && GOTOOLCHAIN=$toolchain go test -c -o "$out" "$pkg")
    scan "$pkg" "$out" || status=1
  done
done
exit $status
