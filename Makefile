# Homehub. `make` builds bin/homehubd with the companion inside it.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web test test-docker check clean

all: build

# The companion (web/) is built to static files and embedded into the Go binary.
web:
	cd web && npm ci && npm run build

build: web
	go build -ldflags "$(LDFLAGS)" -o bin/homehubd ./cmd/homehubd

# Fast checks a contributor runs before pushing; CI runs the same.
check:
	test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: gofmt -w ."; exit 1; }
	go vet ./...
	go test ./...

test: check

# The Docker adapter's end-to-end test needs a running Docker.
test-docker:
	HOMEHUB_DOCKER_TESTS=1 go test ./internal/adapters/docker/ -run Integration -v

clean:
	rm -rf bin web/build
