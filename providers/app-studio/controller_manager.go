/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

// Multicluster controller manager — reconciles Project CRs across EVERY
// tenant workspace that has bound this provider's APIExport, via the kcp
// apiexport multicluster provider. The library watches the provider's
// APIExportEndpointSlice and engages one wildcard watcher PER SHARD (the
// slice advertises one endpoint per kcp shard — binding a single URL would
// silently hide every tenant on the other shards).
//
// This is where the deterministic lifecycle lives: the HTTP layer only
// writes Project spec; the reconciler converges infrastructure instances and
// mirrors their status back. OPT-IN via FAROS_PROVIDER_KUBECONFIG — without
// it the provider runs REST/portal-only.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-app-studio/bindings"
	"github.com/faroshq/provider-app-studio/controller/project"
	"github.com/faroshq/provider-app-studio/controller/session"
	"github.com/faroshq/provider-app-studio/controller/studio"
	appscheme "github.com/faroshq/provider-app-studio/scheme"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// endpointSliceName matches the provider's APIExport name by convention
// (sdkinstall.Bootstrap creates the slice under the same name at init).
const endpointSliceName = apiExportName

// errControllerDisabled is the sentinel runServe checks so it can log +
// continue without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")

// controllerRetryInterval bounds the delay between manager setup/start
// attempts. A controller can be unavailable while the provider workspace is
// being bootstrapped, so retrying is expected; the HTTP process must not claim
// readiness until one of those attempts has a live manager.
const controllerRetryInterval = 15 * time.Second

type controllerMode string

const (
	controllerModeRESTOnly controllerMode = "rest-only"
	controllerModeRequired controllerMode = "required"
)

// controllerState is deliberately independent from process liveness. The
// provider can keep serving its REST surface while a required controller is
// starting or recovering, but Kubernetes/provider readiness must remain false
// in those states.
type controllerState string

const (
	controllerStateRESTOnly controllerState = "rest-only"
	controllerStateStarting controllerState = "starting"
	controllerStateReady    controllerState = "ready"
	controllerStateFailed   controllerState = "failed"
	controllerStateStopped  controllerState = "stopped"
)

type controllerHealthSnapshot struct {
	Required bool
	State    controllerState
	Error    string
}

// controllerHealth is the small dependency shared by the HTTP readiness
// handler and the heartbeat loop. Keeping it instance-owned avoids a global
// readiness flag leaking across tests or future provider instances.
type controllerHealth struct {
	mu       sync.RWMutex
	required bool
	state    controllerState
	lastErr  string
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
	return controllerHealthSnapshot{
		Required: h.required,
		State:    h.state,
		Error:    h.lastErr,
	}
}

func (h *controllerHealth) markStarting() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = controllerStateStarting
	h.lastErr = ""
}

func (h *controllerHealth) markReady() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = controllerStateReady
	h.lastErr = ""
}

func (h *controllerHealth) markFailed(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = controllerStateFailed
	h.lastErr = ""
	if err != nil {
		h.lastErr = err.Error()
	}
}

func (h *controllerHealth) markStopped(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = controllerStateStopped
	h.lastErr = ""
	if err != nil {
		h.lastErr = err.Error()
	}
}

func (h *controllerHealth) markRESTOnly() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.required = false
	h.state = controllerStateRESTOnly
	h.lastErr = ""
}

func (h *controllerHealth) ready() bool {
	snapshot := h.snapshot()
	return !snapshot.Required || snapshot.State == controllerStateReady
}

func (h *controllerHealth) heartbeatStatus() string {
	snapshot := h.snapshot()
	if !snapshot.Required || snapshot.State == controllerStateReady {
		return "healthy"
	}
	if snapshot.State == controllerStateStarting {
		return "starting"
	}
	return "unhealthy"
}

// controllerDeps carries the runtime collaborators the Project reconciler
// shares with the HTTP layer: the on-disk workspace store (commit
// convergence reads it), the assistant-busy gate, and the hub address for
// MCP commit calls.
type controllerDeps struct {
	Actions     bindings.ActionsRuntimeConfig
	Workspace   *workspace.FileStore
	Busy        func(workspace.Scope) bool
	Owns        func(workspace.Scope) bool
	Store       store.Store
	HubBase     string
	HubInsecure bool
}

// startControllerManager builds the multicluster manager, starts the Project
// reconciler, and blocks until the manager exits. A nil config means "skip the
// manager, run REST-only". Keeping Start synchronous is important: callers can
// observe both setup errors and post-start exits and re-enter the bounded retry
// loop instead of hiding the manager in a detached goroutine.
//
// Deliberately NOT leader-elected (unlike code/vibe-studio/infrastructure):
// the Project reconciler converges commits from the pod-local workspace
// FileStore the HTTP assistant writes to, and pod readiness requires the
// manager to be running (controllerReadyRunnable below) — a lease would both
// strand sessions whose files live on a non-leader and wedge rollouts on a
// never-ready standby. The provider is single-replica by design (chart pins
// replicaCount: 1); scaling it needs shared workspace storage first. When a health
// dependency is supplied, a controller-runtime RunnableFunc is registered
// before Start. That runnable marks health ready only when the manager starts
// launching runnables and then blocks for manager cancellation; any error
// returned from Start immediately transitions it back to failed in
// runControllerManager.
func startControllerManager(ctx context.Context, config *rest.Config, deps controllerDeps, healthStates ...*controllerHealth) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())
	scheme := appscheme.NewScheme()

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

	if err := (&project.Reconciler{
		Actions:     deps.Actions,
		Workspace:   deps.Workspace,
		Busy:        deps.Busy,
		Owns:        deps.Owns,
		HubBase:     deps.HubBase,
		HubInsecure: deps.HubInsecure,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("project controller: %w", err)
	}
	if err := (&session.Reconciler{Store: deps.Store}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("session controller: %w", err)
	}
	if err := (&studio.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("studio controller: %w", err)
	}

	if len(healthStates) > 0 && healthStates[0] != nil {
		if err := mgr.GetLocalManager().Add(controllerReadyRunnable(healthStates[0])); err != nil {
			return fmt.Errorf("controller health runnable: %w", err)
		}
	}

	log.Printf("app-studio controller manager starting (endpointSlice=%s)", endpointSliceName)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("controller manager exited: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("controller manager exited without an error")
}

// controllerReadyRunnable is registered with the underlying
// controller-runtime manager before Start. Its Start method is therefore the
// readiness transition: setup can still fail without advertising health, and
// the runnable remains alive for the manager lifetime.
func controllerReadyRunnable(health *controllerHealth) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		health.markReady()
		<-ctx.Done()
		return nil
	})
}

// controllerModeFromEnv makes REST-only operation an intentional local-dev
// choice while keeping a configured provider kubeconfig controller-required
// by default. The chart sets required explicitly so a production pod cannot
// accidentally become ready as a REST-only process.
func controllerModeFromEnv() controllerMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_STUDIO_CONTROLLER_MODE"))) {
	case string(controllerModeRESTOnly), "rest_only", "rest":
		return controllerModeRESTOnly
	case string(controllerModeRequired), "controller":
		return controllerModeRequired
	case "":
		if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_STUDIO_REST_ONLY")), "true") {
			return controllerModeRESTOnly
		}
		if strings.TrimSpace(os.Getenv("FAROS_PROVIDER_KUBECONFIG")) != "" ||
			strings.TrimSpace(os.Getenv("KUBECONFIG")) != "" {
			return controllerModeRequired
		}
		return controllerModeRESTOnly
	default:
		// Fail closed for an invalid mode: a deployment with a typo must not
		// advertise a REST-only health contract while its controller is absent.
		log.Printf("unknown APP_STUDIO_CONTROLLER_MODE=%q; requiring controller", os.Getenv("APP_STUDIO_CONTROLLER_MODE"))
		return controllerModeRequired
	}
}

// runControllerManager owns the complete manager lifecycle. Every setup or
// post-start error transitions health to failed, waits a bounded interval, and
// retries until ctx is cancelled. The function takes loader/starter functions
// so lifecycle tests can exercise transitions without starting a real kcp
// cluster.
func runControllerManager(
	ctx context.Context,
	health *controllerHealth,
	loadConfig func() (*rest.Config, error),
	start func(context.Context, *rest.Config, controllerDeps) error,
	deps controllerDeps,
	retryInterval time.Duration,
) {
	runControllerManagerWithRetryGate(ctx, health, loadConfig, start, deps, retryInterval, waitControllerRetry)
}

// waitControllerRetry is the production retry gate. Keeping the wait behind a
// small function seam lets lifecycle tests release each retry explicitly and
// assert failed/starting transitions without relying on timer scheduling.
func waitControllerRetry(ctx context.Context, retryInterval time.Duration) bool {
	if retryInterval < 0 {
		retryInterval = 0
	}
	timer := time.NewTimer(retryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runControllerManagerWithRetryGate(
	ctx context.Context,
	health *controllerHealth,
	loadConfig func() (*rest.Config, error),
	start func(context.Context, *rest.Config, controllerDeps) error,
	deps controllerDeps,
	retryInterval time.Duration,
	retryGate func(context.Context, time.Duration) bool,
) {
	if health == nil {
		health = newControllerHealth(true)
	}
	if !health.snapshot().Required {
		health.markRESTOnly()
		log.Printf("controller manager disabled: explicit REST-only mode")
		return
	}
	if loadConfig == nil || start == nil {
		err := errors.New("controller manager lifecycle dependencies are not configured")
		health.markFailed(err)
		log.Printf("controller manager not ready: %v", err)
		return
	}

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			health.markStopped(err)
			return
		}
		health.markStarting()

		config, err := loadConfig()
		if err == nil {
			err = start(ctx, config, deps)
			if err == nil && ctx.Err() == nil {
				err = errors.New("controller manager exited without an error")
			}
		}
		if ctx.Err() != nil {
			health.markStopped(ctx.Err())
			return
		}
		if err == nil {
			err = errors.New("controller manager exited without an error")
		}
		health.markFailed(err)
		log.Printf("controller manager not ready (attempt %d): %v; retrying in %s", attempt, err, retryInterval)

		if retryGate == nil {
			retryGate = waitControllerRetry
		}
		if !retryGate(ctx, retryInterval) {
			health.markStopped(ctx.Err())
			return
		}
	}
}
