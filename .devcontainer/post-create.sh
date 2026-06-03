#!/usr/bin/env bash
set -euo pipefail

echo "==> Installing Go tools for Kubernetes controller development"

# controller-gen: generates CRD manifests and RBAC from Go types
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

# kustomize: for manifest overlays
go install sigs.k8s.io/kustomize/kustomize/v5@latest

# setup-envtest: for running controller-runtime integration tests
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

# goimports: import formatting
go install golang.org/x/tools/cmd/goimports@latest

# golangci-lint: linter
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
  | sh -s -- -b "$(go env GOPATH)/bin" latest

echo "==> Done"
