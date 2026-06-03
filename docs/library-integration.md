# Library Integration Protocol

## Overview

This document defines the contract between the shekel Python library running inside pods and the shekel-controller. The protocol has two directions:

- **Inbound (controller → library):** Configuration delivery via Budget ConfigMap.
- **Outbound (library → controller):** Spend reporting via Spend Report ConfigMap.

Both directions use standard Kubernetes ConfigMaps as the data transport. No sidecar, no custom HTTP endpoint, no gRPC — just ConfigMap reads and writes that any Kubernetes-aware pod can perform.

---

## Inbound: Config Discovery (SHEK-16)

### How the Library Finds Its Config

When the shekel library initializes inside a pod (on first `with budget():` call, or at import time if configured), it performs in-cluster config discovery:

1. Check environment variables first (set by webhook or directly):
   - `AGENT_BUDGET_USD` — if present and non-empty, use it directly
   - `SHEKEL_BUDGET_NAME` — if present, load the full config from ConfigMap

2. If `SHEKEL_BUDGET_NAME` is set, look up the Budget ConfigMap:
   - Name: `shekel-budget-{SHEKEL_BUDGET_NAME}`
   - Namespace: `SHEKEL_NAMESPACE` env var, or current pod's namespace (via `/var/run/secrets/kubernetes.io/serviceaccount/namespace`)

3. If the ConfigMap is found, read all shekel-relevant keys and merge them with env vars (env vars take precedence).

4. If neither env vars nor ConfigMap are present: fall back to the library's default behavior (no-op / track-only).

### Config Keys Read from Budget ConfigMap

| Key | Library Behaviour |
|-----|-------------------|
| `AGENT_BUDGET_USD` | Sets `max_usd` for the budget context |
| `SHEKEL_BUDGET_NAME` | Sets `name` for the budget; used as Redis key base |
| `SHEKEL_WARN_AT` | Sets `warn_at` threshold |
| `SHEKEL_MAX_LLM_CALLS` | Sets `max_llm_calls` |
| `SHEKEL_KILL_SWITCH` | If `true`, library immediately raises `BudgetExceededError` on any LLM call |
| `REDIS_URL` | Connects to Redis backend; if absent, in-process enforcement only |
| `SHEKEL_FALLBACK_AT_PCT` | Sets `fallback.at_pct` |
| `SHEKEL_FALLBACK_MODEL` | Sets `fallback.model` |
| `SHEKEL_SCOPE_MODE` | `shared` or `per-group`; affects Redis key construction |
| `SHEKEL_GROUP_BY` | Label key used for group identification (per-group mode) |
| `SHEKEL_GROUP_VALUE` | Value of `groupBy` label for this pod (per-group mode) |
| `SHEKEL_PERIOD_START` | Informational; used to align local rolling window |
| `SHEKEL_PERIOD_END` | Informational; library can warn when approaching period boundary |

### Kill-Switch Behaviour

When `SHEKEL_KILL_SWITCH=true` is detected:
- Any currently-active `budget()` context: the library sets an internal flag that causes the next LLM call to raise `BudgetExceededError` immediately (without making the actual API call).
- New `budget()` contexts started after kill-switch detection: raise `BudgetExceededError` before the first call.

The library polls the Budget ConfigMap at the **config refresh interval** (default 30 seconds) to detect kill-switch changes without restarting the pod.

### Config Refresh

The library maintains a background thread (or async task) that re-reads the Budget ConfigMap every `SHEKEL_CONFIG_REFRESH_INTERVAL` seconds (default: 30). On each refresh:
- If `SHEKEL_KILL_SWITCH` changed from `false` to `true`: activate kill-switch immediately.
- If `SHEKEL_KILL_SWITCH` changed from `true` to `false` (period reset): clear kill-switch, restore budget.
- If `AGENT_BUDGET_USD` changed (e.g., dynamic budget adjustment): update active budget limit.

The refresh uses the in-cluster service account token and the Kubernetes API. If the API is unreachable, the library uses the last-known config (fail-safe).

---

## Outbound: Spend Reporting (SHEK-17)

### Spend Report ConfigMap

Each pod writes its own Spend Report ConfigMap, identified by the pod's name:

**Naming:** `shekel-spend-{pod-name}`
**Namespace:** same as the pod

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: shekel-spend-{pod-name}
  namespace: {pod-namespace}
  labels:
    shekel.io/spend-report: "true"
    shekel.io/budget: {budget-name}
    shekel.io/group: {group-value}    # only in per-group mode; value of groupBy label
data:
  spent_usd: "12.3456"               # total USD spent in current period by this pod
  call_count: "42"                   # total LLM calls in current period
  last_updated: "2024-01-15T10:30:00Z"  # RFC3339; controller uses this to detect stale reports
  pod_name: "my-agent-7f4d9b-x2k8p"
  budget_name: "my-budget"
  period_start: "2024-01-01T00:00:00Z"  # period this spend belongs to
```

### Reporting Interval

The library flushes its local spend counter to the Spend Report ConfigMap every `SHEKEL_REPORT_INTERVAL` seconds (default: 15). The flush is a `createOrUpdate` (create on first write, patch on subsequent writes).

The library uses the pod's service account token for this write. The RBAC permission required is `create,patch` on `configmaps` in the pod's namespace (see [deployment.md](deployment.md) for the default ClusterRole).

### Local Accumulation

The library accumulates spend in memory between flushes. If the pod crashes between flushes, up to `SHEKEL_REPORT_INTERVAL` seconds of spend may be lost from the aggregated view. This is an acceptable trade-off: spend that isn't reported is spend the controller doesn't count against the budget. The Redis backend provides a complementary hard guarantee that prevents overspend regardless of reporting gaps.

### Pod Termination

On graceful shutdown (`SIGTERM`), the library's shutdown hook performs a final spend flush before exiting. This minimizes the reporting gap on normal pod lifecycle events (scale-down, rolling update).

On crash (non-graceful), the last flushed value remains in the ConfigMap. The controller detects the pod is gone (ConfigMap `last_updated` is stale) and stops counting it in aggregation.

### Spend Reset on Period Boundary

When the controller resets the period, it deletes all Spend Report ConfigMaps for the budget (identified by the label selector). On the next flush cycle, the library recreates the ConfigMap with a zeroed counter (since the library itself also resets its local accumulator when it detects a new `period_start` from the Budget ConfigMap refresh).

---

## Service Account Requirements

Pods using shekel's in-cluster integration need a ServiceAccount with the following permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: shekel-pod-reporter
rules:
  # Read the Budget ConfigMap (config discovery + kill-switch polling)
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["shekel-budget-*"]
    verbs: ["get", "watch"]

  # Write the pod's own Spend Report ConfigMap
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["shekel-spend-*"]
    verbs: ["get", "patch", "update"]
```

The Helm chart includes a `shekel-pod-reporter` ClusterRole and a RoleBinding that can be applied per-namespace. Alternatively, the webhook can inject the `serviceAccountName` into matching pods if a designated service account is configured in the ShekelBudget.

---

## Environment Variables Summary

All variables can be set manually in the pod spec, or injected automatically by the webhook. Env vars always take precedence over ConfigMap values.

| Variable | Source | Description |
|----------|--------|-------------|
| `AGENT_BUDGET_USD` | Budget ConfigMap | Hard USD cap |
| `SHEKEL_BUDGET_NAME` | Budget ConfigMap | Budget identifier; triggers ConfigMap discovery |
| `SHEKEL_NAMESPACE` | Webhook (`fieldRef`) | Pod's namespace; used to locate ConfigMap |
| `SHEKEL_POD_NAME` | Webhook (`fieldRef`) | Pod's name; used as Spend Report ConfigMap name |
| `REDIS_URL` | Budget ConfigMap or Secret | Redis connection string |
| `SHEKEL_KILL_SWITCH` | Budget ConfigMap | Immediate enforcement signal |
| `SHEKEL_WARN_AT` | Budget ConfigMap | Warning threshold fraction |
| `SHEKEL_MAX_LLM_CALLS` | Budget ConfigMap | Call count cap |
| `SHEKEL_FALLBACK_AT_PCT` | Budget ConfigMap | Fallback model threshold |
| `SHEKEL_FALLBACK_MODEL` | Budget ConfigMap | Fallback model name |
| `SHEKEL_SCOPE_MODE` | Budget ConfigMap | `shared` or `per-group` |
| `SHEKEL_GROUP_BY` | Budget ConfigMap | Label key for group identification |
| `SHEKEL_GROUP_VALUE` | Webhook (`fieldRef`) | Value of `groupBy` label on this pod |
| `SHEKEL_REPORT_INTERVAL` | Pod spec | Spend flush interval in seconds (default: 15) |
| `SHEKEL_CONFIG_REFRESH_INTERVAL` | Pod spec | Config re-read interval in seconds (default: 30) |

---

## Sequence Diagram

```
Pod start
  │
  ├─ Library init
  │    ├─ Read env vars (injected by webhook)
  │    ├─ If SHEKEL_BUDGET_NAME: fetch Budget ConfigMap from K8s API
  │    └─ Configure budget(max_usd=..., name=..., backend=RedisBackend(...))
  │
  ├─ Background: config refresh loop (every 30s)
  │    └─ Re-read Budget ConfigMap → apply changes (kill-switch, period reset)
  │
  ├─ Background: spend report loop (every 15s)
  │    └─ Flush local spend counter → createOrUpdate Spend Report ConfigMap
  │
  ├─ LLM call
  │    ├─ shekel intercepts → check in-process budget
  │    ├─ If Redis configured: Lua script → atomic check/commit
  │    ├─ If kill-switch active: raise BudgetExceededError (no call made)
  │    └─ Call proceeds → tokens tracked → spend accumulated locally
  │
  └─ SIGTERM (graceful shutdown)
       └─ Final spend flush → Spend Report ConfigMap updated
```
