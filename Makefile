# Homelab. `make` builds bin/homelabd with the companion inside it, and bin/homelab-services.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web services test test-docker check check-services check-web check-handover clean

all: build

# The companion (web/) is built to static files and embedded into the Go binary.
web:
	cd web && npm ci && npm run build

build: web services
	go build -ldflags "$(LDFLAGS)" -o bin/homelabd ./cmd/homelabd

# homelab-services (Rust): files, databases and data. homelabd starts it from next to itself.
services:
	cd services && HOMELAB_VERSION=$(VERSION) cargo build --release --locked
	mkdir -p bin && cp services/target/release/homelab-services bin/homelab-services

# Fast checks a contributor runs before pushing; CI runs the same.
check:
	test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: gofmt -w ."; exit 1; }
	go vet ./...
	go test ./...

# The Rust side's checks; CI runs the same.
check-services:
	cd services && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test

# The companion's checks (web/). Not part of `make test`, which is Go and Rust; CI runs the same.
check-web:
	cd web && npm ci && npm run check && npm test

# The handover notes script (scripts/handover.py) and its tests. Python 3, no packages.
check-handover:
	python3 scripts/test_handover.py

test: check check-services check-web check-handover

# The Docker adapter's end-to-end test needs a running Docker.
test-docker:
	HOMELAB_DOCKER_TESTS=1 go test ./internal/adapters/docker/ -run Integration -v

clean:
	rm -rf bin web/build services/target
