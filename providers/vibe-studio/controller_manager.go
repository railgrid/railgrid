// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package main

// Multicluster controller manager — reconciles Project CRs across EVERY
// tenant workspace that has bound this provider's APIExport, via the kcp
// apiexport multicluster provider. The library watches the provider's
// APIExportEndpointSlice and engages one wildcard watcher PER SHARD (the
// slice advertises one endpoint per kcp shard — binding a single URL would
// silently hide every tenant on the other shards).
//
// This is where the deterministic lifecycle lives: the wizard only writes
// Project spec; the reconciler converges infrastructure instances and mirrors
// their status back. OPT-IN via KEDGE_PROVIDER_KUBECONFIG — without it the
// provider runs REST/portal-only.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-vibe-studio/controller/project"
	"github.com/faroshq/provider-vibe-studio/controller/studio"
	"github.com/faroshq/provider-vibe-studio/controller/vibesession"
	vibescheme "github.com/faroshq/provider-vibe-studio/scheme"
	"github.com/faroshq/provider-vibe-studio/store"
)

// endpointSliceName matches the provider's APIExport name by convention
// (sdkinstall.Bootstrap creates the slice under the same name at init).
const endpointSliceName = apiExportName

// startControllerManager builds the multicluster manager and starts the
// Project + Session reconcilers. A nil config means "skip the manager, run
// REST-only". st backs the Session reconciler's status mirror + purge.
func startControllerManager(ctx context.Context, config *rest.Config, st store.Store, hubBase string, hubInsecure bool) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())
	scheme := vibescheme.NewScheme()

	provider, err := apiexport.New(config, endpointSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}

	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP; disable controller-runtime metrics
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	if err := (&studio.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up the studio controller: %w", err)
	}
	if err := (&project.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("project controller: %w", err)
	}
	if err := (&vibesession.Reconciler{Store: st, HubBase: hubBase, HubInsecure: hubInsecure}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("session controller: %w", err)
	}

	go func() {
		log.Printf("vibe-studio controller manager starting (endpointSlice=%s)", endpointSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("controller manager exited: %v", err)
		}
	}()
	return nil
}

// loadControllerConfig resolves the rest.Config for the provider's kcp
// workspace: KEDGE_PROVIDER_KUBECONFIG → KUBECONFIG → in-cluster SA. Returns
// errControllerDisabled when none resolve.
func loadControllerConfig() (*rest.Config, error) {
	if p := os.Getenv("KEDGE_PROVIDER_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("KEDGE_PROVIDER_KUBECONFIG: %w", err)
		}
		return c, nil
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("KUBECONFIG: %w", err)
		}
		return c, nil
	}
	c, err := rest.InClusterConfig()
	if err != nil {
		return nil, errControllerDisabled
	}
	return c, nil
}

// errControllerDisabled is the sentinel main() checks so it can log + continue
// without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")
