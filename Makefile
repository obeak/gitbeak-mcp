.PHONY: build run test clean docker-build

BINARY := gitbeak-mcp
MAIN := ./cmd/mcp/

build:
	CGO_ENABLED=0 go build -o $(BINARY) $(MAIN)

run:
	go run $(MAIN)

test:
	go test ./...

clean:
	rm -f $(BINARY)

docker-build:
	docker build -t ghcr.io/obeak/gitbeak-mcp:local .
