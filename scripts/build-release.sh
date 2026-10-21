#!/bin/sh
# Builds homelabd for every computer Homelab runs on into dist/, with checksums, and
# homelab-services (Rust) for Linux. Used by the release and test-build workflows; the
# companion must already be built. homelab-services has C inside (SQLite), so it builds
# with zig as the C compiler and linker. Needs:
#   rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl
#   pip install ziglang cargo-zigbuild
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
for arch in amd64:x86_64 arm64:aarch64; do
	rust=${arch#*:}-unknown-linux-musl; arch=${arch%:*}
	(cd services && HOMELAB_VERSION=$version cargo zigbuild --release --locked --target "$rust")
	cp "services/target/$rust/release/homelab-services" "dist/homelab-services-linux-$arch"
done
dist/homelabd-linux-amd64 version | grep -q "$version"
dist/homelab-services-linux-amd64 version | grep -q "$version"
(cd dist && sha256sum homelabd-* homelab-services-* > checksums.txt)
