VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build pi test vet clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o heatctl ./cmd/heatctl

# Raspberry Pi (64-bit Raspberry Pi OS)
pi:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/heatctl-linux-arm64 ./cmd/heatctl

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -rf heatctl dist
