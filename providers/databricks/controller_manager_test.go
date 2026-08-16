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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	databricksscheme "github.com/faroshq/provider-databricks/scheme"
	"github.com/faroshq/provider-databricks/tenant"
)

func TestControllerHealthLifecycle(t *testing.T) {
	health := newControllerHealth(true)
	if got := health.snapshot(); got.State != controllerStateStarting || !got.Required {
		t.Fatalf("initial controller health = %+v, want required/starting", got)
	}
	if health.ready() || health.heartbeatStatus() != "starting" {
		t.Fatal("required controller should not be ready before start")
	}
	health.markFailed(errors.New("endpoint slice unavailable"))
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "endpoint slice unavailable" {
		t.Fatalf("failed controller health = %+v, want failure", got)
	}
	if health.ready() || health.heartbeatStatus() != "unhealthy" {
		t.Fatal("failed required controller should not be ready or healthy")
	}
	health.markReady()
	if got := health.snapshot(); got.State != controllerStateReady || !health.ready() || health.heartbeatStatus() != "healthy" {
		t.Fatalf("running controller health = %+v, want ready/healthy", got)
	}
	health.markStopped(context.Canceled)
	if got := health.snapshot(); got.State != controllerStateStopped || got.Error != context.Canceled.Error() || health.ready() {
		t.Fatalf("stopped controller health = %+v, want stopped/not ready", got)
	}
}

func TestControllerOptionsForRetryableManagerAllowsStableNames(t *testing.T) {
	options := controllerOptionsForRetryableManager()
	if options.SkipNameValidation == nil || !*options.SkipNameValidation {
		t.Fatal("retryable manager must allow stable controller names across process-lifetime rebuilds")
	}
}

func TestControllerReadyRunnableGatesAuthorityUntilProviderDiscovery(t *testing.T) {
	health := newControllerHealth(true)
	authority := &managerAuthority{}
	mgr := &startupGateManager{}
	providerStarted := make(chan struct{})
	providerDiscovered := make(chan struct{})
	ready := make(chan struct{})
	stopped := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- controllerReadyRunnable(
			health,
			func(waitCtx context.Context) error {
				select {
				case <-providerStarted:
				case <-waitCtx.Done():
					return waitCtx.Err()
				}
				select {
				case <-providerDiscovered:
					return nil
				case <-waitCtx.Done():
					return waitCtx.Err()
				}
			},
			nil,
			func() {
				authority.Set(mgr)
				health.markReady()
				close(ready)
			},
			func() {
				authority.Clear(mgr)
				close(stopped)
			},
		).Start(ctx)
	}()

	close(providerStarted)
	if got := health.snapshot(); got.State != controllerStateStarting {
		t.Fatalf("health before provider discovery = %+v, want starting", got)
	}
	if _, err := authority.GetCluster(context.Background(), multicluster.ClusterName("tenant")); err == nil || err.Error() != "provider controller manager unavailable" {
		t.Fatalf("authority before provider discovery = %v, want unavailable", err)
	}

	close(providerDiscovered)
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("controller did not become ready after provider discovery")
	}
	if got := health.snapshot(); got.State != controllerStateReady {
		t.Fatalf("health after provider discovery = %+v, want ready", got)
	}
	if _, err := authority.GetCluster(context.Background(), multicluster.ClusterName("tenant")); err == nil || err.Error() != "startup gate manager" {
		t.Fatalf("authority after provider discovery = %v, want live manager", err)
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("controller did not clear authority on exit")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("controller ready runnable returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller ready runnable did not stop")
	}
	if _, err := authority.GetCluster(context.Background(), multicluster.ClusterName("tenant")); err == nil || err.Error() != "provider controller manager unavailable" {
		t.Fatalf("authority after manager exit = %v, want unavailable", err)
	}
}

func TestRunControllerManagerRetriesSetupAndPostStartFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	health := newControllerHealth(true)
	var loads, starts atomic.Int32
	firstRetry := make(chan struct{})
	allowFirstRetry := make(chan struct{})
	secondRetry := make(chan struct{})
	allowSecondRetry := make(chan struct{})
	secondReady := make(chan struct{})
	done := make(chan struct{})

	loadConfig := func() (*rest.Config, error) {
		if loads.Add(1) == 1 {
			return nil, errors.New("provider kubeconfig is not bootstrapped")
		}
		return &rest.Config{}, nil
	}
	start := func(startCtx context.Context, _ *rest.Config) error {
		if starts.Add(1) == 1 {
			return errors.New("manager exited after start")
		}
		health.markReady()
		close(secondReady)
		<-startCtx.Done()
		return startCtx.Err()
	}
	retryCalls := atomic.Int32{}
	retryGate := func(retryCtx context.Context, _ time.Duration) bool {
		switch retryCalls.Add(1) {
		case 1:
			close(firstRetry)
			select {
			case <-allowFirstRetry:
				return true
			case <-retryCtx.Done():
				return false
			}
		case 2:
			close(secondRetry)
			select {
			case <-allowSecondRetry:
				return true
			case <-retryCtx.Done():
				return false
			}
		default:
			return false
		}
	}

	go func() {
		runControllerManagerWithRetryGate(ctx, health, loadConfig, start, 15*time.Second, retryGate)
		close(done)
	}()
	waitForDatabricksControllerSignal(t, firstRetry)
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "provider kubeconfig is not bootstrapped" {
		t.Fatalf("first failed health = %+v", got)
	}
	close(allowFirstRetry)
	waitForDatabricksControllerSignal(t, secondRetry)
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "manager exited after start" {
		t.Fatalf("second failed health = %+v", got)
	}
	close(allowSecondRetry)
	waitForDatabricksControllerSignal(t, secondReady)
	if starts.Load() != 2 || !health.ready() {
		t.Fatalf("starts=%d ready=%v, want 2/true", starts.Load(), health.ready())
	}
	cancel()
	waitForDatabricksControllerSignal(t, done)
	if health.snapshot().State != controllerStateStopped {
		t.Fatalf("final health = %+v, want stopped", health.snapshot())
	}
}

func TestRunControllerManagerLeavesRESTOnlyModeReady(t *testing.T) {
	health := newControllerHealth(false)
	var loads, starts atomic.Int32
	runControllerManager(context.Background(), health,
		func() (*rest.Config, error) {
			loads.Add(1)
			return nil, errors.New("must not load")
		},
		func(context.Context, *rest.Config) error {
			starts.Add(1)
			return errors.New("must not start")
		}, 0)
	if got := health.snapshot(); got.State != controllerStateRESTOnly || !health.ready() || health.heartbeatStatus() != "healthy" {
		t.Fatalf("REST-only health = %+v, want ready/rest-only", got)
	}
	if loads.Load() != 0 || starts.Load() != 0 {
		t.Fatalf("REST-only lifecycle called dependencies: load=%d start=%d", loads.Load(), starts.Load())
	}
}

func TestControllerModeFromEnvRequiresExplicitRESTOnlyOptIn(t *testing.T) {
	t.Setenv("DATABRICKS_CONTROLLER_MODE", "")
	t.Setenv("DATABRICKS_REST_ONLY", "")
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", "")
	t.Setenv("DATABRICKS_KUBECONFIG", "")
	t.Setenv("KUBECONFIG", "")
	if got := controllerModeFromEnv(); got != controllerModeRESTOnly {
		t.Fatalf("unset controller mode without kubeconfig = %q, want rest-only", got)
	}
	t.Setenv("DATABRICKS_KUBECONFIG", "/var/run/secrets/faros/provider-kubeconfig")
	if got := controllerModeFromEnv(); got != controllerModeRequired {
		t.Fatalf("DATABRICKS_KUBECONFIG controller mode = %q, want required", got)
	}
	t.Setenv("DATABRICKS_REST_ONLY", "true")
	if got := controllerModeFromEnv(); got != controllerModeRESTOnly {
		t.Fatalf("explicit REST-only opt-in with DATABRICKS_KUBECONFIG = %q, want rest-only", got)
	}
	t.Setenv("DATABRICKS_REST_ONLY", "")

	t.Setenv("DATABRICKS_CONTROLLER_MODE", "required")
	if got := controllerModeFromEnv(); got != controllerModeRequired {
		t.Fatalf("required controller mode = %q, want required", got)
	}
	t.Setenv("DATABRICKS_CONTROLLER_MODE", "rest-only")
	if got := controllerModeFromEnv(); got != controllerModeRESTOnly {
		t.Fatalf("rest-only controller mode = %q, want rest-only", got)
	}
	t.Setenv("DATABRICKS_CONTROLLER_MODE", "invalid")
	if got := controllerModeFromEnv(); got != controllerModeRequired {
		t.Fatalf("invalid controller mode = %q, want fail-closed required", got)
	}
}

func TestControllerProviderReadinessTimesOutAndRecoversWithoutSleep(t *testing.T) {
	started := make(chan struct{})
	close(started)
	missing := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()).Resource(apiExportEndpointSliceGVR)
	var timeoutPassed time.Duration
	timedOut := &controllerProviderReadiness{
		started:        started,
		endpoint:       missing,
		endpointName:   apiExportName,
		startupTimeout: time.Nanosecond,
		withTimeout: func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
			timeoutPassed = timeout
			return context.WithDeadline(parent, time.Unix(0, 0))
		},
	}
	err := timedOut.Wait(context.Background())
	if !errors.Is(err, errControllerProviderDiscoveryTimeout) {
		t.Fatalf("missing endpoint readiness error = %v, want classified timeout", err)
	}
	if timeoutPassed != controllerStartupTimeoutMin {
		t.Fatalf("startup timeout passed to gate = %s, want safe minimum %s", timeoutPassed, controllerStartupTimeoutMin)
	}

	readyObject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": apiExportName},
		"status": map[string]any{
			"endpoints": []any{map[string]any{"url": "https://provider.example"}},
		},
	}}
	readyClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), readyObject).Resource(apiExportEndpointSliceGVR)
	recovered := &controllerProviderReadiness{
		started:      started,
		endpoint:     readyClient,
		endpointName: apiExportName,
		withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
	}
	if err := recovered.Wait(context.Background()); err != nil {
		t.Fatalf("recovered endpoint readiness error = %v, want ready", err)
	}
}

func TestControllerProviderRunnableWaitsForInitializationBeforeReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initRelease := make(chan struct{})
	providerStarted := make(chan struct{})
	providerDone := make(chan error, 1)
	provider := &controllerProviderRunnable{
		Provider: &controllerProviderStub{},
		runnable: controllerProviderRunnableStub(func(providerCtx context.Context, _ multicluster.Aware) error {
			close(providerStarted)
			<-providerCtx.Done()
			return providerCtx.Err()
		}),
		started: make(chan struct{}),
		result:  providerDone,
		initialize: func(initCtx context.Context) error {
			select {
			case <-initRelease:
				return nil
			case <-initCtx.Done():
				return initCtx.Err()
			}
		},
		startupTimeout: controllerStartupTimeoutMin,
		withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
	}
	readyObject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": apiExportName},
		"status": map[string]any{
			"endpoints": []any{map[string]any{"url": "https://provider.example"}},
		},
	}}
	endpoint := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), readyObject).Resource(apiExportEndpointSliceGVR)
	readiness := &controllerProviderReadiness{
		started:      provider.started,
		providerDone: provider.result,
		endpoint:     endpoint,
		endpointName: apiExportName,
		withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
	}
	health := newControllerHealth(true)
	authority := &managerAuthority{}
	mgr := &startupGateManager{}
	readySignal := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- controllerReadyRunnable(
			health,
			readiness.Wait,
			nil,
			func() {
				authority.Set(mgr)
				health.markReady()
				close(readySignal)
			},
			func() { authority.Clear(mgr) },
		).Start(ctx)
	}()
	startDone := make(chan error, 1)
	go func() { startDone <- provider.Start(ctx, nil) }()

	waitForDatabricksControllerSignal(t, providerStarted)
	if health.ready() || authority.Ready() {
		t.Fatal("pre-existing endpoint made authority/health ready before provider initialization")
	}
	close(initRelease)
	waitForDatabricksControllerSignal(t, providerStarted)
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider readiness signal did not follow initialization")
	}
	waitForDatabricksControllerSignal(t, readySignal)
	if !health.ready() || !authority.Ready() {
		t.Fatal("provider initialization did not unlock readiness")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readiness runnable did not stop")
	}
	select {
	case <-startDone:
	case <-time.After(time.Second):
		t.Fatal("provider runnable did not stop")
	}
}

func TestControllerProviderTransportReadinessTracksActualProviderCache(t *testing.T) {
	endpointPath := "/apis/" + apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version + "/" + apiExportEndpointSliceGVR.Resource
	listStarted := make(chan struct{})
	watchStarted := make(chan struct{})
	var listOnce, watchOnce sync.Once
	providerTransport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		response := func(body any) (*http.Response, error) {
			encoded, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(encoded))),
				Request:    r,
			}, nil
		}
		switch strings.TrimRight(r.URL.Path, "/") {
		case "/api":
			return response(map[string]any{"apiVersion": "v1", "kind": "APIVersions", "versions": []any{"v1"}})
		case "/apis":
			return response(map[string]any{
				"apiVersion": "v1",
				"kind":       "APIGroupList",
				"groups": []any{map[string]any{
					"name":             apiExportEndpointSliceGVR.Group,
					"preferredVersion": map[string]any{"groupVersion": apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version, "version": apiExportEndpointSliceGVR.Version},
					"versions":         []any{map[string]any{"groupVersion": apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version, "version": apiExportEndpointSliceGVR.Version}},
				}},
			})
		case "/apis/" + apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version:
			return response(map[string]any{
				"apiVersion":   apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version,
				"kind":         "APIResourceList",
				"groupVersion": apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version,
				"resources": []any{map[string]any{
					"name":  apiExportEndpointSliceGVR.Resource,
					"kind":  "APIExportEndpointSlice",
					"verbs": []any{"get", "list", "watch"},
				}},
			})
		}
		if !strings.HasSuffix(r.URL.Path, endpointPath) {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Request: r}, nil
		}
		if strings.EqualFold(r.URL.Query().Get("watch"), "true") {
			watchOnce.Do(func() { close(watchStarted) })
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       &controllerBlockingReadCloser{ctx: r.Context(), initial: []byte(`{"type":"BOOKMARK","object":{"metadata":{"resourceVersion":"1"}}}`)},
				Request:    r,
			}, nil
		}
		listOnce.Do(func() { close(listStarted) })
		listBody, err := json.Marshal(map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha1",
			"kind":       "APIExportEndpointSliceList",
			"metadata":   map[string]any{"resourceVersion": "1"},
			"items":      []any{},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(listBody))),
			Request:    r,
		}, nil
	})

	providerConfig, providerReadiness := controllerProviderConfig(&rest.Config{Host: "https://kcp.example", Transport: providerTransport})
	provider, err := apiexport.New(providerConfig, apiExportName, apiexport.Options{Scheme: databricksscheme.NewScheme()})
	if err != nil {
		t.Fatalf("create apiexport provider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- provider.Start(ctx, nil) }()
	waitForDatabricksControllerSignal(t, listStarted)
	waitForDatabricksControllerSignal(t, watchStarted)
	if err := providerReadiness.Wait(ctx); err != nil {
		t.Fatalf("actual provider cache readiness = %v, want ready after list/watch", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("actual provider stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("actual provider did not stop after cancellation")
	}
}

func TestControllerProviderTransportReadinessPreservesExistingWrapper(t *testing.T) {
	var existingCalls atomic.Int32
	config := &rest.Config{
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				existingCalls.Add(1)
				return rt.RoundTrip(req)
			})
		},
	}
	providerConfig, _ := controllerProviderConfig(config)
	wrapped := providerConfig.WrapTransport(roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}))
	if _, err := wrapped.RoundTrip(httptest.NewRequest(http.MethodGet, "https://kcp.example/api/v1/healthz", nil)); err != nil {
		t.Fatalf("wrapped transport request: %v", err)
	}
	if existingCalls.Load() != 1 {
		t.Fatalf("existing transport wrapper calls = %d, want 1", existingCalls.Load())
	}
}

func TestControllerProviderTransportReadinessTimeoutCancelsAttemptBeforeRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	health := newControllerHealth(true)
	authority := &managerAuthority{}
	var starts atomic.Int32
	listStarted := make(chan struct{})
	providerStopped := make(chan struct{})
	retryStarted := make(chan struct{})
	allowRetry := make(chan struct{})
	recovered := make(chan struct{})
	done := make(chan struct{})
	loadConfig := func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example"}, nil }
	start := func(startCtx context.Context, _ *rest.Config) error {
		if starts.Add(1) == 1 {
			endpointPath := "/apis/" + apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version + "/" + apiExportEndpointSliceGVR.Resource
			listStartedOnce := sync.Once{}
			providerTransport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				response := func(body any) (*http.Response, error) {
					encoded, err := json.Marshal(body)
					if err != nil {
						return nil, err
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(string(encoded))),
						Request:    r,
					}, nil
				}
				switch strings.TrimRight(r.URL.Path, "/") {
				case "/api":
					return response(map[string]any{"apiVersion": "v1", "kind": "APIVersions", "versions": []any{"v1"}})
				case "/apis":
					return response(map[string]any{
						"apiVersion": "v1",
						"kind":       "APIGroupList",
						"groups": []any{map[string]any{
							"name":             apiExportEndpointSliceGVR.Group,
							"preferredVersion": map[string]any{"groupVersion": apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version, "version": apiExportEndpointSliceGVR.Version},
							"versions":         []any{map[string]any{"groupVersion": apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version, "version": apiExportEndpointSliceGVR.Version}},
						}},
					})
				case "/apis/" + apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version:
					return response(map[string]any{
						"apiVersion":   apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version,
						"kind":         "APIResourceList",
						"groupVersion": apiExportEndpointSliceGVR.Group + "/" + apiExportEndpointSliceGVR.Version,
						"resources": []any{map[string]any{
							"name":  apiExportEndpointSliceGVR.Resource,
							"kind":  "APIExportEndpointSlice",
							"verbs": []any{"get", "list", "watch"},
						}},
					})
				}
				if !strings.HasSuffix(r.URL.Path, endpointPath) {
					return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Request: r}, nil
				}
				listStartedOnce.Do(func() { close(listStarted) })
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       &controllerBlockingReadCloser{ctx: r.Context(), stopped: providerStopped},
					Request:    r,
				}, nil
			})
			providerConfig, providerReadiness := controllerProviderConfig(&rest.Config{Host: "https://kcp.example", Transport: providerTransport})
			provider, err := apiexport.New(providerConfig, apiExportName, apiexport.Options{Scheme: databricksscheme.NewScheme()})
			if err != nil {
				return err
			}
			providerRunnable := &controllerProviderRunnable{
				Provider:       provider,
				runnable:       provider,
				started:        make(chan struct{}),
				result:         make(chan error, 1),
				initialize:     providerReadiness.Wait,
				startupTimeout: controllerStartupTimeoutMin,
			}
			deadlineReady := make(chan *controllerTestDeadlineContext, 1)
			providerRunnable.withTimeout = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				deadline := newControllerTestDeadlineContext(parent)
				deadlineReady <- deadline
				return deadline, deadline.expire
			}
			readyObject := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apis.kcp.io/v1alpha1",
				"kind":       "APIExportEndpointSlice",
				"metadata":   map[string]any{"name": apiExportName},
				"status": map[string]any{
					"endpoints": []any{map[string]any{"url": "https://provider.example"}},
				},
			}}
			endpoint := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), readyObject).Resource(apiExportEndpointSliceGVR)
			readiness := &controllerProviderReadiness{
				started:      providerRunnable.started,
				providerDone: providerRunnable.result,
				endpoint:     endpoint,
				endpointName: apiExportName,
				withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
					return context.WithCancel(parent)
				},
			}
			attemptCtx, cancelAttempt := context.WithCancel(startCtx)
			defer cancelAttempt()
			providerDone := make(chan error, 1)
			go func() { providerDone <- providerRunnable.Start(attemptCtx, nil) }()
			readyDone := make(chan error, 1)
			go func() {
				readyDone <- controllerReadyRunnable(health, readiness.Wait, nil, func() {
					authority.Set(&startupGateManager{})
					health.markReady()
				}, nil).Start(attemptCtx)
			}()
			select {
			case <-listStarted:
			case <-time.After(time.Second):
				return errors.New("actual provider did not start the blocked endpoint request")
			}
			if health.ready() || authority.Ready() {
				return errors.New("preexisting endpoint made readiness authoritative before actual provider startup")
			}
			deadline := <-deadlineReady
			deadline.expire()
			providerErr := <-providerDone
			if !errors.Is(providerErr, errControllerProviderDiscoveryTimeout) {
				return fmt.Errorf("provider timeout = %v, want classified discovery timeout", providerErr)
			}
			waitErr := <-readyDone
			if waitErr == nil {
				return errors.New("readiness gate unexpectedly succeeded before actual provider signal")
			}
			select {
			case <-providerStopped:
			case <-time.After(time.Second):
				return errors.New("stuck provider request was not canceled after startup timeout")
			}
			return providerErr
		}
		mgr := &startupGateManager{}
		authority.Set(mgr)
		health.markReady()
		close(recovered)
		<-startCtx.Done()
		authority.Clear(mgr)
		return startCtx.Err()
	}
	retryGate := func(retryCtx context.Context, _ time.Duration) bool {
		close(retryStarted)
		select {
		case <-allowRetry:
			return true
		case <-retryCtx.Done():
			return false
		}
	}
	go func() {
		runControllerManagerWithRetryGate(ctx, health, loadConfig, start, 0, retryGate)
		close(done)
	}()
	waitForDatabricksControllerSignal(t, retryStarted)
	if got := health.snapshot(); got.State != controllerStateFailed || !strings.Contains(got.Error, errControllerProviderDiscoveryTimeout.Error()) {
		t.Fatalf("stuck provider health = %+v, want classified timeout failure", got)
	}
	if health.ready() || authority.Ready() {
		t.Fatal("stuck provider left readiness or authority enabled before retry")
	}
	close(allowRetry)
	waitForDatabricksControllerSignal(t, recovered)
	if starts.Load() != 2 || !health.ready() || !authority.Ready() {
		t.Fatalf("recovered attempt starts=%d ready=%v authority=%v, want 2/true/true", starts.Load(), health.ready(), authority.Ready())
	}
	cancel()
	waitForDatabricksControllerSignal(t, done)
}

func TestControllerSetupDeadlineClassifiesStalledSetup(t *testing.T) {
	called := make(chan struct{})
	err := controllerBoundedSetup(context.Background(), time.Nanosecond, func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		return context.WithDeadline(parent, time.Unix(0, 0))
	}, func(setupCtx context.Context) error {
		close(called)
		return setupCtx.Err()
	})
	if !errors.Is(err, errControllerProviderDiscoveryTimeout) {
		t.Fatalf("stalled setup error = %v, want classified timeout", err)
	}
	select {
	case <-called:
	default:
		t.Fatal("bounded setup callback was not invoked")
	}
}

func TestControllerReadinessTimeoutRetriesUntilEndpointRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	health := newControllerHealth(true)
	started := make(chan struct{})
	close(started)
	missing := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()).Resource(apiExportEndpointSliceGVR)
	readyObject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": apiExportName},
		"status": map[string]any{
			"endpoints": []any{map[string]any{"url": "https://provider.example"}},
		},
	}}
	readyClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), readyObject).Resource(apiExportEndpointSliceGVR)
	var starts atomic.Int32
	retryStarted := make(chan struct{})
	allowRetry := make(chan struct{})
	ready := make(chan struct{})
	done := make(chan struct{})
	loadConfig := func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example"}, nil }
	start := func(startCtx context.Context, _ *rest.Config) error {
		if starts.Add(1) == 1 {
			readiness := &controllerProviderReadiness{
				started:        started,
				endpoint:       missing,
				endpointName:   apiExportName,
				startupTimeout: controllerStartupTimeoutMin,
				withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
					return context.WithDeadline(parent, time.Unix(0, 0))
				},
			}
			return readiness.Wait(startCtx)
		}
		readiness := &controllerProviderReadiness{
			started:      started,
			endpoint:     readyClient,
			endpointName: apiExportName,
			withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				return context.WithCancel(parent)
			},
		}
		if err := readiness.Wait(startCtx); err != nil {
			return err
		}
		health.markReady()
		close(ready)
		<-startCtx.Done()
		return startCtx.Err()
	}
	retryGate := func(retryCtx context.Context, _ time.Duration) bool {
		close(retryStarted)
		select {
		case <-allowRetry:
			return true
		case <-retryCtx.Done():
			return false
		}
	}

	go func() {
		runControllerManagerWithRetryGate(ctx, health, loadConfig, start, 0, retryGate)
		close(done)
	}()
	waitForDatabricksControllerSignal(t, retryStarted)
	if got := health.snapshot(); got.State != controllerStateFailed || !strings.Contains(got.Error, errControllerProviderDiscoveryTimeout.Error()) {
		t.Fatalf("timeout health = %+v, want failed classified timeout", got)
	}
	close(allowRetry)
	waitForDatabricksControllerSignal(t, ready)
	if !health.ready() || starts.Load() != 2 {
		t.Fatalf("recovered controller starts=%d ready=%v, want 2/true", starts.Load(), health.ready())
	}
	cancel()
	waitForDatabricksControllerSignal(t, done)
}

func TestControllerEndpointLossClearsAuthorityAndRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	health := newControllerHealth(true)
	authority := &managerAuthority{}
	var starts atomic.Int32
	retryStarted := make(chan struct{})
	allowRetry := make(chan struct{})
	recovered := make(chan struct{})
	providerStopped := make(chan struct{})
	done := make(chan struct{})
	loadConfig := func() (*rest.Config, error) { return &rest.Config{Host: "https://kcp.example"}, nil }
	start := func(startCtx context.Context, _ *rest.Config) error {
		attempt := starts.Add(1)
		attemptCtx, cancelAttempt := context.WithCancel(startCtx)
		if attempt == 1 {
			go func() {
				<-attemptCtx.Done()
				close(providerStopped)
			}()
		}
		started := make(chan struct{})
		close(started)
		object := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha1",
			"kind":       "APIExportEndpointSlice",
			"metadata":   map[string]any{"name": apiExportName},
			"status": map[string]any{
				"endpoints": []any{map[string]any{"url": "https://provider.example"}},
			},
		}}
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object).Resource(apiExportEndpointSliceGVR)
		mgr := &startupGateManager{}
		readiness := &controllerProviderReadiness{
			started:      started,
			endpoint:     client,
			endpointName: apiExportName,
			withTimeout: func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				return context.WithCancel(parent)
			},
			pollGate: func(pollCtx context.Context, _ time.Duration) bool {
				if attempt == 1 {
					if err := client.Delete(context.Background(), apiExportName, metav1.DeleteOptions{}); err != nil {
						return false
					}
					return true
				}
				<-pollCtx.Done()
				return false
			},
		}
		onReady := func() {
			authority.Set(mgr)
			health.markReady()
			if attempt == 2 {
				close(recovered)
			}
		}
		onStop := func() {
			authority.Clear(mgr)
			cancelAttempt()
		}
		return controllerReadyRunnable(health, readiness.Wait, readiness.Monitor, onReady, onStop).Start(attemptCtx)
	}
	retryGate := func(retryCtx context.Context, _ time.Duration) bool {
		if starts.Load() == 1 {
			select {
			case <-providerStopped:
			case <-retryCtx.Done():
				return false
			}
		}
		close(retryStarted)
		select {
		case <-allowRetry:
			return true
		case <-retryCtx.Done():
			return false
		}
	}

	go func() {
		runControllerManagerWithRetryGate(ctx, health, loadConfig, start, 0, retryGate)
		close(done)
	}()
	waitForDatabricksControllerSignal(t, retryStarted)
	if got := health.snapshot(); got.State != controllerStateFailed || !strings.Contains(got.Error, errControllerProviderEndpointLost.Error()) {
		t.Fatalf("endpoint-loss health = %+v, want failed endpoint loss", got)
	}
	if authority.Ready() || health.ready() {
		t.Fatal("endpoint loss left authority or readiness enabled")
	}
	close(allowRetry)
	waitForDatabricksControllerSignal(t, recovered)
	if starts.Load() != 2 || !authority.Ready() || !health.ready() {
		t.Fatalf("recovered controller starts=%d authority=%v ready=%v, want 2/true/true", starts.Load(), authority.Ready(), health.ready())
	}
	cancel()
	waitForDatabricksControllerSignal(t, done)
	if health.snapshot().State != controllerStateStopped || authority.Ready() {
		t.Fatalf("post-stop state = health:%+v authority:%v, want stopped/false", health.snapshot(), authority.Ready())
	}
}

func TestControllerRecoveryRebindsTenantFactoryBeforeReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	health := newControllerHealth(true)
	authority := &managerAuthority{}
	factory := tenant.NewDeferredClientFactory()
	factory.SetAuthority(authority)
	health.setDependencyReady(func() bool {
		return factory.Configured() && factory.Ready()
	})

	var loads atomic.Int32
	retryStarted := make(chan struct{})
	allowRetry := make(chan struct{})
	recovered := make(chan struct{})
	done := make(chan struct{})
	loadConfig := func() (*rest.Config, error) {
		if loads.Add(1) == 1 {
			return nil, errors.New("provider kubeconfig unavailable")
		}
		return &rest.Config{Host: "https://kcp.example"}, nil
	}
	start := func(startCtx context.Context, config *rest.Config) error {
		if err := factory.SetBaseConfig(config); err != nil {
			return err
		}
		mgr := &startupGateManager{}
		authority.Set(mgr)
		defer authority.Clear(mgr)
		health.markReady()
		close(recovered)
		<-startCtx.Done()
		return startCtx.Err()
	}
	retryGate := func(retryCtx context.Context, _ time.Duration) bool {
		close(retryStarted)
		select {
		case <-allowRetry:
			return true
		case <-retryCtx.Done():
			return false
		}
	}

	go func() {
		runControllerManagerWithRetryGate(ctx, health, loadConfig, start, 0, retryGate)
		close(done)
	}()
	waitForDatabricksControllerSignal(t, retryStarted)
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "provider kubeconfig unavailable" {
		t.Fatalf("initial unavailable health = %+v", got)
	}
	if factory.Configured() || factory.Ready() || health.ready() {
		t.Fatal("tenant dependency became ready before recovered config")
	}

	close(allowRetry)
	waitForDatabricksControllerSignal(t, recovered)
	if !factory.Configured() || !factory.Ready() || !health.ready() {
		t.Fatalf("recovered dependency state = configured:%v ready:%v health:%v, want true/true/true", factory.Configured(), factory.Ready(), health.ready())
	}
	if _, err := factory.For("tenant-cluster", "caller-token"); err != nil {
		t.Fatalf("tenant action client after config recovery: %v", err)
	}

	cancel()
	waitForDatabricksControllerSignal(t, done)
	if health.snapshot().State != controllerStateStopped || factory.Ready() {
		t.Fatalf("post-stop state = health:%+v factoryReady:%v, want stopped/false", health.snapshot(), factory.Ready())
	}
}

func TestControllerDurationsStayWithinSafeBounds(t *testing.T) {
	t.Setenv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", "250ms")
	if got := controllerRetryIntervalFromEnv(); got != controllerRetryIntervalMin {
		t.Fatalf("retry interval below minimum = %s, want %s", got, controllerRetryIntervalMin)
	}
	t.Setenv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", "10m")
	if got := controllerRetryIntervalFromEnv(); got != controllerRetryIntervalMax {
		t.Fatalf("retry interval above maximum = %s, want %s", got, controllerRetryIntervalMax)
	}
	t.Setenv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", "not-a-duration")
	if got := controllerRetryIntervalFromEnv(); got != controllerRetryInterval {
		t.Fatalf("invalid retry interval = %s, want default %s", got, controllerRetryInterval)
	}
	for _, input := range []time.Duration{0, -time.Second, 500 * time.Millisecond} {
		if got := normalizeControllerRetryInterval(input); got != controllerRetryIntervalMin {
			t.Fatalf("direct retry interval %s normalized to %s, want %s", input, got, controllerRetryIntervalMin)
		}
	}
	if got := normalizeControllerRetryInterval(10 * time.Minute); got != controllerRetryIntervalMax {
		t.Fatalf("direct retry interval above maximum = %s, want %s", got, controllerRetryIntervalMax)
	}

	t.Setenv("DATABRICKS_CONTROLLER_STARTUP_TIMEOUT", "500ms")
	if got := controllerStartupTimeoutFromEnv(); got != controllerStartupTimeoutMin {
		t.Fatalf("startup timeout below minimum = %s, want %s", got, controllerStartupTimeoutMin)
	}
	t.Setenv("DATABRICKS_CONTROLLER_STARTUP_TIMEOUT", "10m")
	if got := controllerStartupTimeoutFromEnv(); got != controllerStartupTimeoutMax {
		t.Fatalf("startup timeout above maximum = %s, want %s", got, controllerStartupTimeoutMax)
	}
}

func waitForDatabricksControllerSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("controller lifecycle did not reach expected transition")
	}
}

type startupGateManager struct {
	mcmanager.Manager
}

func (*startupGateManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return nil, errors.New("startup gate manager")
}

type controllerProviderStub struct{}

func (*controllerProviderStub) Get(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return nil, errors.New("provider cluster unavailable")
}

func (*controllerProviderStub) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}

type controllerProviderRunnableStub func(context.Context, multicluster.Aware) error

func (f controllerProviderRunnableStub) Start(ctx context.Context, aware multicluster.Aware) error {
	return f(ctx, aware)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type controllerBlockingReadCloser struct {
	ctx     context.Context
	initial []byte
	offset  int
	stopped chan struct{}
	once    sync.Once
}

func (r *controllerBlockingReadCloser) Read(p []byte) (int, error) {
	if r.offset < len(r.initial) {
		n := copy(p, r.initial[r.offset:])
		r.offset += n
		return n, nil
	}
	<-r.ctx.Done()
	if r.stopped != nil {
		r.once.Do(func() { close(r.stopped) })
	}
	return 0, r.ctx.Err()
}

func (r *controllerBlockingReadCloser) Close() error {
	if r.stopped != nil {
		r.once.Do(func() { close(r.stopped) })
	}
	return nil
}

type controllerTestDeadlineContext struct {
	parent context.Context
	done   chan struct{}
	once   sync.Once
}

func newControllerTestDeadlineContext(parent context.Context) *controllerTestDeadlineContext {
	return &controllerTestDeadlineContext{parent: parent, done: make(chan struct{})}
}

func (c *controllerTestDeadlineContext) Deadline() (time.Time, bool) {
	return time.Unix(0, 0), true
}

func (c *controllerTestDeadlineContext) Done() <-chan struct{} { return c.done }

func (c *controllerTestDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return c.parent.Err()
	}
}

func (c *controllerTestDeadlineContext) Value(key any) any { return c.parent.Value(key) }

func (c *controllerTestDeadlineContext) expire() { c.once.Do(func() { close(c.done) }) }
