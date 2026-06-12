// Copyright 2026 The Faros Authors.
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

	"github.com/faroshq/provider-infrastructure/controller/application"
	"github.com/faroshq/provider-infrastructure/install"
)

// startApplicationController starts the cross-tenant Application instance
// controller (fqdn stamping + OIDC client-secret bridge) in a goroutine.
//
// It runs on the provider's APIExport virtual workspace, so it needs the
// provider kcp config (the same one the controller manager uses). It's
// opt-in: without KEDGE_APP_BASE_DOMAIN and KRO_KUBECONFIG there is nothing
// for it to do (no app domain to compute, no runtime cluster to bridge
// secrets onto), so it stays disabled — preserving the REST-only/stub flow.
func startApplicationController(ctx context.Context, providerConfig *rest.Config) {
	if providerConfig == nil {
		return
	}
	baseDomain := os.Getenv("KEDGE_APP_BASE_DOMAIN")
	kroKubeconfig := os.Getenv("KRO_KUBECONFIG")
	if baseDomain == "" || kroKubeconfig == "" {
		log.Printf("application controller: disabled (need KEDGE_APP_BASE_DOMAIN + KRO_KUBECONFIG; have domain=%v kro=%v)",
			baseDomain != "", kroKubeconfig != "")
		return
	}

	runtimeClient, err := runtimeDynamicClient(kroKubeconfig)
	if err != nil {
		log.Printf("application controller: NOT started: %v", err)
		return
	}

	ctrl, err := application.New(application.Config{
		ProviderConfig: providerConfig,
		APIExportName:  install.APIExportName,
		BaseDomain:     baseDomain,
		Runtime:        runtimeClient,
	})
	if err != nil {
		log.Printf("application controller: NOT started: %v", err)
		return
	}

	go func() {
		log.Printf("application controller: starting (apiExport=%s baseDomain=%s)", install.APIExportName, baseDomain)
		if err := ctrl.Start(ctx); err != nil {
			log.Printf("application controller: stopped: %v", err)
		}
	}()
}

// runtimeDynamicClient builds a dynamic client for the kro runtime cluster
// from KRO_KUBECONFIG — the same cluster the kro backend authors RGDs on and
// where the bridged Secret must land.
func runtimeDynamicClient(kubeconfigPath string) (dynamic.Interface, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading KRO_KUBECONFIG for application controller: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("runtime dynamic client: %w", err)
	}
	return dyn, nil
}
