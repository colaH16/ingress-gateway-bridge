// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	"github.com/colaH16/ingress-gateway-bridge/internal/translate"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestRedirectLifecycleAndParentValidation(t *testing.T) {
	r, c := setup(t)
	ctx := context.Background()
	b := r.Config.Classes["public-apps"]
	b.TLSPolicy = "External"
	b.ParentRefs[0].SectionName = "https"
	b.HTTPRedirectParentRefs = []config.Parent{{Name: "public", Namespace: "apps", SectionName: "http"}}
	r.Config.Classes["public-apps"] = b
	var gw gatewayv1.Gateway
	if err := c.Get(ctx, client.ObjectKey{Name: "public", Namespace: "apps"}, &gw); err != nil {
		t.Fatal(err)
	}
	gw.Spec.Listeners = append(gw.Spec.Listeners, gatewayv1.Listener{Name: "https", Protocol: gatewayv1.HTTPSProtocolType, Port: 443})
	if err := c.Client.Update(ctx, &gw); err != nil {
		t.Fatal(err)
	}
	changeSource(t, c.Client, func(i *networkingv1.Ingress) {
		i.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "existing"}}
		i.Annotations = map[string]string{translate.HTTPSRedirectAnnotation: "true", translate.ResponseHeadersAnnotation: `{"remove":["Server"]}`}
	})
	reconcileSource(t, r)
	if len(listRoutes(t, c)) != 2 {
		t.Fatal("redirect not created")
	}
	beforeCreates, beforeUpdates, beforeDeletes := c.creates, c.updates, c.deletes
	reconcileSource(t, r)
	if c.creates != beforeCreates || c.updates != beforeUpdates || c.deletes != beforeDeletes {
		t.Fatal("redirect reconcile not idempotent")
	}
	if err := r.checkParents(ctx, "apps", b, true); err != nil {
		t.Fatal(err)
	}
	gw.Spec.Listeners[0].Port = 8080
	if err := c.Client.Update(ctx, &gw); err != nil {
		t.Fatal(err)
	}
	if err := r.checkParents(ctx, "apps", b, true); err == nil {
		t.Fatal("redirect on wrong port accepted")
	}
	changeSource(t, c.Client, func(i *networkingv1.Ingress) { delete(i.Annotations, translate.HTTPSRedirectAnnotation) })
	reconcileSource(t, r)
	if len(listRoutes(t, c)) != 1 {
		t.Fatal("stale redirect retained")
	}
	manual := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "apps"}}
	if err := c.Client.Create(ctx, manual); err != nil {
		t.Fatal(err)
	}
	changeSource(t, c.Client, func(i *networkingv1.Ingress) {
		i.Annotations[translate.ResponseHeadersAnnotation] = `{"unknown":"private"}`
	})
	reconcileSource(t, r)
	routes := listRoutes(t, c)
	if len(routes) != 1 || routes[0].Name != "manual" {
		t.Fatal("invalid input cleanup affected manual route")
	}
}

func TestRedirectGatewayWatch(t *testing.T) {
	r, _ := setup(t)
	b := r.Config.Classes["public-apps"]
	b.HTTPRedirectParentRefs = []config.Parent{{Name: "redirect-only", Namespace: "apps", SectionName: "http"}}
	r.Config.Classes["public-apps"] = b
	requests := r.forGateway(context.Background(), &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "redirect-only", Namespace: "apps"}})
	if len(requests) != 1 || requests[0].NamespacedName != sourceKey {
		t.Fatal("redirect Gateway change not watched")
	}
}
