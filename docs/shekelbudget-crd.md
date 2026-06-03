# ShekelBudget CRD

## Overview

`ShekelBudget` is a namespaced custom resource that declares a budget policy for a group of pods. It is the single configuration surface that operators interact with — everything else (ConfigMaps, webhook injections, spend aggregation) is derived from it.

- **API Group:** `shekel.io`
- **Version:** `v1alpha1`
- **Kind:** `ShekelBudget`
- **Short name:** `sb`
- **Scope:** Namespaced

---

## Full Schema

```yaml
apiVersion: shekel.io/v1alpha1
kind: ShekelBudget
metadata:
  name: string                    # budget identifier; also used as Redis key prefix
  namespace: string               # namespace where Budget ConfigMap is created

spec:
  # ── Budget limits ─────────────────────────────────────────────────
  maxUSD: float                   # required; hard USD spending cap for the period
  warnAt: float                   # optional; fraction 0.0–1.0 at which to warn (e.g. 0.8 = 80%)

  # ── Enforcement period ────────────────────────────────────────────
  period:
    type: string                  # required; one of: daily | weekly | monthly | rolling
    windowSeconds: int            # required only when type=rolling; e.g. 3600 for 1 hour

  # ── Pod selector ──────────────────────────────────────────────────
  # Identifies which pods this budget applies to.
  # The webhook uses this selector to decide which pods to inject.
  selector:
    matchLabels:
      map[string]string           # label key/value pairs
    matchExpressions:             # optional; standard LabelSelectorRequirement list
      - key: string
        operator: string          # In | NotIn | Exists | DoesNotExist
        values: [string]

  # ── Scope mode ────────────────────────────────────────────────────
  scope:
    mode: string                  # shared (default) | per-group
    # shared:    all matching pods share one budget and one Redis key
    # per-group: each distinct value of groupBy label gets its own budget and Redis key
    groupBy: string               # required when mode=per-group; pod label key to group on
                                  # e.g. "tenant-id" → each tenant gets maxUSD independently

  # ── Fallback model ────────────────────────────────────────────────
  fallback:                       # optional
    atPct: float                  # fraction 0.0–1.0; switch model when spend reaches this fraction
    model: string                 # model identifier to switch to (e.g. "gpt-4o-mini")

  # ── Redis backend ─────────────────────────────────────────────────
  redis:                          # optional; enables hard atomic enforcement
    secretRef:
      name: string                # name of a Secret in the same namespace
      key: string                 # key within the Secret holding the Redis URL
                                  # URL format: redis://host:port or rediss://host:port (TLS)
    onUnavailable: string         # closed (default) | open
                                  # closed: raise BudgetExceededError if Redis unreachable
                                  # open:   allow calls through if Redis unreachable
    circuitBreakerThreshold: int  # optional; consecutive errors before circuit opens (default 3)
    circuitBreakerCooldown: float # optional; seconds before retry after circuit opens (default 10)

  # ── LLM call cap ──────────────────────────────────────────────────
  maxLLMCalls: int                # optional; hard cap on number of LLM API calls per period

status:
  # Written by the controller; do not set manually.
  state: string                   # Active | Warned | Exceeded | Paused
  currentSpendUSD: float          # aggregated spend across all pods in current period
  callCount: int                  # aggregated LLM call count in current period
  periodStart: string             # RFC3339 timestamp; start of current period
  periodEnd: string               # RFC3339 timestamp; end of current period (when reset fires)
  lastResetTime: string           # RFC3339 timestamp; when the last period reset occurred
  podCount: int                   # number of pods currently reporting spend
  conditions:
    - type: string                # BudgetActive | BudgetExceeded | KillSwitchActive | RedisAvailable
      status: string              # True | False | Unknown
      reason: string
      message: string
      lastTransitionTime: string
```

---

## Period Types

| Type | Behaviour |
|------|-----------|
| `daily` | Resets at midnight UTC each day |
| `weekly` | Resets at midnight UTC each Monday |
| `monthly` | Resets at midnight UTC on the 1st of each month |
| `rolling` | Rolling window of `windowSeconds` seconds; equivalent to shekel's `TemporalBudget` |

For `daily`, `weekly`, and `monthly`, the controller computes `periodEnd` deterministically and requeues the reconcile loop to fire exactly at that time.

For `rolling`, the controller does not reset on a fixed boundary. Instead, Redis tracks the rolling window atomically. `periodEnd` in the status represents the time when the current oldest sample falls out of the window.

---

## Scope Modes

### `shared` (default)
All pods matching the selector draw from a single pool. One Redis key: `shekel:budget:{name}`. One Budget ConfigMap. One aggregated spend total.

Use when a team or service shares a budget.

```yaml
scope:
  mode: shared
```

### `per-group`
Each unique value of the `groupBy` label gets its own independent budget equal to `maxUSD`. Redis keys are namespaced: `shekel:budget:{name}:group:{value}`. The controller creates per-group Spend Report aggregation.

Use for multi-tenant deployments where each tenant gets their own `maxUSD` cap.

```yaml
scope:
  mode: per-group
  groupBy: tenant-id
```

In per-group mode, a pod with label `tenant-id=acme` and a pod with label `tenant-id=globex` each have an independent $100 budget. Neither's spending affects the other.

---

## Examples

### Basic daily budget

```yaml
apiVersion: shekel.io/v1alpha1
kind: ShekelBudget
metadata:
  name: research-team-daily
  namespace: ai-agents
spec:
  maxUSD: 50.00
  warnAt: 0.80
  period:
    type: daily
  selector:
    matchLabels:
      team: research
```

### Monthly budget with fallback model and Redis

```yaml
apiVersion: shekel.io/v1alpha1
kind: ShekelBudget
metadata:
  name: production-agents
  namespace: production
spec:
  maxUSD: 1000.00
  warnAt: 0.75
  period:
    type: monthly
  selector:
    matchLabels:
      shekel.io/budget: production-agents
  fallback:
    atPct: 0.80
    model: gpt-4o-mini
  redis:
    secretRef:
      name: redis-credentials
      key: REDIS_URL
    onUnavailable: closed
  maxLLMCalls: 10000
```

### Per-tenant rolling-window budget

```yaml
apiVersion: shekel.io/v1alpha1
kind: ShekelBudget
metadata:
  name: tenant-hourly-cap
  namespace: saas-platform
spec:
  maxUSD: 5.00
  period:
    type: rolling
    windowSeconds: 3600
  scope:
    mode: per-group
    groupBy: tenant-id
  selector:
    matchLabels:
      app: llm-gateway
  redis:
    secretRef:
      name: redis-credentials
      key: REDIS_URL
```

---

## Validation Rules

The following validation is enforced by the CRD's CEL rules and/or the controller:

| Field | Rule |
|-------|------|
| `spec.maxUSD` | Must be > 0 |
| `spec.warnAt` | Must be in range (0.0, 1.0) exclusive |
| `spec.period.type` | Must be one of `daily`, `weekly`, `monthly`, `rolling` |
| `spec.period.windowSeconds` | Required and > 0 when `period.type=rolling`; forbidden otherwise |
| `spec.scope.mode` | Must be `shared` or `per-group` |
| `spec.scope.groupBy` | Required when `scope.mode=per-group`; forbidden otherwise |
| `spec.fallback.atPct` | Must be in range (0.0, 1.0) exclusive; must be < 1.0 |
| `spec.fallback.model` | Required when `fallback` is present |
| `spec.redis.onUnavailable` | Must be `closed` or `open` |
| `spec.selector` | At least one of `matchLabels` or `matchExpressions` must be non-empty |

---

## Status Conditions

| Condition Type | True Meaning | False Meaning |
|----------------|-------------|---------------|
| `BudgetActive` | Budget is within limits and accepting calls | Budget not yet initialized or paused |
| `BudgetExceeded` | Aggregated spend ≥ `maxUSD`; kill-switch active | Spend within limits |
| `KillSwitchActive` | Budget ConfigMap has `SHEKEL_KILL_SWITCH=true` | ConfigMap allows calls |
| `RedisAvailable` | Controller can reach Redis | Redis connection failed or not configured |

---

## RBAC Requirements for the Controller

The controller needs the following permissions on `ShekelBudget`:

- `get`, `list`, `watch` — to reconcile
- `update`, `patch` — to write status
- `create`, `delete` — not required (user-managed)

Status updates use the `/status` subresource to separate spec and status RBAC.
