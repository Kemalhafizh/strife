#!/bin/sh
# Installs Strife for this machine (Linux or macOS, amd64 or arm64) from the
# latest release, after checking the archive against the release's
# SHA256SUMS.
#
#   curl -fsSL https://<the Strife website>/install.sh | sh
#
# Environment:
#   STRIFE_VERSION      a release tag such as v0.2.0 (default: the latest)
#   STRIFE_INSTALL_DIR  where to install (default: $HOME/.local/bin)
#   STRIFE_RELEASES     base URL of the releases (default: the GitHub project)
set -eu

releases=${STRIFE_RELEASES:-https://github.com/Kemalhafizh/strife/releases}
dir=${STRIFE_INSTALL_DIR:-$HOME/.local/bin}

say() { printf 'strife: %s\n' "$*"; }
die() {
  printf 'strife: %s\n' "$*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported system $(uname -s); on Windows use install.ps1" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

version=${STRIFE_VERSION:-}
if [ -z "$version" ]; then
  # The latest release page redirects to .../tag/<version>; without any
  # release it lands on the release list instead.
  latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest") ||
    die "cannot reach $releases"
  case $latest in
  */tag/*) version=${latest##*/} ;;
  *) die "no release has been published yet; build from source with: go install github.com/kemalhafizh/strife/cmd/strife@latest" ;;
  esac
fi

name="strife_${version}_${os}_${arch}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $name.tar.gz"
curl -fsSL -o "$tmp/$name.tar.gz" "$releases/download/$version/$name.tar.gz" ||
  die "download failed: $releases/download/$version/$name.tar.gz"
curl -fsSL -o "$tmp/SHA256SUMS" "$releases/download/$version/SHA256SUMS" ||
  die "download failed: SHA256SUMS"

want=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "$name.tar.gz is not listed in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$name.tar.gz" | cut -d ' ' -f 1)
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d ' ' -f 1)
else
  die "sha256sum or shasum is needed to check the download"
fi
[ "$want" = "$got" ] || die "checksum mismatch for $name.tar.gz (expected $want, got $got); nothing was installed"
say "checksum ok"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$dir"
for bin in strife strife-agent; do
  if [ -f "$tmp/$name/$bin" ]; then
    cp "$tmp/$name/$bin" "$dir/$bin.new"
    chmod 755 "$dir/$bin.new"
    mv -f "$dir/$bin.new" "$dir/$bin"
  fi
done
say "installed $("$dir/strife" version) in $dir"

case ":$PATH:" in
*":$dir:"*) ;;
*) say "add it to your PATH: export PATH=\"$dir:\$PATH\"" ;;
esac
say "next: strife sim --system raft-unsafe-fsync --faults kill,io-stall --seed 13"
