.PHONY: all build test test-race vet lint tidy clean

all: vet test build

build:
	go build ./...

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -f vibebot.db
	go clean ./...
