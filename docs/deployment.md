# Deployment Guide

## Overview

The shekel-controller is packaged as a Helm chart. It deploys two workloads into a single namespace:

1. **shekel-controller** — the reconciliation loop controller
2. **shekel-webhook** — the mutating admission webhook server

Both share the same container image. The controller is started with `--mode=controller` and the webhook with `--mode=webhook`.

---

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| Kubernetes | ≥ 1.25 | For CEL validation in CRDs |
| Helm | ≥ 3.12 | |
| cert-manager | ≥ 1.13 | Optional; required for automatic TLS. Without it, provide certs manually via `webhook.tls.*` values |

---

## Helm Chart Layout

```
chart/
  Chart.yaml
  values.yaml
  templates/
    crds/
      shekelbudget.yaml               # CRD definition (installed by Helm --set installCRDs=true)
    controller/
      deployment.yaml
      service.yaml                    # metrics endpoint only
      serviceaccount.yaml
    webhook/
      deployment.yaml
      service.yaml                    # port 443 → 9443 in-container
      serviceaccount.yaml
    rbac/
      controller-clusterrole.yaml
      controller-clusterrolebinding.yaml
      webhook-clusterrole.yaml
      webhook-clusterrolebinding.yaml
      pod-reporter-clusterrole.yaml   # for pods; bound per-namespace by users
    cert-manager/
      issuer.yaml                     # conditional on certManager.enabled
      certificate.yaml                # conditional on certManager.enabled
    webhook/
      mutatingwebhookconfiguration.yaml
    metrics/
      servicemonitor.yaml             # conditional on metrics.serviceMonitor.enabled
```

---

## values.yaml

```yaml
# -- Container image
image:
  repository: ghcr.io/arieradle/shekel-controller
  tag: ""                           # defaults to chart appVersion
  pullPolicy: IfNotPresent

# -- Number of controller replicas (leader election active with >1)
controller:
  replicaCount: 1
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 500m
      memory: 256Mi
  nodeSelector: {}
  tolerations: []
  affinity: {}
  # Leader election lease name
  leaderElectionID: shekel-controller-leader

# -- Webhook configuration
webhook:
  replicaCount: 1
  port: 9443
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 200m
      memory: 128Mi
  # TLS configuration: provide either certManager or manual certs
  tls:
    # Set to true if cert-manager is installed in the cluster
    certManager:
      enabled: true
      issuerRef: {}           # optional; use cluster issuer
    # Manual TLS (used when certManager.enabled=false)
    # certPEM: ""             # base64-encoded certificate PEM
    # keyPEM: ""              # base64-encoded private key PEM
    # caPEM: ""               # base64-encoded CA PEM (injected into MutatingWebhookConfiguration)

# -- CRD installation
installCRDs: true

# -- Metrics
metrics:
  enabled: true
  port: 8080
  serviceMonitor:
    enabled: false            # set true if Prometheus Operator is installed
    interval: 30s
    labels: {}

# -- Log level: debug | info | warn | error
logLevel: info

# -- Namespace where the controller and webhook are deployed
# (set automatically by Helm to .Release.Namespace)
```

---

## Installation

### Quick Start

```bash
helm install shekel-controller oci://ghcr.io/arieradle/shekel-controller \
  --namespace shekel-system \
  --create-namespace
```

### Without cert-manager

```bash
# Generate a self-signed cert
openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem \
  -days 365 -nodes \
  -subj "/CN=shekel-webhook.shekel-system.svc"

helm install shekel-controller oci://ghcr.io/arieradle/shekel-controller \
  --namespace shekel-system \
  --create-namespace \
  --set webhook.tls.certManager.enabled=false \
  --set-file webhook.tls.certPEM=cert.pem \
  --set-file webhook.tls.keyPEM=key.pem \
  --set-file webhook.tls.caPEM=cert.pem
```

### With custom values

```bash
helm install shekel-controller oci://ghcr.io/arieradle/shekel-controller \
  --namespace shekel-system \
  --create-namespace \
  -f my-values.yaml
```

---

## RBAC

### Controller ClusterRole

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: shekel-controller
rules:
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets"]
    verbs: ["get", "list", "watch", "patch", "update"]
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets/status"]
    verbs: ["get", "patch", "update"]
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets/finalizers"]
    verbs: ["update"]
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

### Webhook ClusterRole

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: shekel-webhook
rules:
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
```

### Pod Reporter Role (bind per-namespace)

This ClusterRole is installed by the Helm chart but must be bound by users in namespaces where agent pods run. The webhook can optionally create the RoleBinding automatically (controlled by `webhook.createPodReporterBindings`).

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: shekel-pod-reporter
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "watch"]
    # Limit to Budget ConfigMaps only (resourceNames with prefix not directly supported;
    # use a Role per-namespace instead for tighter scoping)
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["create", "get", "patch", "update"]
```

**Binding it in an application namespace:**

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: shekel-pod-reporter
  namespace: ai-agents
subjects:
  - kind: ServiceAccount
    name: default           # or a dedicated SA for agent pods
    namespace: ai-agents
roleRef:
  kind: ClusterRole
  name: shekel-pod-reporter
  apiGroup: rbac.authorization.k8s.io
```

---

## Namespace Configuration for Webhook

By default, the webhook watches all namespaces. Exclude a namespace from injection by labeling it:

```bash
kubectl label namespace kube-system shekel.io/inject=false
kubectl label namespace shekel-system shekel.io/inject=false
```

This is enforced by the `namespaceSelector` in the `MutatingWebhookConfiguration`:
```yaml
namespaceSelector:
  matchExpressions:
    - key: shekel.io/inject
      operator: NotIn
      values: ["false"]
```

---

## Resource Requirements

| Component | CPU Request | CPU Limit | Memory Request | Memory Limit |
|-----------|-------------|-----------|----------------|--------------|
| controller | 50m | 500m | 64Mi | 256Mi |
| webhook | 50m | 200m | 64Mi | 128Mi |

At scale (100+ ShekelBudgets, 1000+ pods reporting spend), increase controller memory to 512Mi and CPU to 1000m.

---

## High Availability

To run multiple replicas of the controller:

```yaml
controller:
  replicaCount: 2
```

Leader election is enabled by default. Only the leader reconciles; standby replicas monitor the Lease and take over within ~15 seconds of leader failure.

The webhook can run multiple replicas without leader election — all replicas serve admission requests:

```yaml
webhook:
  replicaCount: 2
```

For HA webhook deployments, use a `PodDisruptionBudget`:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: shekel-webhook-pdb
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app.kubernetes.io/component: webhook
      app.kubernetes.io/name: shekel-controller
```

---

## Upgrading

### CRD upgrades

CRDs are not automatically upgraded by `helm upgrade` when `installCRDs=true` (Helm does not update existing CRDs to avoid accidental data loss). Apply CRD updates manually:

```bash
kubectl apply -f https://github.com/arieradle/shekel-controller/releases/latest/download/crds.yaml
```

Then upgrade the Helm release:

```bash
helm upgrade shekel-controller oci://ghcr.io/arieradle/shekel-controller \
  --namespace shekel-system
```

### Controller rolling update

The controller Deployment uses `RollingUpdate` strategy with `maxUnavailable=0`. During a rolling update, the old leader continues reconciling until the new pod is ready and wins the leader election. Budget enforcement is uninterrupted.

### Webhook rolling update

The webhook Deployment uses `RollingUpdate` with `maxUnavailable=0`. Since `failurePolicy=Ignore`, pod admission is unaffected even if both webhook pods are momentarily unavailable during the update.

---

## Monitoring

The controller exposes Prometheus metrics at `:8080/metrics`:

| Metric | Description |
|--------|-------------|
| `shekel_reconcile_total` | Total reconcile calls, labeled by `result` (`success`/`error`) |
| `shekel_reconcile_duration_seconds` | Reconcile latency histogram |
| `shekel_budgets_total` | Gauge: number of ShekelBudget objects per state |
| `shekel_spend_aggregated_usd` | Gauge: current aggregated spend per budget |
| `shekel_kill_switch_activations_total` | Counter: kill-switch activations |
| `shekel_period_resets_total` | Counter: period resets |
| `shekel_redis_operations_total` | Counter: Redis operations by result |
| `shekel_redis_latency_seconds` | Histogram: Redis Lua script latency |

Enable the ServiceMonitor for Prometheus Operator:

```yaml
metrics:
  serviceMonitor:
    enabled: true
    labels:
      release: prometheus   # match your Prometheus Operator label selector
```
