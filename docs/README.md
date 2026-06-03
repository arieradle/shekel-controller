# shekel-controller Design Documentation

This directory contains the design documentation for the shekel-controller Kubernetes operator.

Read these in order before implementing:

| Document | Contents |
|----------|----------|
| [overview.md](overview.md) | System architecture, component map, data flow, design decisions |
| [shekelbudget-crd.md](shekelbudget-crd.md) | CRD schema, field definitions, validation rules, examples |
| [controller.md](controller.md) | Reconciliation loop, Budget ConfigMap, spend aggregation, state machine, RBAC |
| [webhook.md](webhook.md) | Mutating webhook, injection logic, TLS, cert-manager |
| [library-integration.md](library-integration.md) | Config discovery protocol, spend reporting protocol, env var contract |
| [redis.md](redis.md) | Redis key layout, Lua script, circuit breaker, failure modes |
| [deployment.md](deployment.md) | Helm chart layout, values reference, RBAC, HA, monitoring |

## Implementation Order

The Jira backlog (SHEK-13 → SHEK-22) already reflects the correct dependency order:

```
SHEK-13  CRD types + registration
    ↓
SHEK-14  Controller: reconcile loop + ConfigMap materialisation
    ↓
SHEK-15  Controller: spend aggregation + kill-switch + period reset
    ↓
SHEK-16  Library: in-cluster config discovery (shekel repo)
    ↓
SHEK-17  Library: periodic spend reporting (shekel repo)
    ↓
SHEK-18  Mutating webhook: pod injection
    ↓
SHEK-19  Webhook TLS + cert-manager
    ↓
SHEK-20  Redis backend integration
    ↓
SHEK-21  Per-group scope mode
    ↓
SHEK-22  Helm chart + RBAC manifests
```

SHEK-16 and SHEK-17 are changes to the [shekel library repo](https://github.com/arieradle/shekel), not this repo.
