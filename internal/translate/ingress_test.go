// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func fixture() *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "website", Namespace: "apps", UID: "source-uid"},
		Spec: networkingv1.IngressSpec{IngressClassName: ptr.To("public-apps"), Rules: []networkingv1.IngressRule{{
			Host: "website.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Path: "/", PathType: ptr.To(networkingv1.PathTypePrefix),
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "website", Port: networkingv1.ServiceBackendPort{Number: 80}}},
			}}}},
		}}},
	}
}

func binding() config.Binding {
	return config.Binding{ParentRefs: []config.Parent{{Name: "public", Namespace: "gateway-system", SectionName: "https"}}}
}

func convert(ingress *networkingv1.Ingress) ([]gatewayv1.HTTPRoute, error) {
	return Convert(ingress, binding(), nil, config.Config{ControllerName: config.ControllerName})
}

func TestLiteralPathSemantics(t *testing.T) {
	for _, tc := range []struct {
		path     string
		kind     networkingv1.PathType
		want     string
		wantKind gatewayv1.PathMatchType
	}{
		{"/wp-admin/", networkingv1.PathTypePrefix, "/wp-admin", gatewayv1.PathMatchPathPrefix},
		{"/admin", networkingv1.PathTypePrefix, "/admin", gatewayv1.PathMatchPathPrefix},
		{"/v1.0", networkingv1.PathTypePrefix, "/v1.0", gatewayv1.PathMatchPathPrefix},
		{"/exact/", networkingv1.PathTypeExact, "/exact/", gatewayv1.PathMatchExact},
		{"/", networkingv1.PathTypePrefix, "/", gatewayv1.PathMatchPathPrefix},
	} {
		t.Run(tc.path+string(tc.kind), func(t *testing.T) {
			ingress := fixture()
			ingress.Spec.Rules[0].HTTP.Paths[0].Path = tc.path
			ingress.Spec.Rules[0].HTTP.Paths[0].PathType = &tc.kind
			routes, err := convert(ingress)
			if err != nil {
				t.Fatal(err)
			}
			match := routes[0].Spec.Rules[0].Matches[0].Path
			if *match.Type != tc.wantKind || *match.Value != tc.want {
				t.Fatalf("got %+v", match)
			}
			if len(routes[0].Spec.Rules[0].Filters) != 0 {
				t.Fatal("unexpected rewrite")
			}
		})
	}
}

func TestHostsDoNotShareRules(t *testing.T) {
	ingress := fixture()
	second := ingress.Spec.Rules[0].DeepCopy()
	second.Host = "admin.example.com"
	second.HTTP.Paths[0].Path = "/private"
	second.HTTP.Paths[0].Backend.Service.Name = "admin"
	ingress.Spec.Rules = append(ingress.Spec.Rules, *second)
	routes, err := convert(ingress)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes", len(routes))
	}
	for _, route := range routes {
		if len(route.Spec.Rules) != 1 {
			t.Fatal("host inherited a rule")
		}
		if string(route.Spec.Hostnames[0]) == second.Host && route.Spec.Rules[0].BackendRefs[0].Name != "admin" {
			t.Fatal("wrong host backend")
		}
		owner := metav1.GetControllerOf(&route)
		if owner == nil || owner.UID != ingress.UID || *owner.BlockOwnerDeletion {
			t.Fatal("incorrect ownership")
		}
		if *route.Spec.ParentRefs[0].Namespace != "gateway-system" {
			t.Fatal("wrong target namespace")
		}
	}
}

func TestUnsupportedInputRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*networkingv1.Ingress)
	}{
		{"unknown bridge annotation", func(i *networkingv1.Ingress) {
			i.Annotations = map[string]string{AnnotationPrefix + "unknown": "private-value"}
		}},
		{"implementation specific", func(i *networkingv1.Ingress) {
			i.Spec.Rules[0].HTTP.Paths[0].PathType = ptr.To(networkingv1.PathTypeImplementationSpecific)
		}},
		{"wildcard host", func(i *networkingv1.Ingress) { i.Spec.Rules[0].Host = "*.example.com" }},
		{"default backend", func(i *networkingv1.Ingress) { i.Spec.DefaultBackend = &i.Spec.Rules[0].HTTP.Paths[0].Backend }},
		{"TLS", func(i *networkingv1.Ingress) { i.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "certificate"}} }},
		{"resource backend", func(i *networkingv1.Ingress) {
			p := &i.Spec.Rules[0].HTTP.Paths[0]
			p.Backend.Service = nil
			p.Backend.Resource = &corev1.TypedLocalObjectReference{Kind: "Bucket", Name: "bucket"}
		}},
		{"missing pathType", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].PathType = nil }},
		{"ambiguous ports", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Name = "http" }},
		{"out of range", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number = 65536 }},
		{"relative path", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "admin" }},
		{"double slash", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "/foo//bar" }},
		{"dot segment", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "/foo/../bar" }},
		{"query", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "/foo?bar" }},
		{"encoded slash", func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "/foo%2Fbar" }},
		{"too many rules", func(i *networkingv1.Ingress) {
			for n := 0; n < 17; n++ {
				p := i.Spec.Rules[0].HTTP.Paths[0]
				p.Path = "/" + strings.Repeat("a", n+1)
				i.Spec.Rules[0].HTTP.Paths = append(i.Spec.Rules[0].HTTP.Paths, p)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ingress := fixture()
			tc.change(ingress)
			routes, err := convert(ingress)
			var rejected *Rejected
			if !errors.As(err, &rejected) || routes != nil {
				t.Fatalf("expected rejection, got %v / %v", routes, err)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("annotation value leaked")
			}
		})
	}
}

func TestNamedPortUsesServicePortNotTargetPort(t *testing.T) {
	ingress := fixture()
	ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "http"}
	services := map[types.NamespacedName]*corev1.Service{{Namespace: "apps", Name: "website"}: {
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(9000)}}},
	}}
	routes, err := Convert(ingress, binding(), services, config.Config{ControllerName: config.ControllerName})
	if err != nil {
		t.Fatal(err)
	}
	if *routes[0].Spec.Rules[0].BackendRefs[0].Port != 8080 {
		t.Fatal("used targetPort instead of Service port")
	}
	delete(services, types.NamespacedName{Namespace: "apps", Name: "website"})
	_, err = Convert(ingress, binding(), services, config.Config{ControllerName: config.ControllerName})
	var rejected *Rejected
	if err == nil || errors.As(err, &rejected) {
		t.Fatal("missing dependency should be retryable")
	}
}

func TestExplicitTLSWithForeignCertificateAnnotations(t *testing.T) {
	ingress := fixture()
	ingress.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "not-copied"}}
	ingress.Annotations = map[string]string{"cert-manager.io/cluster-issuer": "issuer"}
	b := binding()
	b.TLSPolicy = "External"
	routes, err := Convert(ingress, b, nil, config.Config{ControllerName: config.ControllerName})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || len(routes[0].Annotations) != 2 {
		t.Fatal("source annotation was copied")
	}
}

func TestForeignAnnotationsDoNotChangeOrBlockTranslation(t *testing.T) {
	for _, key := range []string{
		"traefik.ingress.kubernetes.io/router.middlewares",
		"nginx.ingress.kubernetes.io/rewrite-target",
		"haproxy.org/path-rewrite",
		"haproxy-ingress.github.io/config-backend",
		"cloudflare-tunnel-ingress-controller.strrl.dev/disable-chunked-encoding",
		"objectset.rio.cattle.io/id",
		"cert-manager.io/cluster-issuer",
		"example.com/custom",
		// Similar-looking foreign namespaces must not be claimed by the bridge.
		"other." + AnnotationPrefix + "unknown",
	} {
		t.Run(key, func(t *testing.T) {
			ingress := fixture()
			baseline, err := convert(ingress)
			if err != nil {
				t.Fatal(err)
			}
			// Deliberately invalid provider syntax must remain opaque to the bridge.
			value := "[invalid provider syntax(\nnot: valid: yaml"
			ingress.Annotations = map[string]string{key: value}
			routes, err := convert(ingress)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(routes, baseline) {
				t.Fatal("foreign annotation changed generated routes")
			}
			if ingress.Annotations[key] != value {
				t.Fatal("source annotation was modified")
			}
		})
	}
}

func TestBridgeAnnotationErrorsAreDeterministicAndRedacted(t *testing.T) {
	ingress := fixture()
	ingress.Annotations = map[string]string{AnnotationPrefix + "z": "private-z", AnnotationPrefix + "a": "private-a"}
	_, err := convert(ingress)
	want := "unknown bridge Ingress annotations: " + AnnotationPrefix + "a, " + AnnotationPrefix + "z"
	if err == nil || err.Error() != want {
		t.Fatalf("unexpected annotation error: %v", err)
	}
}

func TestHTTPRequiresTCPButAllowsSharedUDPPort(t *testing.T) {
	ingress := fixture()
	key := types.NamespacedName{Namespace: "apps", Name: "website"}
	services := map[types.NamespacedName]*corev1.Service{key: {Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolUDP}}}}}
	if _, err := Convert(ingress, binding(), services, config.Config{ControllerName: config.ControllerName}); err == nil {
		t.Fatal("UDP-only backend accepted")
	}
	services[key].Spec.Ports = append(services[key].Spec.Ports, corev1.ServicePort{Port: 80, Protocol: corev1.ProtocolTCP})
	if _, err := Convert(ingress, binding(), services, config.Config{ControllerName: config.ControllerName}); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicatePaths(t *testing.T) {
	ingress := fixture()
	ingress.Spec.Rules[0].HTTP.Paths = append(ingress.Spec.Rules[0].HTTP.Paths, ingress.Spec.Rules[0].HTTP.Paths[0])
	routes, err := convert(ingress)
	if err != nil || len(routes[0].Spec.Rules) != 1 {
		t.Fatalf("duplicate not collapsed: %v", err)
	}
	ingress.Spec.Rules[0].HTTP.Paths[1].Backend.Service = &networkingv1.IngressServiceBackend{Name: "other", Port: networkingv1.ServiceBackendPort{Number: 80}}
	if _, err := convert(ingress); err == nil {
		t.Fatal("ambiguous backends accepted")
	}
}

func TestClassSelection(t *testing.T) {
	i := fixture()
	i.Annotations = map[string]string{LegacyClass: "legacy"}
	if _, err := ClassName(i); err == nil {
		t.Fatal("conflict accepted")
	}
	i.Spec.IngressClassName = nil
	if name, err := ClassName(i); name != "legacy" || err != nil {
		t.Fatal("legacy class missing")
	}
	i.Annotations = nil
	if name, err := ClassName(i); name != "" || err != nil {
		t.Fatal("implicit class selected")
	}
}

func TestRouteNameStabilityAndLength(t *testing.T) {
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63)
	name := RouteName(long, "website.example.com")
	if len(validation.IsDNS1123Subdomain(name)) != 0 || len(name) > 63 {
		t.Fatalf("invalid route name %s", name)
	}
	if name != RouteName(long, "website.example.com") || name == RouteName(long+"c", "website.example.com") || name == RouteName(long, "admin.example.com") {
		t.Fatal("unstable or colliding name")
	}
}
