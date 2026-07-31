// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Per-agent identity for background runs.
//
// An interactive run acts as the human driving it: their bearer token reaches
// the platform data plane, which authorizes by re-reading the target instance
// as them. A scheduled, heartbeat, wakeup or inbound-channel run has no human,
// so it had no token and instance-backed tools (self-hosted search, a browser
// instance) simply refused to run.
//
// This gives each agent a ServiceAccount of its own in the tenant workspace,
// with a long-lived token, and background runs call the data plane as that
// identity. The shape mirrors the hub's per-MCPServer identity
// (pkg/hub/controllers/mcpserver, ensureMCPIdentity) — the same pattern kcp
// already supports. Long-lived because kcp has no TokenRequest API: its token
// controller populates a Secret, which is what "legacy" means here.
//
// SCOPE, STATED PLAINLY: the agent's ServiceAccount can read every
// infrastructure instance in its workspace, not only the ones its Connections
// name. That is a deliberate choice for a stable Role that does not churn as
// connections change. The consequence is that this token, if read out of the
// workspace, reaches any instance there — including a browser instance holding
// live logins the agent was never wired to. It is a standing credential: it
// does not expire, and revoking it means deleting the ServiceAccount.

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

var (
	serviceAccountGVR     = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	clusterRoleGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	secretsGVR            = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

// identityNamespace is where the agent ServiceAccount and its token Secret
// live — the same namespace the provider already keeps credential Secrets in,
// so the permission claim covers both without widening anything.
const identityNamespace = "default"

// instanceGroup is the infrastructure provider's instance API group. The
// agent's Role grants read across it; instance kinds are created at runtime
// from Templates, so they cannot be enumerated ahead of time and the grant is
// written against the group rather than a fixed resource list.
const instanceGroup = "infrastructure.kedge.faros.sh"

// agentIdentityName is the ServiceAccount / ClusterRole / binding name for one
// agent. Prefixed so it is obviously platform-managed in a workspace a human
// also uses.
func agentIdentityName(agent string) string { return "kedge-agent-" + agent }

// agentTokenSecretName is the Secret kcp's token controller populates.
func agentTokenSecretName(agent string) string { return "kedge-agent-" + agent + "-token" }

// identityCache memoises minted tokens per (cluster, agent). Provisioning is
// idempotent but involves several API calls and a poll, and the background
// scheduler may start many runs a minute — re-minting each time would be a
// needless round-trip storm against the tenant workspace.
//
// Entries expire. These tokens do not — revocation means deleting the
// ServiceAccount — so without a TTL a revoked identity would keep being used
// from memory until the provider restarted. The TTL bounds how long a run can
// act on an identity the tenant has already withdrawn.
type identityCache struct {
	mu     sync.Mutex
	tokens map[string]cachedIdentity
	ttl    time.Duration
	now    func() time.Time // overridable in tests
}

type cachedIdentity struct {
	token   string
	expires time.Time
}

// identityTTL is a compromise: long enough that a per-minute scheduler is not
// re-provisioning constantly, short enough that a withdrawn identity stops
// working within the hour.
const identityTTL = 30 * time.Minute

func newIdentityCache() *identityCache {
	return &identityCache{tokens: map[string]cachedIdentity{}, ttl: identityTTL, now: time.Now}
}

func (c *identityCache) get(cluster, agent string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.tokens[cluster+"/"+agent]
	if !ok || c.now().After(e.expires) {
		return "", false
	}
	return e.token, true
}

func (c *identityCache) put(cluster, agent, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[cluster+"/"+agent] = cachedIdentity{token: token, expires: c.now().Add(c.ttl)}
}

// ensureAgentIdentity provisions (idempotently) the agent's ServiceAccount, its
// read-instances ClusterRole and binding, and its token Secret, then waits for
// kcp's token controller to fill the token in. Every object is created-if-absent
// and never updated, which is what the create-only permission claim allows.
func ensureAgentIdentity(ctx context.Context, dyn dynamic.Interface, agent string) (string, error) {
	name := agentIdentityName(agent)

	sa := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ServiceAccount",
		"metadata":   map[string]any{"name": name, "namespace": identityNamespace},
	}}
	if err := createIfAbsent(ctx, dyn.Resource(serviceAccountGVR).Namespace(identityNamespace), sa); err != nil {
		return "", fmt.Errorf("agent ServiceAccount: %w", err)
	}

	// Read-only, and only over the instance group. Instances are cluster-scoped
	// (the per-template CRDs are), so this has to be a ClusterRole.
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": name},
		"rules": []any{map[string]any{
			"apiGroups": []any{instanceGroup},
			"resources": []any{"*"},
			"verbs":     []any{"get", "list"},
		}},
	}}
	if err := createIfAbsent(ctx, dyn.Resource(clusterRoleGVR), role); err != nil {
		return "", fmt.Errorf("agent ClusterRole: %w", err)
	}

	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": name},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     name,
		},
		"subjects": []any{map[string]any{
			"kind":      "ServiceAccount",
			"name":      name,
			"namespace": identityNamespace,
		}},
	}}
	if err := createIfAbsent(ctx, dyn.Resource(clusterRoleBindingGVR), binding); err != nil {
		return "", fmt.Errorf("agent ClusterRoleBinding: %w", err)
	}

	return ensureAgentToken(ctx, dyn, agent)
}

// ensureAgentToken creates the legacy service-account-token Secret and waits
// for the token controller to populate it. Mirrors pkg/hub/providers
// ensureLegacySAToken; kcp has no TokenRequest subresource, so this is how a
// usable token is obtained.
func ensureAgentToken(ctx context.Context, dyn dynamic.Interface, agent string) (string, error) {
	secretName := agentTokenSecretName(agent)
	secrets := dyn.Resource(secretsGVR).Namespace(identityNamespace)

	sec := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":        secretName,
			"namespace":   identityNamespace,
			"annotations": map[string]any{corev1.ServiceAccountNameKey: agentIdentityName(agent)},
		},
		"type": string(corev1.SecretTypeServiceAccountToken),
	}}
	if err := createIfAbsent(ctx, secrets, sec); err != nil {
		return "", fmt.Errorf("agent token Secret: %w", err)
	}

	var token string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		// Secret data is base64 in unstructured form; NestedString gives the
		// encoded value, so decode through the typed object instead.
		typed, err := fromU[corev1.Secret](got)
		if err != nil {
			return false, nil
		}
		if t := typed.Data[corev1.ServiceAccountTokenKey]; len(t) > 0 {
			token = string(t)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("waiting for kcp's token controller to populate Secret %s/%s: %w", identityNamespace, secretName, err)
	}
	return token, nil
}

// createIfAbsent creates an object, treating AlreadyExists as success. Nothing
// here is ever updated: the provider holds create-only rights on the RBAC
// objects precisely so it cannot later widen a grant it once made.
func createIfAbsent(ctx context.Context, ri dynamic.ResourceInterface, obj *unstructured.Unstructured) error {
	_, err := ri.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
