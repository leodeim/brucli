.PHONY: all build test vet fmt run

all: fmt vet test build

build:
	go build -o brucli .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run: build
	./brucli
