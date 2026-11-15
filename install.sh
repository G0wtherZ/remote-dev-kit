#!/usr/bin/env bash
# Download a pinned macOS release, verify it, and start the interactive setup.
set -euo pipefail

version='0.1.0-alpha.1'
base="https://github.com/G0wtherZ/remote-dev-kit/releases/download/v${version}"
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'Run this installer on your local Mac. Windows: use install.ps1; the wizard sets up the remote Linux/macOS host.' >&2
  exit 1
fi
case "$(uname -m)" in
  arm64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *) echo 'Unsupported Mac architecture.' >&2; exit 1 ;;
esac
for tool in curl shasum tar; do
  command -v "$tool" >/dev/null || { echo "Missing command: $tool" >&2; exit 1; }
done
tmp=$(mktemp -d "${TMPDIR:-/tmp}/rdev-download.XXXXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
archive="remote-dev-kit-${version}-darwin-${arch}.tar.gz"
echo "Downloading Remote Dev Kit ${version} (darwin-${arch})..."
curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' \
  "$base/$archive" -o "$tmp/$archive"
curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' \
  "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
expected=$(awk -v name="$archive" '$2 == name {print $1}' "$tmp/SHA256SUMS")
if [[ ! "$expected" =~ ^[a-f0-9]{64}$ ]]; then
  echo 'Missing or invalid release checksum; installation stopped.' >&2
  exit 1
fi
actual=$(shasum -a 256 "$tmp/$archive")
if [[ "${actual%% *}" != "$expected" ]]; then
  echo 'Release checksum mismatch; installation stopped.' >&2
  exit 1
fi
tar -xzf "$tmp/$archive" -C "$tmp"
release="$tmp/remote-dev-kit-${version}-darwin-${arch}"
/bin/sh "$release/setup.sh" "$@"
