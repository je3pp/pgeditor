#!/bin/sh
#
# Cross-compile pgeditor for Linux, FreeBSD and Windows.
# The version comes from the VERSION file (also embedded into the binary).
#
# Usage:
#   ./build.sh
#   TARGETS="linux/amd64 windows/amd64" ./build.sh
#
# Output: dist/pgeditor-<version>-<os>-<arch>.tar.gz (.zip for Windows)
#         dist/SHA256SUMS

set -eu

APP="pgeditor"
OUT="dist"
TARGETS="${TARGETS:-linux/amd64 freebsd/amd64 windows/amd64}"

die() { echo "error: $*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "go not found in PATH"
[ -f go.mod ] || die "run this script from the project root (go.mod not found)"
[ -f VERSION ] || die "VERSION file not found"

VERSION="$(tr -d ' \t\r\n' < VERSION)"
[ -n "$VERSION" ] || die "VERSION file is empty"

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	elif command -v sha256 >/dev/null 2>&1; then
		sha256 -r "$@"
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$@"
	else
		return 1
	fi
}

echo "==> $APP version $VERSION ($(go version))"

go mod tidy
go vet ./...

rm -rf "$OUT"
mkdir -p "$OUT"

SUPPORTED="$(go tool dist list)"

for target in $TARGETS; do
	os="${target%/*}"
	arch="${target#*/}"

	if ! echo "$SUPPORTED" | grep -qx "$target"; then
		die "unsupported target: $target (see: go tool dist list)"
	fi

	name="${APP}-${VERSION}-${os}-${arch}"
	bin="$APP"
	[ "$os" = "windows" ] && bin="$APP.exe"
	stage="$OUT/$name"

	echo "==> building $target"
	mkdir -p "$stage"

	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -trimpath -ldflags "-s -w" -o "$stage/$bin" .

	for f in README.md LICENSE VERSION; do
		[ -f "$f" ] && cp "$f" "$stage/"
	done

	if [ "$os" = "windows" ] && command -v zip >/dev/null 2>&1; then
		(cd "$OUT" && zip -qr "$name.zip" "$name")
	else
		[ "$os" = "windows" ] && echo "    note: zip not found, using tar.gz for windows"
		tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
	fi

	rm -rf "$stage"
done

echo "==> checksums"
(
	cd "$OUT"
	sha256_file ./*.tar.gz ./*.zip 2>/dev/null > SHA256SUMS || \
		sha256_file ./*.tar.gz > SHA256SUMS || \
		echo "    note: no sha256 tool found, skipping checksums"
)

echo "==> done"
ls -lh "$OUT"
