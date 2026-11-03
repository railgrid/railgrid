// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestProviderAgentIngressCredentialBoundary(t *testing.T) {
	const exportPath = "root:railgrid:tenants:org:providers:edges"
	for _, tc := range []struct {
		name, token, subject, audience, issuer, exportPath, consumerPath, bindingName string
		hashOnly, badBinding, deletingEdge, authenticated, missingExportCluster, want bool
		ambiguous, unrelatedOnly, unrelatedExtra, deletingBinding                     bool
	}{
		{name: "bootstrap", token: "join", want: true},
		{name: "arbitrary org binding name", token: "join", bindingName: "my-edges-api", want: true},
		{name: "arbitrary platform binding name", token: "join", bindingName: "platform-api", exportPath: "root:railgrid:providers:edges", want: true},
		{name: "unrelated named edges binding is ignored", token: "join", bindingName: "actual-api", unrelatedExtra: true, want: true},
		{name: "unrelated binding alone", token: "join", unrelatedOnly: true},
		{name: "ambiguous Edges bindings", token: "join", ambiguous: true},
		{name: "terminating binding", token: "join", deletingBinding: true},
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
			if tc.bindingName != "" {
				binding.SetName(tc.bindingName)
			}
			if tc.unrelatedOnly {
				_ = unstructured.SetNestedField(binding.Object, "root:railgrid:providers:other", "spec", "reference", "export", "path")
			}
			if tc.deletingBinding {
				binding.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-02T00:00:00Z"
			}
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
			objects := []runtime.Object{binding, edge, logical}
			if tc.ambiguous || tc.unrelatedExtra {
				extra := binding.DeepCopy()
				extra.SetName("edges")
				if tc.ambiguous {
					extra.SetName("another-edges-api")
				}
				if tc.unrelatedExtra {
					_ = unstructured.SetNestedField(extra.Object, "root:railgrid:providers:other", "spec", "reference", "export", "path")
				}
				objects = append(objects, extra)
			}
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}: "APIBindingList"}, objects...)
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

type agentIngressHTTPTransport func(*http.Request) (*http.Response, error)

func (f agentIngressHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderAgentIngressSharesTransportAndBoundsCheckTime(t *testing.T) {
	var transports, requests atomic.Int32
	cfg := &rest.Config{Host: "https://hub.invalid", WrapTransport: func(http.RoundTripper) http.RoundTripper {
		transports.Add(1)
		return agentIngressHTTPTransport(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 10*time.Second {
				t.Error("credential read has no bounded deadline")
			}
			var body string
			switch {
			case strings.HasSuffix(r.URL.Path, "/logicalclusters/cluster"):
				body = `{"apiVersion":"core.kcp.io/v1alpha1","kind":"LogicalCluster","metadata":{"name":"cluster","annotations":{"kcp.io/path":"root:railgrid:tenants:org:workspace"}}}`
			case strings.HasSuffix(r.URL.Path, "/apibindings"):
				body = `{"apiVersion":"apis.kcp.io/v1alpha2","kind":"APIBindingList","items":[{"metadata":{"name":"renamed-api"},"spec":{"reference":{"export":{"path":"root:railgrid:tenants:org:providers:edges","name":"edges-export"}}},"status":{"phase":"Bound","apiExportClusterName":"provider"}}]}`
			case strings.HasSuffix(r.URL.Path, "/kubernetesclusters/edge"):
				body = `{"apiVersion":"edges.railgrid.ai/v1alpha1","kind":"KubernetesCluster","metadata":{"name":"edge","uid":"uid"},"status":{"joinToken":"join"}}`
			default:
				t.Fatalf("unexpected credential read %s", r.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
	}}
	resolve := newProviderAgentIngressResolver(cfg)
	for range 2 {
		target, err := resolve(context.Background(), "consumer", "kubernetesclusters", "edge", "join")
		if err != nil || target.ExportCluster != "provider" {
			t.Fatalf("resolution: %+v %v", target, err)
		}
	}
	if transports.Load() != 1 || requests.Load() != 6 {
		t.Fatalf("transports=%d requests=%d", transports.Load(), requests.Load())
	}
}
