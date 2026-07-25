.PHONY: all build test test-race lint vet e2e tidy clean

BIN := bin/c2quay

all: build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/c2quay

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

e2e:
	go test -tags=e2e -count=1 ./test/e2e/...

tidy:
	go mod tidy

clean:
	rm -rf bin
