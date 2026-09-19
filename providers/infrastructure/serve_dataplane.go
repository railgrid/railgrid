// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"log"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/provider-infrastructure/dataplane"
	sdkdataplane "github.com/railgrid/provider-sdk/dataplane"
)

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

	// The data plane's only credential is the caller's own bearer: the SDK
	// factory keeps the provider kubeconfig's host and CA and drops every way
	// of authenticating as the provider, so a request with no token fails
	// rather than silently acting as the platform.
	callers, err := sdkdataplane.NewCallerFactory(kcpConfig)
	if err != nil {
		log.Printf("data plane: disabled (caller factory: %v)", err)
		return nil
	}
	options := []dataplane.HandlerOption{}
	// Persistent component execution is the only executor: a dedicated worker
	// container owns lifecycle state while sharing the component PVC/toolchain.
	executor, execErr := dataplane.NewPersistentExecutor(runtime)
	if execErr != nil {
		log.Printf("data plane exec: disabled: %v", execErr)
	} else {
		options = append(options, dataplane.WithExec(executor))
	}

	log.Printf("data plane: enabled (runtime cluster: %s)", src)
	return dataplane.NewHandler(
		callers,
		dataplane.NewTemplateContractGetter(providerDyn),
		runtime,
		options...,
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
