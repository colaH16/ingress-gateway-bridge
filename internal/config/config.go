// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"
)

const ControllerName = "ingress-gateway-bridge.colah16.github.io/controller"

type Config struct {
	ControllerName string             `json:"controllerName"`
	Classes        map[string]Binding `json:"classes"`
}

type Binding struct {
	ParentRefs []Parent `json:"parentRefs"`
	// Optional HTTP listeners used only by an opt-in HTTPS redirect route.
	HTTPRedirectParentRefs []Parent `json:"httpRedirectParentRefs,omitempty"`
	// External explicitly delegates TLS to the existing Gateway or tunnel edge.
	// It does not copy Ingress TLS secrets or configure certificates.
	TLSPolicy string `json:"tlsPolicy,omitempty"`
}

type Parent struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	SectionName string `json:"sectionName,omitempty"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("read bridge config: %w", err)
	}
	if cfg.ControllerName == "" {
		cfg.ControllerName = ControllerName
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if len(validation.IsQualifiedName(c.ControllerName)) != 0 || !strings.Contains(c.ControllerName, "/") {
		return fmt.Errorf("controllerName must be a domain-qualified name")
	}
	if len(c.Classes) == 0 {
		return fmt.Errorf("at least one class binding is required")
	}
	for name, binding := range c.Classes {
		if len(validation.IsDNS1123Subdomain(name)) != 0 || len(name) > 253 {
			return fmt.Errorf("invalid IngressClass name %q", name)
		}
		if len(binding.ParentRefs) == 0 || len(binding.ParentRefs) > 16 {
			return fmt.Errorf("class %s must have 1..16 parentRefs", name)
		}
		if binding.TLSPolicy != "" && binding.TLSPolicy != "Reject" && binding.TLSPolicy != "External" {
			return fmt.Errorf("class %s: tlsPolicy must be Reject or External", name)
		}
		seen := map[Parent]bool{}
		if len(binding.HTTPRedirectParentRefs) > 16 {
			return fmt.Errorf("class %s must have at most 16 httpRedirectParentRefs", name)
		}
		for _, p := range binding.AllParents() {
			if len(validation.IsDNS1123Subdomain(p.Name)) != 0 || len(p.Name) > 253 ||
				len(validation.IsDNS1123Label(p.Namespace)) != 0 ||
				(p.SectionName != "" && len(validation.IsDNS1123Subdomain(p.SectionName)) != 0) {
				return fmt.Errorf("class %s: invalid Gateway parent reference", name)
			}
			if seen[p] {
				return fmt.Errorf("class %s: duplicate Gateway parent reference", name)
			}
			seen[p] = true
		}
		for _, p := range binding.HTTPRedirectParentRefs {
			if p.SectionName == "" {
				return fmt.Errorf("class %s: httpRedirectParentRefs require an explicit sectionName", name)
			}
		}
		// HTTPRoute CEL requires references to one Gateway to either all set or all
		// omit sectionName. Forwarding and redirect refs are separate routes.
		for _, parents := range [][]Parent{binding.ParentRefs, binding.HTTPRedirectParentRefs} {
			scoped := map[Parent]bool{}
			for _, p := range parents {
				gateway := Parent{Name: p.Name, Namespace: p.Namespace}
				if previous, seen := scoped[gateway]; seen && previous != (p.SectionName != "") {
					return fmt.Errorf("class %s: references to one Gateway must all set or all omit sectionName", name)
				}
				scoped[gateway] = p.SectionName != ""
			}
		}
		// Without a sectionName the forwarding route would also attach to the HTTP
		// listener and compete with the redirect route for the same host and "/".
		for _, p := range binding.ParentRefs {
			if len(binding.HTTPRedirectParentRefs) != 0 && p.SectionName == "" {
				return fmt.Errorf("class %s: parentRefs require an explicit sectionName when httpRedirectParentRefs are set", name)
			}
		}
	}
	return nil
}

func (b Binding) References() []gatewayv1.ParentReference {
	return references(b.ParentRefs)
}

func (b Binding) AllParents() []Parent {
	return append(append([]Parent(nil), b.ParentRefs...), b.HTTPRedirectParentRefs...)
}

func (b Binding) RedirectReferences() []gatewayv1.ParentReference {
	return references(b.HTTPRedirectParentRefs)
}

func references(parents []Parent) []gatewayv1.ParentReference {
	refs := make([]gatewayv1.ParentReference, 0, len(parents))
	for _, parent := range parents {
		namespace := gatewayv1.Namespace(parent.Namespace)
		group, kind := gatewayv1.Group(gatewayv1.GroupName), gatewayv1.Kind("Gateway")
		ref := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(parent.Name), Namespace: &namespace, Group: &group, Kind: &kind}
		if parent.SectionName != "" {
			section := gatewayv1.SectionName(parent.SectionName)
			ref.SectionName = &section
		}
		refs = append(refs, ref)
	}
	return refs
}
