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
		for _, p := range binding.ParentRefs {
			if len(validation.IsDNS1123Subdomain(p.Name)) != 0 || len(p.Name) > 253 ||
				len(validation.IsDNS1123Label(p.Namespace)) != 0 ||
				(p.SectionName != "" && (len(validation.IsDNS1123Label(p.SectionName)) != 0)) {
				return fmt.Errorf("class %s: invalid Gateway parent reference", name)
			}
			if seen[p] {
				return fmt.Errorf("class %s: duplicate Gateway parent reference", name)
			}
			seen[p] = true
		}
	}
	return nil
}

func (b Binding) References() []gatewayv1.ParentReference {
	refs := make([]gatewayv1.ParentReference, 0, len(b.ParentRefs))
	for _, parent := range b.ParentRefs {
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
