// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	ControllerAnnotation = "ingress-gateway-bridge.colah16.github.io/controller"
	SourceAnnotation     = "ingress-gateway-bridge.colah16.github.io/source"
	LegacyClass          = "kubernetes.io/ingress.class"
)

// Rejected means the source cannot be represented by the supported subset.
// The reconciler withdraws only its own routes; it never ignores such features.
type Rejected struct{ Reason string }

func (e *Rejected) Error() string { return e.Reason }

func reject(format string, args ...any) error {
	return &Rejected{Reason: fmt.Sprintf(format, args...)}
}

func ClassName(ingress *networkingv1.Ingress) (string, error) {
	legacy := ingress.Annotations[LegacyClass]
	if ingress.Spec.IngressClassName != nil {
		name := *ingress.Spec.IngressClassName
		if legacy != "" && legacy != name {
			return "", reject("ingressClassName conflicts with the legacy class annotation")
		}
		return name, nil
	}
	return legacy, nil
}

// Convert is a pure translation. It neither fetches nor modifies Kubernetes objects.
// Each host gets its own route so a host cannot inherit another host's paths.
func Convert(ingress *networkingv1.Ingress, binding config.Binding,
	services map[types.NamespacedName]*corev1.Service, cfg config.Config) ([]gatewayv1.HTTPRoute, error) {
	if ingress.UID == "" {
		return nil, reject("source Ingress must have a UID")
	}
	if err := checkAnnotations(ingress.Annotations, cfg.IgnoredAnnotations); err != nil {
		return nil, err
	}
	if len(ingress.Spec.TLS) != 0 && binding.TLSPolicy != "External" {
		return nil, reject("Ingress TLS requires tlsPolicy: External and preconfigured TLS; secrets are not copied")
	}
	if ingress.Spec.DefaultBackend != nil {
		return nil, reject("defaultBackend is not supported yet; configure an explicit host/path rule")
	}
	if len(ingress.Spec.Rules) == 0 {
		return nil, reject("at least one HTTP rule is required")
	}
	rulesByHost := map[string][]gatewayv1.HTTPRouteRule{}
	seenByHost := map[string]map[string]gatewayv1.HTTPBackendRef{}
	for _, rule := range ingress.Spec.Rules {
		if strings.Contains(rule.Host, "*") {
			return nil, reject("wildcard Ingress hosts have different matching semantics from Gateway API and are not supported yet")
		}
		if rule.Host != "" && len(validation.IsDNS1123Subdomain(rule.Host)) != 0 {
			return nil, reject("invalid host in Ingress")
		}
		if rule.HTTP == nil || len(rule.HTTP.Paths) == 0 {
			return nil, reject("every rule must contain HTTP paths")
		}
		if seenByHost[rule.Host] == nil {
			seenByHost[rule.Host] = map[string]gatewayv1.HTTPBackendRef{}
		}
		for _, path := range rule.HTTP.Paths {
			match, err := pathMatch(path)
			if err != nil {
				return nil, err
			}
			backend, err := backendRef(ingress.Namespace, path.Backend, services)
			if err != nil {
				return nil, err
			}
			key := string(*match.Type) + ":" + *match.Value
			if previous, exists := seenByHost[rule.Host][key]; exists {
				if previous.Name != backend.Name || *previous.Port != *backend.Port {
					return nil, reject("duplicate host/path points to different backends")
				}
				continue
			}
			seenByHost[rule.Host][key] = backend
			rulesByHost[rule.Host] = append(rulesByHost[rule.Host], gatewayv1.HTTPRouteRule{
				Matches:     []gatewayv1.HTTPRouteMatch{{Path: &match}},
				BackendRefs: []gatewayv1.HTTPBackendRef{backend},
			})
		}
	}
	hosts := make([]string, 0, len(rulesByHost))
	for host, rules := range rulesByHost {
		if len(rules) > 16 {
			return nil, reject("a host exceeds the Gateway API limit of 16 rules per HTTPRoute")
		}
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	routes := make([]gatewayv1.HTTPRoute, 0, len(hosts))
	for _, host := range hosts {
		route := gatewayv1.HTTPRoute{
			TypeMeta: metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "HTTPRoute"},
			ObjectMeta: metav1.ObjectMeta{
				Name: RouteName(ingress.Name, host), Namespace: ingress.Namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "ingress-gateway-bridge"},
				Annotations: map[string]string{
					ControllerAnnotation: cfg.ControllerName,
					SourceAnnotation:     ingress.Namespace + "/" + ingress.Name,
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: "Ingress",
					Name: ingress.Name, UID: ingress.UID, Controller: ptr.To(true),
					// False avoids requiring permission to change Ingress finalizers.
					BlockOwnerDeletion: ptr.To(false),
				}},
			},
			Spec: gatewayv1.HTTPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: binding.References()},
				Rules:           rulesByHost[host],
			},
		}
		if host != "" {
			route.Spec.Hostnames = []gatewayv1.Hostname{gatewayv1.Hostname(host)}
		}
		routes = append(routes, route)
	}
	return routes, nil
}

// A fixed hash of the full source name and host avoids truncation and wildcard collisions.
func RouteName(ingressName, host string) string {
	sum := sha256.Sum256([]byte(ingressName + "\x00" + host))
	stem := ingressName
	if len(stem) > 39 {
		stem = stem[:39]
	}
	stem = strings.TrimRight(stem, "-.")
	return fmt.Sprintf("%s-bridge-%x", stem, sum[:8])
}

func pathMatch(path networkingv1.HTTPIngressPath) (gatewayv1.HTTPPathMatch, error) {
	if path.PathType == nil {
		return gatewayv1.HTTPPathMatch{}, reject("pathType is required")
	}
	var kind gatewayv1.PathMatchType
	switch *path.PathType {
	case networkingv1.PathTypeExact:
		kind = gatewayv1.PathMatchExact
	case networkingv1.PathTypePrefix:
		kind = gatewayv1.PathMatchPathPrefix
	default:
		return gatewayv1.HTTPPathMatch{}, reject("ImplementationSpecific paths need an explicit provider adapter and are not supported yet")
	}
	if !strings.HasPrefix(path.Path, "/") || strings.Contains(path.Path, "//") ||
		strings.Contains(path.Path, "%2f") || strings.Contains(path.Path, "%2F") ||
		strings.ContainsAny(path.Path, "?#") || strings.Contains(path.Path, "/../") ||
		strings.Contains(path.Path, "/./") || strings.HasSuffix(path.Path, "/..") || strings.HasSuffix(path.Path, "/.") || len(path.Path) > 1024 {
		return gatewayv1.HTTPPathMatch{}, reject("path cannot be represented as a Gateway API literal path")
	}
	value := path.Path
	if kind == gatewayv1.PathMatchPathPrefix && value != "/" {
		value = strings.TrimRight(value, "/")
	}
	return gatewayv1.HTTPPathMatch{Type: &kind, Value: &value}, nil
}

func backendRef(namespace string, backend networkingv1.IngressBackend,
	services map[types.NamespacedName]*corev1.Service) (gatewayv1.HTTPBackendRef, error) {
	if backend.Resource != nil || backend.Service == nil {
		return gatewayv1.HTTPBackendRef{}, reject("only Service backends are supported")
	}
	svc := backend.Service
	if len(validation.IsDNS1035Label(svc.Name)) != 0 {
		return gatewayv1.HTTPBackendRef{}, reject("invalid backend Service name")
	}
	if (svc.Port.Name == "") == (svc.Port.Number == 0) {
		return gatewayv1.HTTPBackendRef{}, reject("backend must have exactly one numeric or named Service port")
	}
	port := svc.Port.Number
	if svc.Port.Name != "" {
		service := services[types.NamespacedName{Namespace: namespace, Name: svc.Name}]
		if service == nil {
			return gatewayv1.HTTPBackendRef{}, fmt.Errorf("named-port backend Service %s/%s is not available", namespace, svc.Name)
		}
		for _, candidate := range service.Spec.Ports {
			if candidate.Name == svc.Port.Name {
				if candidate.Protocol != "" && candidate.Protocol != corev1.ProtocolTCP {
					return gatewayv1.HTTPBackendRef{}, reject("HTTPRoute requires a TCP Service port")
				}
				port = candidate.Port
				break
			}
		}
		if port == 0 {
			return gatewayv1.HTTPBackendRef{}, fmt.Errorf("named Service port is not available for %s/%s", namespace, svc.Name)
		}
	}
	if port < 1 || port > 65535 {
		return gatewayv1.HTTPBackendRef{}, reject("backend Service port must be 1..65535")
	}
	if service := services[types.NamespacedName{Namespace: namespace, Name: svc.Name}]; service != nil {
		found, tcp := false, false
		for _, candidate := range service.Spec.Ports {
			if candidate.Port == port {
				found = true
				tcp = tcp || candidate.Protocol == "" || candidate.Protocol == corev1.ProtocolTCP
			}
		}
		if found && !tcp {
			return gatewayv1.HTTPBackendRef{}, reject("HTTPRoute requires a TCP Service port")
		}
	}
	gatewayPort := gatewayv1.PortNumber(port)
	group, kind := gatewayv1.Group(""), gatewayv1.Kind("Service")
	return gatewayv1.HTTPBackendRef{BackendRef: gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Name: gatewayv1.ObjectName(svc.Name), Port: &gatewayPort, Group: &group, Kind: &kind,
		},
		Weight: ptr.To(int32(1)),
	}}, nil
}

func checkAnnotations(annotations map[string]string, ignored []string) error {
	allowed := map[string]bool{
		LegacyClass: true,
		"kubectl.kubernetes.io/last-applied-configuration": true,
		"meta.helm.sh/release-name":                        true, "meta.helm.sh/release-namespace": true,
		"field.cattle.io/publicEndpoints": true,
	}
	for _, name := range ignored {
		allowed[name] = true
	}
	var unsupported []string
	for name := range annotations {
		if !allowed[name] {
			unsupported = append(unsupported, name)
		}
	}
	sort.Strings(unsupported)
	if len(unsupported) != 0 {
		// Never include annotation values: they may contain credentials or URLs.
		return reject("unsupported annotations: %s", strings.Join(unsupported, ", "))
	}
	return nil
}

func ServiceNames(ingress *networkingv1.Ingress) []string {
	names := map[string]bool{}
	if backend := ingress.Spec.DefaultBackend; backend != nil && backend.Service != nil {
		names[backend.Service.Name] = true
	}
	for _, rule := range ingress.Spec.Rules {
		if rule.HTTP != nil {
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil {
					names[path.Backend.Service.Name] = true
				}
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
