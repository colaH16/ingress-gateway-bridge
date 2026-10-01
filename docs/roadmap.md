# Roadmap

1. **Initial bridge:** explicit class-to-existing-Gateway binding, standard host/path/
   Service routing, ownership and cleanup, strict rejection, dry-run, API tests.
2. **An isolated live test:** review one application's Ingress and its existing
   Route before switching classes. Validate the actual target controller, including
   security and rollback. Deploy only after explicit approval.
3. **Provider adapters:** inventory real annotations and middleware references;
   support standard HTTPRoute filters where semantics are equivalent. Never silently
   omit BasicAuth/ForwardAuth or other required behavior. Unsupported provider
   features need an explicit replacement design.
4. **TLS and operational visibility:** deliberate certificate/BackendTLSPolicy
   integration, class parameters/configuration reload, source status/conditions,
   metrics, event deduplication, and packaging for upgrades.
5. **Broader migration:** move applications in reviewed groups after tests prove
   parity. Keep rollback manifests and remove temporary controllers/resources after
   the cutover is verified. TCP/UDP/gRPC require separately specified behavior.

Public examples remain generic. Infrastructure credentials, actual domains, tunnel
IDs, and production GitOps manifests belong in the separate operating repository.
