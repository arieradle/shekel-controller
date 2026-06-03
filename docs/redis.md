# Redis Enforcement Backend

## Overview

The Redis backend provides hard atomic enforcement of budget limits across multiple pods. Without Redis, each pod tracks spend independently in-process, and the controller aggregates asynchronously — meaning a brief window exists where multiple pods could collectively overspend before the controller's kill-switch fires.

With Redis, every LLM call executes a Lua script that atomically checks the remaining budget and either commits the spend or rejects the call. Overspend is structurally impossible at the granularity of a single LLM call, regardless of how many pods are running concurrently.

Redis is optional. For workloads where a brief overspend window is acceptable (e.g., internal tools, development environments), Redis is unnecessary overhead.

---

## Key Architecture

All budget state for a given budget lives in a single Redis hash. A Lua script performs a single round-trip: check + commit atomically.

### Shared Mode

```
Redis key:  shekel:budget:{budget-name}
Hash fields:
  spent_usd       float as string   cumulative USD spent in current window
  call_count      integer           cumulative LLM call count in current window
  window_start    unix timestamp    start of the current enforcement window
  window_end      unix timestamp    end of the current enforcement window (for rolling)
  max_usd         float as string   limit (written by controller on create/reset)
  max_calls       integer           call limit (written by controller, 0 = unlimited)
```

### Per-Group Mode

Each group gets its own Redis hash:
```
Redis key:  shekel:budget:{budget-name}:group:{group-value}
```

All fields are identical to shared mode. Groups do not share state.

---

## Lua Script (Atomic Check + Commit)

The shekel library executes this Lua script on every LLM call. The controller does not execute this script — it uses a separate reset command.

```lua
-- KEYS[1] = Redis hash key (e.g. "shekel:budget:my-budget")
-- ARGV[1] = current unix timestamp (seconds)
-- ARGV[2] = usd_cost of this call (string, may be "0" for pre-call checks)
-- ARGV[3] = call_count increment (always "1")
-- ARGV[4] = window_end (for rolling budgets; "0" for fixed-period budgets)

local key = KEYS[1]
local now = tonumber(ARGV[1])
local cost = tonumber(ARGV[2])
local window_end = tonumber(ARGV[4])

-- Check if window has expired (rolling budgets only)
if window_end > 0 then
  local stored_end = tonumber(redis.call('HGET', key, 'window_end') or '0')
  if now > stored_end then
    -- Window expired: reset all counters
    redis.call('HMSET', key,
      'spent_usd', '0',
      'call_count', '0',
      'window_start', tostring(now),
      'window_end', tostring(window_end)
    )
  end
end

-- Read current state
local max_usd = tonumber(redis.call('HGET', key, 'max_usd') or '0')
local max_calls = tonumber(redis.call('HGET', key, 'max_calls') or '0')
local spent = tonumber(redis.call('HGET', key, 'spent_usd') or '0')
local calls = tonumber(redis.call('HGET', key, 'call_count') or '0')

-- Check limits BEFORE committing
-- USD check (if max_usd > 0)
if max_usd > 0 and (spent + cost) > max_usd then
  return {'exceeded', 'usd', tostring(spent), tostring(max_usd)}
end

-- Call count check (if max_calls > 0)
if max_calls > 0 and (calls + 1) > max_calls then
  return {'exceeded', 'calls', tostring(calls), tostring(max_calls)}
end

-- Commit: increment counters atomically
redis.call('HINCRBYFLOAT', key, 'spent_usd', tostring(cost))
redis.call('HINCRBY', key, 'call_count', '1')

return {'ok', tostring(spent + cost), tostring(calls + 1)}
```

**Return values:**
- `['ok', new_spent, new_calls]` — call permitted; library proceeds with LLM request
- `['exceeded', 'usd', spent, limit]` — library raises `BudgetExceededError`
- `['exceeded', 'calls', count, limit]` — library raises `BudgetExceededError`

### Two-Phase Calls (Pre-check + Commit)

For LLM calls where cost is unknown before the call (most cases), the library runs two Lua script invocations:

1. **Pre-check** (`cost=0`, `call_count=1`): verifies there is remaining budget and increments the call counter. If the budget is already at zero, the call is rejected immediately without making an API request.
2. **Post-commit** (`cost=actual_usd_from_response`, `call_count=0`): adds the actual token cost after the response is received.

This ensures call counts are accurate even if the post-commit step fails (e.g., pod crash after response received but before commit). The pre-check also prevents calls when the budget is exactly zero.

---

## Controller Redis Operations

The controller interacts with Redis for two operations only:

### Initialize Budget Key

Called during first reconcile when Redis is configured, and after any spec change that affects limits:

```
HMSET shekel:budget:{name}
  max_usd    {spec.maxUSD}
  max_calls  {spec.maxLLMCalls or 0}
  spent_usd  0
  call_count 0
  window_start {now}
  window_end   {period_end or 0}
```

If the key already exists (e.g., controller restart), the controller only updates `max_usd` and `max_calls` if they changed. It does not reset `spent_usd` — preserving the current period's spend through controller restarts.

### Reset on Period Boundary

```
DEL shekel:budget:{name}
```

followed immediately by re-initialization with zeroed counters. Using `DEL + HMSET` rather than individual field resets ensures the key is never in a partial state visible to the Lua script.

For per-group mode, the controller uses `SCAN` to find all keys matching `shekel:budget:{name}:group:*` and deletes them in batches.

---

## Failure Modes

### Redis Unreachable

Controlled by `spec.redis.onUnavailable`:

**`closed` (default — fail-safe):** The Lua script call fails. The shekel library treats this as a budget refusal and raises `BudgetExceededError`. No LLM call is made. The controller sets `RedisAvailable=False` condition and emits a Warning event.

**`open` (fail-permissive):** The Lua script call fails. The shekel library falls back to in-process enforcement for this call. The LLM call proceeds. The controller logs the connectivity failure but does not change the ShekelBudget state.

### Circuit Breaker

After `spec.redis.circuitBreakerThreshold` consecutive Redis failures (default: 3), the library opens its circuit breaker and stops calling Redis for `spec.redis.circuitBreakerCooldown` seconds (default: 10). During the open period, the library applies `onUnavailable` semantics to all calls without attempting Redis.

After `circuitBreakerCooldown` seconds, the library sends a single probe. If it succeeds, the circuit closes and Redis enforcement resumes. If it fails, the cooldown resets.

### Partial Commit Failure

If the pod crashes after the pre-check but before the post-commit, the call counter is incremented but the spend is not recorded. This means the Redis key slightly under-reports actual spend. This is an unavoidable trade-off of two-phase enforcement without distributed transactions. The gap is bounded by the cost of a single LLM call.

---

## Redis Connection String

The connection URL is stored in a Kubernetes Secret and referenced by `spec.redis.secretRef`. The shekel library reads it from the `REDIS_URL` environment variable injected by the webhook.

Supported URL formats:
- `redis://host:port` — plain TCP
- `redis://host:port/db` — with database number
- `redis://:password@host:port` — with password
- `rediss://host:port` — TLS (recommended for production)
- `redis+sentinel://sentinel1:port,sentinel2:port/master-name` — Sentinel mode

---

## Production Checklist

| Requirement | Reason |
|-------------|--------|
| Persistent Redis (not in-memory only) | Budget state must survive Redis restarts; use `appendonly yes` in Redis config |
| TLS (`rediss://`) | The `REDIS_URL` contains credentials and traverses the network |
| Redis high availability (Sentinel or Cluster) | Controller and pods both depend on Redis for enforcement |
| Separate Redis instance per environment | Prevents dev/staging budgets interfering with production |
| Budget name versioning during rolling deployments | If spec changes during a rollout, old pods and new pods may see different `max_usd`; use versioned names (e.g. `my-budget-v2`) to avoid `BudgetConfigMismatchError` |

---

## Observability

The controller exposes the following Prometheus metrics for Redis health:

| Metric | Type | Description |
|--------|------|-------------|
| `shekel_redis_operations_total` | Counter | Redis Lua script calls, labeled by `budget`, `result` (`ok`/`exceeded`/`error`) |
| `shekel_redis_circuit_breaker_state` | Gauge | 0=closed, 1=open, per budget |
| `shekel_redis_latency_seconds` | Histogram | Lua script round-trip latency |
| `shekel_redis_reset_total` | Counter | Period resets executed by the controller |
