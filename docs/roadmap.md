# Roadmap

1. **Initial bridge:** explicit class-to-existing-Gateway binding, standard host/path/
   Service routing, ownership and cleanup, bridge-only annotation validation,
   dry-run, API tests.
2. **An isolated live test:** review one application's Ingress and its existing
   Route before switching classes. Validate the actual target controller, including
   security and rollback. Deploy only after explicit approval.
3. **Standard Ingress spec coverage:** review remaining standard fields and define
   their behavior on the selected Gateway. Keep unsupported fields explicit. TLS
   belongs to Gateway listeners; do not imply that HTTPRoute carries certificates.
4. **Operational visibility and packaging:** configuration reload, source
   status/conditions, metrics, event deduplication, and packaging for upgrades.
5. **Explicit application opt-in:** move selected applications in reviewed groups after tests prove
   parity. Keep rollback manifests and remove temporary controllers/resources after
   the cutover is verified. TCP/UDP/gRPC require separately specified behavior.

Middleware CRD readers and translations of Traefik, NGINX, HAProxy, or other
controllers' annotations are outside the current scope. A bridge-specific annotation
API is deferred; if added later, only its documented keys and values will be validated.

Public examples remain generic. Infrastructure credentials, actual domains, tunnel
IDs, and production GitOps manifests belong in the separate operating repository.
