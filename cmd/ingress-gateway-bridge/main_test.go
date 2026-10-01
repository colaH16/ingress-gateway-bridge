// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestDryRunRejectsLeaseWritesBeforeLoadingKubeconfig(t *testing.T) {
	err := run("does-not-exist.yaml", "0", "0", true, true)
	if err == nil || !strings.Contains(err.Error(), "Lease") {
		t.Fatalf("got %v", err)
	}
}
