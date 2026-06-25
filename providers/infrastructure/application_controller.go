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
// opt-in on KEDGE_APP_BASE_DOMAIN: without an app domain there is no hostname
// to compute, so it stays disabled — preserving the REST-only/stub flow. The
// kro runtime cluster (where the bridged Secret lands) is resolved the same
// way the kro backend resolves it — explicit KRO_KUBECONFIG, else the pod's
// in-cluster config — so the operator's in-cluster-runtime mode is honored
// (it does NOT mount a KRO_KUBECONFIG in that mode).
func startApplicationController(ctx context.Context, providerConfig *rest.Config) {
	if providerConfig == nil {
		return
	}
	baseDomain := os.Getenv("KEDGE_APP_BASE_DOMAIN")
	if baseDomain == "" {
		log.Printf("application controller: disabled (need KEDGE_APP_BASE_DOMAIN to compute app hostnames)")
		return
	}

	runtimeClient, runtimeSrc, err := runtimeDynamicClient()
	if err != nil {
		log.Printf("application controller: disabled (no kro runtime cluster: %v)", err)
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
		log.Printf("application controller: starting (apiExport=%s baseDomain=%s runtime=%s)", install.APIExportName, baseDomain, runtimeSrc)
		if err := ctrl.Start(ctx); err != nil {
			log.Printf("application controller: stopped: %v", err)
		}
	}()
}

// runtimeDynamicClient builds a dynamic client for the kro runtime cluster the
// controller bridges OIDC secrets onto — the same cluster the kro backend
// authors RGDs on. It mirrors the kro backend's resolution in
// controller_manager.go: explicit KRO_KUBECONFIG, else the pod's in-cluster
// config (the operator's in-cluster-runtime mode). Errors when neither is
// available (dev/REST-only), so the controller stays disabled rather than
// pointing at the wrong cluster. Returns the source for logging.
func runtimeDynamicClient() (dynamic.Interface, string, error) {
	var cfg *rest.Config
	var src string
	if p := os.Getenv("KRO_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, "", fmt.Errorf("loading KRO_KUBECONFIG: %w", err)
		}
		cfg, src = c, "KRO_KUBECONFIG="+p
	} else if c, err := rest.InClusterConfig(); err == nil {
		cfg, src = c, "in-cluster"
	} else {
		return nil, "", fmt.Errorf("KRO_KUBECONFIG unset and not running in a pod")
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("runtime dynamic client: %w", err)
	}
	return dyn, src, nil
}
