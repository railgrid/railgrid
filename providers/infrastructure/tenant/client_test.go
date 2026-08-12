// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestConfigForRejectsUnsafeLogicalClusterIDs(t *testing.T) {
	factory := NewClientFactory(&rest.Config{Host: "https://hub.example"})
	for _, clusterID := range []string{".", "..", "ws/path", "ws?query", "ws#fragment", "ws%2Fpath", "ws\nother"} {
		t.Run(clusterID, func(t *testing.T) {
			if _, err := factory.configFor(clusterID, "caller-token"); err == nil {
				t.Fatalf("cluster ID %q must be rejected", clusterID)
			}
		})
	}
	cfg, err := factory.configFor("root:faros-org_1.2", "caller-token")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.Host, "/clusters/root:faros-org_1.2") {
		t.Fatalf("cluster host = %q", cfg.Host)
	}
}
