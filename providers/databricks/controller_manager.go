// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Multicluster controller manager — reconciles the provider's tenant-authored
// CRs (Connection / Warehouse / Table) across every workspace that bound the
// APIExport.
//
// Leader-elected: serving resolves tenant clusters through the slice-backed
// authority (tenant.SliceAuthority), never through this manager, so only the
// replica holding the Lease runs the reconcilers while every replica keeps
// serving actions/discovery/MCP. The manager is rebuilt fresh each term — a
// stopped controller-runtime manager cannot be restarted — and a per-term
// watchdog stops it when the endpoint slice loses its last usable URL, so the
// next term rebuilds against live discovery instead of silently watching
// nothing.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkinstall "github.com/faroshq/provider-sdk/install"
	"github.com/faroshq/provider-sdk/leaderelection"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/connection"
	"github.com/faroshq/provider-databricks/controller/table"
	"github.com/faroshq/provider-databricks/controller/warehouse"
	databricksscheme "github.com/faroshq/provider-databricks/scheme"
	"github.com/faroshq/provider-databricks/tenant"
)

const (
	controllerRetryInterval    = 15 * time.Second
	controllerRetryIntervalMin = 1 * time.Second
	controllerRetryIntervalMax = 5 * time.Minute

	controllerStartupTimeout    = 1 * time.Minute
	controllerStartupTimeoutMin = 1 * time.Second
	controllerStartupTimeoutMax = 5 * time.Minute

	// controllerLeaseName gates the reconcilers on a Lease in the provider
	// workspace ("default" namespace — kcp serves Leases in every logical
	// cluster).
	controllerLeaseName = "databricks-controllers"

	// slicePollInterval paces the per-term endpoint watchdog and the pre-start
	// wait for a usable endpoint.
	slicePollInterval = 5 * time.Second
)

type controllerMode string

const (
	controllerModeRESTOnly controllerMode = "rest-only"
	controllerModeRequired controllerMode = "required"
)

// controllerState is independent from HTTP process liveness AND from
// readiness: under leader election a standby replica never runs the manager,
// so controller state is reported on /readyz for observability but does not
// gate it — serving readiness is the slice-backed authority's.
type controllerState string

const (
	controllerStateRESTOnly controllerState = "rest-only"
	controllerStateStarting controllerState = "starting"
	controllerStateReady    controllerState = "ready"
	controllerStateFailed   controllerState = "failed"
	controllerStateStopped  controllerState = "stopped"
	controllerStateStandby  controllerState = "standby"
)

type controllerHealthSnapshot struct {
	Required bool
	State    controllerState
	Error    string
}

type controllerHealth struct {
	mu              sync.RWMutex
	required        bool
	state           controllerState
	lastErr         string
	dependencyReady func() bool
}

func newControllerHealth(required bool) *controllerHealth {
	state := controllerStateRESTOnly
	if required {
		state = controllerStateStarting
	}
	return &controllerHealth{required: required, state: state}
}

func (h *controllerHealth) snapshot() controllerHealthSnapshot {
	if h == nil {
		return controllerHealthSnapshot{State: controllerStateRESTOnly}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return controllerHealthSnapshot{Required: h.required, State: h.state, Error: h.lastErr}
}

func (h *controllerHealth) setState(state controllerState, err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = state
	h.lastErr = ""
	if err != nil {
		h.lastErr = err.Error()
	}
}

func (h *controllerHealth) markStarting() { h.setState(controllerStateStarting, nil) }
func (h *controllerHealth) markReady()    { h.setState(controllerStateReady, nil) }
func (h *controllerHealth) markStandby()  { h.setState(controllerStateStandby, nil) }

func (h *controllerHealth) markFailed(err error) { h.setState(controllerStateFailed, err) }
func (h *controllerHealth) markStopped(err error) {
	h.setState(controllerStateStopped, err)
}

// ready gates /readyz and heartbeat eligibility on SERVING readiness only:
// the dependency check (tenant factory configured + slice authority
// discovered). Controller state deliberately does not participate — under
// leader election a standby replica runs no manager yet must serve traffic.
func (h *controllerHealth) ready() bool {
	if h == nil {
		return true
	}
	h.mu.RLock()
	check := h.dependencyReady
	h.mu.RUnlock()
	return check == nil || check()
}

// setDependencyReady adds a provider-specific readiness gate without
// conflating process liveness with tenant/action availability. It is installed
// before serving and remains immutable for the provider lifetime.
func (h *controllerHealth) setDependencyReady(check func() bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.dependencyReady = check
	h.mu.Unlock()
}

func (h *controllerHealth) heartbeatStatus() string {
	if h.ready() {
		return "healthy"
	}
	if h.snapshot().State == controllerStateStarting {
		return "starting"
	}
	return "unhealthy"
}

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

func controllerOptionsForRetryableManager() ctrlconfig.Controller {
	skipNameValidation := true
	// Controller names register process-globally; a manager rebuilt for a
	// later leadership term must skip that check.
	return ctrlconfig.Controller{SkipNameValidation: &skipNameValidation}
}

// runLeaderElectedControllers waits for a provider kubeconfig, then campaigns
// for the controller lease forever; every won term runs one freshly built
// manager. Setup failures inside a term (endpoint slice missing, VW not
// published) step down and re-campaign, which replaces the old bespoke
// retry/readiness machinery — the election loop IS the retry loop.
func runLeaderElectedControllers(ctx context.Context, health *controllerHealth, loadConfig func() (*rest.Config, error), onConfig func(*rest.Config) error, validator backend.Validator) {
	retryInterval := controllerRetryIntervalFromEnv()
	var config *rest.Config
	for {
		c, err := loadConfig()
		if err == nil {
			config = c
			break
		}
		health.markFailed(err)
		log.Printf("controller manager waiting for kubeconfig: %v; retrying in %s", err, retryInterval)
		select {
		case <-ctx.Done():
			health.markStopped(ctx.Err())
			return
		case <-time.After(retryInterval):
		}
	}
	if onConfig != nil {
		if err := onConfig(config); err != nil {
			// Caller-token client configuration failed; actions fail closed
			// with a clear error, controllers still run.
			log.Printf("controller manager: %v", err)
		}
	}
	health.markStandby()
	if err := leaderelection.Run(ctx, leaderelection.Options{
		Config:    config,
		Namespace: leaderelection.DefaultNamespace,
		Name:      controllerLeaseName,
	}, func(termCtx context.Context) {
		health.markStarting()
		if err := runControllerManagerTerm(termCtx, config, validator, health); err != nil {
			health.markFailed(err)
			log.Printf("controller manager exited: %v", err)
		}
		if termCtx.Err() == nil {
			// Still leader but the term callback returned — leaderelection
			// releases the lease; record standby so /readyz introspection
			// doesn't report a stale "ready".
			health.markStandby()
		}
	}); err != nil {
		health.markFailed(err)
		log.Printf("controller leader election failed; controllers are not running: %v", err)
		return
	}
	health.markStopped(ctx.Err())
}

// runControllerManagerTerm builds and runs the manager for one leadership
// term, blocking in Start until the term ends or the endpoint watchdog fires.
func runControllerManagerTerm(ctx context.Context, config *rest.Config, validator backend.Validator, health *controllerHealth) error {
	ctrl.SetLogger(klog.NewKlogr())
	scheme := databricksscheme.NewScheme()

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	workspacePath := envOr("DATABRICKS_WORKSPACE_PATH", defaultWorkspacePath)
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, dyn, apiExportName, apiExportName, workspacePath); err != nil {
		return fmt.Errorf("ensuring APIExportEndpointSlice: %w", err)
	}

	// The endpoint slice is the multicluster provider's discovery input: a
	// manager built before it carries a usable URL appears started while
	// silently watching no tenant workspaces. Wait, bounded per term.
	slice := dyn.Resource(apiExportEndpointSliceGVR)
	if err := waitForSliceEndpoint(ctx, slice, apiExportName, controllerStartupTimeoutFromEnv()); err != nil {
		return err
	}

	provider, err := apiexport.New(config, apiExportName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: controllerOptionsForRetryableManager(),
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}
	if err := (&connection.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("connection controller: %w", err)
	}
	if err := (&warehouse.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("warehouse controller: %w", err)
	}
	if err := (&table.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("table controller: %w", err)
	}

	// Endpoint watchdog: the VW URL set can disappear after startup (shard
	// topology change, slice rewritten). Stop the manager so the next term
	// rebuilds against live discovery.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchdogErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(slicePollInterval)
		defer ticker.Stop()
		misses := 0
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			discovered, err := sliceEndpointDiscovered(runCtx, slice, apiExportName)
			switch {
			case err != nil || !discovered:
				// Tolerate transient read failures; three consecutive misses
				// (~15s) is a real loss.
				misses++
				if misses < 3 {
					continue
				}
				if err == nil {
					err = fmt.Errorf("APIExportEndpointSlice %q has no usable endpoint", apiExportName)
				}
				watchdogErr <- err
				cancel()
				return
			default:
				misses = 0
			}
		}
	}()

	health.markReady()
	log.Printf("databricks controller manager starting (endpointSlice=%s)", apiExportName)
	err = mgr.Start(runCtx)
	select {
	case werr := <-watchdogErr:
		return fmt.Errorf("multicluster provider lost readiness: %w", werr)
	default:
	}
	return err
}

// waitForSliceEndpoint blocks until the endpoint slice publishes at least one
// usable URL, the timeout elapses, or ctx ends.
func waitForSliceEndpoint(ctx context.Context, slice dynamic.ResourceInterface, name string, timeout time.Duration) error {
	timeout = clampControllerDuration(timeout, controllerStartupTimeout, controllerStartupTimeoutMin, controllerStartupTimeoutMax)
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		discovered, err := sliceEndpointDiscovered(waitCtx, slice, name)
		if err != nil && waitCtx.Err() == nil {
			return err
		}
		if discovered {
			return nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("multicluster provider discovery timed out after %s: APIExportEndpointSlice %q has no usable endpoint", timeout, name)
		case <-time.After(slicePollInterval):
		}
	}
}

// sliceEndpointDiscovered reports whether the slice carries at least one
// endpoint with a non-empty URL. NotFound is "not yet", not an error.
func sliceEndpointDiscovered(ctx context.Context, slice dynamic.ResourceInterface, name string) (bool, error) {
	obj, err := slice.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading APIExportEndpointSlice %q: %w", name, err)
	}
	return len(tenant.SliceEndpointURLs(obj)) > 0, nil
}

func controllerModeFromEnv() controllerMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DATABRICKS_CONTROLLER_MODE"))) {
	case string(controllerModeRESTOnly), "rest_only", "rest":
		return controllerModeRESTOnly
	case string(controllerModeRequired), "controller":
		return controllerModeRequired
	case "":
		if strings.EqualFold(strings.TrimSpace(os.Getenv("DATABRICKS_REST_ONLY")), "true") {
			return controllerModeRESTOnly
		}
		if strings.TrimSpace(os.Getenv("FAROS_PROVIDER_KUBECONFIG")) != "" || strings.TrimSpace(os.Getenv("DATABRICKS_KUBECONFIG")) != "" || strings.TrimSpace(os.Getenv("KUBECONFIG")) != "" {
			return controllerModeRequired
		}
		return controllerModeRESTOnly
	default:
		log.Printf("unknown DATABRICKS_CONTROLLER_MODE=%q; requiring controller", os.Getenv("DATABRICKS_CONTROLLER_MODE"))
		return controllerModeRequired
	}
}

func controllerRetryIntervalFromEnv() time.Duration {
	return controllerDurationFromEnv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", controllerRetryInterval, controllerRetryIntervalMin, controllerRetryIntervalMax)
}

func controllerStartupTimeoutFromEnv() time.Duration {
	return controllerDurationFromEnv("DATABRICKS_CONTROLLER_STARTUP_TIMEOUT", controllerStartupTimeout, controllerStartupTimeoutMin, controllerStartupTimeoutMax)
}

func controllerDurationFromEnv(key string, defaultValue, minValue, maxValue time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		log.Printf("invalid %s=%q; using %s", key, raw, defaultValue)
		return defaultValue
	}
	if value < minValue {
		log.Printf("%s=%q is below the safe minimum; using %s", key, raw, minValue)
		return minValue
	}
	if value > maxValue {
		log.Printf("%s=%q exceeds the safe maximum; using %s", key, raw, maxValue)
		return maxValue
	}
	return value
}

func clampControllerDuration(value, defaultValue, minValue, maxValue time.Duration) time.Duration {
	if value <= 0 {
		return defaultValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func parseBoolEnv(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}
