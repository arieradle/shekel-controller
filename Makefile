SHELL := /bin/bash

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.21.0
SETUP_ENVTEST_VERSION  ?= release-0.19

# Binaries (placed in local bin/ to avoid polluting PATH)
LOCALBIN     ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
SETUP_ENVTEST  ?= $(LOCALBIN)/setup-envtest

# Image
IMG ?= ghcr.io/arieradle/shekel-controller:latest

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run gofmt and goimports against code.
	gofmt -w .
	goimports -w .

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(abspath $(LOCALBIN)) -p path)" \
	  go test ./... -coverprofile cover.out

ENVTEST_K8S_VERSION ?= 1.31.0

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build controller binary.
	go build -o bin/manager ./cmd/...

.PHONY: docker-build
docker-build: ## Build docker image.
	docker build -t $(IMG) .

##@ Code Generation

.PHONY: manifests
manifests: controller-gen ## Generate CRD and RBAC manifests.
	$(CONTROLLER_GEN) rbac:roleName=shekel-controller-role crd:allowDangerousTypes=true webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

##@ Tools

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: envtest
envtest: $(SETUP_ENVTEST) ## Download setup-envtest locally if necessary.
$(SETUP_ENVTEST): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)
