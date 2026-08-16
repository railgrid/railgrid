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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkinstall "github.com/faroshq/provider-sdk/install"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/connection"
	"github.com/faroshq/provider-databricks/controller/table"
	"github.com/faroshq/provider-databricks/controller/warehouse"
	databricksscheme "github.com/faroshq/provider-databricks/scheme"
)

const (
	controllerRetryInterval    = 15 * time.Second
	controllerRetryIntervalMin = 1 * time.Second
	controllerRetryIntervalMax = 5 * time.Minute

	controllerStartupTimeout    = 1 * time.Minute
	controllerStartupTimeoutMin = 1 * time.Second
	controllerStartupTimeoutMax = 5 * time.Minute
)

var errControllerProviderDiscoveryTimeout = errors.New("multicluster provider discovery timed out")

var errControllerProviderEndpointLost = errors.New("multicluster provider endpoint became unavailable")

type controllerMode string

const (
	controllerModeRESTOnly controllerMode = "rest-only"
	controllerModeRequired controllerMode = "required"
)

// controllerState is independent from HTTP process liveness. A provider can
// keep serving its REST surface while a required manager is starting or
// recovering, but readiness and heartbeat eligibility remain false in those
// states.
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
	if snapshot.Required && snapshot.State != controllerStateReady {
		return false
	}
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
	snapshot := h.snapshot()
	if h.ready() && (!snapshot.Required || snapshot.State == controllerStateReady) {
		return "healthy"
	}
	if snapshot.State == controllerStateStarting {
		return "starting"
	}
	return "unhealthy"
}

// managerAuthority is installed in the tenant client factory once. The
// controller retry loop swaps the currently running manager into it and clears
// it after an exit, so requests cannot use a manager that is no longer alive.
type managerAuthority struct {
	mu  sync.RWMutex
	mgr mcmanager.Manager
}

// Ready reports whether the authority points at a currently running manager.
// Tenant ClientFactory uses this as part of its readiness gate so a recovered
// kubeconfig cannot make actions look usable before manager startup completes.
func (a *managerAuthority) Ready() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mgr != nil
}

func (a *managerAuthority) Set(mgr mcmanager.Manager) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.mgr = mgr
	a.mu.Unlock()
}

func (a *managerAuthority) Clear(mgr mcmanager.Manager) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.mgr == mgr {
		a.mgr = nil
	}
	a.mu.Unlock()
}

func (a *managerAuthority) GetCluster(ctx context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	if a == nil {
		return nil, errors.New("provider controller manager unavailable")
	}
	a.mu.RLock()
	mgr := a.mgr
	a.mu.RUnlock()
	if mgr == nil {
		return nil, errors.New("provider controller manager unavailable")
	}
	return mgr.GetCluster(ctx, name)
}

// controllerProviderRunnable preserves the provider's discovery lifecycle but
// exposes the point at which multicluster-runtime has actually started it.
// The readiness runnable waits for this signal before it checks the endpoint
// slice, so a manager that has only been constructed cannot publish authority.
type controllerProviderRunnable struct {
	multicluster.Provider
	runnable       multicluster.ProviderRunnable
	started        chan struct{}
	result         chan error
	once           sync.Once
	initialize     func(context.Context) error
	startupTimeout time.Duration
	withTimeout    func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

func (p *controllerProviderRunnable) Start(ctx context.Context, aware multicluster.Aware) error {
	providerCtx, stopProvider := context.WithCancel(ctx)
	defer stopProvider()
	providerResult := make(chan error, 1)
	go func() {
		providerResult <- p.runnable.Start(providerCtx, aware)
	}()

	if p.initialize != nil {
		initCtx, cancel, timeout := p.startupContext(ctx)
		initResult := make(chan error, 1)
		go func() {
			err := p.initialize(initCtx)
			if err == nil && initCtx.Err() != nil {
				err = initCtx.Err()
			}
			initResult <- err
		}()
		select {
		case err := <-providerResult:
			cancel()
			return p.report(err)
		case err := <-initResult:
			if err != nil {
				err = controllerProviderReadinessWaitError(ctx, initCtx, timeout, err)
				cancel()
				return p.report(err)
			}
		}
		cancel()
	}

	select {
	case err := <-providerResult:
		return p.report(err)
	default:
	}
	p.once.Do(func() { close(p.started) })
	err := <-providerResult
	return p.report(err)
}

func (p *controllerProviderRunnable) report(err error) error {
	if p.result != nil {
		p.result <- err
	}
	return err
}

func (p *controllerProviderRunnable) startupContext(ctx context.Context) (context.Context, context.CancelFunc, time.Duration) {
	timeout := controllerStartupTimeout
	if p != nil && p.startupTimeout > 0 {
		timeout = clampControllerDuration(p.startupTimeout, controllerStartupTimeout, controllerStartupTimeoutMin, controllerStartupTimeoutMax)
	}
	withTimeout := context.WithTimeout
	if p != nil && p.withTimeout != nil {
		withTimeout = p.withTimeout
	}
	derived, cancel := withTimeout(ctx, timeout)
	return derived, cancel, timeout
}

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// controllerProviderTransportReadiness is driven by the actual apiexport
// provider cache. Its wrapped transport observes the provider's first
// successful LIST and WATCH for APIExportEndpointSlice, rather than using an
// unrelated informer that could become ready before the provider itself.
type controllerProviderTransportReadiness struct {
	endpointSuffix string
	ready          chan struct{}

	mu           sync.Mutex
	listComplete bool
	watchStarted bool
	once         sync.Once
}

func newControllerProviderTransportReadiness() *controllerProviderTransportReadiness {
	return &controllerProviderTransportReadiness{
		endpointSuffix: "/" + apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version + "/" + apiExportEndpointSliceGVR.Resource,
		ready:          make(chan struct{}),
	}
}

func (r *controllerProviderTransportReadiness) Wait(ctx context.Context) error {
	if r == nil {
		return errors.New("multicluster provider readiness is not configured")
	}
	select {
	case <-r.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *controllerProviderTransportReadiness) wrap(rt http.RoundTripper) http.RoundTripper {
	return &controllerProviderReadinessRoundTripper{next: rt, readiness: r}
}

func (r *controllerProviderTransportReadiness) matches(req *http.Request) bool {
	if r == nil || req == nil || req.URL == nil || req.Method != http.MethodGet {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(req.URL.Path, "/"), r.endpointSuffix)
}

func (r *controllerProviderTransportReadiness) markListComplete() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.listComplete = true
	r.maybeReadyLocked()
	r.mu.Unlock()
}

func (r *controllerProviderTransportReadiness) markWatchStarted() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.watchStarted = true
	r.maybeReadyLocked()
	r.mu.Unlock()
}

func (r *controllerProviderTransportReadiness) maybeReadyLocked() {
	if r.listComplete && r.watchStarted {
		r.once.Do(func() { close(r.ready) })
	}
}

type controllerProviderReadinessRoundTripper struct {
	next      http.RoundTripper
	readiness *controllerProviderTransportReadiness
}

func (r *controllerProviderReadinessRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !r.readiness.matches(req) {
		return resp, err
	}
	if strings.EqualFold(req.URL.Query().Get("watch"), "true") {
		r.readiness.markWatchStarted()
		if strings.EqualFold(req.URL.Query().Get("sendInitialEvents"), "true") && resp.Body != nil {
			resp.Body = &controllerProviderReadinessBody{
				ReadCloser: resp.Body,
				onRead:     r.readiness.markListComplete,
			}
		}
		return resp, nil
	}
	if resp.Body == nil {
		r.readiness.markListComplete()
		return resp, nil
	}
	resp.Body = &controllerProviderReadinessBody{
		ReadCloser: resp.Body,
		onEOF:      r.readiness.markListComplete,
	}
	return resp, nil
}

type controllerProviderReadinessBody struct {
	io.ReadCloser
	onRead   func()
	onEOF    func()
	readOnce sync.Once
	eofOnce  sync.Once
}

func (r *controllerProviderReadinessBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.readOnce.Do(func() {
			if r.onRead != nil {
				r.onRead()
			}
		})
	}
	if err == io.EOF {
		r.eofOnce.Do(func() {
			if r.onEOF != nil {
				r.onEOF()
			}
		})
	}
	return n, err
}

func controllerProviderConfig(config *rest.Config) (*rest.Config, *controllerProviderTransportReadiness) {
	providerConfig := rest.CopyConfig(config)
	readiness := newControllerProviderTransportReadiness()
	providerConfig.WrapTransport = transport.Wrappers(providerConfig.WrapTransport, readiness.wrap)
	return providerConfig, readiness
}

// controllerProviderReadiness is the startup gate for tenant actions. The
// endpoint slice is the multicluster provider's discovery input; requiring a
// non-empty status.endpoints list means the provider has a usable virtual
// workspace to discover, rather than merely an informer that was registered.
type controllerProviderReadiness struct {
	started        <-chan struct{}
	providerDone   <-chan error
	endpoint       dynamic.ResourceInterface
	endpointName   string
	pollInterval   time.Duration
	startupTimeout time.Duration
	withTimeout    func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	pollGate       func(context.Context, time.Duration) bool
}

func (r *controllerProviderReadiness) Wait(ctx context.Context) error {
	if r == nil {
		return nil
	}
	waitCtx, cancel, timeout := r.startupContext(ctx)
	defer cancel()
	select {
	case <-r.started:
	case <-ctx.Done():
		return ctx.Err()
	case <-waitCtx.Done():
		return controllerProviderReadinessWaitError(ctx, waitCtx, timeout, nil)
	case err := <-r.providerDone:
		return controllerProviderExitError(err)
	}

	interval := r.pollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	for {
		ready, err := r.endpointDiscovered(waitCtx)
		if err != nil {
			return controllerProviderReadinessWaitError(ctx, waitCtx, timeout, err)
		}
		if ready {
			if err := r.providerExitError(); err != nil {
				return err
			}
			return nil
		}
		if err := r.providerExitError(); err != nil {
			return err
		}
		if !r.waitForPoll(waitCtx, interval) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return controllerProviderReadinessWaitError(ctx, waitCtx, timeout, nil)
		}
	}
}

// Monitor keeps the manager gated after startup. A provider endpoint can
// disappear or lose all usable URLs after the initial readiness check; that
// must clear authority and force the lifecycle retry loop to rebuild it.
func (r *controllerProviderReadiness) Monitor(ctx context.Context) error {
	if r == nil {
		return nil
	}
	interval := r.pollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	for {
		if err := r.providerExitError(); err != nil {
			return err
		}
		checkCtx, cancel, timeout := r.startupContext(ctx)
		ready, err := r.endpointDiscovered(checkCtx)
		if err == nil && checkCtx.Err() != nil {
			err = checkCtx.Err()
		}
		if err != nil {
			err = controllerProviderReadinessWaitError(ctx, checkCtx, timeout, err)
		}
		cancel()
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("%w: APIExportEndpointSlice %q has no usable endpoint", errControllerProviderEndpointLost, r.endpointName)
		}
		if !r.waitForPoll(ctx, interval) {
			return ctx.Err()
		}
	}
}

func (r *controllerProviderReadiness) waitForPoll(ctx context.Context, interval time.Duration) bool {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	if r.pollGate != nil {
		return r.pollGate(ctx, interval)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *controllerProviderReadiness) startupContext(ctx context.Context) (context.Context, context.CancelFunc, time.Duration) {
	timeout := controllerStartupTimeout
	if r != nil && r.startupTimeout > 0 {
		timeout = clampControllerDuration(r.startupTimeout, controllerStartupTimeout, controllerStartupTimeoutMin, controllerStartupTimeoutMax)
	}
	withTimeout := context.WithTimeout
	if r != nil && r.withTimeout != nil {
		withTimeout = r.withTimeout
	}
	waitCtx, cancel := withTimeout(ctx, timeout)
	return waitCtx, cancel, timeout
}

func controllerProviderReadinessWaitError(parent, waitCtx context.Context, timeout time.Duration, err error) error {
	if parentErr := parent.Err(); parentErr != nil {
		return parentErr
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		if err == nil {
			return fmt.Errorf("%w after %s", errControllerProviderDiscoveryTimeout, timeout)
		}
		return fmt.Errorf("%w after %s: %v", errControllerProviderDiscoveryTimeout, timeout, err)
	}
	if err != nil {
		return err
	}
	return waitCtx.Err()
}

func (r *controllerProviderReadiness) providerExitError() error {
	if r.providerDone == nil {
		return nil
	}
	select {
	case err := <-r.providerDone:
		return controllerProviderExitError(err)
	default:
		return nil
	}
}

func controllerProviderExitError(err error) error {
	if err == nil {
		return errors.New("multicluster provider exited before readiness")
	}
	return fmt.Errorf("multicluster provider exited before readiness: %w", err)
}

func (r *controllerProviderReadiness) endpointDiscovered(ctx context.Context) (bool, error) {
	if r.endpoint == nil {
		return true, nil
	}
	obj, err := r.endpoint.Get(ctx, r.endpointName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading APIExportEndpointSlice %q: %w", r.endpointName, err)
	}
	endpoints, found, err := unstructured.NestedSlice(obj.Object, "status", "endpoints")
	if err != nil {
		return false, fmt.Errorf("reading APIExportEndpointSlice %q status.endpoints: %w", r.endpointName, err)
	}
	if !found {
		return false, nil
	}
	for _, endpoint := range endpoints {
		fields, ok := endpoint.(map[string]any)
		if !ok {
			continue
		}
		if url, ok := fields["url"].(string); ok && strings.TrimSpace(url) != "" {
			return true, nil
		}
	}
	return false, nil
}

func controllerBoundedSetup(ctx context.Context, timeout time.Duration, withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc), setup func(context.Context) error) error {
	if setup == nil {
		return errors.New("controller manager setup is not configured")
	}
	timeout = clampControllerDuration(timeout, controllerStartupTimeout, controllerStartupTimeoutMin, controllerStartupTimeoutMax)
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	setupCtx, cancel := withTimeout(ctx, timeout)
	err := setup(setupCtx)
	if err == nil && setupCtx.Err() != nil {
		err = setupCtx.Err()
	}
	if err != nil {
		classified := controllerProviderReadinessWaitError(ctx, setupCtx, timeout, err)
		cancel()
		return classified
	}
	cancel()
	return nil
}

// newControllerManager performs all setup that must succeed before a
// controller can become ready. In particular, the endpoint slice is a hard
// prerequisite: a manager without it would appear started while silently
// watching no tenant workspaces.
func newControllerManager(ctx context.Context, config *rest.Config, validator backend.Validator) (mcmanager.Manager, error) {
	mgr, _, err := newControllerManagerWithReadiness(ctx, config, validator)
	return mgr, err
}

func newControllerManagerWithReadiness(ctx context.Context, config *rest.Config, validator backend.Validator) (mcmanager.Manager, *controllerProviderReadiness, error) {
	if config == nil {
		return nil, nil, errControllerDisabled
	}
	ctrl.SetLogger(klog.NewKlogr())
	scheme := databricksscheme.NewScheme()

	var dyn dynamic.Interface
	setupTimeout := controllerStartupTimeoutFromEnv()
	if err := controllerBoundedSetup(ctx, setupTimeout, nil, func(setupCtx context.Context) error {
		var err error
		dyn, err = dynamic.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("dynamic client: %w", err)
		}
		workspacePath := envOr("DATABRICKS_WORKSPACE_PATH", defaultWorkspacePath)
		if err := sdkinstall.EnsureAPIExportEndpointSlice(setupCtx, dyn, apiExportName, apiExportName, workspacePath); err != nil {
			return fmt.Errorf("ensuring APIExportEndpointSlice: %w", err)
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("controller manager setup: %w", err)
	}

	providerConfig, providerTransportReadiness := controllerProviderConfig(config)
	provider, err := apiexport.New(providerConfig, apiExportName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	providerRunnable := &controllerProviderRunnable{
		Provider:       provider,
		runnable:       provider,
		started:        make(chan struct{}),
		result:         make(chan error, 1),
		initialize:     providerTransportReadiness.Wait,
		startupTimeout: setupTimeout,
	}
	// The lifecycle below reconstructs a manager after readiness loss while
	// retaining stable controller names for metrics and logs. controller-runtime
	// keeps its name registry for the process lifetime, so a replacement manager
	// must skip that process-global check. The three names registered below are
	// still explicit and distinct within every manager instance.
	mgr, err := mcmanager.New(config, providerRunnable, manager.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: controllerOptionsForRetryableManager(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating multicluster manager: %w", err)
	}
	if err := (&connection.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("connection controller: %w", err)
	}
	if err := (&warehouse.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("warehouse controller: %w", err)
	}
	if err := (&table.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("table controller: %w", err)
	}
	return mgr, &controllerProviderReadiness{
		started:        providerRunnable.started,
		providerDone:   providerRunnable.result,
		endpoint:       dyn.Resource(apiExportEndpointSliceGVR),
		endpointName:   apiExportName,
		startupTimeout: controllerStartupTimeoutFromEnv(),
	}, nil
}

func controllerOptionsForRetryableManager() ctrlconfig.Controller {
	skipNameValidation := true
	return ctrlconfig.Controller{SkipNameValidation: &skipNameValidation}
}

// startControllerManager preserves the small setup API used by local callers:
// it builds the manager and starts it in a goroutine. Production serving uses
// startControllerManagerAttempt below so setup and manager exit participate in
// the retry/readiness lifecycle.
func startControllerManager(ctx context.Context, config *rest.Config, validator backend.Validator) (mcmanager.Manager, error) {
	mgr, err := newControllerManager(ctx, config, validator)
	if err != nil {
		return nil, err
	}
	go func() {
		log.Printf("databricks controller manager starting (endpointSlice=%s)", apiExportName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("controller manager exited: %v", err)
		}
	}()
	return mgr, nil
}

func startControllerManagerAttempt(ctx context.Context, config *rest.Config, validator backend.Validator, health *controllerHealth, authority *managerAuthority) error {
	mgr, providerReadiness, err := newControllerManagerWithReadiness(ctx, config, validator)
	if err != nil {
		return err
	}
	if authority != nil {
		defer authority.Clear(mgr)
	}
	if health != nil || authority != nil {
		if err := mgr.GetLocalManager().Add(controllerReadyRunnable(
			health,
			providerReadiness.Wait,
			providerReadiness.Monitor,
			func() {
				if authority != nil {
					authority.Set(mgr)
				}
				if health != nil {
					health.markReady()
				}
			},
			func() {
				if authority != nil {
					authority.Clear(mgr)
				}
			},
		)); err != nil {
			return fmt.Errorf("controller health runnable: %w", err)
		}
	}
	log.Printf("databricks controller manager starting (endpointSlice=%s)", apiExportName)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("controller manager exited: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("controller manager exited without an error")
}

func controllerReadyRunnable(health *controllerHealth, waitForUsable, monitor func(context.Context) error, onReady, onStop func()) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		if waitForUsable != nil {
			if err := waitForUsable(ctx); err != nil {
				return fmt.Errorf("multicluster provider not discovered: %w", err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if onReady != nil {
			onReady()
		} else if health != nil {
			health.markReady()
		}
		if monitor != nil {
			if err := monitor(ctx); err != nil {
				if ctx.Err() != nil {
					if onStop != nil {
						onStop()
					}
					return nil
				}
				if health != nil {
					health.markFailed(err)
				}
				if onStop != nil {
					onStop()
				}
				return fmt.Errorf("multicluster provider lost readiness: %w", err)
			}
			return nil
		}
		<-ctx.Done()
		if onStop != nil {
			onStop()
		}
		return nil
	})
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

func normalizeControllerRetryInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return controllerRetryIntervalMin
	}
	return clampControllerDuration(interval, controllerRetryInterval, controllerRetryIntervalMin, controllerRetryIntervalMax)
}

// runControllerManager owns setup, manager start/exit, health transitions, and
// bounded recovery. A configured provider keeps serving /healthz while this
// loop retries, but /readyz and heartbeat eligibility remain false until the
// manager's runnables have launched.
func runControllerManager(ctx context.Context, health *controllerHealth, loadConfig func() (*rest.Config, error), start func(context.Context, *rest.Config) error, retryInterval time.Duration) {
	runControllerManagerWithRetryGate(ctx, health, loadConfig, start, retryInterval, waitControllerRetry)
}

func waitControllerRetry(ctx context.Context, retryInterval time.Duration) bool {
	retryInterval = normalizeControllerRetryInterval(retryInterval)
	timer := time.NewTimer(retryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runControllerManagerWithRetryGate(ctx context.Context, health *controllerHealth, loadConfig func() (*rest.Config, error), start func(context.Context, *rest.Config) error, retryInterval time.Duration, retryGate func(context.Context, time.Duration) bool) {
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
	if retryGate == nil {
		retryGate = waitControllerRetry
	}
	retryInterval = normalizeControllerRetryInterval(retryInterval)
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			health.markStopped(err)
			return
		}
		health.markStarting()
		config, err := loadConfig()
		if err == nil {
			err = start(ctx, config)
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
		if !retryGate(ctx, retryInterval) {
			health.markStopped(ctx.Err())
			return
		}
	}
}

func parseBoolEnv(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}
