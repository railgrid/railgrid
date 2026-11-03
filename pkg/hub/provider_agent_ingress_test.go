// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestProviderAgentIngressCredentialBoundary(t *testing.T) {
	const exportPath = "root:railgrid:tenants:org:providers:edges"
	for _, tc := range []struct {
		name, token, subject, audience, issuer, exportPath, consumerPath              string
		hashOnly, badBinding, deletingEdge, authenticated, missingExportCluster, want bool
	}{
		{name: "bootstrap", token: "join", want: true},
		{name: "platform bootstrap", token: "join", exportPath: "root:railgrid:providers:edges", want: true},
		{name: "missing resolved export identity", token: "join", missingExportCluster: true},
		{name: "foreign provider organization", token: "join", exportPath: "root:railgrid:tenants:foreign:providers:edges"},
		{name: "foreign consumer organization", token: "join", consumerPath: "root:railgrid:tenants:foreign:workspace"},
		{name: "non tenant consumer", token: "join", consumerPath: "root:railgrid:providers:edges"},
		{name: "cleared bootstrap uses hash", token: "join", hashOnly: true, want: true},
		{name: "rotated bootstrap denied", token: "old-join"},
		{name: "human never forwarded", token: "human", subject: "alice", audience: serviceaccounts.WorkloadIdentityTokenAudience, authenticated: true},
		{name: "another service account denied", token: "foreign", subject: "system:serviceaccount:default:admin", audience: serviceaccounts.WorkloadIdentityTokenAudience, authenticated: true},
		{name: "scoped credential", issuer: "consumer", token: "agent", subject: "agent", audience: serviceaccounts.WorkloadIdentityTokenAudience, authenticated: true, want: true},
		{name: "foreign cluster with matching account name", issuer: "foreign", token: "agent", subject: "agent", audience: serviceaccounts.WorkloadIdentityTokenAudience, authenticated: true},
		{name: "missing issuer with matching account name", token: "agent", subject: "agent", audience: serviceaccounts.WorkloadIdentityTokenAudience, authenticated: true},
		{name: "wrong audience", token: "agent", subject: "agent", audience: "foreign", authenticated: true},
		{name: "revoked credential", token: "agent", subject: "agent", audience: serviceaccounts.WorkloadIdentityTokenAudience},
		{name: "deleted export", token: "join", badBinding: true},
		{name: "deleting edge", token: "join", deletingEdge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindingPath := tc.exportPath
			if bindingPath == "" {
				bindingPath = exportPath
			}
			binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "edges"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": bindingPath, "name": "edges-export"}}}, "status": map[string]any{"phase": "Bound", "apiExportClusterName": "org-provider"}}}
			if tc.missingExportCluster {
				delete(binding.Object["status"].(map[string]any), "apiExportClusterName")
			}
			if tc.badBinding {
				binding.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "APIExportValid", "status": "False"}}
			}
			sum := sha256.Sum256([]byte("join"))
			status := map[string]any{"joinTokenHash": hex.EncodeToString(sum[:])}
			if !tc.hashOnly {
				status["joinToken"] = "join"
			}
			edge := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "edges.railgrid.ai/v1alpha1", "kind": "KubernetesCluster", "metadata": map[string]any{"name": "edge", "uid": "current-edge-uid"}, "status": status}}
			if tc.deletingEdge {
				edge.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-02T00:00:00Z"
			}
			consumerPath := tc.consumerPath
			if consumerPath == "" {
				consumerPath = "root:railgrid:tenants:org:workspace"
			}
			logical := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": consumerPath}}}}
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), binding, edge, logical)
			reviews := 0
			target, err := resolveProviderAgentIngress(context.Background(), client, "consumer", "kubernetesclusters", "edge", tc.token, func(_ context.Context, token string) (*authnv1.TokenReview, error) {
				reviews++
				if token != tc.token {
					t.Fatal("reviewed another credential")
				}
				subject := tc.subject
				if subject == "agent" {
					subject = "system:serviceaccount:default:" + identity.ServiceAccountName(identity.Owner{Provider: "edges", Group: "edges.railgrid.ai", Kind: "KubernetesCluster", Name: "edge", UID: "current-edge-uid"})
				}
				issuer := tc.issuer
				if issuer == "" && tc.name != "missing issuer with matching account name" {
					issuer = "consumer"
				}
				return &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: tc.authenticated, Audiences: []string{tc.audience}, User: authnv1.UserInfo{Username: subject, Extra: map[string]authnv1.ExtraValue{"authentication.kcp.io/cluster-name": {issuer}}}}}, nil
			})
			if (err == nil) != tc.want {
				t.Fatalf("target=%+v err=%v", target, err)
			}
			if tc.want && (target != (providers.AgentIngressTarget{OrgUUID: "org", ExportPath: bindingPath, ExportName: "edges-export", ExportCluster: "org-provider"})) {
				t.Fatal("wrong binding")
			}
			if tc.token == "join" && reviews != 0 {
				t.Fatal("bootstrap token leaked into TokenReview path")
			}
		})
	}
}
