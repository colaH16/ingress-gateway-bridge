// SPDX-License-Identifier: Apache-2.0

//go:build integration

package controller

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// This test starts a local API server and etcd. It never uses an existing cluster.
func TestAPIServerReconciliation(t *testing.T) {
	crds := os.Getenv("GATEWAY_API_CRDS")
	if crds == "" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("run make test-integration to provide pinned Gateway API CRDs and envtest binaries")
	}
	environment := &envtest.Environment{
		UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths:  []string{crds}, ErrorIfCRDPathMissing: true,
	}
	restConfig, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := testScheme(t)
	apiClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager, err := ctrl.NewManager(restConfig, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	r := &IngressReconciler{Client: manager.GetClient(), Config: testConfig()}
	if err := r.SetupWithManager(ctx, manager); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager did not stop")
		}
	})
	syncContext, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	if !manager.GetCache().WaitForCacheSync(syncContext) {
		t.Fatal("cache did not sync")
	}
	ingress := source()
	ingress.UID = ""
	ingress.Annotations = map[string]string{
		"traefik.ingress.kubernetes.io/router.middlewares": "legacy-auth",
		"nginx.ingress.kubernetes.io/rewrite-target":       "/legacy",
		"objectset.rio.cattle.io/id":                       "management-metadata",
	}
	objects := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "public-apps"}, Spec: networkingv1.IngressClassSpec{Controller: r.Config.ControllerName}},
		&gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "apps"}, Spec: gatewayv1.GatewaySpec{GatewayClassName: "example", Listeners: []gatewayv1.Listener{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "website", Namespace: "apps"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}}},
		ingress,
	}
	for _, object := range objects {
		if err := apiClient.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	var route gatewayv1.HTTPRoute
	waitFor(t, func() bool {
		var routes gatewayv1.HTTPRouteList
		if err := apiClient.List(ctx, &routes, client.InNamespace("apps")); err != nil {
			t.Fatal(err)
		}
		if len(routes.Items) != 1 {
			return false
		}
		route = routes.Items[0]
		return true
	})
	if !r.owns(&route, ingress) {
		t.Fatal("API owner reference differs from the actual Ingress UID")
	}
	// Admission defaults must not cause an endless HTTPRoute update loop.
	rv := route.ResourceVersion
	for n := 0; n < 5; n++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: sourceKey}); err != nil {
			t.Fatal(err)
		}
		if err := apiClient.Get(ctx, client.ObjectKeyFromObject(&route), &route); err != nil {
			t.Fatal(err)
		}
		if route.ResourceVersion != rv {
			t.Fatal("API defaulting caused a spurious update")
		}
	}
	if err := apiClient.Get(ctx, sourceKey, ingress); err != nil {
		t.Fatal(err)
	}
	ingress.Spec.Rules[0].HTTP.Paths[0].Path = "/admin/"
	ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "http"}
	if err := apiClient.Update(ctx, ingress); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		if err := apiClient.Get(ctx, client.ObjectKeyFromObject(&route), &route); err != nil {
			t.Fatal(err)
		}
		return *route.Spec.Rules[0].Matches[0].Path.Value == "/admin"
	})
	var service corev1.Service
	if err := apiClient.Get(ctx, types.NamespacedName{Namespace: "apps", Name: "website"}, &service); err != nil {
		t.Fatal(err)
	}
	service.Spec.Ports[0].Port = 8080
	if err := apiClient.Update(ctx, &service); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		if err := apiClient.Get(ctx, client.ObjectKeyFromObject(&route), &route); err != nil {
			t.Fatal(err)
		}
		return *route.Spec.Rules[0].BackendRefs[0].Port == 8080
	})
	if err := apiClient.Get(ctx, sourceKey, ingress); err != nil {
		t.Fatal(err)
	}
	ingress.Spec.IngressClassName = ptr.To("another-controller")
	if err := apiClient.Update(ctx, ingress); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var routes gatewayv1.HTTPRouteList
		if err := apiClient.List(ctx, &routes, client.InNamespace("apps")); err != nil {
			t.Fatal(err)
		}
		return len(routes.Items) == 0
	})
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("controller did not reach the expected state")
}
