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

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// BoundProviderAttestor authenticates exactly the registered provider copy that
// the target workspace has bound. A catalog name, caller header or unverified
// JWT claim is never enough to select the TokenReview workspace.
// Unscoped legacy requests retain the platform-only authenticator.
type BoundProviderAttestor struct {
	*ProviderAttestor
	bindings *APIBindingChecker
	review   func(context.Context, string, string) (*authnv1.TokenReview, error)
}

func NewBoundProviderAttestor(config *rest.Config, registry *providers.Registry, platform providers.HeartbeatAuthenticator) (*BoundProviderAttestor, error) {
	bindings, err := NewAPIBindingChecker(config, registry)
	if err != nil {
		return nil, err
	}
	return &BoundProviderAttestor{ProviderAttestor: NewProviderAttestor(platform), bindings: bindings, review: func(ctx context.Context, cluster, token string) (*authnv1.TokenReview, error) {
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		return client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
	}}, nil
}

// AttestInWorkspace never positively caches an old binding or registration.
// Native TokenReview validates the provider SA's current UID. Its trusted issuer
// extra must also match the actual bound provider workspace: TokenReview can
// authenticate a foreign cluster's identically named account.
func (a *BoundProviderAttestor) AttestInWorkspace(ctx context.Context, r *http.Request, provider, clusterID string) (string, error) {
	return a.attest(ctx, r, provider, clusterID, false)
}

// AttestCleanupInWorkspace keeps revocation possible while a binding is being
// disabled: owner finalizers must revoke identities before kcp can finish its
// cascade. It never grants minting authority and still pins the registered copy.
func (a *BoundProviderAttestor) AttestCleanupInWorkspace(ctx context.Context, r *http.Request, provider, clusterID string) (string, error) {
	return a.attest(ctx, r, provider, clusterID, true)
}

func (a *BoundProviderAttestor) attest(ctx context.Context, r *http.Request, provider, clusterID string, cleanup bool) (string, error) {
	if a == nil || a.bindings == nil || a.review == nil {
		return "", providers.ErrHeartbeatAuthUnavailable
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", providers.ErrHeartbeatNoBearer
	}
	entry, err := a.bindings.boundProviderOperation(ctx, clusterID, provider, cleanup)
	if err != nil {
		return "", err
	}
	review, err := a.review(ctx, entry.WorkspaceCluster, strings.TrimSpace(token))
	if err != nil {
		return "", fmt.Errorf("%w: provider TokenReview: %v", providers.ErrHeartbeatAuthUnavailable, err)
	}
	if review == nil || !providers.TokenReviewMatchesCluster(review.Status, entry.WorkspaceCluster) {
		return "", providers.ErrHeartbeatTokenRejected
	}
	if review.Status.User.Username != providers.ProviderSAUsername {
		return "", providers.ErrHeartbeatWrongIdentity
	}
	return providers.ProviderSAUsername, nil
}

func (c *APIBindingChecker) boundProviderOperation(ctx context.Context, clusterID, provider string, cleanup bool) (providers.Provider, error) {
	refused := providers.ErrHeartbeatWrongIdentity
	if c == nil || c.clients == nil || c.registry == nil {
		return providers.Provider{}, providers.ErrHeartbeatAuthUnavailable
	}
	if !logicalcluster.Name(clusterID).IsValid() || provider == "" {
		return providers.Provider{}, refused
	}
	client, err := c.clients(clusterID)
	if err != nil {
		return providers.Provider{}, fmt.Errorf("%w: workspace client", providers.ErrHeartbeatAuthUnavailable)
	}
	bindings, err := client.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return providers.Provider{}, fmt.Errorf("%w: workspace bindings", providers.ErrHeartbeatAuthUnavailable)
	}
	entries := c.registry.List()
	for _, binding := range bindings.Items {
		phase, _, _ := unstructuredNestedString(binding.Object, "status", "phase")
		if (!cleanup && binding.GetDeletionTimestamp() != nil) || phase != "Bound" {
			continue
		}
		// Both the reference and kcp's resolved provider cluster must match; a
		// retargeted spec with stale status cannot vouch for the replacement copy.
		path, _, _ := unstructuredNestedString(binding.Object, "spec", "reference", "export", "path")
		name, _, _ := unstructuredNestedString(binding.Object, "spec", "reference", "export", "name")
		cluster, _, _ := unstructuredNestedString(binding.Object, "status", "apiExportClusterName")
		healthy := true
		if status, ok := binding.Object["status"].(map[string]any); ok {
			if conditions, ok := status["conditions"].([]any); ok {
				for _, raw := range conditions {
					condition, ok := raw.(map[string]any)
					if ok && (condition["type"] == "APIExportValid" || !cleanup && condition["type"] == "Ready") && condition["status"] == "False" {
						healthy = false
					}
				}
			}
		}
		if !healthy || cluster == "" {
			continue
		}
		for _, entry := range entries {
			if entry.Name != provider || entry.WorkspaceCluster != cluster || entry.APIExportPath != path || entry.APIExportName != name || !cleanup && !entry.Ready() {
				continue
			}
			if entry.OrgUUID != "" {
				if entry.CatalogEntryCluster != entry.WorkspaceCluster {
					return providers.Provider{}, refused
				}
				logical, err := client.Resource(schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}).Get(ctx, "cluster", metav1.GetOptions{})
				if err != nil {
					return providers.Provider{}, fmt.Errorf("%w: consumer workspace", providers.ErrHeartbeatAuthUnavailable)
				}
				parts := strings.Split(logical.GetAnnotations()["kcp.io/path"], ":")
				if len(parts) != 5 || strings.Join(parts[:3], ":") != "root:railgrid:tenants" || parts[3] != entry.OrgUUID {
					return providers.Provider{}, refused
				}
			}
			return entry, nil
		}
	}
	return providers.Provider{}, refused
}
