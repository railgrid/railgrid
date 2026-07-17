/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package edgesconn

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestInvalidJoinTokenRejected proves the tunnel's auth boundary: an agent that
// presents a token which is NOT the edge's issued status.joinToken must be
// rejected, so the edge never reports connected. Uses a LinuxServer (no kind
// cluster needed) — authorization runs before any SSH work.
func TestInvalidJoinTokenRejected(t *testing.T) {
	const edgeName = "reject-srv"

	workDir := t.TempDir()
	kubeconfig := filepath.Join(workDir, "kedge.kubeconfig")
	runCLI(t, kubeconfig, kedgeBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)

	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	runCLI(t, kubeconfig, kedgeBin, "edge", "create", edgeName, "--type", "server")
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(linuxServerGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})

	// Ensure the edge is provisioned (a real join token exists) before we try a
	// bogus one — otherwise "not connected" would be trivially true.
	waitForJoinToken(t, tenantAdmin, linuxServerGVR, edgeName)

	// Run the agent with a token that is NOT the issued join token.
	startAgent(t, edgeName, "definitely-not-the-real-join-token", tenantWS,
		"--type", "server", "--ssh-proxy-port", "22099")

	// The edge must stay disconnected — poll for a window; any connect is a bug.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		got, err := tenantAdmin.Resource(linuxServerGVR).Get(ctxWithTimeout(t, 5*time.Second), edgeName, metav1.GetOptions{})
		if err == nil {
			if conn, _, _ := unstructured.NestedBool(got.Object, "status", "connected"); conn {
				t.Fatal("edge reported connected with an invalid join token")
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Log("edge correctly stayed disconnected with an invalid join token")
}
