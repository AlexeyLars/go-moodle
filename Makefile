.PHONY: build test vet fmt setup

build:
	go build -trimpath -ldflags="-s -w" -o bin/go-moodle ./cmd/go-moodle

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

setup: build
	./bin/go-moodle setup --env-file .env
