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

// Package status reports agent status back to the hub.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

// dialAndFetchSSHHostKey connects to the SSH server on the given local port and
// captures its public host key by performing a handshake with a capturing
// HostKeyCallback. The key is returned in authorized_keys format
// ("<type> <base64>"). An empty string is returned on any error.
//
// This approach is correct for both production (real sshd on port 22) and
// e2e tests (embedded TestSSHServer with an in-memory random key), because
// it asks the actual server for its key rather than reading a file that may
// belong to a different sshd instance.
func dialAndFetchSSHHostKey(port int, logger klog.Logger) string {
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	var capturedKey gossh.PublicKey
	captureCallback := func(_ string, _ net.Addr, key gossh.PublicKey) error {
		capturedKey = key
		// Return an error to abort the handshake after capturing the key.
		return fmt.Errorf("host key captured")
	}

	cfg := &gossh.ClientConfig{
		User:            "key-probe",
		Auth:            []gossh.AuthMethod{gossh.Password("")},
		HostKeyCallback: captureCallback,
		Timeout:         5 * time.Second,
	}

	// We expect Dial to fail (captureCallback returns an error), but by that
	// point capturedKey will be set.
	_, _ = gossh.Dial("tcp", addr, cfg) //nolint:errcheck // expected to fail

	if capturedKey == nil {
		logger.V(4).Info("Could not fetch SSH host key from server", "addr", addr)
		return ""
	}

	// MarshalAuthorizedKey returns "<type> <base64>\n"; strip trailing newline.
	key := strings.TrimRight(string(gossh.MarshalAuthorizedKey(capturedKey)), "\n")
	logger.V(4).Info("Fetched SSH host key from server", "addr", addr, "keyType", capturedKey.Type())
	return key
}

const (
	// HeartbeatInterval is how often the agent sends heartbeats to the hub.
	HeartbeatInterval = 30 * time.Second
)

// EdgeReporter sends heartbeats for an Edge resource.
// It works for both EdgeTypeKubernetes and EdgeTypeServer.
type EdgeReporter struct {
	edgeName        string
	hubClient       *kedgeclient.Client
	tunnelState     <-chan bool // receives true on connect, false on disconnect; may be nil
	tunnelConnected bool
	// sshProxyPort is the local port of the SSH daemon the agent proxies to.
	// Zero means SSH host key reporting is disabled (non-server-mode edges).
	sshProxyPort int
}

// NewEdgeReporter creates a new EdgeReporter.
// tunnelState is the channel produced by tunnel.StartProxyTunnel; pass nil to
// skip tunnel-state tracking (tunnelConnected will always report false).
// sshProxyPort is the local SSH daemon port to probe for its host key (server
// mode only); pass 0 to skip SSH host key reporting.
func NewEdgeReporter(edgeName string, hubClient *kedgeclient.Client, tunnelState <-chan bool, sshProxyPort int) *EdgeReporter {
	return &EdgeReporter{
		edgeName:     edgeName,
		hubClient:    hubClient,
		tunnelState:  tunnelState,
		sshProxyPort: sshProxyPort,
	}
}

// Run starts the edge heartbeat reporter and blocks until ctx is cancelled.
func (r *EdgeReporter) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("edge-status-reporter")
	logger.Info("Starting edge status reporter", "edgeName", r.edgeName)

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	// First heartbeat immediately.
	r.sendHeartbeat(ctx, logger)

	for {
		select {
		case <-ctx.Done():
			return nil
		case connected, ok := <-r.tunnelState:
			if ok {
				r.tunnelConnected = connected
				r.sendHeartbeat(ctx, logger)
			}
		case <-ticker.C:
			r.sendHeartbeat(ctx, logger)
		}
	}
}

func (r *EdgeReporter) sendHeartbeat(ctx context.Context, logger klog.Logger) {
	status := kedgev1alpha1.EdgeStatus{
		Phase:     kedgev1alpha1.EdgePhaseReady,
		Connected: r.tunnelConnected,
	}
	// The hub may set Hostname/WorkspaceURL; we only patch the fields we own.
	statusPatch := map[string]interface{}{
		"phase":     string(status.Phase),
		"connected": status.Connected,
	}

	// Report the sshd host public key so the hub can verify the agent's identity.
	// We dial the SSH server directly to fetch its actual key, which works for
	// both the real sshd (production) and the embedded TestSSHServer (e2e tests).
	if r.sshProxyPort > 0 {
		if hostKey := dialAndFetchSSHHostKey(r.sshProxyPort, logger); hostKey != "" {
			statusPatch["sshHostKey"] = hostKey
		}
	}

	patch := map[string]interface{}{
		"status": statusPatch,
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		logger.Error(err, "failed to marshal edge status patch")
		return
	}

	_, err = r.hubClient.Edges().Patch(ctx, r.edgeName,
		types.MergePatchType, patchBytes,
		metav1.PatchOptions{}, "status")
	if err != nil {
		logger.Error(err, "failed to update edge status", "edge", r.edgeName)
		return
	}

	logger.V(4).Info("Edge heartbeat sent", "edge", r.edgeName,
		"phase", string(status.Phase), "connected", status.Connected)
}
