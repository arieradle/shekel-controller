# Mutating Admission Webhook

## Overview

The mutating admission webhook intercepts `Pod` creation requests and injects shekel configuration into matching pods. This makes budget enforcement zero-touch for application developers: they deploy pods with a label, and the system automatically configures the shekel library inside.

The webhook is a separate Deployment from the controller, exposing an HTTPS endpoint that the Kubernetes API server calls synchronously during pod admission.

---

## How It Works

```
Developer creates Pod
        │
        ▼
Kubernetes API Server
        │
        │  AdmissionReview (CREATE Pod)
        ▼
Mutating Webhook Server
        │
        ├─ Find ShekelBudget whose selector matches this pod's labels
        │   └─ If none match → admit unchanged (pass through)
        │
        ├─ Check that Budget ConfigMap exists in pod's namespace
        │   └─ If missing → admit unchanged (fail-open on missing config)
        │
        ├─ Build JSON Patch:
        │   └─ inject env vars into each container
        │
        ▼
Kubernetes API Server applies patch → Pod admitted with shekel config
```

---

## Matching Logic

For each incoming pod, the webhook:

1. Lists all `ShekelBudget` resources in the pod's namespace.
2. For each ShekelBudget, evaluates `spec.selector` against the pod's labels using standard Kubernetes label selector semantics.
3. If exactly one ShekelBudget matches: inject from that budget's ConfigMap.
4. If multiple ShekelBudgets match: inject from the most specific one (most `matchLabels` keys). If still ambiguous, inject from the lexicographically first budget name and emit a Warning event.
5. If no ShekelBudget matches: admit the pod unchanged.

---

## Injected Environment Variables

The webhook injects env vars using `valueFrom.configMapKeyRef` so that the values stay live — if the controller updates the Budget ConfigMap (e.g., to activate the kill-switch), running pods pick up the change without restart.

```yaml
env:
  - name: AGENT_BUDGET_USD
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: AGENT_BUDGET_USD
  - name: SHEKEL_BUDGET_NAME
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: SHEKEL_BUDGET_NAME
  - name: SHEKEL_KILL_SWITCH
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: SHEKEL_KILL_SWITCH
  - name: REDIS_URL
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: REDIS_URL
        optional: true            # omitted if Redis not configured
  - name: SHEKEL_WARN_AT
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: SHEKEL_WARN_AT
        optional: true
  - name: SHEKEL_FALLBACK_AT_PCT
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: SHEKEL_FALLBACK_AT_PCT
        optional: true
  - name: SHEKEL_FALLBACK_MODEL
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: SHEKEL_FALLBACK_MODEL
        optional: true
  - name: SHEKEL_NAMESPACE
    value: "{pod-namespace}"
  - name: SHEKEL_POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name  # pod's own name, for spend report identification
```

**Per-group mode:** When `scope.mode=per-group`, the webhook additionally injects:
```yaml
  - name: SHEKEL_GROUP_BY
    valueFrom:
      configMapKeyRef:
        name: shekel-budget-{budget-name}
        key: SHEKEL_GROUP_BY
  - name: SHEKEL_GROUP_VALUE
    valueFrom:
      fieldRef:
        fieldPath: metadata.labels['{groupBy-label-key}']
```

**Existing env vars:** If a pod already has `AGENT_BUDGET_USD` set (e.g., set directly in the Deployment spec), the webhook skips injection for that variable. Explicit pod configuration always wins over webhook injection.

---

## Opt-Out

Pods can opt out of injection by adding the annotation:
```yaml
metadata:
  annotations:
    shekel.io/inject: "false"
```

Alternatively, namespace-level opt-out can be configured by annotating the namespace:
```yaml
metadata:
  annotations:
    shekel.io/inject: "false"
```

---

## Spend Report Volume

In addition to env vars, the webhook mounts a projected volume that gives the pod a writable path for spend reporting:

```yaml
volumes:
  - name: shekel-spend
    emptyDir: {}
volumeMounts:
  - name: shekel-spend
    mountPath: /var/shekel
```

The shekel library uses this path as a local spend cache. This is supplementary to the ConfigMap-based reporting mechanism (see [library-integration.md](library-integration.md)) and improves reliability if the Kubernetes API is temporarily unreachable.

---

## MutatingWebhookConfiguration

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: shekel-pod-injector
  annotations:
    cert-manager.io/inject-ca-from: "{namespace}/shekel-webhook-cert"
webhooks:
  - name: pod-injector.shekel.io
    admissionReviewVersions: ["v1"]
    clientConfig:
      service:
        name: shekel-webhook
        namespace: "{controller-namespace}"
        path: /mutate-v1-pod
      caBundle: ""              # injected by cert-manager
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["pods"]
        scope: "Namespaced"
    namespaceSelector:
      matchExpressions:
        - key: shekel.io/inject
          operator: NotIn
          values: ["false"]
    objectSelector:
      matchExpressions:
        - key: shekel.io/inject
          operator: NotIn
          values: ["false"]
    failurePolicy: Ignore       # fail-open: if webhook is down, pods are admitted unchanged
    sideEffects: None
    timeoutSeconds: 5
```

**`failurePolicy: Ignore`** — the webhook is on the critical path for pod admission. If the webhook is unavailable, pods should still be admitted (they just won't have shekel config injected). This is the correct default for a cost-governance layer; it should never block workloads.

---

## TLS — cert-manager Integration

The webhook server requires a TLS certificate trusted by the Kubernetes API server. cert-manager manages this automatically.

```yaml
# Self-signed issuer (suitable for most clusters)
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: shekel-selfsigned-issuer
  namespace: "{controller-namespace}"
spec:
  selfSigned: {}

---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: shekel-webhook-cert
  namespace: "{controller-namespace}"
spec:
  secretName: shekel-webhook-tls
  issuerRef:
    name: shekel-selfsigned-issuer
  dnsNames:
    - shekel-webhook.{controller-namespace}.svc
    - shekel-webhook.{controller-namespace}.svc.cluster.local
```

The `MutatingWebhookConfiguration` carries the `cert-manager.io/inject-ca-from` annotation pointing to the Certificate. cert-manager patches the `caBundle` field automatically when the certificate is issued or rotated.

### Without cert-manager

For clusters without cert-manager, the Helm chart accepts `webhook.tls.certPEM` and `webhook.tls.keyPEM` values. These are stored in a Secret and mounted into the webhook pod. The `caBundle` in the `MutatingWebhookConfiguration` must be set manually to the CA that signed the certificate.

---

## Webhook Server Implementation

The webhook server is a minimal HTTPS server implementing a single handler:

```
POST /mutate-v1-pod
  Content-Type: application/json
  Body: AdmissionReview

  Response: AdmissionReview with response.allowed=true and response.patch (base64 JSON Patch)
```

The server:
- Decodes the incoming `AdmissionReview`
- Extracts the pod object from `request.object`
- Runs the matching logic (list ShekelBudgets, evaluate selectors)
- If a match: reads the Budget ConfigMap from the cache (the webhook shares the controller-runtime informer cache), builds the JSON Patch, returns it
- If no match: returns `allowed=true` with no patch

The webhook uses the same informer cache as the controller. `ShekelBudget` and `ConfigMap` objects are cached; no live API calls are made during pod admission (critical for latency).

---

## RBAC Requirements

```yaml
rules:
  # Read ShekelBudgets to evaluate selectors
  - apiGroups: ["shekel.io"]
    resources: ["shekelbudgets"]
    verbs: ["get", "list", "watch"]

  # Read Budget ConfigMaps to verify they exist before injecting
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]

  # Emit Warning events for ambiguous selector matches
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
```
