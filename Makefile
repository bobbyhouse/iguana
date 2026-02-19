.PHONY: build test generate

build:
	GOEXPERIMENT= go build -o bin/iguana .

test:
	GOEXPERIMENT= go test ./...

generate:
	baml-cli generate
	GOEXPERIMENT= gofmt -w .
	GOEXPERIMENT= goimports -w .
