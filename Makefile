.PHONY: test build run-cloud run-agent

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/cloudnvr-cloud ./cmd/cloud
	go build -o bin/cloudnvr-agent ./cmd/agent

run-cloud:
	go run ./cmd/cloud

run-agent:
	go run ./cmd/agent
