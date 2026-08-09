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

package hub

import (
	"testing"

	listeneroptions "github.com/platform-mesh/kubernetes-graphql-gateway/listener/options"
)

func TestEmbeddedGraphQLListenerOptionsDisableStandaloneListeners(t *testing.T) {
	hubOpts := &Options{
		GraphQLAPIExportSliceName:      "graphql.apiexports",
		GraphQLAPIExportLogicalCluster: "root:kedge:providers",
	}
	const (
		kubeconfigPath = "/tmp/embedded-graphql-kubeconfig"
		grpcAddr       = "127.0.0.1:25063"
	)

	got := newEmbeddedGraphQLListenerOptions(hubOpts, kubeconfigPath, grpcAddr)

	if got.Common.HealthProbeBindAddress != "0" {
		t.Errorf("health probe bind address = %q, want disabled (\"0\")", got.Common.HealthProbeBindAddress)
	}
	if got.Common.Metrics.BindAddress != "0" {
		t.Errorf("metrics bind address = %q, want disabled (\"0\")", got.Common.Metrics.BindAddress)
	}

	// Embedded GraphQL still relies on the listener's gRPC transport and kcp
	// anchor configuration; disabling the auxiliary servers must not alter
	// those contracts.
	if got.Provider != "kcp" {
		t.Errorf("provider = %q, want kcp", got.Provider)
	}
	if got.SchemaHandler != "grpc" {
		t.Errorf("schema handler = %q, want grpc", got.SchemaHandler)
	}
	if got.Common.Kubeconfig != kubeconfigPath {
		t.Errorf("kubeconfig = %q, want %q", got.Common.Kubeconfig, kubeconfigPath)
	}
	if got.GRPCListenAddr != grpcAddr {
		t.Errorf("gRPC listen address = %q, want %q", got.GRPCListenAddr, grpcAddr)
	}
	if got.ResourceGVR != "apibindings.v1alpha2.apis.kcp.io" {
		t.Errorf("resource GVR = %q, want APIBinding anchor", got.ResourceGVR)
	}
	wantAnchor := `object.spec.reference.export.name == "graphql.apiexports"`
	if got.AnchorResource != wantAnchor {
		t.Errorf("anchor resource = %q, want %q", got.AnchorResource, wantAnchor)
	}
	if got.ProviderKcp == nil {
		t.Fatal("provider kcp options are nil")
	}
	if got.ProviderKcp.APIExportEndpointSliceName != hubOpts.GraphQLAPIExportSliceName {
		t.Errorf("APIExportEndpointSliceName = %q, want %q", got.ProviderKcp.APIExportEndpointSliceName, hubOpts.GraphQLAPIExportSliceName)
	}
	if got.ProviderKcp.APIExportEndpointSliceLogicalCluster != hubOpts.GraphQLAPIExportLogicalCluster {
		t.Errorf("APIExportEndpointSliceLogicalCluster = %q, want %q", got.ProviderKcp.APIExportEndpointSliceLogicalCluster, hubOpts.GraphQLAPIExportLogicalCluster)
	}
	if got.ProviderKcp.WorkspaceSchemaKubeconfigOverride != kubeconfigPath {
		t.Errorf("workspace schema kubeconfig = %q, want %q", got.ProviderKcp.WorkspaceSchemaKubeconfigOverride, kubeconfigPath)
	}

	// Check that options which are not specific to embedded transport retain
	// the upstream defaults.
	wantDefaults := listeneroptions.NewOptions()
	if got.GRPCMaxSendMsgSize != wantDefaults.GRPCMaxSendMsgSize {
		t.Errorf("gRPC max send message size = %d, want upstream default %d", got.GRPCMaxSendMsgSize, wantDefaults.GRPCMaxSendMsgSize)
	}
	if got.Common.Metrics.Secure != wantDefaults.Common.Metrics.Secure {
		t.Errorf("metrics secure = %t, want upstream default %t", got.Common.Metrics.Secure, wantDefaults.Common.Metrics.Secure)
	}
}
