# Design

## Ownership boundaries

The bridge owns generated HTTPRoutes. Application owners continue to own their
Ingress objects. Infrastructure operators own IngressClasses, binding configuration,
Gateways/GatewayClasses, listener permissions, certificates, backend TLS policies,
DNS, and Cloudflare tunnels.

The bridge handles only classes present in its explicit binding configuration and
owned by its IngressClass controller name. It does not convert every class. Foreign
NGINX/Traefik/HAProxy annotations, including malformed values, remain on the source
and are neither interpreted nor copied to the generated Route. Only annotations
in `ingress-gateway-bridge.colah16.github.io/` belong to the bridge's validation
contract. No bridge-specific Ingress input keys are defined yet; unknown keys are
rejected. Future known keys must reject invalid syntax and values.

One installation handles multiple classes through a single configuration file.
If several writer replicas are used, enable leader election and grant Lease
permissions. All replicas in an installation must use the same configuration.
Separate installations must use different controller names, class sets, and
leader-election IDs; the initial CLI currently uses a fixed leader-election ID,
so it is intended for one installation per cluster.

No Helm post-render patches, upstream image modifications, custom CRD, or admission
webhook are required for the initial version.

## Translation

1. Read the Ingress's explicit class (field, or non-conflicting legacy annotation).
2. Require both an explicit binding and the bridge's IngressClass controller name.
3. Validate the supported source subset before constructing any route.
4. Group rules by literal host. Preserve each host's distinct paths and backends.
5. Convert Exact/Prefix to Exact/PathPrefix; resolve named Service ports.
6. Set explicit parentRefs from the binding, including namespace and optional listener.
7. Set an Ingress UID ownerReference and bridge identification annotations.

One HTTPRoute per host avoids a common error: putting several hostnames on a route
whose rules combine unrelated hosts' paths. Route names contain a bounded prefix and
a hash of the **full** source name plus host. A hostname change creates a new Route
and then removes the old one. Identical duplicate matches are collapsed; conflicting
backends for the same match are rejected. More than 16 rules per host are rejected
instead of emitting a resource that violates the Gateway API schema.

Prefix trailing slashes are normalized. `/admin/` becomes `PathPrefix: /admin`, which
matches `/admin` and `/admin/settings`, but not `/admin-tools` or `/foo/admin` in a
conforming implementation. Dots remain literal: `/v1.0` is not a regular expression.
Exact trailing slashes remain significant. The bridge adds no regex anchors or
provider-specific path syntax to HTTPRoute.

Wildcard hosts are initially rejected because Ingress wildcards match one DNS
label while Gateway API wildcard hostname behavior can cover multiple labels.
ImplementationSpecific paths cannot be inferred from the path text alone.
Default backends require a deliberate policy for their shared-Gateway scope.

## Reconciliation and events

The controller watches Ingresses (including annotation-only updates), owned
HTTPRoutes, backend Services, IngressClasses, Gateways, and Namespace labels.
Service and class indexes narrow dependency events to relevant Ingresses.

All desired route names are checked for ownership before mutation. The complete
spec of an owned route is managed by the bridge; other metadata is preserved.
Gateway defaulted fields are explicitly emitted to avoid continuous API updates.
Deletes use UID and resourceVersion preconditions. Writes across several HTTPRoutes
are **not atomic**; a later API error can leave an intermediate state until retry.

Invalid bridge annotations and unsupported standard source fields withdraw
bridge-owned routes and report a rejection.
Transient read or named-port resolution failures leave the last route in place.
Gateways/listener permissions are checked before writes, but the destination
controller decides hostname intersections, certificates, and final Route acceptance.
The bridge does not set Ingress loadBalancer status. Inspect HTTPRoute parent status
for `Accepted` and `ResolvedRefs` and Gateway status for `Programmed`.

Same-path ties across separate Ingress objects follow the destination Gateway's
HTTPRoute precedence, not a legacy controller's private conflict-resolution rules.
Security isolation and access denial require an audited Gateway configuration;
route withdrawal alone cannot prevent another catch-all route from matching.

## Existing converter assessment

The Kubernetes SIGs project [ingress2gateway](https://github.com/kubernetes-sigs/ingress2gateway)
was inspected at v1.0.0. It is a batch migration tool that groups Ingresses, generates
Gateways, and translates provider annotations. Its common converter is not a
drop-in reconciliation engine for individually owned routes bound to pre-existing
Gateways. The initial bridge uses a small purpose-built standard-field converter,
without copying upstream implementation code. Translating the existing annotation
languages of Traefik, NGINX, HAProxy, or other controllers is outside this project's
scope; new optional features will use the bridge's own input contract.

References:

- [Gateway API migration guide](https://gateway-api.sigs.k8s.io/guides/getting-started/migrating-from-ingress/)
- [HTTPRoute](https://gateway-api.sigs.k8s.io/reference/api-types/httproute/)
- [Gateway API 1.5 specification](https://gateway-api.sigs.k8s.io/reference/api-spec/1.5/spec/)
- [controller-runtime version compatibility](https://github.com/kubernetes-sigs/controller-runtime#versioning-maintenance-and-compatibility)
