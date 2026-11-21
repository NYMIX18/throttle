# Review guide

How to study this project over a weekend so that you can explain every line of it.

## Reading order (about 3 hours)

Read in this order. Each file builds on the previous one.

1. `internal/limiter/limiter.go`: the `Limiter` interface and `Decision`. Everything else exists to implement or call this.
2. `internal/limiter/token_bucket.go`: the core algorithm. Work through `Allow` by hand with `rate=1, burst=3`.
3. `internal/limiter/store.go`: why the map is sharded, what `update` guarantees, and what the janitor does.
4. `internal/limiter/sliding_window.go`: the weighted estimate. Re-derive the numbers in `TestSlidingWindowWeightsPreviousWindow`.
5. `internal/limiter/limiter_test.go`: read the fake clock first; every test depends on it.
6. `internal/gateway/gateway.go`: the request pipeline and the key functions.
7. `internal/proxy/pool.go`: round robin, passive and active health checks, `Rewrite`.
8. `internal/metrics/metrics.go`: atomics vs mutex, the ring buffer, the histogram.
9. `internal/admin/admin.go` and `web/index.html`: how the dashboard gets its data.
10. `cmd/throttle/main.go`: wiring, signals, graceful shutdown.

## Run it before you read it

```bash
make check          # must be green before anything else
make demo           # then open http://localhost:9090
make load           # in another terminal
```

Then watch the dashboard while you change `configs/throttle.json` (restart between edits):

- Set `"algorithm": "sliding_window"` and compare the shape of the chart.
- Set `"requests": 1000` and see the blocked bars disappear.
- Kill one backend process and watch it leave rotation, then restart it and watch it return.

## Break it on purpose

You learn the most by predicting what fails, then checking.

| Experiment | Predict, then verify |
|---|---|
| Replace the 64 shards with 1 (`numShards = 1`) and run `make bench` | Which benchmark gets slower, and by how much at `-cpu 8`? |
| Remove the `if b.tokens > burst` cap in `TokenBucket.Allow` | Which test fails? What would happen in production after a client is idle for an hour? |
| Set the token bucket `idleTTL` to 1 second | Which behaviour changes? Write a test that proves it. |
| Remove the mutex in `store.update`, run `go test -race` | What does the race detector report? |
| Set `trust_forwarded: true` and send `X-Forwarded-For: 1.2.3.4` with random values | Why is this a bypass when no trusted proxy sits in front? |
| Replace `Rewrite` with a plain `Director` | What does the upstream see in `X-Forwarded-For`? |

## Questions you should be able to answer

**1. Why a token bucket and not a sliding window as the default?**
Real clients are bursty (a page load fires several requests at once). A token bucket permits that burst but still enforces the average, and it is exact and tiny: two numbers per key. A sliding window counter is smoother and better for strict quotas, which is why both exist behind one interface.

**2. How does the token bucket refill without a timer?**
Lazily. On every `Allow` I compute `elapsed * rate`, add that many tokens (capped at `burst`), and update the timestamp. A key that nobody calls costs no CPU.

**3. Why shard the map instead of using one mutex or `sync.Map`?**
One mutex serializes every request. `sync.Map` is optimized for keys written once and read many times; here every call is a read-modify-write. Sharding gives independent locks, so different clients rarely contend. The benchmark against a naive single mutex quantifies it.

**4. What is the worst case for your design?**
Every request using the same key: they all hit one shard. That is the `SingleHotKey` benchmark. It is still correct (it must be, since one client's budget is one piece of state), just not parallel.

**5. How do you stop memory growing without bound?**
A janitor evicts idle keys. For the token bucket the idle threshold is at least the time to refill from empty, because evicting earlier would reset a rate-limited client to a full bucket, which is a bug an attacker could exploit. The metrics client table is also capped, since anyone can invent unlimited keys.

**6. What happens if you run two instances?**
Each keeps its own state, so a client gets roughly double the limit. Fixing that needs shared state, typically Redis with an atomic Lua script, so the read-modify-write is not split across round trips. It is on the roadmap and the `Limiter` interface is where it plugs in.

**7. Why `Rewrite` instead of `Director`?**
`Rewrite` removes inbound `Forwarded` and `X-Forwarded-*` headers before I add mine, so a client cannot forge its address to the upstream. With `Director` those headers are passed through and appended to.

**8. How is a request counted if the upstream fails?**
It passed the limiter, so it counts as allowed, and a 502/503/504 also increments the upstream error counter. The failed backend is marked unhealthy immediately (passive check) and revived by the next active probe.

**9. Why are there two listeners?**
So admin and metrics routes are structurally unreachable through proxied traffic, and so the control plane can bind to localhost while the data plane is public.

**10. How does graceful shutdown work?**
`signal.NotifyContext` cancels a context on SIGINT/SIGTERM. Health checking stops, then `http.Server.Shutdown` stops accepting new connections and waits (up to `shutdown_timeout`) for in-flight requests to finish.

**11. How do you test time-dependent code without sleeping?**
The store takes a `now func() time.Time`. Production passes `time.Now`; tests pass a fake clock they advance explicitly. Tests are instant and deterministic.

**12. What would you change first with more time?**
Per-route limits and a Redis-backed limiter, in that order: per-route is a small change with a big usability gain, and Redis addresses the main scaling limitation.

## Honest caveats to volunteer in an interview

- It is a single-node, in-memory limiter. Say so before they ask.
- Sliding-window `Retry-After` is an estimate.
- You can point at the Known limitations section in the README to show you have thought about where it stops being correct.

## Make it yours

Interviewers can tell the difference between code you read and code you understand. Before you present it:

- Change something non-trivial: add a third algorithm (fixed window or leaky bucket), or add per-route limits.
- Write one new test of your own.
- Record your own benchmark numbers in the README table.
- Add a screenshot of the dashboard under load.
