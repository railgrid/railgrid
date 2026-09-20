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

	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
	"github.com/railgrid/provider-sdk/claimscope"
)

// edgesProviderName is this provider's CatalogEntry name, and therefore the
// value of the railgrid.ai/owner label on every Secret it owns. It must match
// the permission-claim selector in manifest.yaml.
const edgesProviderName = "edges"

// markEdgeConnected records, on tunnel open, the observations only the
// agent-ingress handler has and that must be durable at once: it clears the
// bootstrap joinToken (when the agent got a durable credential), stores the
// agent-reported hostname, SSH credentials (as a Secret + status ref) and sshd
// host key, and stamps the public proxy URL.
//
// It deliberately does NOT write status.connected, status.phase,
// status.lastHeartbeatTime or the Registered condition. Tunnel liveness is
// recorded only in the registry Lease (ConnManager.Store → Registry.ClaimTunnel,
// renewed by the sweeper) and the edge lifecycle reconciler
// (internal/edgectrl) is the single writer that derives those fields from it —
// so there is one owner per field and no hub-vs-reconciler write race.
//
// clearJoinToken should only be true when the agent has received a durable
// credential (kubeconfig) — otherwise the agent would be unable to reconnect
// after a restart. hostname, when non-empty, is the agent-reported machine
// hostname (X-Railgrid-Agent-Hostname). Best-effort: errors are logged but
// not propagated.
func (p *Server) markEdgeConnected(ctx context.Context, gvr schema.GroupVersionResource, cluster, name string, sshCreds *sshCredsFromAgent, hostname string, clearJoinToken bool) {
	cfg, err := p.tenantConfigFor(ctx, cluster)
	if err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to resolve tenant config",
			"cluster", cluster, "edge", name)
		return
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to create dynamic client",
			"cluster", cluster, "edge", name)
		return
	}

	// Clear joinToken with a dedicated MergePatch BEFORE the read-modify-write
	// loop below. MergePatch has no resourceVersion check, so it can't conflict
	// with the agent-side edge_reporter heartbeat patches or the lifecycle
	// reconciler's status writes. The retry loop's UpdateStatus, by contrast,
	// can lose every attempt under contention and silently leave joinToken
	// set — which broke TestJoinTokenClearedAfterRegistration once the agent's
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

	// Read-modify-write of the handler-owned fields. It races the agent-side
	// edge_reporter (merge patches) and the edge reconcilers (RV-checked
	// updates), so retry on conflict until UpdateStatus wins; joinToken
	// clearing above is already durable independent of this.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		edge, err := dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Re-clear joinToken in case the targeted MergePatch above raced and
		// lost to the TokenReconciler (extra safety, normally a no-op).
		status, _, _ := unstructured.NestedMap(edge.Object, "status")
		if status == nil {
			status = map[string]interface{}{}
		}
		if clearJoinToken {
			delete(status, "joinToken")
		}

		// Stamp the public proxy URL so `railgrid edge kubeconfig` / `railgrid ssh`
		// have an address to externalize. This was previously set by the hub's
		// (now-deleted) mount_reconciler; it moved here when the edge plane
		// became a standalone provider. Idempotent: same value on every
		// reconnect. Empty when edgeProxyPublicPath is unconfigured.
		if gvr.Resource == macOSServerResource {
			// MacOSServer is Service-only. Clear any stale URL if an object was
			// restored from an older status snapshot; consumers must not infer an
			// SSH or Kubernetes data plane from this kind's shared status shape.
			delete(status, "URL")
		} else if url := p.edgeProxyStatusURL(gvr, cluster, name); url != "" {
			status["URL"] = url
		}

		// The agent's heartbeat patch deliberately leaves hostname alone (it is
		// hub-owned), so this is the only writer: record what the agent
		// reported on this tunnel open, keep the previous value otherwise.
		if hostname != "" {
			status["hostname"] = hostname
		}

		now := metav1.NewTime(time.Now())

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
		// from MITM protection. Write-once: see applyReportedSSHHostKey.
		if sshCreds != nil && sshCreds.HostKey != "" {
			applyReportedSSHHostKey(status, sshCreds.HostKey, now)
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

	p.logger.Info("Recorded agent-reported edge facts on tunnel open",
		"cluster", cluster, "edge", name, "hostname", hostname, "joinTokenCleared", clearJoinToken)
}

// applyReportedSSHHostKey records the agent-reported sshd host key into
// status.sshHostKey the first time one is seen and never replaces it: the report
// is agent-asserted, so letting every reconnect overwrite the recorded key would
// let a compromised agent rotate the key the hub pins SSH sessions to, silently.
// A later report of a different key sets the SSHHostKeyChanged condition (with
// both fingerprints) for an operator to resolve by pinning spec.sshHostKey or
// clearing status.sshHostKey; a report matching the recorded key clears it.
func applyReportedSSHHostKey(status map[string]interface{}, reported string, now metav1.Time) {
	existing, _, _ := unstructured.NestedString(status, "sshHostKey")
	switch {
	case existing == "":
		status["sshHostKey"] = reported
	case sameSSHHostKey(existing, reported):
		if findStatusCondition(status, edgeapi.ConnectionConditionSSHHostKeyChanged) != nil {
			setStatusCondition(status, metav1.Condition{
				Type:               edgeapi.ConnectionConditionSSHHostKeyChanged,
				Status:             metav1.ConditionFalse,
				Reason:             "Match",
				Message:            "The agent-reported SSH host key matches status.sshHostKey.",
				LastTransitionTime: now,
			})
		}
	default:
		setStatusCondition(status, metav1.Condition{
			Type:   edgeapi.ConnectionConditionSSHHostKeyChanged,
			Status: metav1.ConditionTrue,
			Reason: "FingerprintMismatch",
			Message: fmt.Sprintf("The agent reported SSH host key %s but status.sshHostKey holds %s; the recorded key is kept. "+
				"Pin spec.sshHostKey, or clear status.sshHostKey to accept the reported key.",
				sshHostKeyFingerprint(reported), sshHostKeyFingerprint(existing)),
			LastTransitionTime: now,
		})
	}
}

// findStatusCondition returns the condition of the given type from an
// unstructured status map, or nil.
func findStatusCondition(status map[string]interface{}, condType string) map[string]interface{} {
	conditions, _, _ := unstructured.NestedSlice(status, "conditions")
	for _, c := range conditions {
		if cMap, ok := c.(map[string]interface{}); ok && cMap["type"] == condType {
			return cMap
		}
	}
	return nil
}

// setStatusCondition replaces (or appends) the condition of cond's type in an
// unstructured status map. LastTransitionTime is preserved from the existing
// condition when its status is unchanged.
func setStatusCondition(status map[string]interface{}, cond metav1.Condition) {
	if existing := findStatusCondition(status, cond.Type); existing != nil {
		if existing["status"] == string(cond.Status) {
			if ltt, ok := existing["lastTransitionTime"].(string); ok && ltt != "" {
				// RFC3339Nano parses the seconds-precision form as well, so it
				// also tolerates a fractional-seconds value that reached the
				// object by some other route than a metav1.Time marshal.
				if t, err := time.Parse(time.RFC3339Nano, ltt); err == nil {
					cond.LastTransitionTime = metav1.NewTime(t)
				}
			}
		}
	}
	condJSON, _ := json.Marshal(cond)
	var condMap map[string]interface{}
	_ = json.Unmarshal(condJSON, &condMap)

	conditions, _, _ := unstructured.NestedSlice(status, "conditions")
	found := false
	for i, c := range conditions {
		cMap, ok := c.(map[string]interface{})
		if ok && cMap["type"] == cond.Type {
			conditions[i] = condMap
			found = true
			break
		}
	}
	if !found {
		conditions = append(conditions, condMap)
	}
	status["conditions"] = conditions
}

// storeSSHCredentials creates a Secret with the agent's SSH credentials and
// sets the sshCredentials reference in the edge status map.  Called hub-side
// with admin credentials so the agent doesn't need kcp API access.
func (p *Server) storeSSHCredentials(ctx context.Context, cfg *rest.Config, cluster, edgeName string, creds *sshCredsFromAgent, status map[string]interface{}) error {
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	const ns = "railgrid-system"
	if err := ensureEdgeNamespace(ctx, k8sClient, ns); err != nil {
		return err
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
			// The owner label is what keeps this Secret inside this
			// provider's `secrets` permission claim, which is scoped to it
			// (manifest.yaml). Without it the provider writes a credential
			// it can no longer read back: kcp's APIExport virtual workspace
			// filters LIST/WATCH by the claim and answers GET with a 404.
			Labels: claimscope.WithOwner(
				map[string]string{"edges.railgrid.ai/edge": edgeName},
				edgesProviderName,
			),
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
