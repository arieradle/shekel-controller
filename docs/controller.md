# Controller Design

## Overview

The shekel controller is a standard Kubernetes controller built with [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime). It watches `ShekelBudget` custom resources and their associated Spend Report ConfigMaps, and drives the system toward the desired state declared in the CRD.

The controller is the only component with write access to the Budget ConfigMap and the ShekelBudget status. All other components (pods, webhook) are read-only consumers of state the controller produces.

---

## Reconciliation Triggers

The controller's reconcile queue fires when:

1. A `ShekelBudget` resource is created, updated, or deleted.
2. A Spend Report ConfigMap with label `shekel.io/budget={name}` is created or updated.
3. The controller requeues itself at `periodEnd` time (for period reset).
4. A requeue after error (exponential backoff).

---

## Reconciliation Loop

```
func Reconcile(ctx, req) (Result, error):

  1. Fetch ShekelBudget by req.NamespacedName
     → if NotFound: return (no requeue) — already deleted

  2. If DeletionTimestamp set:
     → run cleanup finalizer (delete Budget ConfigMap, delete Spend Report ConfigMaps)
     → remove finalizer
     → return

  3. Ensure finalizer is set on the ShekelBudget

  4. Resolve Redis URL (if spec.redis configured):
     → read Secret[spec.redis.secretRef.name][spec.redis.secretRef.key]
     → if Secret missing: update condition RedisAvailable=False, requeue with error

  5. Compute period bounds:
     → periodStart, periodEnd = computePeriod(spec.period, status.lastResetTime)

  6. Check for period reset:
     → if now >= periodEnd:
         → resetPeriod(ctx, budget, redisURL)   (see Period Reset below)
         → recompute periodStart, periodEnd

  7. Aggregate spend:
     → list all ConfigMaps with label shekel.io/spend-report=true AND shekel.io/budget={name}
     → sum spent_usd and call_count fields
     → aggregatedSpend, aggregatedCalls = aggregate(spendReports)

  8. Determine state:
     → if aggregatedSpend >= spec.maxUSD:          state = Exceeded
     → elif aggregatedSpend >= spec.maxUSD * spec.warnAt: state = Warned
     → else:                                        state = Active

  9. Materialize Budget ConfigMap (see Budget ConfigMap below):
     → desired = buildConfigMap(budget, redisURL, state, periodStart, periodEnd)
     → createOrUpdate(desired)

  10. Update ShekelBudget status:
      → status.state = state
      → status.currentSpendUSD = aggregatedSpend
      → status.callCount = aggregatedCalls
      → status.periodStart = periodStart
      → status.periodEnd = periodEnd
      → status.podCount = len(spendReports)
      → status.conditions = buildConditions(state, redisAvailable)
      → patch status subresource

  11. Emit Kubernetes Event if state changed (Active→Warned, Warned→Exceeded, Exceeded→Active)

  12. Requeue at periodEnd (so reset fires on time):
      → return Result{RequeueAfter: periodEnd.Sub(now)}
```

---

## Budget ConfigMap

The Budget ConfigMap is the contract surface between the controller and the in-pod shekel library. The controller owns this ConfigMap and is the only writer.

**Naming:** `shekel-budget-{budget-name}`
**Namespace:** same as the ShekelBudget

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: shekel-budget-{budget-name}
  namespace: {namespace}
  labels:
    shekel.io/managed-by: shekel-controller
    shekel.io/budget: {budget-name}
  ownerReferences:
    - apiVersion: shekel.io/v1alpha1
      kind: ShekelBudget
      name: {budget-name}
      uid: {uid}
      controller: true
      blockOwnerDeletion: true
data:
  AGENT_BUDGET_USD: "100.00"          # set to "0" when kill-switch active
  SHEKEL_BUDGET_NAME: "{budget-name}"
  SHEKEL_WARN_AT: "0.80"              # omitted if not configured
  SHEKEL_MAX_LLM_CALLS: "1000"        # omitted if not configured
  SHEKEL_KILL_SWITCH: "false"         # "true" when state=Exceeded
  SHEKEL_PERIOD_START: "2024-01-01T00:00:00Z"
  SHEKEL_PERIOD_END: "2024-01-31T23:59:59Z"
  SHEKEL_SCOPE_MODE: "shared"         # "shared" | "per-group"
  SHEKEL_GROUP_BY: "tenant-id"        # omitted for shared mode
  REDIS_URL: "redis://..."            # omitted if redis not configured
  SHEKEL_FALLBACK_AT_PCT: "0.80"      # omitted if not configured
  SHEKEL_FALLBACK_MODEL: "gpt-4o-mini" # omitted if not configured
```

**Kill-switch activation:** When `state=Exceeded`, the controller sets `SHEKEL_KILL_SWITCH=true` and `AGENT_BUDGET_USD=0`. The shekel library checks `SHEKEL_KILL_SWITCH` on its periodic config re-read (see [library-integration.md](library-integration.md)) and raises `BudgetExceededError` on the next LLM call.

**OwnerReference:** The Budget ConfigMap has an owner reference to the ShekelBudget. When the ShekelBudget is deleted, Kubernetes garbage-collects the ConfigMap automatically. The controller also sets a finalizer to delete Spend Report ConfigMaps (which are not owned directly).

---

## Spend Aggregation

The controller lists all ConfigMaps matching:
```
label: shekel.io/spend-report=true
label: shekel.io/budget={name}
namespace: {same namespace as ShekelBudget}
```

Each Spend Report ConfigMap has the following structure (written by the library):
```yaml
data:
  spent_usd: "12.3456"
  call_count: "42"
  last_updated: "2024-01-15T10:30:00Z"
  pod_name: "my-agent-7f4d9b-x2k8p"
  budget_name: "my-budget"
```

The controller sums `spent_usd` and `call_count` across all reports. It discards reports where `last_updated` is older than `2 × the library's reporting interval` (default: older than 120 seconds), treating absent pods as zero contributors. This prevents stale reports from inflating aggregated spend after pod termination.

**Per-group aggregation:** In `per-group` scope mode, the controller groups spend reports by the value of the `shekel.io/group` label on the Spend Report ConfigMap, computing per-group totals separately. The ShekelBudget status reports total aggregated spend; per-group breakdowns are available as annotations on the status.

---

## Period Reset

When `now >= periodEnd`:

1. If Redis is configured: call `backend.reset(budgetName)` to zero the Redis hash.
   - For per-group mode: enumerate all known group keys (`shekel:budget:{name}:group:*`) and reset each.
2. Delete all Spend Report ConfigMaps for this budget (listed by label).
3. Update `status.lastResetTime = now`.
4. Recompute `periodStart` and `periodEnd` for the new period.
5. Update Budget ConfigMap: restore `AGENT_BUDGET_USD`, set `SHEKEL_KILL_SWITCH=false`, update period timestamps.
6. Update ShekelBudget status: `state=Active`, `currentSpendUSD=0`, `callCount=0`.

The controller uses `RequeueAfter` to schedule the reconcile exactly at `periodEnd`. Kubernetes clock skew and scheduling jitter mean the requeue may fire slightly late; this is acceptable for budget enforcement (a few-second grace window at period boundary is not a concern).

---

## State Machine

```
                    ┌──────────────────────────────┐
                    │                              │
                    ▼           period reset       │
         ┌──────────────────┐  ────────────────▶  │
  ──────▶│     Active       │                     │
  init   └──────────────────┘                     │
                 │                                 │
                 │ spend ≥ warnAt × maxUSD          │
                 ▼                                 │
         ┌──────────────────┐     period reset     │
         │     Warned       │  ────────────────▶  │
         └──────────────────┘                     │
                 │                                 │
                 │ spend ≥ maxUSD                  │
                 ▼                                 │
         ┌──────────────────┐                     │
         │    Exceeded      │ ─────────────────────┘
         │ (kill-switch on) │
         └──────────────────┘
                 │
                 │ manual patch spec.paused=true
                 ▼
         ┌──────────────────┐
         │     Paused       │  (future: manual override)
         └──────────────────┘
```

Transitions are one-way except for period reset, which always returns to `Active` regardless of previous state.

---

## Kubernetes Events

The controller emits Events on the ShekelBudget object for the following transitions:

| Reason | Type | Trigger |
|--------|------|---------|
| `BudgetWarning` | Warning | State transitions to `Warned` |
| `BudgetExceeded` | Warning | State transitions to `Exceeded` |
| `KillSwitchActivated` | Warning | Budget ConfigMap updated with kill-switch |
| `PeriodReset` | Normal | Budget resets at period boundary |
| `RedisUnavailable` | Warning | Redis connection check fails |
| `ConfigMapMaterialized` | Normal | Budget ConfigMap created for first time |

---

## Concurrency and Leader Election

Multiple controller replicas must not reconcile the same resource simultaneously. The controller uses `controller-runtime`'s built-in leader election (backed by a Kubernetes Lease object) to ensure only one replica is active at a time. Standby replicas monitor the lease and take over if the leader fails.

Reconcile calls for the same resource are serialized by the work queue. Reconcile calls for different resources may run concurrently (bounded by `MaxConcurrentReconciles`, default 1, configurable via Helm values).

---

## Error Handling and Retries

- **Transient errors** (network, API server unavailable): return `error` from Reconcile → controller-runtime requeues with exponential backoff (1s, 2s, 4s… up to 10m).
- **Permanent errors** (invalid CRD, missing Secret): set a `Failed` condition on the ShekelBudget and requeue after a longer interval (5m). Do not loop infinitely.
- **Redis unavailable**: if `onUnavailable=closed`, treat as transient error and requeue. If `onUnavailable=open`, log a warning, set `RedisAvailable=False` condition, and continue reconciling.

---

## RBAC Requirements

```yaml
rules:
  # ShekelBudget CRD
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets"]
    verbs: ["get", "list", "watch", "patch", "update"]
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets/status"]
    verbs: ["get", "patch", "update"]
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets/finalizers"]
    verbs: ["update"]

  # ConfigMaps (Budget ConfigMaps + Spend Report ConfigMaps)
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Secrets (for Redis URL)
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]

  # Events
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]

  # Leader election
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```
