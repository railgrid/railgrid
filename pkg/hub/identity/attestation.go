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

package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// ProviderAttestor authenticates a provider-asserted request. It is
// deliberately the SAME check the heartbeat performs — bearer → TokenReview in
// the provider's own workspace → subject must be
// system:serviceaccount:default:provider (pkg/hub/providers/heartbeat_auth.go)
// — because a provider that can prove it is itself for a heartbeat can prove
// it is itself for an identity request, and one verifier is one thing to get
// right. The heartbeat authenticator's positive and negative caches come along
// with it, which matters: this endpoint is reached on every token refresh.
type ProviderAttestor struct {
	authenticate providers.HeartbeatAuthenticator
}

// NewProviderAttestor wraps a heartbeat authenticator.
func NewProviderAttestor(authenticate providers.HeartbeatAuthenticator) *ProviderAttestor {
	return &ProviderAttestor{authenticate: authenticate}
}

// Attest verifies that r's bearer is provider's own service account and
// returns the authenticated subject.
func (a *ProviderAttestor) Attest(ctx context.Context, r *http.Request, provider string) (string, error) {
	if a == nil || a.authenticate == nil {
		return "", fmt.Errorf("provider attestation is unavailable")
	}
	if strings.TrimSpace(provider) == "" {
		return "", providers.ErrHeartbeatWrongIdentity
	}
	if err := a.authenticate(ctx, r, provider); err != nil {
		return "", err
	}
	return providers.ProviderSAUsername, nil
}

// DynamicOwnerProbe reads owner objects in a tenant workspace with the hub's
// own kcp credential — the same way the workload exchange re-reads the Project
// it is asked to mint for, and for the same reason: attestation proves who is
// asking, never what they are asking about.
type DynamicOwnerProbe struct {
	clients func(clusterID string) (dynamic.Interface, error)
}

// NewDynamicOwnerProbe builds a probe over the hub's kcp config.
func NewDynamicOwnerProbe(kcpConfig *rest.Config) (*DynamicOwnerProbe, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	return &DynamicOwnerProbe{clients: func(clusterID string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
		return dynamic.NewForConfig(cfg)
	}}, nil
}

// NewOwnerProbeForClient is a focused test seam: it never changes the rules.
func NewOwnerProbeForClient(client dynamic.Interface) *DynamicOwnerProbe {
	return &DynamicOwnerProbe{clients: func(string) (dynamic.Interface, error) { return client, nil }}
}

// Exists implements OwnerProbe. Owner objects are cluster-scoped in every
// consumer this service has (Agent, the edge kinds, Project, the factory
// binding), which is also what makes a single dynamic Get enough.
func (p *DynamicOwnerProbe) Exists(ctx context.Context, clusterID string, owner Owner) (bool, string, error) {
	if p == nil || p.clients == nil {
		return false, "", fmt.Errorf("owner probe is unavailable")
	}
	dyn, err := p.clients(clusterID)
	if err != nil {
		return false, "", err
	}
	gvr := schema.GroupVersionResource{Group: owner.Group, Version: owner.Version, Resource: owner.Resource}
	object, err := dyn.Resource(gvr).Get(ctx, owner.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	uid := string(object.GetUID())
	// A caller that names a UID must name the live one. Without this an
	// identity minted for a deleted Agent would be handed straight back when
	// an Agent of the same name is recreated — which is exactly the
	// "recreated owner inherits the old credential" hole the deterministic
	// name closes on the minting side.
	if owner.UID != "" && owner.UID != uid {
		return false, uid, nil
	}
	return true, uid, nil
}

// APIBindingChecker answers whether a provider's APIExport is bound in a
// tenant workspace, by reading the workspace's APIBindings as the hub.
type APIBindingChecker struct {
	clients  func(clusterID string) (dynamic.Interface, error)
	registry *providers.Registry
}

var apiBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

// NewAPIBindingChecker builds a checker over the hub's kcp config.
func NewAPIBindingChecker(kcpConfig *rest.Config, registry *providers.Registry) (*APIBindingChecker, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	return &APIBindingChecker{clients: func(clusterID string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
		return dynamic.NewForConfig(cfg)
	}, registry: registry}, nil
}

// NewBindingCheckerForClient is a focused test seam.
func NewBindingCheckerForClient(client dynamic.Interface, registry *providers.Registry) *APIBindingChecker {
	return &APIBindingChecker{clients: func(string) (dynamic.Interface, error) { return client, nil }, registry: registry}
}

// IsBound implements BindingChecker. It matches on the APIExport the provider
// declares, not on the binding's name: a tenant names its bindings, and a
// name is not a claim about what is bound.
func (c *APIBindingChecker) IsBound(clusterID, provider string) (bool, error) {
	if c == nil || c.clients == nil || c.registry == nil {
		return false, fmt.Errorf("binding checker is unavailable")
	}
	entry, ok := c.registry.Get(provider)
	if !ok || entry.APIExportName == "" {
		return false, nil
	}
	dyn, err := c.clients(clusterID)
	if err != nil {
		return false, err
	}
	list, err := dyn.Resource(apiBindingGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	for _, item := range list.Items {
		name, found, _ := unstructuredNestedString(item.Object, "spec", "reference", "export", "name")
		if found && name == entry.APIExportName {
			return true, nil
		}
	}
	return false, nil
}

func unstructuredNestedString(obj map[string]any, fields ...string) (string, bool, error) {
	current := any(obj)
	for _, field := range fields {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false, nil
		}
		current, ok = m[field]
		if !ok {
			return "", false, nil
		}
	}
	value, ok := current.(string)
	return value, ok, nil
}
