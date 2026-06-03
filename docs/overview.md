# Shekel Controller — Architecture Overview

## What It Is

`shekel-controller` is a Kubernetes operator that brings the [shekel](https://arieradle.github.io/shekel) LLM budget enforcement library into a cluster-native deployment model.

Without this operator, teams must manually configure `AGENT_BUDGET_USD`, `REDIS_URL`, and other shekel environment variables for each pod. There is no centralized spend visibility, no automatic kill-switch across a fleet of pods, and no period-based budget resets.

With this operator, an operator declares a `ShekelBudget` custom resource. The controller handles everything else: materializing config into ConfigMaps, injecting it into pods automatically via a webhook, aggregating spend reports, enforcing kill-switches, and resetting budgets on schedule.

---

## System Components

```
┌─────────────────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                                   │
│                                                                       │
│  ┌──────────────────┐        ┌──────────────────────────────────┐    │
│  │  ShekelBudget    │──────▶│  shekel-controller               │    │
│  │  (CRD)           │        │                                  │    │
│  │                  │◀──────│  - Reconciliation loop            │    │
│  │  spec:           │ status │  - ConfigMap materialisation      │    │
│  │    maxUSD: 100   │ update │  - Spend aggregation             │    │
│  │    period: daily │        │  - Kill-switch enforcement       │    │
│  │    selector: ... │        │  - Period reset                  │    │
│  └──────────────────┘        └──────────────┬───────────────────┘    │
│                                             │ creates/updates         │
│                                             ▼                         │
│                               ┌────────────────────────┐             │
│                               │  Budget ConfigMap       │             │
│                               │  (per namespace)        │             │
│                               │                         │             │
│                               │  AGENT_BUDGET_USD=100   │             │
│                               │  SHEKEL_BUDGET_NAME=... │             │
│                               │  REDIS_URL=...          │             │
│                               │  SHEKEL_KILL_SWITCH=... │             │
│                               └──────────┬──────────────┘             │
│                                          │ read by                    │
│         ┌────────────────────────────────┼──────────────────────┐    │
│         │                               ▼                        │    │
│         │  ┌─────────────────────────────────────┐              │    │
│         │  │  Mutating Admission Webhook          │              │    │
│         │  │                                      │              │    │
│         │  │  Intercepts Pod admission            │              │    │
│         │  │  Injects env vars from ConfigMap     │              │    │
│         │  └──────────────────────────────────────┘              │    │
│         │                                                         │    │
│         │  ┌──────────────────────────────────────────────────┐  │    │
│         │  │  Agent Pod                                        │  │    │
│         │  │                                                   │  │    │
│         │  │  [shekel library]                                 │  │    │
│         │  │    - auto-discovers config from ConfigMap         │  │    │
│         │  │    - patches OpenAI/Anthropic SDK                 │  │    │
│         │  │    - enforces AGENT_BUDGET_USD                    │  │    │
│         │  │    - periodically reports spend back ─────────────┼──┼──▶│
│         │  │                                                   │  │    │
│         │  └──────────────────────────────────────────────────┘  │    │
│         └─────────────────────────────────────────────────────────┘    │
│                                                                       │
│  ┌───────────────────────────────────────────────┐                   │
│  │  Redis (optional)                             │                   │
│  │  - Hard atomic enforcement across all pods    │                   │
│  │  - Single Lua-script round-trip per LLM call  │                   │
│  └───────────────────────────────────────────────┘                   │
└─────────────────────────────────────────────────────────────────────┘
```

---

## The Five Components

### 1. ShekelBudget CRD
A namespaced custom resource that is the single source of truth for a budget policy. It declares the USD limit, enforcement period, target pod selector, Redis config, and scope mode (shared vs per-group). The controller writes observed spend and state back to its `.status` field.

See: [shekelbudget-crd.md](shekelbudget-crd.md)

### 2. Controller
A standard Kubernetes reconciliation-loop controller (built with controller-runtime). It watches ShekelBudget resources and their associated spend-report ConfigMaps. On each reconcile it materializes a Budget ConfigMap, aggregates spend, updates the CRD status, triggers kill-switches when limits are exceeded, and requeues for period resets.

See: [controller.md](controller.md)

### 3. Mutating Admission Webhook
A webhook registered with the Kubernetes API server that intercepts Pod creation events. It identifies pods that match a ShekelBudget's label selector and injects the required shekel environment variables — sourced from the Budget ConfigMap — into the pod spec. This is zero-touch for application developers.

See: [webhook.md](webhook.md)

### 4. Library Integration Protocol
Defines the contract between the in-pod shekel Python library and the operator. The library auto-discovers its configuration by reading a well-known ConfigMap. It periodically writes spend data back to a per-pod Spend Report ConfigMap that the controller aggregates. The kill-switch is delivered by the controller updating the Budget ConfigMap; the library picks it up on its next reconcile cycle.

See: [library-integration.md](library-integration.md)

### 5. Redis Enforcement Backend
For hard atomic enforcement across multiple pods (preventing overspend that in-process budget tracking cannot prevent), the controller provisions a Redis-backed enforcement layer. Budget state lives in a single Redis hash per budget name, updated atomically by a Lua script on every LLM call. Per-group scope mode namespaces Redis keys by a pod label value for independent per-group tracking.

See: [redis.md](redis.md)

---

## Data Flow

### Happy Path (Pod Makes LLM Calls)

```
1. Operator creates ShekelBudget CR
2. Controller reconciles → creates Budget ConfigMap in namespace
3. Developer deploys Pod with matching label
4. Webhook intercepts Pod admission → injects env vars from Budget ConfigMap
5. Pod starts; shekel library reads Budget ConfigMap → configures itself
6. Pod makes LLM call → shekel intercepts → checks budget (Redis if configured)
7. Call succeeds → shekel records spend locally
8. Every N seconds, shekel writes spend to Spend Report ConfigMap
9. Controller watches Spend Report ConfigMaps → aggregates → updates ShekelBudget status
10. If aggregated spend ≥ warn_at threshold → controller updates status + emits event
11. Budget ConfigMap unchanged; pods continue
```

### Kill-Switch Path (Budget Exceeded)

```
12. Aggregated spend reaches max_usd
13. Controller sets SHEKEL_KILL_SWITCH=true + AGENT_BUDGET_USD=0 in Budget ConfigMap
14. Controller updates ShekelBudget status.state = Exceeded
15. Controller emits Kubernetes Event
16. Running pods: shekel library detects kill-switch on next config re-read → raises BudgetExceededError
17. New pods: Budget ConfigMap has AGENT_BUDGET_USD=0 → library refuses all calls immediately
18. Redis path: Redis key depleted → Lua script rejects all new calls without waiting for config re-read
```

### Period Reset Path

```
19. Controller requeues reconcile at period end time
20. On requeue: controller checks if current time ≥ period_end
21. If yes: controller resets Redis budget key (backend.reset(name))
22. Controller zeros Spend Report ConfigMaps (or deletes and recreates)
23. Controller sets SHEKEL_KILL_SWITCH=false + AGENT_BUDGET_USD restored in Budget ConfigMap
24. Controller updates ShekelBudget status with new period_start, period_end, current_spend=0
25. Running pods: shekel library detects restored budget on next config re-read → resumes normally
```

---

## Design Decisions

### Namespace Scope
`ShekelBudget` is namespace-scoped. The Budget ConfigMap is created in the same namespace. This aligns with Kubernetes RBAC principles: a team owns a namespace and its budgets. Cross-namespace budget policies are not supported in v1alpha1.

### ConfigMap as Integration Primitive
Rather than requiring pods to call a webhook or sidecar, the shekel library reads a ConfigMap and writes a ConfigMap. This is the simplest possible K8s primitive with broad RBAC support, no network dependency at read time, and native audit logging.

### Soft Kill-Switch via ConfigMap, Hard Kill-Switch via Redis
The ConfigMap-based kill-switch is eventually consistent: the library picks it up on its next polling interval (default 30s). This is acceptable for cost governance — a brief overage of seconds is tolerable.

For stricter requirements, Redis provides synchronous enforcement: the Lua script rejects calls immediately when the budget is depleted, before the library's polling cycle.

Both mechanisms are active simultaneously when Redis is configured.

### Per-Pod Spend Reports (Not Aggregated Writes)
Each pod writes its own Spend Report ConfigMap rather than all pods writing to one. This avoids write contention. The controller does the aggregation server-side. The tradeoff is O(N) ConfigMaps for N pods, which is acceptable at typical agent fleet sizes.

### No Custom Metrics Server Required
Spend data flows through ConfigMaps, not Prometheus or a custom metrics API. This avoids an infrastructure dependency. Observability integrations (OpenTelemetry, Langfuse) remain the library's concern, not the controller's.
