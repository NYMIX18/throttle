#!/usr/bin/env bash
# Starts three demo backends plus Throttle. Press Ctrl+C to stop everything.
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p bin
go build -o bin/ ./cmd/...

pids=()
cleanup() { kill "${pids[@]}" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

for port in 9001 9002 9003; do
  ./bin/demo-backend -port "$port" -name "backend-$port" -latency 40ms &
  pids+=($!)
done

./bin/throttle -config configs/throttle.json &
pids+=($!)

echo
echo "  Dashboard: http://localhost:9090"
echo "  Try:       curl -i -H 'X-API-Key: me' http://localhost:8080/"
echo "  Traffic:   go run ./cmd/loadgen   (in another terminal)"
echo

wait
