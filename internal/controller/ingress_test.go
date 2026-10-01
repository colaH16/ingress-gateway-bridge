// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"reflect"
	"testing"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	"github.com/colaH16/ingress-gateway-bridge/internal/translate"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var sourceKey = types.NamespacedName{Namespace: "apps", Name: "website"}

func source() *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: sourceKey.Name, Namespace: sourceKey.Namespace, UID: "original-source"},
		Spec: networkingv1.IngressSpec{IngressClassName: ptr.To("public-apps"), Rules: []networkingv1.IngressRule{{
			Host: "website.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Path: "/", PathType: ptr.To(networkingv1.PathTypePrefix),
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "website", Port: networkingv1.ServiceBackendPort{Number: 80}}},
			}}}},
		}}},
	}
}

func testConfig() config.Config {
	return config.Config{ControllerName: config.ControllerName, Classes: map[string]config.Binding{
		"public-apps": {ParentRefs: []config.Parent{{Name: "public", Namespace: "apps", SectionName: "http"}}},
	}}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, networkingv1.AddToScheme, gatewayv1.Install} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func setup(t *testing.T, additional ...client.Object) (*IngressReconciler, *countingClient) {
	t.Helper()
	objects := []client.Object{
		source(),
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "public-apps"}, Spec: networkingv1.IngressClassSpec{Controller: config.ControllerName}},
		&gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "apps"}, Spec: gatewayv1.GatewaySpec{GatewayClassName: "example", Listeners: []gatewayv1.Listener{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}}}},
	}
	objects = append(objects, additional...)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).
		WithIndex(&networkingv1.Ingress{}, classIndex, func(o client.Object) []string {
			name, _ := translate.ClassName(o.(*networkingv1.Ingress))
			return []string{name}
		}).
		WithIndex(&networkingv1.Ingress{}, serviceIndex, func(o client.Object) []string {
			var keys []string
			for _, name := range translate.ServiceNames(o.(*networkingv1.Ingress)) {
				keys = append(keys, o.GetNamespace()+"/"+name)
			}
			return keys
		}).Build()
	counted := &countingClient{Client: c}
	return &IngressReconciler{Client: counted, Config: testConfig()}, counted
}

type countingClient struct {
	client.Client
	creates, updates, deletes int
}

func (c *countingClient) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	c.creates++
	return c.Client.Create(ctx, o, opts...)
}
func (c *countingClient) Update(ctx context.Context, o client.Object, opts ...client.UpdateOption) error {
	c.updates++
	return c.Client.Update(ctx, o, opts...)
}
func (c *countingClient) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	c.deletes++
	return c.Client.Delete(ctx, o, opts...)
}

func reconcileSource(t *testing.T, r *IngressReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: sourceKey}); err != nil {
		t.Fatal(err)
	}
}

func listRoutes(t *testing.T, c client.Client) []gatewayv1.HTTPRoute {
	t.Helper()
	var routes gatewayv1.HTTPRouteList
	if err := c.List(context.Background(), &routes); err != nil {
		t.Fatal(err)
	}
	return routes.Items
}

func changeSource(t *testing.T, c client.Client, modify func(*networkingv1.Ingress)) {
	t.Helper()
	var ingress networkingv1.Ingress
	if err := c.Get(context.Background(), sourceKey, &ingress); err != nil {
		t.Fatal(err)
	}
	modify(&ingress)
	if err := c.Update(context.Background(), &ingress); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUpdateAndNoOp(t *testing.T) {
	r, c := setup(t)
	reconcileSource(t, r)
	routes := listRoutes(t, c)
	if len(routes) != 1 || !r.owns(&routes[0], source()) {
		t.Fatal("route not owned by source")
	}
	reconcileSource(t, r)
	if c.creates != 1 || c.updates != 0 || c.deletes != 0 {
		t.Fatal("idempotent reconcile wrote to API")
	}
	changeSource(t, c.Client, func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "/new" })
	reconcileSource(t, r)
	if c.updates != 1 || *listRoutes(t, c)[0].Spec.Rules[0].Matches[0].Path.Value != "/new" {
		t.Fatal("path change not reconciled")
	}
	var ingress networkingv1.Ingress
	if err := c.Get(context.Background(), sourceKey, &ingress); err != nil {
		t.Fatal(err)
	}
	expected := source()
	expected.Spec.Rules[0].HTTP.Paths[0].Path = "/new"
	if !reflect.DeepEqual(ingress.Spec, expected.Spec) || ingress.UID != expected.UID {
		t.Fatal("source Ingress was mutated")
	}
}

func TestHostRemovalOnlyDeletesOwnedRoute(t *testing.T) {
	r, c := setup(t, &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "manual-route", Namespace: "apps"}})
	reconcileSource(t, r)
	oldName := translate.RouteName(sourceKey.Name, "website.example.com")
	changeSource(t, c.Client, func(i *networkingv1.Ingress) { i.Spec.Rules[0].Host = "new.example.com" })
	reconcileSource(t, r)
	routes := listRoutes(t, c)
	if len(routes) != 2 || c.deletes != 1 {
		t.Fatal("unexpected cleanup")
	}
	for _, route := range routes {
		if route.Name == oldName {
			t.Fatal("stale host retained")
		}
	}
}

func TestUnmappedClassWithdrawsOwnedRoutes(t *testing.T) {
	r, c := setup(t)
	reconcileSource(t, r)
	changeSource(t, c.Client, func(i *networkingv1.Ingress) { i.Spec.IngressClassName = ptr.To("other-controller") })
	reconcileSource(t, r)
	if len(listRoutes(t, c)) != 0 {
		t.Fatal("old route still active")
	}
}

func TestIngressClassOwnershipChangeWithdrawsRoutes(t *testing.T) {
	r, c := setup(t)
	reconcileSource(t, r)
	var class networkingv1.IngressClass
	if err := c.Get(context.Background(), types.NamespacedName{Name: "public-apps"}, &class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Controller = "example.com/other"
	if err := c.Client.Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	reconcileSource(t, r)
	if len(listRoutes(t, c)) != 0 {
		t.Fatal("continued handling another controller's class")
	}
}

func TestUnsupportedAnnotationWithdrawsOnlyOwnedRoutes(t *testing.T) {
	r, c := setup(t, &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "manual-route", Namespace: "apps"}})
	r.Recorder = record.NewFakeRecorder(10)
	reconcileSource(t, r)
	changeSource(t, c.Client, func(i *networkingv1.Ingress) {
		i.Annotations = map[string]string{"traefik.ingress.kubernetes.io/router.middlewares": "auth"}
	})
	reconcileSource(t, r)
	routes := listRoutes(t, c)
	if len(routes) != 1 || routes[0].Name != "manual-route" {
		t.Fatal("unsupported authentication was silently dropped")
	}
	select {
	case <-r.Recorder.(*record.FakeRecorder).Events:
	default:
		t.Fatal("rejection event missing")
	}
}

func TestDryRunNeverWritesEvenForCleanup(t *testing.T) {
	r, c := setup(t)
	reconcileSource(t, r)
	c.creates, c.updates, c.deletes = 0, 0, 0
	r.DryRun = true
	r.Recorder = record.NewFakeRecorder(10)
	changeSource(t, c.Client, func(i *networkingv1.Ingress) { i.Spec.Rules[0].HTTP.Paths[0].Path = "/changed" })
	reconcileSource(t, r)
	changeSource(t, c.Client, func(i *networkingv1.Ingress) { i.Annotations = map[string]string{"example.com/auth": "value"} })
	reconcileSource(t, r)
	if c.creates+c.updates+c.deletes != 0 || len(listRoutes(t, c)) != 1 {
		t.Fatal("dry-run wrote to API")
	}
	select {
	case <-r.Recorder.(*record.FakeRecorder).Events:
		t.Fatal("dry-run wrote an event")
	default:
	}
	r2, c2 := setup(t)
	r2.DryRun = true
	reconcileSource(t, r2)
	if c2.creates+c2.updates+c2.deletes != 0 || len(listRoutes(t, c2)) != 0 {
		t.Fatal("dry-run created a route")
	}
}

func TestUnownedCollisionNotAdopted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		owner      bool
		uid        types.UID
		annotation string
	}{
		{"manual", false, "", ""},
		{"old ingress UID", true, "different-source", config.ControllerName},
		{"different bridge", true, source().UID, "example.com/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes, err := translate.Convert(source(), testConfig().Classes["public-apps"], nil, testConfig())
			if err != nil {
				t.Fatal(err)
			}
			route := routes[0]
			route.OwnerReferences = nil
			if tc.owner {
				route.OwnerReferences = []metav1.OwnerReference{{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Name: sourceKey.Name, UID: tc.uid, Controller: ptr.To(true)}}
			}
			route.Annotations[translate.ControllerAnnotation] = tc.annotation
			r, c := setup(t, &route)
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: sourceKey}); err == nil {
				t.Fatal("collision accepted")
			}
			if c.creates+c.updates+c.deletes != 0 {
				t.Fatal("collision mutated resources")
			}
		})
	}
}

func TestMissingNamedPortPreservesLastRoute(t *testing.T) {
	r, c := setup(t)
	reconcileSource(t, r)
	changeSource(t, c.Client, func(i *networkingv1.Ingress) {
		i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "http"}
	})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: sourceKey}); err == nil {
		t.Fatal("missing dependency accepted")
	}
	if len(listRoutes(t, c)) != 1 || c.deletes != 0 {
		t.Fatal("dependency failure removed working route")
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "website", Namespace: "apps"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}}
	if err := c.Client.Create(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	reconcileSource(t, r)
	if *listRoutes(t, c)[0].Spec.Rules[0].BackendRefs[0].Port != 8080 {
		t.Fatal("named port not resolved")
	}
	requests := r.forService(context.Background(), svc)
	if len(requests) != 1 || requests[0].NamespacedName != sourceKey {
		t.Fatal("Service event did not requeue source")
	}
	svc.Spec.Ports[0].Port = 8081
	if err := c.Client.Update(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	reconcileSource(t, r)
	if *listRoutes(t, c)[0].Spec.Rules[0].BackendRefs[0].Port != 8081 {
		t.Fatal("named port change not applied")
	}
}

func TestParentNamespaceAndKindPermission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		allowed  *gatewayv1.AllowedRoutes
		nsLabels map[string]string
		want     bool
	}{
		{"same default denied", nil, nil, false},
		{"all", &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromAll)}}, nil, true},
		{"selector allowed", &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromSelector), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"exposure": "public"}}}}, map[string]string{"exposure": "public"}, true},
		{"selector denied", &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromSelector), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"exposure": "public"}}}}, nil, false},
		{"other kind", &gatewayv1.AllowedRoutes{Kinds: []gatewayv1.RouteGroupKind{{Kind: "TCPRoute"}}, Namespaces: &gatewayv1.RouteNamespaces{From: ptr.To(gatewayv1.NamespacesFromAll)}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := setup(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: tc.nsLabels}}, &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "gateway-system"}, Spec: gatewayv1.GatewaySpec{GatewayClassName: "example", Listeners: []gatewayv1.Listener{{Name: "http", Protocol: gatewayv1.HTTPProtocolType, Port: 80, AllowedRoutes: tc.allowed}}}})
			r.Config.Classes["public-apps"] = config.Binding{ParentRefs: []config.Parent{{Name: "other", Namespace: "gateway-system", SectionName: "http"}}}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: sourceKey})
			if (err == nil) != tc.want {
				t.Fatalf("expected allowed=%v, got %v", tc.want, err)
			}
			if !tc.want && c.creates != 0 {
				t.Fatal("route created for disallowed listener")
			}
		})
	}
}

func TestWatchDependencyMappings(t *testing.T) {
	r, _ := setup(t)
	ctx := context.Background()
	if len(r.forClass(ctx, &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "public-apps"}})) != 1 {
		t.Fatal("class watch missing")
	}
	if len(r.forGateway(ctx, &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "apps"}})) != 1 {
		t.Fatal("Gateway watch missing")
	}
	if len(r.forGateway(ctx, &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "apps"}})) != 0 {
		t.Fatal("unrelated Gateway selected")
	}
	if len(r.forNamespace(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}})) != 1 {
		t.Fatal("namespace selector watch missing")
	}
}
