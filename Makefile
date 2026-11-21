.PHONY: build test race bench fmt vet check run demo load docker clean

build:
	go build -o bin/ ./cmd/...

test:
	go test ./...

race:
	go test -race -count=1 ./...

bench:
	go test -run '^$$' -bench . -benchmem -benchtime=2s ./internal/limiter

fmt:
	gofmt -w .

vet:
	go vet ./...

# Everything CI runs. Run this before you push.
check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
	go vet ./...
	go test -race -count=1 -cover ./...

# Start 3 demo backends and Throttle together. Dashboard: http://localhost:9090
demo:
	./scripts/demo.sh

# Generate traffic against a running demo for 20 seconds.
load:
	go run ./cmd/loadgen -d 20s

docker:
	docker compose up --build

clean:
	rm -rf bin
