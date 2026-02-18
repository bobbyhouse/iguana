.PHONY: build test generate

build:
	go build -o bin/igu .

test:
	go test ./...

generate:
	baml-cli generate
