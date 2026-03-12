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

package builder

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

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

var edgeGVR = schema.GroupVersionResource{
	Group:    "kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "edges",
}

// markEdgeConnected updates an Edge's status to Connected=true, Phase=Ready,
// clears the bootstrap JoinToken, and sets the Registered condition to True.
// It is called by the agent-proxy handler when a join-token-authenticated
// tunnel is established, because in that flow the agent's edge_reporter cannot
// call the kcp API directly (the join token is not a valid kcp credential).
// Best-effort: errors are logged but not propagated.
func (p *virtualWorkspaces) markEdgeConnected(ctx context.Context, cluster, name string, sshCreds *sshCredsFromAgent) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcp.AppendClusterPath(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to create dynamic client",
			"cluster", cluster, "edge", name)
		return
	}

	// Get the current edge so we can read and update its status.
	edge, err := dynClient.Resource(edgeGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to get edge",
			"cluster", cluster, "edge", name)
		return
	}

	// Build the updated status: clear joinToken, set connected/phase, set Registered condition.
	status, _, _ := unstructured.NestedMap(edge.Object, "status")
	if status == nil {
		status = map[string]interface{}{}
	}
	status["connected"] = true
	status["phase"] = string(kedgev1alpha1.EdgePhaseReady)
	delete(status, "joinToken")

	// Set the Registered condition to True.
	now := metav1.NewTime(time.Now())
	registeredCondition := metav1.Condition{
		Type:               kedgev1alpha1.EdgeConditionRegistered,
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
		if ok && cMap["type"] == kedgev1alpha1.EdgeConditionRegistered {
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

	if err := unstructured.SetNestedField(edge.Object, status, "status"); err != nil {
		p.logger.Error(err, "markEdgeConnected: failed to set status",
			"cluster", cluster, "edge", name)
		return
	}

	_, err = dynClient.Resource(edgeGVR).UpdateStatus(ctx, edge, metav1.UpdateOptions{})
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
func (p *virtualWorkspaces) storeSSHCredentials(ctx context.Context, cfg *rest.Config, cluster, edgeName string, creds *sshCredsFromAgent, status map[string]interface{}) error {
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
			Labels:    map[string]string{"kedge.faros.sh/edge": edgeName},
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

	// Note: status.URL is managed by the hub's mount_reconciler (full edges-proxy
	// SSH URL).  Do NOT set it here — a relative /clusters/... path would break
	// the CLI's SSH WebSocket dialler.

	p.logger.Info("SSH credentials stored for edge", "cluster", cluster, "edge", edgeName, "user", creds.User)
	return nil
}

// markEdgeDisconnected patches an Edge's status to Connected=false,
// Phase=Disconnected on the hub.  It is called by the agent-proxy-v2 handler
// when the agent's revdial tunnel closes so that the hub's view of edge
// connectivity is accurate even when the agent process dies without sending a
// clean disconnect heartbeat.
//
// It is best-effort: errors are logged but not propagated.
func (p *virtualWorkspaces) markEdgeDisconnected(ctx context.Context, cluster, name string) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcp.AppendClusterPath(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "markEdgeDisconnected: failed to create dynamic client",
			"cluster", cluster, "edge", name)
		return
	}

	patch := []byte(`{"status":{"connected":false,"phase":"Disconnected"}}`)
	_, err = dynClient.Resource(edgeGVR).Patch(ctx, name,
		types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		p.logger.Error(err, "markEdgeDisconnected: failed to patch edge status",
			"cluster", cluster, "edge", name)
		return
	}

	p.logger.Info("Edge marked Disconnected on tunnel close",
		"cluster", cluster, "edge", name)
}
