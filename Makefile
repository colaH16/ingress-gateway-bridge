GO ?= go
ENVTEST_VERSION := v0.0.0-20260305142021-f9589b9f2b9d
ENVTEST_K8S_VERSION := 1.35.0

.PHONY: test test-integration build fmt

test:
	$(GO) test -race -p 2 ./...
	$(GO) vet -p 2 ./...

test-integration:
	@set -eu; \
	assets="$$($(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION) use -p path $(ENVTEST_K8S_VERSION))"; \
	crds="$$($(GO) list -m -f '{{.Dir}}' sigs.k8s.io/gateway-api)/config/crd/standard"; \
	KUBEBUILDER_ASSETS="$$assets" GATEWAY_API_CRDS="$$crds" \
	$(GO) test -tags=integration -p 2 ./internal/controller -run TestAPIServerReconciliation -count=1

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/ingress-gateway-bridge ./cmd/ingress-gateway-bridge

fmt:
	gofmt -w cmd internal
