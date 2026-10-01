// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	"github.com/colaH16/ingress-gateway-bridge/internal/translate"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	classIndex   = "bridge.ingressClass"
	serviceIndex = "bridge.backendService"
)

type IngressReconciler struct {
	client.Client
	Config   config.Config
	DryRun   bool
	Recorder record.EventRecorder
}

func (r *IngressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ingress networkingv1.Ingress
	if err := r.Get(ctx, req.NamespacedName, &ingress); err != nil {
		// Owner references let Kubernetes garbage-collect routes after deletion.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ingress.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	existing, err := r.ownedRoutes(ctx, &ingress)
	if err != nil {
		return ctrl.Result{}, err
	}
	className, err := translate.ClassName(&ingress)
	if err != nil {
		return ctrl.Result{}, r.rejectSource(ctx, &ingress, existing, err)
	}
	binding, enabled := r.Config.Classes[className]
	if !enabled {
		return ctrl.Result{}, r.removeRoutes(ctx, existing)
	}
	var class networkingv1.IngressClass
	if err := r.Get(ctx, types.NamespacedName{Name: className}, &class); err != nil {
		return ctrl.Result{}, fmt.Errorf("read IngressClass %s: %w", className, err)
	}
	if class.Spec.Controller != r.Config.ControllerName {
		return ctrl.Result{}, r.removeRoutes(ctx, existing)
	}
	services := map[types.NamespacedName]*corev1.Service{}
	for _, name := range translate.ServiceNames(&ingress) {
		key := types.NamespacedName{Namespace: ingress.Namespace, Name: name}
		var service corev1.Service
		if err := r.Get(ctx, key, &service); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return ctrl.Result{}, err
		}
		services[key] = &service
	}
	desired, err := translate.Convert(&ingress, binding, services, r.Config)
	if err != nil {
		var rejected *translate.Rejected
		if errors.As(err, &rejected) {
			return ctrl.Result{}, r.rejectSource(ctx, &ingress, existing, err)
		}
		// A missing named Service/port is a dependency failure. Preserve the last
		// route and retry; a Service watch also triggers reconciliation.
		return ctrl.Result{}, err
	}
	if err := r.checkParents(ctx, ingress.Namespace, binding); err != nil {
		return ctrl.Result{}, err
	}
	// Validate all name collisions before writing any route for this Ingress.
	current := map[string]*gatewayv1.HTTPRoute{}
	for i := range desired {
		var route gatewayv1.HTTPRoute
		if err := r.Get(ctx, client.ObjectKeyFromObject(&desired[i]), &route); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return ctrl.Result{}, err
		}
		if !r.owns(&route, &ingress) {
			return ctrl.Result{}, fmt.Errorf("HTTPRoute name collision at %s/%s; refusing to adopt it", route.Namespace, route.Name)
		}
		current[route.Name] = &route
	}
	wanted := map[string]bool{}
	for i := range desired {
		route := &desired[i]
		wanted[route.Name] = true
		previous := current[route.Name]
		if previous == nil {
			if r.DryRun {
				log.FromContext(ctx).Info("HTTPRoute plan", "action", "create", "route", client.ObjectKeyFromObject(route), "spec", route.Spec)
			} else if err := r.Create(ctx, route); err != nil {
				return ctrl.Result{}, err
			}
			continue
		}
		next := previous.DeepCopy()
		next.Spec = route.Spec
		if next.Annotations == nil {
			next.Annotations = map[string]string{}
		}
		for key, value := range route.Annotations {
			next.Annotations[key] = value
		}
		if next.Labels == nil {
			next.Labels = map[string]string{}
		}
		for key, value := range route.Labels {
			next.Labels[key] = value
		}
		if apiequality.Semantic.DeepEqual(previous.Spec, next.Spec) &&
			apiequality.Semantic.DeepEqual(previous.Annotations, next.Annotations) &&
			apiequality.Semantic.DeepEqual(previous.Labels, next.Labels) {
			continue
		}
		if r.DryRun {
			log.FromContext(ctx).Info("HTTPRoute plan", "action", "update", "route", client.ObjectKeyFromObject(next), "spec", next.Spec)
		} else if err := r.Update(ctx, next); err != nil {
			return ctrl.Result{}, err
		}
	}
	var stale []gatewayv1.HTTPRoute
	for _, route := range existing {
		if !wanted[route.Name] {
			stale = append(stale, route)
		}
	}
	return ctrl.Result{}, r.removeRoutes(ctx, stale)
}

func (r *IngressReconciler) owns(route *gatewayv1.HTTPRoute, ingress *networkingv1.Ingress) bool {
	owner := metav1.GetControllerOf(route)
	return owner != nil && owner.APIVersion == networkingv1.SchemeGroupVersion.String() &&
		owner.Kind == "Ingress" && owner.UID == ingress.UID && owner.Name == ingress.Name &&
		route.Annotations[translate.ControllerAnnotation] == r.Config.ControllerName
}

func (r *IngressReconciler) ownedRoutes(ctx context.Context, ingress *networkingv1.Ingress) ([]gatewayv1.HTTPRoute, error) {
	var routes gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routes, client.InNamespace(ingress.Namespace)); err != nil {
		return nil, err
	}
	var owned []gatewayv1.HTTPRoute
	for _, route := range routes.Items {
		if r.owns(&route, ingress) {
			owned = append(owned, route)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	return owned, nil
}

func (r *IngressReconciler) removeRoutes(ctx context.Context, routes []gatewayv1.HTTPRoute) error {
	for i := range routes {
		route := &routes[i]
		if r.DryRun {
			log.FromContext(ctx).Info("HTTPRoute plan", "action", "delete", "route", client.ObjectKeyFromObject(route))
			continue
		}
		uid, rv := route.UID, route.ResourceVersion
		err := r.Delete(ctx, route, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
		if client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func (r *IngressReconciler) rejectSource(ctx context.Context, ingress *networkingv1.Ingress, routes []gatewayv1.HTTPRoute, err error) error {
	log.FromContext(ctx).Error(err, "Ingress conversion rejected; withdrawing bridge-owned routes", "ingress", client.ObjectKeyFromObject(ingress), "dryRun", r.DryRun)
	if !r.DryRun && r.Recorder != nil {
		r.Recorder.Event(ingress, corev1.EventTypeWarning, "ConversionRejected", err.Error())
	}
	return r.removeRoutes(ctx, routes)
}

// The destination controller remains responsible for listener hostname matching,
// certificate validation, and HTTPRoute Accepted/ResolvedRefs/Programmed status.
func (r *IngressReconciler) checkParents(ctx context.Context, namespace string, binding config.Binding) error {
	for _, parent := range binding.ParentRefs {
		var gateway gatewayv1.Gateway
		key := types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name}
		if err := r.Get(ctx, key, &gateway); err != nil {
			return fmt.Errorf("read Gateway %s: %w", key, err)
		}
		eligible := false
		for _, listener := range gateway.Spec.Listeners {
			if parent.SectionName != "" && string(listener.Name) != parent.SectionName {
				continue
			}
			if listener.Protocol != gatewayv1.HTTPProtocolType && listener.Protocol != gatewayv1.HTTPSProtocolType {
				continue
			}
			allowed := listener.AllowedRoutes
			if allowed != nil && len(allowed.Kinds) != 0 {
				acceptsHTTP := false
				for _, kind := range allowed.Kinds {
					if kind.Kind == "HTTPRoute" && (kind.Group == nil || *kind.Group == gatewayv1.Group(gatewayv1.GroupName)) {
						acceptsHTTP = true
					}
				}
				if !acceptsHTTP {
					continue
				}
			}
			from := gatewayv1.NamespacesFromSame
			if allowed != nil && allowed.Namespaces != nil && allowed.Namespaces.From != nil {
				from = *allowed.Namespaces.From
			}
			switch from {
			case gatewayv1.NamespacesFromAll:
				eligible = true
			case gatewayv1.NamespacesFromSame:
				eligible = namespace == gateway.Namespace
			case gatewayv1.NamespacesFromSelector:
				if allowed.Namespaces.Selector == nil {
					continue
				}
				selector, err := metav1.LabelSelectorAsSelector(allowed.Namespaces.Selector)
				if err != nil {
					return err
				}
				var ns corev1.Namespace
				if err := r.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
					return err
				}
				eligible = selector.Matches(labels.Set(ns.Labels))
			}
			if eligible {
				break
			}
		}
		if !eligible {
			return fmt.Errorf("Gateway %s has no selected HTTP(S) listener that allows namespace %s", key, namespace)
		}
	}
	return nil
}

func (r *IngressReconciler) SetupWithManager(ctx context.Context, manager ctrl.Manager) error {
	if err := manager.GetFieldIndexer().IndexField(ctx, &networkingv1.Ingress{}, classIndex, func(object client.Object) []string {
		name, _ := translate.ClassName(object.(*networkingv1.Ingress))
		return []string{name}
	}); err != nil {
		return err
	}
	if err := manager.GetFieldIndexer().IndexField(ctx, &networkingv1.Ingress{}, serviceIndex, func(object client.Object) []string {
		ingress := object.(*networkingv1.Ingress)
		var keys []string
		for _, name := range translate.ServiceNames(ingress) {
			keys = append(keys, ingress.Namespace+"/"+name)
		}
		return keys
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(manager).
		For(&networkingv1.Ingress{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.forService)).
		Watches(&networkingv1.IngressClass{}, handler.EnqueueRequestsFromMapFunc(r.forClass)).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.forGateway)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.forNamespace)).
		Complete(r)
}

func (r *IngressReconciler) requests(ctx context.Context, options ...client.ListOption) []reconcile.Request {
	var ingresses networkingv1.IngressList
	if err := r.List(ctx, &ingresses, options...); err != nil {
		log.FromContext(ctx).Error(err, "list Ingresses for dependency change")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(ingresses.Items))
	for i := range ingresses.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ingresses.Items[i])})
	}
	return requests
}

func (r *IngressReconciler) forService(ctx context.Context, object client.Object) []reconcile.Request {
	return r.requests(ctx, client.MatchingFields{serviceIndex: object.GetNamespace() + "/" + object.GetName()})
}

func (r *IngressReconciler) forClass(ctx context.Context, object client.Object) []reconcile.Request {
	return r.requests(ctx, client.MatchingFields{classIndex: object.GetName()})
}

func (r *IngressReconciler) forGateway(ctx context.Context, object client.Object) []reconcile.Request {
	var requests []reconcile.Request
	for name, binding := range r.Config.Classes {
		for _, parent := range binding.ParentRefs {
			if parent.Name == object.GetName() && parent.Namespace == object.GetNamespace() {
				requests = append(requests, r.requests(ctx, client.MatchingFields{classIndex: name})...)
				break
			}
		}
	}
	return requests
}

func (r *IngressReconciler) forNamespace(ctx context.Context, object client.Object) []reconcile.Request {
	return r.requests(ctx, client.InNamespace(object.GetName()))
}
