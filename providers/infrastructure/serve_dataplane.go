// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"log"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/provider-infrastructure/dataplane"
	"github.com/faroshq/provider-infrastructure/tenant"
)

// instanceGroupVersion is the group/version of every per-template instance CRD;
// the resource (plural) comes from the request path.
var instanceGroupVersion = schema.GroupVersion{Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1"}

// buildDataPlaneHandler wires the data-plane subresource handler for serve.
// Returns nil (the handler then reports 503) when the provider has no kcp config
// or no runtime cluster — the dev/REST-only flow.
//
//   - InstanceGetter authorizes + fetches the instance AS THE CALLER via the
//     tenant client factory (caller RBAC is the access gate).
//   - ContractGetter reads Templates with the provider's own kcp client
//     (platform-owned, cluster-scoped) to find the dataPlane contract.
//   - Runtime holds the only runtime-cluster credential in the request path.
func buildDataPlaneHandler(kcpConfig *rest.Config) *dataplane.Handler {
	if kcpConfig == nil {
		log.Printf("data plane: disabled (no kcp config)")
		return nil
	}

	providerDyn, err := dynamic.NewForConfig(kcpConfig)
	if err != nil {
		log.Printf("data plane: disabled (provider dynamic client: %v)", err)
		return nil
	}

	runtimeCfg, src := loadDataPlaneRuntimeConfig()
	runtime, err := dataplane.NewRuntime(runtimeCfg)
	if err != nil {
		log.Printf("data plane: disabled (runtime client: %v)", err)
		return nil
	}
	if runtime == nil {
		log.Printf("data plane: disabled (no runtime cluster config: KRO_KUBECONFIG unset, not in a pod)")
		return nil
	}
	runtimeDyn, err := dynamic.NewForConfig(runtimeCfg)
	if err != nil {
		log.Printf("data plane: disabled (runtime dynamic client: %v)", err)
		return nil
	}

	log.Printf("data plane: enabled (runtime cluster: %s)", src)
	return dataplane.NewHandler(
		&tenantInstanceGetter{factory: tenant.NewClientFactory(kcpConfig)},
		dataplane.NewTemplateContractGetter(providerDyn),
		runtime,
		dataplane.WithPreviewRouteManager(dataplane.NewPreviewRouteManager(runtimeDyn, dataplane.PreviewRouteConfigFromEnv())),
	)
}

// loadDataPlaneRuntimeConfig resolves the cluster where workloads run, matching
// the controller manager's kro runtime resolution: explicit KRO_KUBECONFIG,
// else the pod's in-cluster config. Returns (nil, "") when neither is present.
func loadDataPlaneRuntimeConfig() (*rest.Config, string) {
	if p := os.Getenv("KRO_KUBECONFIG"); p != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			log.Printf("data plane: KRO_KUBECONFIG set but unloadable: %v", err)
			return nil, ""
		}
		return cfg, "KRO_KUBECONFIG=" + p
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, "in-cluster"
	}
	return nil, ""
}

// tenantInstanceGetter authorizes and fetches a workload instance as the caller.
// The instance CRs are cluster-scoped in the tenant's kcp workspace, so the GET
// is namespaceless. A 403/404 from the caller's RBAC is the data-plane gate.
type tenantInstanceGetter struct {
	factory *tenant.ClientFactory
}

func (g *tenantInstanceGetter) Get(ctx context.Context, workspace, token, resource, name string) (*unstructured.Unstructured, error) {
	dyn, err := g.factory.For(workspace, token)
	if err != nil {
		return nil, err
	}
	gvr := instanceGroupVersion.WithResource(resource)
	return dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
}
