# ingress-gateway-bridge

A Kubernetes controller that reconciles Ingress resources into Gateway API HTTPRoutes.
Applications keep their Ingress manifests; operators explicitly bind selected
bridge-owned IngressClasses to existing Gateways.

```text
Application Ingress
    │ ingressClassName
    ▼
ingress-gateway-bridge
    │ class → Gateway parentRefs
    ▼
HTTPRoute in the application's namespace
    ├── an existing Cilium Gateway
    └── an existing Cloudflare Gateway
```

**Status: initial development version.** The bridge handles explicitly selected
IngressClasses and standard routing fields. It is not a universal converter for
existing Ingress controllers. Current development focuses on Ingress `spec` routing
and live data-plane validation. Middleware support is deferred. Optional bridge-owned annotations preserve
HTTP-to-HTTPS redirects and request/response headers. No upstream controller patches are required.

## Supported subset

| Ingress feature | Initial behavior |
| --- | --- |
| Explicit `ingressClassName` | Bound to pre-existing Gateway parentRefs |
| Legacy class annotation | Supported if it does not conflict with the field |
| No class | Ignored; no implicit default class |
| Literal hostname or no hostname | One HTTPRoute per distinct host |
| `Exact` | Gateway API `Exact`; preserves trailing slash |
| `Prefix` | Gateway API `PathPrefix`; element-based, not substring or regex matching |
| Service + numeric port | Preserved as a Service backendRef |
| Service + named port | Resolved to the Service's `port`, never `targetPort` |
| Ingress TLS | Rejected unless the binding explicitly uses `tlsPolicy: External` |
| NGINX, Traefik, HAProxy, and other foreign annotations | Ignored, even with invalid provider syntax; preserved on the source |
| Unknown bridge-specific annotation | Rejected, with its key reported |
| `ImplementationSpecific`, wildcard hosts, `defaultBackend`, resource backends | Rejected until their semantics have an explicit implementation |

Each selected IngressClass must have
`spec.controller: ingress-gateway-bridge.colah16.github.io/controller`.
Do not share that class with another active Ingress controller.
The binding selects a **Gateway object**, not a GatewayClass or a tunnel by hostname.
Unmapped classes, deleted IngressClasses, and classes owned by another controller
are not converted; their bridge-owned Routes are withdrawn.

`tlsPolicy: External` means the operator has already configured TLS on the Gateway
or tunnel edge. The bridge does not copy Secrets, issue certificates,
create Gateways/GatewayClasses, or change DNS/tunnels. A backend port of `443` does
not imply backend HTTPS; configure the target implementation's backend TLS support
(for example, a supported BackendTLSPolicy) separately.

## Optional redirect and header annotations

All keys use the `ingress-gateway-bridge.colah16.github.io/` prefix:

| Key | Value and behavior |
| --- | --- |
| `https-redirect` | Literal `"true"` or `"false"`; default false |
| `https-redirect-status-code` | `"301"` or `"302"`; default 302; requires redirect enabled |
| `request-headers` | JSON object with Gateway API header filter `set`, `add`, and/or `remove` |
| `response-headers` | Same JSON format, applied to responses |

Header filters apply to every forwarding rule in that Ingress. Split Ingresses if
paths need different headers. Header names are case-insensitive; duplicate names
across operations, unknown/duplicate JSON fields, empty filters, invalid header
syntax, and values exceeding Gateway API limits are rejected. Errors report keys,
never annotation values. Malformed bridge options withdraw only that source's
bridge-owned Routes. Foreign controller annotations remain opaque.

HTTPS redirects require `spec.tls`, `tlsPolicy: External`, and separately configured
HTTP listener references. They create an additional owned HTTPRoute per host,
matching `/`, preserving host, path, and query while redirecting to HTTPS port 443.
The bridge validates HTTP port 80 redirect listeners and HTTPS port 443 forwarding
listeners before writes. A class with `httpRedirectParentRefs` must also set
`sectionName` on every forwarding `parentRefs` entry, so forwarding never attaches
to the HTTP listener. No redirect is added unless the Ingress opts in:

```yaml
# Class binding (operator configuration)
classes:
  public-apps:
    tlsPolicy: External
    parentRefs:
    - name: public
      namespace: gateway-system
      sectionName: https
    httpRedirectParentRefs:
    - name: public
      namespace: gateway-system
      sectionName: http
```

```yaml
# Ingress metadata; spec.tls references an existing certificate.
annotations:
  ingress-gateway-bridge.colah16.github.io/https-redirect: "true"
  ingress-gateway-bridge.colah16.github.io/request-headers: >-
    {"set":[{"name":"X-Forwarded-Proto","value":"https"}]}
  ingress-gateway-bridge.colah16.github.io/response-headers: >-
    {"set":[{"name":"X-Content-Type-Options","value":"nosniff"}],"remove":["Server"]}
```

The target Gateway implementation must support the selected filters. The bridge
does not configure Gateway certificates, copy Secrets, or infer TLS from a backend
port. A Gateway proxy may add its own `Server` header after filtering the upstream
response; verify the observable response with your implementation.

## Build and test

Go 1.25 or later is required. The image and CI currently use Go 1.27.1.
Dependencies are pinned to controller-runtime 0.23.3 and Gateway API 1.5.1.

```sh
make test              # unit tests, race detector, vet
make test-integration  # local API server + etcd, downloaded by official envtest
make build
```

Integration tests always set `UseExistingCluster: false`. They do not use your
current kubeconfig or run a Gateway data plane.

## Preview without writes

Copy [the example bindings](examples/controller/bridge.yaml) to `bridge.local.yaml`,
then supply your own class and existing Gateway names:

```sh
./bin/ingress-gateway-bridge --config bridge.local.yaml --dry-run=true
```

The controller reads Ingresses, IngressClasses, Services, Namespaces, Gateways, and
HTTPRoutes, and logs planned route creates/updates/deletes. Dry-run does not write
Routes, Events, status, finalizers, or leader-election Leases. Leader election is
therefore rejected in dry-run mode. Configuration is read at startup; restart the
controller after changing bindings. Plan logs contain the complete planned Route
spec, including `request-headers`/`response-headers` values; do not put secrets in
those annotations, or restrict access to the controller logs accordingly.

The [example controller manifests](examples/controller/) run in dry-run mode with
read-only RBAC. The optional [writer RBAC](examples/writer-rbac.yaml) and
`--dry-run=false` are both required to enable writes. These examples are generic;
none have been deployed by this project bootstrap.

## Install the controller

The controller currently ships example Kubernetes manifests, not a Helm chart.
The initial implementation is on `feat/controller-bootstrap`; `main` does not
contain the controller yet. Start from that branch while it is under development:

```sh
git clone --branch feat/controller-bootstrap https://github.com/colaH16/ingress-gateway-bridge.git
cd ingress-gateway-bridge
```

Prerequisites:

- Gateway API CRDs and a working Cilium or Cloudflare Gateway implementation.
- An existing Gateway with listeners that allow HTTPRoutes from the application's
  namespace and match its hostnames. The bridge does not install either component.
- Existing application Services and permission to install the example RBAC.
- A published controller image your nodes can pull. Use a tested image digest
  when enabling writes; a successful build is not a data-plane validation.

First edit `examples/controller/bridge.yaml`: each class maps to an existing
Gateway's name, namespace, and optional listener `sectionName`. References to the
same Gateway within a class must either all set `sectionName` or all omit it,
as required by the HTTPRoute schema. Keep the class
names in `examples/controller/ingressclasses.yaml` consistent. If an Ingress has
`spec.tls`, select `tlsPolicy: External` only after the Gateway or tunnel edge's
TLS is configured. Ingress TLS does not configure a Gateway certificate.

Install the read-only preview (the example defaults to `--dry-run=true`):

```sh
kubectl kustomize examples/controller
kubectl apply -k examples/controller
kubectl rollout status deployment/ingress-gateway-bridge -n ingress-gateway-bridge
kubectl logs deployment/ingress-gateway-bridge -n ingress-gateway-bridge
```

Review the rendered resources before applying them. A source Ingress selecting a
mapped class is required to see a planned conversion. In dry-run mode no
HTTPRoute is created, updated, or deleted.

To enable HTTPRoute creation:

1. Grant writer permissions. `examples/writer-rbac.yaml` is a cluster-wide
   example; installations scoped to specific application namespaces should use
   namespace Roles/RoleBindings for its HTTPRoute and Event rules instead.
2. In `examples/controller/deployment.yaml`, change `--dry-run=true` to
   `--dry-run=false`, pin the tested image digest, and set
   `spec.strategy.type: Recreate` for the single writer replica. Multiple writers
   require leader election and its Lease permissions.
3. Apply the RBAC and the controller manifests, then wait for readiness:

```sh
# If the cluster-wide example writer scope is appropriate for your installation:
kubectl apply -f examples/writer-rbac.yaml
kubectl apply -k examples/controller
kubectl rollout status deployment/ingress-gateway-bridge -n ingress-gateway-bridge
```

Adapt `examples/ingress.yaml` to your Services, application namespace, hostnames,
and mapped class, then apply it. Ensure the Gateway's `allowedRoutes` permits that
namespace. For an existing manually managed HTTPRoute, withdraw that manifest and
confirm its removal before switching the source Ingress; the bridge will not
adopt a Route owned by another manager.

```sh
kubectl apply -f examples/ingress.yaml
kubectl get httproute -n demo-apps
kubectl describe httproute -n demo-apps
kubectl get events -n demo-apps --field-selector involvedObject.kind=Ingress
```

Check that generated Routes have an ownerReference to the source Ingress and
`Accepted=True` / `ResolvedRefs=True` from the target implementation, then test
HTTP requests through the actual Gateway. Do not apply or commit generated
HTTPRoutes as application input. Binding changes require a controller restart;
the example ConfigMap generator changes the Deployment's ConfigMap reference
when you reapply the kustomization.

To withdraw an application, delete or change its source Ingress first and verify
its bridge-owned Routes disappear. Uninstalling only the controller does not
remove existing Routes. Remove its manifests and writer RBAC after applications
are withdrawn. Keep your Gateway implementation, Gateways, Services, DNS, and
certificates under their existing management.

## Resource lifecycle and rejection

Generated HTTPRoutes have a controller ownerReference to the source Ingress UID.
The bridge updates its own route specs, removes stale hosts or unmapped classes,
and refuses to adopt any route it does not own. Kubernetes garbage collection
removes owned Routes after source deletion. There is no keep-resources setting or
bridge finalizer. Uninstalling the bridge itself does not delete routes while the
source Ingress still exists; withdraw routes explicitly before uninstalling it.

An invalid bridge annotation or unsupported standard Ingress field withdraws
**only bridge-owned Routes** and
produces a warning (an Event in write mode). This is not a guarantee of access
denial: other catch-all Routes may still match. Audit the target Gateway before
changing authentication or other security features. Paths containing characters
the HTTPRoute schema does not accept (for example spaces or non-ASCII) are rejected
the same way.

Deleting a mapped IngressClass, or changing its `spec.controller`, withdraws
bridge-owned Routes; recreating the class restores them through the class watch.
Missing dependencies retain the last applied route and are retried: a missing
named-port Service or port, a missing or non-permitting Gateway, and transient API
read failures (including timeouts and `Forbidden`). An IngressClass read failure
other than NotFound is also retried rather than treated as a deletion. Unsupported
fields are reported even when another path's dependency is still missing. Missing numeric-port backends are represented
as backendRefs; the Gateway controller reports `ResolvedRefs=False` when invalid.

The bridge owns only the Ingress annotation namespace
`ingress-gateway-bridge.colah16.github.io/`. Only the documented input keys below are accepted; unknown keys are rejected. The `controller` and `source`
keys placed on generated HTTPRoutes are output metadata, not Ingress input options.
Known input annotations validate their syntax and values.

NGINX, Traefik, HAProxy, cert-manager, Fleet, and other foreign annotations are
opaque to the bridge. They do not block conversion and are not copied to HTTPRoute.
The standard `kubernetes.io/ingress.class` annotation remains a class selector.
There is no annotation allowlist or `ignoredAnnotations` configuration. Changing
an Ingress to a bridge-owned class does not carry forward foreign authentication,
rewrite, or origin behavior; configure required behavior through the target Gateway
or the documented bridge options before explicitly opting in.

Read [the design](docs/design.md), [test boundaries](docs/testing.md), and
[roadmap](docs/roadmap.md) before a live migration.

## Images

[GitHub Actions](.github/workflows/ci.yaml) runs the tests before building and
publishing `linux/amd64` and `linux/arm64` images to:

```text
ghcr.io/colah16/ingress-gateway-bridge
```

Pushes to `main`, `feat/**`, `fix/**`, and `v*` tags publish branch/version and full
commit-SHA tags. Pull requests build without publishing. The workflow uses the
built-in `GITHUB_TOKEN`; no PAT is required. Actions are pinned to commit SHAs and
Dependabot proposes updates. No automatic deployment is configured.

GitHub may initially create a private GHCR package even for a public repository.
If anonymous pulls are needed, set the package visibility to public after its first
publication; source repository visibility alone does not make that package public.

## License

[Apache-2.0](LICENSE).
