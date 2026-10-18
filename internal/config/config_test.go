// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedirectParents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parents []Parent
		valid   bool
	}{
		{"explicit HTTP listener", []Parent{{Name: "public", Namespace: "gateway-system", SectionName: "http"}}, true},
		{"missing section", []Parent{{Name: "public", Namespace: "gateway-system"}}, false},
		{"implicit namespace", []Parent{{Name: "public", SectionName: "http"}}, false},
		{"overlaps forwarding", []Parent{{Name: "public", Namespace: "gateway-system", SectionName: "https"}}, false},
		{"duplicate redirect", []Parent{{Name: "public", Namespace: "gateway-system", SectionName: "http"}, {Name: "public", Namespace: "gateway-system", SectionName: "http"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Binding{TLSPolicy: "External", ParentRefs: []Parent{{Name: "public", Namespace: "gateway-system", SectionName: "https"}}, HTTPRedirectParentRefs: tc.parents}
			cfg := Config{ControllerName: ControllerName, Classes: map[string]Binding{"public-apps": b}}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

// A forwarding parentRef without sectionName also attaches to the HTTP listener,
// where its "/" rule would compete with the redirect route for the same hosts.
func TestRedirectRequiresExplicitForwardingListener(t *testing.T) {
	b := Binding{TLSPolicy: "External",
		ParentRefs:             []Parent{{Name: "public", Namespace: "gateway-system"}},
		HTTPRedirectParentRefs: []Parent{{Name: "public", Namespace: "gateway-system", SectionName: "http"}},
	}
	cfg := Config{ControllerName: ControllerName, Classes: map[string]Binding{"public-apps": b}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("redirect binding accepted a forwarding parentRef without sectionName")
	}
}

// Parent references must match what the HTTPRoute CRD admits.
func TestParentRefsMatchHTTPRouteSchema(t *testing.T) {
	gw := func(name, section string) Parent { return Parent{Name: name, Namespace: "apps", SectionName: section} }
	for _, tc := range []struct {
		name    string
		parents []Parent
		valid   bool
	}{
		{"dotted listener", []Parent{gw("gw", "https.public")}, true},
		{"253-character listener", []Parent{gw("gw", strings.Repeat("a", 253))}, true},
		{"254-character listener", []Parent{gw("gw", strings.Repeat("a", 254))}, false},
		{"slash in listener", []Parent{gw("gw", "https/public")}, false},
		{"uppercase listener", []Parent{gw("gw", "HTTPS")}, false},
		{"mixed scoping on one Gateway", []Parent{gw("gw", ""), gw("gw", "https")}, false},
		{"mixed scoping across Gateways", []Parent{gw("gw", ""), gw("other", "https")}, true},
		{"distinct listeners on one Gateway", []Parent{gw("gw", "https"), gw("gw", "https-alt")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{ControllerName: ControllerName, Classes: map[string]Binding{"apps": {ParentRefs: tc.parents}}}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"valid", "classes:\n  public-apps:\n    parentRefs:\n    - name: public\n      namespace: gateway-system\n      sectionName: https\n", true},
		{"unknown field", "classes: {}\nkeepResources: true\n", false},
		{"no bindings", "classes: {}\n", false},
		{"implicit namespace", "classes:\n  public-apps:\n    parentRefs:\n    - name: public\n", false},
		{"typo TLS policy", "classes:\n  public-apps:\n    tlsPolicy: Ignore\n    parentRefs:\n    - name: public\n      namespace: gateway-system\n", false},
		{"duplicate mapping", "classes:\n  public-apps:\n    parentRefs:\n    - name: public\n      namespace: gateway-system\n    - name: public\n      namespace: gateway-system\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bridge.yaml")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, err)
			}
			if err == nil && cfg.ControllerName != ControllerName {
				t.Fatal("default controller missing")
			}
		})
	}
}
