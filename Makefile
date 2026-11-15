.PHONY: test test-v bench run up down logs e2e demo

test:            ## vet + all tests with the race detector
	go vet ./...
	go test -race ./...

test-v:          ## verbose tests (shows latency measurements)
	go test -race -v ./...

bench:           ## allocator micro-benchmark
	go test -run='^$$' -bench=. -benchmem ./internal/allocator

run:             ## run locally without Docker
	go run ./cmd/loadmanager

up:              ## build and start the container in the background
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f

BASE_URL ?= http://localhost:8080

e2e:             ## end-to-end scenarios against a running service (bash + curl only)
	BASE_URL=$(BASE_URL) ./scenarios/run-all.sh

demo: up e2e
