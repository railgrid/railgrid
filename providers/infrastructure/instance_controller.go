// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/provider-sdk/leaderelection"

	krobackend "github.com/railgrid/provider-infrastructure/backend/kro"
	"github.com/railgrid/provider-infrastructure/controller/instance"
	"github.com/railgrid/provider-infrastructure/install"
	"github.com/railgrid/provider-infrastructure/networkpolicy"
)

// startInstanceController starts the cross-tenant Instance controller —
// the seam between the flattened tenant-facing Instance kind and the
// per-template kro CRs on the runtime cluster (values validation, platform
// stamps, secret bridging, runtime sync, status mirror) — in a goroutine.
//
// It runs on the provider's APIExport virtual workspace, so it needs the
// provider kcp config (the same one the controller manager uses). The kro
// runtime cluster is resolved the same way the kro backend resolves it —
// explicit KRO_KUBECONFIG, else the pod's in-cluster config — so the
// operator's in-cluster-runtime mode is honored. Without a runtime cluster
// there is nothing to materialize instances on, so the controller stays
// disabled (dev/REST-only flows).
//
// RAILGRID_APP_BASE_DOMAIN is optional here: without it, instances that ask to
// be published fail their reconcile with a clear message, while internal
// templates keep provisioning.
func startInstanceController(ctx context.Context, providerConfig *rest.Config) {
	if providerConfig == nil {
		return
	}

	runtimeClient, runtimeCfg, runtimeSrc, err := runtimeDynamicClient()
	if err != nil {
		log.Printf("instance controller: disabled (no kro runtime cluster: %v)", err)
		return
	}

	baseDomain := os.Getenv("RAILGRID_APP_BASE_DOMAIN")

	// Tenant runtime-namespace ingress isolation (off unless
	// RAILGRID_TENANT_NETWORK_POLICY_ENABLED=true). The exposure Gateway's
	// namespace is always admitted, resolved exactly as the kro backend
	// resolves ${railgrid.gatewayNamespace} so the policy follows the HTTPRoutes.
	gatewayNamespace := os.Getenv("RAILGRID_GATEWAY_NAMESPACE")
	if gatewayNamespace == "" {
		gatewayNamespace = krobackend.DefaultGatewayNamespace
	}
	netpol, err := networkpolicy.FromEnv(gatewayNamespace)
	if err == nil {
		err = netpol.Validate()
	}
	if err != nil {
		log.Printf("instance controller: NOT started: %v", err)
		return
	}
	log.Printf("instance controller: tenant network policy enabled=%t gatewayNamespace=%q allowedNamespaces=%q allowedCIDRs=%q",
		netpol.Enabled, netpol.GatewayNamespace, netpol.AllowedNamespaces, netpol.AllowedCIDRs)

	// Leader-elected: instances own runtime-cluster state (kro CRs, bridged
	// secrets), so exactly one replica may reconcile them. The controller —
	// and its manager — is rebuilt fresh each term; a stopped
	// controller-runtime manager cannot be restarted.
	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    providerConfig,
			Namespace: leaderelection.DefaultNamespace,
			Name:      instanceLeaseName,
		}, func(termCtx context.Context) {
			ctrl, err := instance.New(instance.Config{
				ProviderConfig:       providerConfig,
				APIExportName:        install.APIExportName,
				BaseDomain:           baseDomain,
				Runtime:              runtimeClient,
				RuntimeConfig:        runtimeCfg,
				CodingSandboxEnabled: codingSandboxEnabled(),
				NetworkPolicy:        netpol,
			})
			if err != nil {
				log.Printf("instance controller: NOT started: %v", err)
				return
			}
			log.Printf("instance controller: starting (apiExport=%s baseDomain=%q runtime=%s)", install.APIExportName, baseDomain, runtimeSrc)
			if err := ctrl.Start(termCtx); err != nil {
				log.Printf("instance controller: stopped: %v", err)
			}
		}); err != nil {
			log.Printf("instance controller: leader election failed; controller is not running: %v", err)
		}
	}()
}

// runtimeDynamicClient builds a dynamic client for the kro runtime cluster —
// the same cluster the kro backend authors RGDs on — and returns the
// rest.Config it was built from (the Instance controller runs a watch cache
// over it). It mirrors the kro backend's resolution in controller_manager.go:
// explicit KRO_KUBECONFIG, else the pod's in-cluster config (the operator's
// in-cluster-runtime mode). Errors when neither is available
// (dev/REST-only), so the controller stays disabled rather than pointing at
// the wrong cluster. Returns the source for logging.
func runtimeDynamicClient() (dynamic.Interface, *rest.Config, string, error) {
	var cfg *rest.Config
	var src string
	if p := os.Getenv("KRO_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, nil, "", fmt.Errorf("loading KRO_KUBECONFIG: %w", err)
		}
		cfg, src = c, "KRO_KUBECONFIG="+p
	} else if c, err := rest.InClusterConfig(); err == nil {
		cfg, src = c, "in-cluster"
	} else {
		return nil, nil, "", fmt.Errorf("KRO_KUBECONFIG unset and not running in a pod")
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("runtime dynamic client: %w", err)
	}
	return dyn, cfg, src, nil
}
