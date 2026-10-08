// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"errors"
	"strings"
	"testing"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRedirectAndHeaders(t *testing.T) {
	i := fixture()
	i.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "existing-certificate"}}
	second := i.Spec.Rules[0].DeepCopy()
	second.Host = "second.example.com"
	i.Spec.Rules = append(i.Spec.Rules, *second)
	i.Annotations = map[string]string{
		HTTPSRedirectAnnotation:   "true",
		RequestHeadersAnnotation:  `{"set":[{"name":"X-Forwarded-Proto","value":"https"}]}`,
		ResponseHeadersAnnotation: `{"set":[{"name":"Strict-Transport-Security","value":"max-age=15552000; includeSubDomains; preload"}],"add":[{"name":"X-Example","value":"one"}],"remove":["Server"]}`,
	}
	b := binding()
	b.TLSPolicy = "External"
	b.HTTPRedirectParentRefs = []config.Parent{{Name: "public", Namespace: "gateway-system", SectionName: "http"}}
	routes, err := Convert(i, b, nil, config.Config{ControllerName: config.ControllerName})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 {
		t.Fatalf("want two forward and two redirect routes, got %d", len(routes))
	}
	for _, route := range routes {
		host := string(route.Spec.Hostnames[0])
		if owner := metav1.GetControllerOf(&route); owner == nil || owner.UID != i.UID {
			t.Fatal("lost source owner")
		}
		rule := route.Spec.Rules[0]
		if route.Name == RedirectRouteName(i.Name, host) {
			if string(*route.Spec.ParentRefs[0].SectionName) != "http" || len(rule.BackendRefs) != 0 || len(rule.Filters) != 1 {
				t.Fatal("redirect mixed with forwarding")
			}
			redirect := rule.Filters[0].RequestRedirect
			if redirect == nil || *redirect.Scheme != "https" || *redirect.Port != 443 || *redirect.StatusCode != 302 || *rule.Matches[0].Path.Value != "/" {
				t.Fatal("incorrect redirect")
			}
		} else {
			if route.Name != RouteName(i.Name, host) || string(*route.Spec.ParentRefs[0].SectionName) != "https" {
				t.Fatal("forward identity changed")
			}
			if len(rule.Filters) != 2 || rule.Filters[0].RequestHeaderModifier.Set[0].Value != "https" || rule.Filters[1].ResponseHeaderModifier.Remove[0] != "Server" {
				t.Fatal("headers lost")
			}
		}
	}
	// An explicit false disables only the redirect; the forwarding route is stable.
	i.Annotations[HTTPSRedirectAnnotation] = "false"
	routes, err = Convert(i, b, nil, config.Config{ControllerName: config.ControllerName})
	if err != nil || len(routes) != 2 {
		t.Fatalf("disabled redirect: %v / %d", err, len(routes))
	}
}

func TestInvalidRouteOptionsRejectedWithoutValues(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"boolean", HTTPSRedirectAnnotation, "private-value"},
		{"status requires redirect", RedirectStatusAnnotation, "301"},
		{"invalid JSON", RequestHeadersAnnotation, "private-value"},
		{"unknown field", RequestHeadersAnnotation, `{"private-value":[]}`},
		{"duplicate JSON key", RequestHeadersAnnotation, `{"remove":["Server"],"remove":["private-value"]}`},
		{"duplicate nested key", RequestHeadersAnnotation, `{"set":[{"name":"X-Test","value":"a","value":"private-value"}]}`},
		{"unknown nested field", RequestHeadersAnnotation, `{"set":[{"name":"X-Test","value":"a","private-value":true}]}`},
		{"case insensitive duplicate", RequestHeadersAnnotation, `{"set":[{"name":"X-Test","value":"private-value"}],"remove":["x-test"]}`},
		{"invalid header name", RequestHeadersAnnotation, `{"remove":["private-value:bad"]}`},
		{"newline", ResponseHeadersAnnotation, `{"set":[{"name":"X-Test","value":"private-value\r\nInjected: true"}]}`},
		{"null", ResponseHeadersAnnotation, "null"},
		{"missing value", RequestHeadersAnnotation, `{"set":[{"name":"X-Test"}]}`},
		{"null value", RequestHeadersAnnotation, `{"set":[{"name":"X-Test","value":null}]}`},
		{"empty", ResponseHeadersAnnotation, `{}`},
		{"length", ResponseHeadersAnnotation, `{"set":[{"name":"X-Test","value":"` + strings.Repeat("private-value", 400) + `"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := fixture()
			i.Annotations = map[string]string{tc.key: tc.value}
			routes, err := convert(i)
			var rejected *Rejected
			if !errors.As(err, &rejected) || routes != nil {
				t.Fatalf("invalid option accepted: %v", err)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("annotation value leaked")
			}
		})
	}
}

func TestRedirectRequiresExplicitTLSAndHTTPParents(t *testing.T) {
	i := fixture()
	i.Annotations = map[string]string{HTTPSRedirectAnnotation: "true"}
	b := binding()
	b.TLSPolicy = "External"
	if _, err := Convert(i, b, nil, config.Config{}); err == nil {
		t.Fatal("redirect without TLS and parents accepted")
	}
	i.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "existing"}}
	if _, err := Convert(i, b, nil, config.Config{}); err == nil {
		t.Fatal("redirect without HTTP parents accepted")
	}
	b.HTTPRedirectParentRefs = []config.Parent{{Name: "public", Namespace: "gateway-system", SectionName: "http"}}
	i.Annotations[RedirectStatusAnnotation] = "308"
	if _, err := Convert(i, b, nil, config.Config{}); err == nil {
		t.Fatal("unsupported redirect accepted")
	}
	i.Annotations[RedirectStatusAnnotation] = "301"
	routes, err := Convert(i, b, nil, config.Config{})
	if err != nil || *routes[1].Spec.Rules[0].Filters[0].RequestRedirect.StatusCode != 301 {
		t.Fatal("301 rejected", err)
	}
}
