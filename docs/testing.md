# Testing boundaries

`make test` covers host isolation, Exact/Prefix literal semantics, named Service
ports, class selection, unsupported features, ownership collisions, idempotence,
source preservation, stale route cleanup, dry-run writes, listener namespace/kind
permissions, and dependency event mapping. It runs the Go race detector and vet.

`make test-integration` downloads the official controller-runtime envtest tools,
starts a **local** Kubernetes 1.35.0 API server and etcd, installs Gateway API 1.5.1
CRDs from the pinned Go module, and runs the manager with real informers. It checks:

- HTTPRoute schema admission and owner references using the real Ingress UID.
- API defaulting does not cause a repeated update loop.
- An Ingress update reaches the route without an explicit Reconcile invocation.
- A named Service port update reaches the route through the Service watch.
- Changing the source to an unmapped class removes its owned route.

The integration test explicitly disables use of an existing cluster. The local API
server has no kube-controller-manager, garbage collector, or Gateway implementation.
Owner references are verified, but actual garbage collection and client HTTP/TLS
traffic are not covered by envtest. Multi-platform image builds verify compilation
for AMD64/ARM64, not runtime behavior on ARM hardware.

Before a production migration, use an isolated application and check the actual
Cilium/Cloudflare implementations for path precedence, redirects, certificates,
backend TLS, HTTP headers, request bodies, WebSocket/gRPC requirements, access
control, source updates/deletion, and rollback. A rendered or admitted HTTPRoute
does not prove data-plane behavior. The project bootstrap does not perform a live
migration or deploy the controller to the operating cluster.
