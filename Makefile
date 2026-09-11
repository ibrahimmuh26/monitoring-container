.PHONY: build test lint fmt check-config smoke-test

build:
	go build -trimpath -o bin/monitoring-container ./cmd/monitoring-container

test:
	go test -race -cover ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)

fmt:
	gofmt -w cmd internal

check-config:
	go run ./cmd/monitoring-container --config configs/pilot.example.yaml --check-config
	go run ./cmd/monitoring-container --config configs/incidents.example.yaml --check-config

# Build monitoring-container:local first. Creates disposable local containers.
smoke-test:
	bash scripts/smoke-test.sh
