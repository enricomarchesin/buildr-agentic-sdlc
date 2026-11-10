VERSION ?= 1.0.0
test:
	go test ./...
build:
	go build -ldflags "-X main.version=$(VERSION)" -o heatctl .
arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/heatctl-linux-arm64 .
