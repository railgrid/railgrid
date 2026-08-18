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
// their status back. OPT-IN via FAROS_PROVIDER_KUBECONFIG — without it the
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
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/faroshq/provider-sdk/leaderelection"
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

// controllerLeaseName gates the reconcilers on a Lease in the provider
// workspace ("default" namespace — kcp serves Leases in every logical
// cluster), so the chart's multi-replica default runs one set of controllers,
// not one per replica. Non-leaders keep serving REST/portal.
const controllerLeaseName = "vibe-studio-controllers"

// startControllerManager campaigns for the controller lease and — while
// leader — runs the multicluster manager with the Project + Session
// reconcilers. A nil config means "skip the manager, run REST-only". st backs
// the Session reconciler's status mirror + purge.
func startControllerManager(ctx context.Context, config *rest.Config, st store.Store, hubBase string, hubInsecure bool) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())

	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    config,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := runControllerManager(termCtx, config, st, hubBase, hubInsecure); err != nil {
				log.Printf("controller manager exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; controllers are not running: %v", err)
		}
	}()
	return nil
}

// runControllerManager builds the multicluster manager and blocks in Start
// until the leadership term ends. Called once per term — a stopped
// controller-runtime manager cannot be restarted.
func runControllerManager(ctx context.Context, config *rest.Config, st store.Store, hubBase string, hubInsecure bool) error {
	scheme := vibescheme.NewScheme()

	provider, err := apiexport.New(config, endpointSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP; disable controller-runtime metrics
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
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

	log.Printf("vibe-studio controller manager starting (endpointSlice=%s)", endpointSliceName)
	return mgr.Start(ctx)
}

// loadControllerConfig resolves the rest.Config for the provider's kcp
// workspace: FAROS_PROVIDER_KUBECONFIG → KUBECONFIG → in-cluster SA. Returns
// errControllerDisabled when none resolve.
func loadControllerConfig() (*rest.Config, error) {
	if p := os.Getenv("FAROS_PROVIDER_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("FAROS_PROVIDER_KUBECONFIG: %w", err)
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
