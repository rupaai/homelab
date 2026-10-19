#!/bin/sh
# Builds homelabd for every computer Homelab runs on into dist/, with checksums.
# Used by the release and test-build workflows; the companion must already be built.
# Usage: scripts/build-release.sh <version>
set -eu
version=${1:?usage: scripts/build-release.sh <version>}
rm -rf dist
mkdir dist
for target in linux/amd64 linux/arm64 darwin/arm64; do
	os=${target%/*}; arch=${target#*/}
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "dist/homelabd-$os-$arch" ./cmd/homelabd
done
dist/homelabd-linux-amd64 version | grep -q "$version"
(cd dist && sha256sum homelabd-* > checksums.txt)
