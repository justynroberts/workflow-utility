#!/bin/sh
# Install pdwf from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/justynroberts/workflow-utility/main/install.sh | sh
#
# Options (environment variables):
#   PREFIX=/usr/local/bin   where to put pdwf (default: ~/.local/bin)
#   VERSION=v0.1.0          a specific release (default: latest)
#
# It downloads the binary for this OS/CPU, checks it against the release's
# checksums.txt, and refuses to install if they do not match.
set -eu

REPO="justynroberts/workflow-utility"
PREFIX="${PREFIX:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"

fail() { echo "pdwf install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) fail "unsupported OS $(uname -s). On Windows, download pdwf-windows-amd64.exe from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *) fail "unsupported CPU $(uname -m)" ;;
esac
asset="pdwf-$os-$arch"

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

command -v curl >/dev/null 2>&1 || fail "curl is needed"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $asset ($VERSION)..."
curl -fsSL "$base/$asset" -o "$tmp/pdwf" || fail "download failed: $base/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "could not download checksums.txt"

want="$(grep " $asset\$" "$tmp/checksums.txt" | awk '{print $1}')"
[ -n "$want" ] || fail "no checksum for $asset in checksums.txt"
if command -v shasum >/dev/null 2>&1; then
  got="$(shasum -a 256 "$tmp/pdwf" | awk '{print $1}')"
else
  got="$(sha256sum "$tmp/pdwf" | awk '{print $1}')"
fi
[ "$want" = "$got" ] || fail "checksum mismatch for $asset (expected $want, got $got). Not installed."

mkdir -p "$PREFIX"
chmod 0755 "$tmp/pdwf"
mv "$tmp/pdwf" "$PREFIX/pdwf"
echo "Installed $("$PREFIX/pdwf" version | tail -1) to $PREFIX/pdwf"

case ":$PATH:" in
  *":$PREFIX:"*) echo "Run: pdwf help" ;;
  *) case "${SHELL:-}" in
       */zsh)  rc="$HOME/.zshrc" ;;
       */bash) rc="$HOME/.bashrc" ;;
       *)      rc="$HOME/.profile" ;;
     esac
     echo
     echo "$PREFIX is not on your PATH. Add it with:"
     echo "  echo 'export PATH=\"$PREFIX:\$PATH\"' >> $rc"
     echo "then open a new terminal, or run it directly now: $PREFIX/pdwf help" ;;
esac
