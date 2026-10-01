// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
