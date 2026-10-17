#!/usr/bin/env bash
# Cross-compiles release archives into dist/: strife for Linux, macOS and
# Windows (amd64, arm64) and strife-agent for Linux, plus SHA256SUMS and an
# SPDX SBOM. Usage: scripts/release/build.sh v0.2.0
set -euo pipefail
version=${1:?usage: build.sh VERSION}
ldflags="-s -w -X github.com/kemalhafizh/strife/internal/version.Version=${version}"
rm -rf dist
mkdir -p dist/stage
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="strife_${version}_${os}_${arch}"
  stage="dist/stage/$name"
  mkdir -p "$stage"
  ext=""
  [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$stage/strife$ext" ./cmd/strife
  if [ "$os" = linux ]; then
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$stage/strife-agent" ./cmd/strife-agent
  fi
  cp LICENSE README.md "$stage/"
  if [ "$os" = windows ]; then
    (cd dist/stage && zip -qr "../$name.zip" "$name")
  else
    tar -C dist/stage -czf "dist/$name.tar.gz" "$name"
  fi
done
scripts/release/sbom.py "dist/stage/strife_${version}_linux_amd64/strife" "$version" >"dist/strife_${version}.spdx.json"
rm -rf dist/stage
(cd dist && sha256sum ./*.tar.gz ./*.zip ./*.spdx.json | sed 's# \./# #' >SHA256SUMS)
cat dist/SHA256SUMS
