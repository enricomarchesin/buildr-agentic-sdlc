BINARY  := heatctl
VERSION := 1.0.0
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/enrico/heatctl/internal/buildinfo.Version=$(VERSION) \
           -X github.com/enrico/heatctl/internal/buildinfo.Commit=$(COMMIT) \
           -X github.com/enrico/heatctl/internal/buildinfo.BuildDate=$(DATE)

.PHONY: build build-arm64 test vet check install clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/heatctl

# The binary for the Raspberry Pi (C1).
build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/heatctl

test:
	go test ./...

vet:
	go vet ./...

check: vet test

# Installs the arm64 binary; run it on the Pi or with DESTDIR set.
install: build-arm64
	install -m 0755 $(BINARY)-linux-arm64 /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY) $(BINARY)-linux-arm64
