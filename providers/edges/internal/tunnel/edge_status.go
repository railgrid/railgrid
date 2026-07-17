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

package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
	"github.com/faroshq/provider-edges/internal/kcpurl"
	"github.com/faroshq/provider-sdk/revdial"
)

// markEdgeConnected updates an Edge's status to Connected=true, Phase=Ready,
// and sets the Registered condition to True.
// When clearJoinToken is true, the bootstrap JoinToken is also cleared from status.
// clearJoinToken should only be true when the agent has received a durable credential
// (kubeconfig) — otherwise the agent would be unable to reconnect after a restart.
// It is called by the agent-proxy handler when a tunnel is established.
// Best-effort: errors are logged but not propagated.
func (p *Server) markEdgeConnected(ctx context.Context, gvr schema.GroupVersionResource, cluster, name string, sshCreds *sshCredsFromAgent, clearJoinToken bool) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcpurl.ClusterURL(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to create dynamic client",
			"cluster", cluster, "edge", name)
		return
	}

	// Clear joinToken with a dedicated MergePatch BEFORE the read-modify-write
	// loop below. MergePatch has no resourceVersion check, so it can't conflict
	// with the agent-side edge_reporter and hub-side stampEdgeHeartbeat
	// patchers that race us. The retry loop's UpdateStatus, by contrast, can
	// lose every attempt under contention and silently leave joinToken set —
	// which broke TestJoinTokenClearedAfterRegistration once the agent's
	// edge_reporter started heartbeating in join-token mode.
	if clearJoinToken {
		patch := []byte(`{"status":{"joinToken":null}}`)
		if _, perr := dynClient.Resource(gvr).Patch(ctx, name,
			types.MergePatchType, patch, metav1.PatchOptions{}, "status"); perr != nil {
			p.logger.Error(perr, "markEdgeConnected: failed to clear joinToken",
				"cluster", cluster, "edge", name)
			// Continue: the read-modify-write loop below will retry on conflict.
		}
	}

	// Read-modify-write of status races against the hub-side
	// stampEdgeHeartbeat patcher (started from the same handler) and the
	// agent-side edge_reporter that runs as soon as out-of-cluster join-token
	// agents refresh their hub client. Retry on conflict until UpdateStatus
	// wins; joinToken clearing above is already durable independent of this.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		edge, err := dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Build the updated status: set connected/phase, set Registered condition,
		// and re-clear joinToken in case the targeted MergePatch above raced and
		// lost to the TokenReconciler (extra safety, normally a no-op).
		status, _, _ := unstructured.NestedMap(edge.Object, "status")
		if status == nil {
			status = map[string]interface{}{}
		}
		status["connected"] = true
		status["phase"] = string(edgeapi.ConnectionPhaseReady)
		if clearJoinToken {
			delete(status, "joinToken")
		}

		// Stamp the public proxy URL so `kedge kubeconfig edge` / `kedge ssh`
		// have an address to externalize. This was previously set by the hub's
		// (now-deleted) mount_reconciler; it moved here when the edge plane
		// became a standalone provider. Idempotent: same value on every
		// reconnect. Empty when edgeProxyPublicPath is unconfigured.
		if url := p.edgeProxyStatusURL(gvr, cluster, name); url != "" {
			status["URL"] = url
		}

		// Set the Registered condition to True.
		now := metav1.NewTime(time.Now())
		registeredCondition := metav1.Condition{
			Type:               edgeapi.ConnectionConditionRegistered,
			Status:             metav1.ConditionTrue,
			Reason:             "AgentRegistered",
			Message:            "Agent has registered and received a durable ServiceAccount credential.",
			LastTransitionTime: now,
		}
		condJSON, _ := json.Marshal(registeredCondition)
		var condMap map[string]interface{}
		_ = json.Unmarshal(condJSON, &condMap)

		// Replace or append the Registered condition in the conditions array.
		conditions, _, _ := unstructured.NestedSlice(status, "conditions")
		found := false
		for i, c := range conditions {
			cMap, ok := c.(map[string]interface{})
			if ok && cMap["type"] == edgeapi.ConnectionConditionRegistered {
				conditions[i] = condMap
				found = true
				break
			}
		}
		if !found {
			conditions = append(conditions, condMap)
		}
		status["conditions"] = conditions

		// If the agent sent SSH credentials, create a secret and set sshCredentials in status.
		if sshCreds != nil && sshCreds.User != "" {
			if err := p.storeSSHCredentials(ctx, cfg, cluster, name, sshCreds, status); err != nil {
				p.logger.Error(err, "markEdgeConnected: failed to store SSH credentials",
					"cluster", cluster, "edge", name)
				// Continue — edge status update is more important.
			}
		}

		// Persist the agent's sshd host public key so the hub can perform strict
		// host-key verification on subsequent SSH sessions. This is independent of
		// auth credentials: an agent without password/privateKey still benefits
		// from MITM protection.
		if sshCreds != nil && sshCreds.HostKey != "" {
			status["sshHostKey"] = sshCreds.HostKey
		}

		if err := unstructured.SetNestedField(edge.Object, status, "status"); err != nil {
			return fmt.Errorf("setting status: %w", err)
		}

		_, err = dynClient.Resource(gvr).UpdateStatus(ctx, edge, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to update edge status",
			"cluster", cluster, "edge", name)
		return
	}

	p.logger.Info("Edge marked Ready and registered on join-token tunnel open",
		"cluster", cluster, "edge", name)
}

// storeSSHCredentials creates a Secret with the agent's SSH credentials and
// sets the sshCredentials reference in the edge status map.  Called hub-side
// with admin credentials so the agent doesn't need kcp API access.
func (p *Server) storeSSHCredentials(ctx context.Context, cfg *rest.Config, cluster, edgeName string, creds *sshCredsFromAgent, status map[string]interface{}) error {
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	const ns = "kedge-system"
	// Ensure namespace exists.
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k8sClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %s: %w", ns, err)
		}
	} else if err != nil {
		return fmt.Errorf("checking namespace %s: %w", ns, err)
	}

	secretName := edgeName + "-ssh-credentials"
	secretData := map[string][]byte{}
	if creds.Password != "" {
		secretData["password"] = []byte(creds.Password)
	}
	if len(creds.PrivateKey) > 0 {
		secretData["privateKey"] = creds.PrivateKey
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels:    map[string]string{"edges.kedge.faros.sh/edge": edgeName},
		},
		Type: corev1.SecretTypeOpaque,
		Data: secretData,
	}

	_, err = k8sClient.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k8sClient.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	} else if err == nil {
		_, err = k8sClient.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("creating/updating SSH credentials secret: %w", err)
	}

	// Build the sshCredentials status field.
	sshStatus := map[string]interface{}{
		"username": creds.User,
	}
	secretRef := map[string]interface{}{
		"name":      secretName,
		"namespace": ns,
	}
	if creds.Password != "" {
		sshStatus["passwordSecretRef"] = secretRef
	}
	if len(creds.PrivateKey) > 0 {
		sshStatus["privateKeySecretRef"] = secretRef
	}
	status["sshCredentials"] = sshStatus

	// Note: status.URL (the public /services/providers/edges/edgeproxy SSH URL)
	// is stamped by markEdgeConnected via edgeProxyStatusURL. Do NOT set it here
	// — a relative /clusters/... path would break the CLI's SSH WebSocket dialler.

	p.logger.Info("SSH credentials stored for edge", "cluster", cluster, "edge", edgeName, "user", creds.User)
	return nil
}

// runEdgeHeartbeatLoop ticks until ctx is cancelled, stamping the Edge's
// status.lastHeartbeatTime from dialer.LastPong on each tick. Cancellation
// happens when the agent-proxy handler observes dialer.Done(), so the loop
// terminates within one tick of the tunnel dying.
func (p *Server) runEdgeHeartbeatLoop(ctx context.Context, gvr schema.GroupVersionResource, cluster, name string, dialer *revdial.Dialer) {
	// First stamp immediately so the LAST HEARTBEAT column becomes non-empty
	// without waiting for the first tick.
	p.stampEdgeHeartbeat(ctx, gvr, cluster, name, dialer.LastPong())

	ticker := time.NewTicker(edgeHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.stampEdgeHeartbeat(ctx, gvr, cluster, name, dialer.LastPong())
		}
	}
}

// stampEdgeHeartbeat patches an Edge's status.lastHeartbeatTime to t.  It is
// used by the agent-proxy-v2 handler to surface revdial-level liveness (the
// last successful "pong" from the agent) on the Edge resource.  Agents
// connected via join token can't write their own kcp status, so the hub does
// it for them.
//
// Best-effort: errors are logged at V(4) only — heartbeat staleness is a soft
// signal, and the LifecycleReconciler will eventually reconcile state.
func (p *Server) stampEdgeHeartbeat(ctx context.Context, gvr schema.GroupVersionResource, cluster, name string, t time.Time) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcpurl.ClusterURL(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.V(4).Info("stampEdgeHeartbeat: failed to create dynamic client",
			"cluster", cluster, "edge", name, "err", err)
		return
	}

	// MergePatch with RFC3339-formatted timestamp; the field is typed as
	// metav1.Time (date-time) in the APIResourceSchema.
	patch := []byte(`{"status":{"lastHeartbeatTime":"` + t.UTC().Format(time.RFC3339) + `"}}`)
	_, err = dynClient.Resource(gvr).Patch(ctx, name,
		types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		p.logger.V(4).Info("stampEdgeHeartbeat: patch failed",
			"cluster", cluster, "edge", name, "err", err)
	}
}

// markEdgeDisconnected patches an Edge's status to Connected=false,
// Phase=Disconnected on the hub.  It is called by the agent-proxy-v2 handler
// when the agent's revdial tunnel closes so that the hub's view of edge
// connectivity is accurate even when the agent process dies without sending a
// clean disconnect heartbeat.
//
// It is best-effort: errors are logged but not propagated.
func (p *Server) markEdgeDisconnected(ctx context.Context, gvr schema.GroupVersionResource, cluster, name string) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcpurl.ClusterURL(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "markEdgeDisconnected: failed to create dynamic client",
			"cluster", cluster, "edge", name)
		return
	}

	patch := []byte(`{"status":{"connected":false,"phase":"Disconnected"}}`)
	_, err = dynClient.Resource(gvr).Patch(ctx, name,
		types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		p.logger.Error(err, "markEdgeDisconnected: failed to patch edge status",
			"cluster", cluster, "edge", name)
		return
	}

	p.logger.Info("Edge marked Disconnected on tunnel close",
		"cluster", cluster, "edge", name)
}
