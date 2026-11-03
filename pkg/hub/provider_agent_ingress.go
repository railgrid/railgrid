// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

// newProviderAgentIngressResolver addresses by the workspace's current binding,
// not the catalog's org shadowing rule. Credential validation precedes forwarding:
// only this Edge's join token or its UID-bound agent ServiceAccount can cross to
// an org-owned Edges backend. Human tokens never take this path.
func newProviderAgentIngressResolver(config *rest.Config) providers.AgentIngressResolver {
	// Share connections across all consumer scopes; never cache routing decisions.
	httpClient, transportErr := rest.HTTPClientFor(config)
	return func(ctx context.Context, cluster, resource, name, token string) (providers.AgentIngressTarget, error) {
		if transportErr != nil {
			return providers.AgentIngressTarget{}, transportErr
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
		client, err := dynamic.NewForConfigAndClient(cfg, httpClient)
		if err != nil {
			return providers.AgentIngressTarget{}, err
		}
		return resolveProviderAgentIngress(ctx, client, cluster, resource, name, token, func(ctx context.Context, token string) (*authnv1.TokenReview, error) {
			kube, err := kubernetes.NewForConfigAndClient(cfg, httpClient)
			if err != nil {
				return nil, err
			}
			return kube.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token, Audiences: []string{serviceaccounts.WorkloadIdentityTokenAudience}}}, metav1.CreateOptions{})
		})
	}
}

func resolveProviderAgentIngress(ctx context.Context, client dynamic.Interface, cluster, resource, name, token string, review func(context.Context, string) (*authnv1.TokenReview, error)) (providers.AgentIngressTarget, error) {
	kinds := map[string]string{"kubernetesclusters": "KubernetesCluster", "linuxservers": "LinuxServer", "macosservers": "MacOSServer"}
	kind := kinds[resource]
	if token == "" || kind == "" {
		return providers.AgentIngressTarget{}, fmt.Errorf("invalid agent credential scope")
	}
	logical, err := client.Resource(schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return providers.AgentIngressTarget{}, err
	}
	parts := strings.Split(logical.GetAnnotations()["kcp.io/path"], ":")
	if len(parts) != 5 || strings.Join(parts[:3], ":") != "root:railgrid:tenants" || parts[3] == "" || parts[4] == "" {
		return providers.AgentIngressTarget{}, fmt.Errorf("agent consumer workspace is not a tenant workspace")
	}
	org := parts[3]
	// Metadata names are user-chosen. Select the actual native export coordinates
	// from the organization's visible Edges bindings, refusing ambiguous matches.
	bindings, err := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return providers.AgentIngressTarget{}, err
	}
	var target providers.AgentIngressTarget
	selected := false
	for _, binding := range bindings.Items {
		exportPath, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
		if exportPath != "root:railgrid:providers:edges" && exportPath != "root:railgrid:tenants:"+org+":providers:edges" {
			continue
		}
		phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
		if binding.GetDeletionTimestamp() != nil || phase != "Bound" {
			continue
		}
		invalid := false
		conditions, _, _ := unstructured.NestedSlice(binding.Object, "status", "conditions")
		for _, condition := range conditions {
			c, ok := condition.(map[string]any)
			if ok && c["type"] == "APIExportValid" && c["status"] == "False" {
				invalid = true
			}
		}
		if invalid {
			continue
		}
		exportName, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
		exportCluster, _, _ := unstructured.NestedString(binding.Object, "status", "apiExportClusterName")
		if exportName == "" || exportCluster == "" {
			continue
		}
		if selected {
			return providers.AgentIngressTarget{}, fmt.Errorf("edges binding is ambiguous")
		}
		target = providers.AgentIngressTarget{OrgUUID: org, ExportPath: exportPath, ExportName: exportName, ExportCluster: exportCluster}
		selected = true
	}
	if !selected {
		return providers.AgentIngressTarget{}, fmt.Errorf("edges binding is unavailable")
	}
	edge, err := client.Resource(schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: resource}).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return providers.AgentIngressTarget{}, err
	}
	if edge.GetDeletionTimestamp() != nil || edge.GetUID() == "" {
		return providers.AgentIngressTarget{}, fmt.Errorf("edge is deleting or has no UID")
	}
	join, _, _ := unstructured.NestedString(edge.Object, "status", "joinToken")
	hash, _, _ := unstructured.NestedString(edge.Object, "status", "joinTokenHash")
	sum := sha256.Sum256([]byte(token))
	if (join != "" && subtle.ConstantTimeCompare([]byte(join), []byte(token)) == 1) || (join == "" && hash != "" && subtle.ConstantTimeCompare([]byte(hash), []byte(hex.EncodeToString(sum[:]))) == 1) {
		return target, nil
	}
	verified, err := review(ctx, token)
	if err != nil {
		return providers.AgentIngressTarget{}, err
	}
	owner := identity.Owner{Provider: "edges", Group: "edges.railgrid.ai", Kind: kind, Name: name, UID: string(edge.GetUID())}
	expected := "system:serviceaccount:default:" + identity.ServiceAccountName(owner)
	if verified == nil || !providers.TokenReviewMatchesCluster(verified.Status, cluster) || verified.Status.User.Username != expected || !slices.Contains(verified.Status.Audiences, serviceaccounts.WorkloadIdentityTokenAudience) {
		return providers.AgentIngressTarget{}, fmt.Errorf("credential is not this edge's scoped identity")
	}
	return target, nil
}
