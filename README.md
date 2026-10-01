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
and live data-plane validation. Middleware support and a bridge-specific input
annotation API are deferred. No upstream controller patches are required.

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
Unmapped classes and classes owned by another controller are not converted.

`tlsPolicy: External` means the operator has already configured TLS on the Gateway
or tunnel edge. The bridge does not copy Secrets, issue certificates, redirect HTTP,
create Gateways/GatewayClasses, or change DNS/tunnels. A backend port of `443` does
not imply backend HTTPS; configure the target implementation's backend TLS support
(for example, a supported BackendTLSPolicy) separately.

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
controller after changing bindings.

The [example controller manifests](examples/controller/) run in dry-run mode with
read-only RBAC. The optional [writer RBAC](examples/writer-rbac.yaml) and
`--dry-run=false` are both required to enable writes. These examples are generic;
none have been deployed by this project bootstrap.

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
changing authentication or other security features. Missing dependencies retain
the last applied route and are retried. Missing numeric-port backends are represented
as backendRefs; the Gateway controller reports `ResolvedRefs=False` when invalid.

The bridge owns only the Ingress annotation namespace
`ingress-gateway-bridge.colah16.github.io/`. No Ingress input keys are defined in
that namespace yet, so unknown keys are rejected. The `controller` and `source`
keys placed on generated HTTPRoutes are output metadata, not Ingress input options.
Future bridge input annotations must validate their own syntax and values.

NGINX, Traefik, HAProxy, cert-manager, Fleet, and other foreign annotations are
opaque to the bridge. They do not block conversion and are not copied to HTTPRoute.
The standard `kubernetes.io/ingress.class` annotation remains a class selector.
There is no annotation allowlist or `ignoredAnnotations` configuration. Changing
an Ingress to a bridge-owned class does not carry forward foreign authentication,
rewrite, or origin behavior; configure required behavior through the target Gateway
or future bridge-specific features before explicitly opting in.

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
