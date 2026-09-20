/*
Copyright 2026 The Railgrid Authors.

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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/railgrid/test/e2e/framework"
)

// TestSSHThroughTunnel drives the LinuxServer half of the edges data plane:
// it registers a LinuxServer, runs a server-mode `railgrid agent` that proxies to
// an embedded in-process SSH server, and proves `railgrid ssh <edge> -- echo …`
// runs a command down the reverse tunnel (railgrid ssh → hub backend proxy →
// out-of-process edges provider → agent → local sshd). No kind cluster needed —
// the agent's backing "host" is the embedded test SSH server.
func TestSSHThroughTunnel(t *testing.T) {
	const (
		edgeName = "conn-srv"
		sshPort  = 22022
		marker   = "railgrid_ssh_tunnel_ok"
	)

	workDir := suiteTempDir(t, "ssh-server-mode")
	kubeconfig := filepath.Join(workDir, "railgrid.kubeconfig")

	// 1. Log in + resolve the tenant workspace.
	runCLI(t, kubeconfig, railgridBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	t.Logf("tenant workspace = %s", tenantWS)
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)

	// 2/3. Enable edges + the edge-proxy grant (idempotent — TestKubectlThroughTunnel
	// may have created them in the same shared tenant workspace).
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	// 4. Embedded in-process SSH server as the agent's backing host. With no
	// users configured it accepts any client (NoClientAuth), so the consumer's
	// end-to-end SSH handshake through the tunnel succeeds without key setup.
	sshCtx, cancelSSH := context.WithCancel(context.Background())
	t.Cleanup(cancelSSH)
	sshSrv := framework.NewTestSSHServer(sshPort)
	if err := sshSrv.Start(sshCtx); err != nil {
		t.Fatalf("start embedded SSH server: %v", err)
	}
	t.Cleanup(sshSrv.Stop)

	// 5. Register the LinuxServer + wait for the join token.
	runCLI(t, kubeconfig, railgridBin, "edge", "create", edgeName, "--type", "server")
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(linuxServerGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenantAdmin, linuxServerGVR, edgeName)

	// 6. Server-mode agent proxying to the embedded sshd.
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "server", "--ssh-proxy-port", strconv.Itoa(sshPort))

	// 7. Wait for the edge to report connected.
	waitForConnected(t, tenantAdmin, linuxServerGVR, edgeName)

	// 8. THE PROOF: run a command over SSH through the tunnel.
	out := sshThroughTunnel(t, kubeconfig, edgeName, marker)
	if !strings.Contains(out, marker) {
		t.Fatalf("railgrid ssh -- echo did not return %q through the tunnel:\n%s", marker, out)
	}
	t.Logf("railgrid ssh through the tunnel returned the marker:\n%s", out)
}

// TestSSHUserMappingInherited proves the default `inherited` SSH user mapping:
// the server-mode agent connects to the backing sshd as its configured
// --ssh-user, so that username is what the sshd sees for the tunneled session.
func TestSSHUserMappingInherited(t *testing.T) {
	const (
		edgeName = "map-srv"
		sshPort  = 22044
		sshUser  = "mappeduser"
		sshPass  = "mappedpass"
	)

	workDir := suiteTempDir(t, "ssh-user-mapping")
	kubeconfig := filepath.Join(workDir, "railgrid.kubeconfig")

	runCLI(t, kubeconfig, railgridBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	sshCtx, cancelSSH := context.WithCancel(context.Background())
	t.Cleanup(cancelSSH)
	sshSrv := framework.NewTestSSHServer(sshPort)
	if err := sshSrv.Start(sshCtx); err != nil {
		t.Fatalf("start embedded SSH server: %v", err)
	}
	t.Cleanup(sshSrv.Stop)

	runCLI(t, kubeconfig, railgridBin, "edge", "create", edgeName, "--type", "server")
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(linuxServerGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenantAdmin, linuxServerGVR, edgeName)

	// Agent configured with an explicit SSH user — inherited mapping uses it.
	startAgent(t, edgeName, joinToken, tenantWS,
		"--type", "server", "--ssh-proxy-port", strconv.Itoa(sshPort),
		"--ssh-user", sshUser, "--ssh-password", sshPass)
	waitForConnected(t, tenantAdmin, linuxServerGVR, edgeName)

	// Open a session through the tunnel, then assert the sshd saw the mapped user.
	_ = sshThroughTunnel(t, kubeconfig, edgeName, "railgrid_ssh_mapping_ok")
	if !waitFor(t, 30*time.Second, func() (bool, string) {
		users := sshSrv.ConnectedUsers()
		if slices.Contains(users, sshUser) {
			return true, ""
		}
		return false, fmt.Sprintf("connected users=%v", users)
	}) {
		t.Fatalf("sshd never saw the mapped user %q; connected users: %v", sshUser, sshSrv.ConnectedUsers())
	}
	t.Logf("inherited mapping: sshd session ran as %q", sshUser)
}

// sshThroughTunnel runs `railgrid ssh <edge> -- echo <marker>` and returns the
// command's STDOUT, retrying briefly (SSH credential/status propagation can lag
// the connected flag by a beat).
//
// Stdout only, and only on a zero exit. The earlier version searched the
// COMBINED output for the marker, and the CLI's failure message embeds the URL
// it dialled — which contains `cmd=echo+<marker>`. Every total failure
// therefore matched: this suite kept both SSH tests green through a data plane
// that was answering 400 at the edges gate, and the log line the test printed
// as its proof was the error message.
func sshThroughTunnel(t *testing.T, kubeconfig, edgeName, marker string) string {
	t.Helper()
	var stdout, attempt string
	if !waitFor(t, 90*time.Second, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, railgridBin, "ssh", edgeName, "--", "echo", marker)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		runErr := cmd.Run()
		stdout = out.String()
		attempt = fmt.Sprintf("exit: %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout, errOut.String())
		if runErr != nil {
			return false, attempt
		}
		return strings.Contains(stdout, marker), attempt
	}) {
		t.Fatalf("railgrid ssh never ran the command through the tunnel; last attempt:\n%s", attempt)
	}
	return stdout
}
