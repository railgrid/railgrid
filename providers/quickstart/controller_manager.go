/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

// The quickstart provider's controller manager — the Pillar 1 half of the
// contract, in the smallest shape that is still correct.
//
// Greetings live in TENANT workspaces, not in the provider's own, so this is a
// multicluster manager over provider-sdk/apiexportprovider: it watches the
// provider's APIExportEndpointSlice, engages one cluster per tenant workspace
// that bound the APIExport, and gives each reconcile a client for the
// workspace its event came from. The provider identity never leaves its
// workspace; the APIExport virtual workspace is the only reach it has.
//
// Three things are wrapped around that, and every provider needs all three:
//
//   - leaderelection.Run, so scaling the Deployment past one replica keeps
//     every Greeting single-writer. Non-leaders keep serving HTTP.
//   - vwhealth.Readiness with the multicluster provider attached for the
//     duration of each leadership term, so /readyz and the hub heartbeat say
//     whether tenant workspaces are actually being watched rather than merely
//     whether the process is up.
//   - a manager rebuilt per term: a stopped controller-runtime manager cannot
//     be restarted.

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
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/railgrid/provider-sdk/apiexportprovider"
	"github.com/railgrid/provider-sdk/leaderelection"
	"github.com/railgrid/provider-sdk/vwhealth"

	"github.com/railgrid/provider-quickstart/controller/greeting"
	quickstartscheme "github.com/railgrid/provider-quickstart/scheme"
)

// endpointSliceName is the APIExportEndpointSlice the multicluster provider
// watches to discover tenant workspaces. `init` creates it (through
// provider-sdk/install.Bootstrap) and names it after the APIExport, which is
// the convention every provider follows.
const endpointSliceName = apiExportName

// controllerLeaseName is the Lease the reconciler campaigns for. It lives in
// the "default" namespace of the PROVIDER's kcp workspace — kcp serves Leases
// in every logical cluster — so no RBAC on the hosting cluster is involved.
const controllerLeaseName = "quickstart-controllers"

// errControllerDisabled is the sentinel runServe checks so it can log and keep
// serving the portal when no provider kubeconfig is mounted.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")

// startControllerManager campaigns for the controller lease and, while leader,
// runs the multicluster manager. It returns as soon as the campaign is under
// way: the caller keeps serving HTTP on every replica, leader or not.
func startControllerManager(ctx context.Context, config *rest.Config, ready *vwhealth.Readiness) error {
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
			if err := runControllerManager(termCtx, config, ready); err != nil {
				log.Printf("controller manager exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; controllers are not running: %v", err)
		}
	}()
	return nil
}

// runControllerManager builds the manager and blocks in Start until the
// leadership term ends. Called once per term.
func runControllerManager(ctx context.Context, config *rest.Config, ready *vwhealth.Readiness) error {
	scheme := quickstartscheme.NewScheme()

	provider, err := apiexportprovider.New(config, endpointSliceName, apiexportprovider.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	// Readiness follows the term: from the moment this replica is leader until
	// it stops being one, /readyz and the heartbeat report whether tenant
	// workspaces are being watched.
	if ready != nil {
		defer ready.Attach("controllers", provider)()
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme: scheme,
		// The provider serves its own HTTP; no second listener.
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Controller names register process-globally, so the manager built for
		// a later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	if err := (&greeting.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("greeting controller: %w", err)
	}

	log.Printf("quickstart controller manager starting (endpointSlice=%s)", endpointSliceName)
	return mgr.Start(ctx)
}

// loadProviderConfig resolves the rest.Config for the provider's own kcp
// workspace, in order: RAILGRID_PROVIDER_KUBECONFIG (the kubeconfig the hub
// mints and the chart mounts), KUBECONFIG, in-cluster. It returns
// errControllerDisabled when none resolve, which keeps `serve` usable for
// portal-only development.
func loadProviderConfig() (*rest.Config, error) {
	for _, env := range []string{"RAILGRID_PROVIDER_KUBECONFIG", "KUBECONFIG"} {
		if path := os.Getenv(env); path != "" {
			c, err := clientcmd.BuildConfigFromFlags("", path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", env, err)
			}
			return c, nil
		}
	}
	c, err := rest.InClusterConfig()
	if err != nil {
		return nil, errControllerDisabled
	}
	return c, nil
}
