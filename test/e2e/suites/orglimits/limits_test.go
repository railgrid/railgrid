// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package orglimits exercises Railgrid's native organization guard against the
// unmodified pinned kcp, including fail-closed coverage for protected consumer
// workspaces.
package orglimits

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/kcp/byolimits"
)

var (
	workspaces      = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	namespaces      = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	configmaps      = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	secrets         = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	resourcequotas  = schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}
	logicalclusters = schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}
	exports         = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	bindings        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
)

func TestNativeOrganizationLimits(t *testing.T) {
	if os.Getenv("RAILGRID_E2E_ORG_LIMITS") != "true" {
		t.Skip("run make test-byo-availability to start an isolated embedded kcp")
	}
	// One embedded server per process: kcp owns process-global feature gates.
	f := start(t)
	railgrid := f.workspace(t, "root", "railgrid")
	tenants := f.workspace(t, railgrid, "tenants")
	alpha := f.workspace(t, tenants, "limits-alpha")
	beta := f.workspace(t, tenants, "limits-beta")
	exporter := f.workspace(t, alpha, "guard-export")
	for _, cluster := range []string{alpha, beta} {
		_, err := f.client(t, cluster).Resource(namespaces).Create(f.ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "default"}}}, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			check(t, err)
		}
		check(t, createWithQuotaConflictRetry(f.ctx, f.maps(t, cluster), configMap("small", "hello")))
	}
	// Mark alpha only after workspace bootstrap writes have completed. The
	// quota uses the supported workspace-wide annotation and covers the
	// namespaced ConfigMap family used by this fixture.
	f.protectConsumerConfigMaps(t, alpha)
	t.Log("native hooks ready; real Workspace initialization and organization admin CRUD pass")

	t.Run("new stored resource requires live quota coverage", func(t *testing.T) {
		check(t, createWithQuotaConflictRetry(f.ctx, f.maps(t, alpha), configMap("covered-write", "covered by consumer quota")))
		secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "uncovered-write", "namespace": "default"}, "data": map[string]any{"value": "eA=="}}}
		_, err := f.client(t, alpha).Resource(secrets).Namespace("default").Create(f.ctx, secret, metav1.CreateOptions{})
		requireQuotaDenial(t, err)
		// The same uncovered resource remains writable in an unmarked workspace.
		secret = secret.DeepCopy()
		secret.SetName("beta-still-works")
		_, err = f.client(t, beta).Resource(secrets).Namespace("default").Create(f.ctx, secret, metav1.CreateOptions{})
		check(t, err)
	})

	t.Run("full admitted object size and cleanup", func(t *testing.T) {
		maps := f.maps(t, alpha)
		err := createWithQuotaConflictRetry(f.ctx, maps, configMap("too-large", strings.Repeat("x", 20000)))
		requireSizeDenial(t, err)
		check(t, createWithQuotaConflictRetry(f.ctx, maps, configMap("patch-target", strings.Repeat("x", 14000))))
		patch := []byte(`{"data":{"extra":"` + strings.Repeat("y", 3000) + `"}}`)
		if int64(len(patch)) >= f.limits.MaxObjectBytes {
			t.Fatal("fixture patch must fit the byte cap")
		}
		_, err = maps.Patch(f.ctx, "patch-target", types.MergePatchType, patch, metav1.PatchOptions{})
		requireSizeDenial(t, err)
		check(t, maps.Delete(f.ctx, "patch-target", metav1.DeleteOptions{}))
		check(t, createWithQuotaConflictRetry(f.ctx, f.maps(t, beta), configMap("still-works", "other organization")))
	})

	t.Run("actual virtual workspace request guard", func(t *testing.T) {
		url := f.server + "/services/apiexport/root/tenancy.kcp.io/clusters/" + alpha + "/apis/tenancy.kcp.io/v1alpha1/workspaces"
		f.waitGET(t, url)
		resp := f.get(t, url, strings.NewReader(strings.Repeat("x", int(f.limits.MaxRequestBytes)+1)))
		body := readResponse(t, resp)
		if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "organization request exceeds byte limit") {
			t.Fatalf("VW byte guard returned %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("own export wildcard streams and other organization", func(t *testing.T) {
		claim := map[string]any{"group": "", "resource": "configmaps", "verbs": []any{"get", "list", "watch"}}
		export := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport", "metadata": map[string]any{"name": "guard-smoke"}, "spec": map[string]any{"permissionClaims": []any{claim}}}}
		_, err := f.client(t, exporter).Resource(exports).Create(f.ctx, export, metav1.CreateOptions{})
		check(t, err)
		accepted := map[string]any{"group": "", "resource": "configmaps", "verbs": []any{"get", "list", "watch"}, "selector": map[string]any{"matchAll": true}, "state": "Accepted"}
		exporterPath := f.logicalClusterPath(t, exporter)
		binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "guard-smoke"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": exporterPath, "name": "guard-smoke"}}, "permissionClaims": []any{accepted}}}}
		_, err = f.client(t, alpha).Resource(bindings).Create(f.ctx, binding, metav1.CreateOptions{})
		check(t, err)
		url := f.server + "/services/apiexport/" + exporter + "/guard-smoke/clusters/*/api/v1/configmaps"
		f.waitGET(t, url)
		// Watch handlers flush headers immediately. Keep every response body
		// open so these are concurrent real server-side watch requests.
		for range f.limits.MaxConcurrentStreams {
			resp := f.get(t, url+"?watch=true", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("watch returned %d: %s", resp.StatusCode, readResponse(t, resp))
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
		}
		resp := f.get(t, url+"?watch=true", nil)
		body := readResponse(t, resp)
		if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "organization request budget exhausted") {
			t.Fatalf("wildcard stream cap returned %d: %s", resp.StatusCode, body)
		}
		// Hold alpha's real watches open while additional watch requests and
		// beta's write run concurrently. The deadline is a generous regression
		// bound for this local fixture, not a production latency promise.
		operationCtx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		defer cancel()
		start := make(chan struct{})
		floodResults := make(chan error, 8)
		for range cap(floodResults) {
			go func() {
				<-start
				floodResults <- f.expectWatchRejection(operationCtx, url+"?watch=TRUE")
			}()
		}
		close(start)
		started := time.Now()
		writeErr := createWithQuotaConflictRetry(operationCtx, f.maps(t, beta), configMap("watch-isolated", "other organization remains writable"))
		elapsed := time.Since(started)
		for range cap(floodResults) {
			check(t, <-floodResults)
		}
		check(t, writeErr)
		t.Logf("other organization write completed in %s under the 10s fixture deadline while alpha's watches stayed open", elapsed.Round(time.Millisecond))
	})
}

func (f *fixture) expectWatchRejection(ctx context.Context, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := f.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), "organization request budget exhausted") {
		return fmt.Errorf("concurrent wildcard watch returned %d: %s", response.StatusCode, body)
	}
	return nil
}

type fixture struct {
	ctx    context.Context
	base   *rest.Config
	server string
	native *kcp.EmbeddedKCP
	http   *http.Client
	limits byolimits.Config
}

func start(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	ports := freePorts(t, 3)
	limits := byolimits.Config{MaxObjectBytes: 16384, MaxRequestBytes: 32768, RequestsPerSecond: 100, Burst: 300, MaxConcurrentRequests: 16, MaxConcurrentStreams: 4, MaxOrganizations: 4, RequestTimeout: 15 * time.Second, StreamTimeout: 30 * time.Second}
	server := kcp.NewEmbeddedKCP(kcp.EmbeddedKCPOptions{RootDir: t.TempDir(), SecurePort: ports[0], EtcdClientPort: ports[1], EtcdPeerPort: ports[2], BindAddress: "127.0.0.1", BYOLimits: limits})
	done := make(chan error, 1)
	go func() { defer close(done); done <- server.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("embedded kcp did not stop")
		}
	})
	select {
	case <-server.Ready():
	case err := <-done:
		t.Fatalf("embedded kcp startup: %v", err)
	case <-ctx.Done():
		t.Fatalf("embedded kcp startup: %v", ctx.Err())
	}
	for server.BYOLimitsReady() != nil {
		select {
		case <-ctx.Done():
			t.Fatalf("native guard readiness: %v", server.BYOLimitsReady())
		case <-time.After(100 * time.Millisecond):
		}
	}
	base := server.AdminConfig()
	httpClient, err := rest.HTTPClientFor(base)
	check(t, err)
	// The test context bounds requests; native stream deadlines additionally
	// bound the intentionally open watch bodies.
	return &fixture{ctx: ctx, base: base, server: strings.Split(base.Host, "/clusters/")[0], native: server, http: httpClient, limits: limits}
}

func (f *fixture) client(t *testing.T, cluster string) dynamic.Interface {
	t.Helper()
	config := rest.CopyConfig(f.base)
	config.Host = f.server + "/clusters/" + cluster
	config.QPS, config.Burst = 100, 300
	client, err := dynamic.NewForConfig(config)
	check(t, err)
	return client
}

func (f *fixture) maps(t *testing.T, cluster string) dynamic.ResourceInterface {
	t.Helper()
	return f.client(t, cluster).Resource(configmaps).Namespace("default")
}

func (f *fixture) logicalClusterPath(t *testing.T, cluster string) string {
	t.Helper()
	logicalCluster, err := f.client(t, cluster).Resource(logicalclusters).Get(f.ctx, "cluster", metav1.GetOptions{})
	check(t, err)
	path := logicalCluster.GetAnnotations()["kcp.io/path"]
	if path == "" {
		t.Fatalf("logical cluster %s has no canonical path", cluster)
	}
	return path
}

func (f *fixture) protectConsumerConfigMaps(t *testing.T, cluster string) {
	t.Helper()
	client := f.client(t, cluster)
	logicalCluster, err := client.Resource(logicalclusters).Get(f.ctx, "cluster", metav1.GetOptions{})
	check(t, err)
	annotations := logicalCluster.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[byolimits.ConsumerQuotaRequiredAnnotation] = byolimits.ConsumerQuotaRequiredValue
	logicalCluster.SetAnnotations(annotations)
	_, err = client.Resource(logicalclusters).Update(f.ctx, logicalCluster, metav1.UpdateOptions{})
	check(t, err)
	check(t, f.native.WaitForBYOConsumerQuotaMarker(f.ctx, cluster))
	adminNamespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": byolimits.ConsumerQuotaNamespace}}}
	_, err = client.Resource(namespaces).Create(f.ctx, adminNamespace, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		check(t, err)
	}
	quota := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ResourceQuota",
		"metadata": map[string]any{
			"name": byolimits.ConsumerQuotaName, "namespace": byolimits.ConsumerQuotaNamespace,
			"annotations": map[string]any{"experimental.quota.kcp.io/cluster-scoped": "true"},
		},
		"spec": map[string]any{"hard": map[string]any{"count/configmaps": "64"}},
	}}
	_, err = client.Resource(resourcequotas).Namespace(byolimits.ConsumerQuotaNamespace).Create(f.ctx, quota, metav1.CreateOptions{})
	check(t, err)
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := client.Resource(resourcequotas).Namespace(byolimits.ConsumerQuotaNamespace).Get(f.ctx, byolimits.ConsumerQuotaName, metav1.GetOptions{})
		if err == nil {
			hard, hardFound, hardErr := unstructured.NestedString(got.Object, "status", "hard", "count/configmaps")
			used, usedFound, usedErr := unstructured.NestedString(got.Object, "status", "used", "count/configmaps")
			if hardFound && usedFound && hardErr == nil && usedErr == nil && hard == "64" {
				_ = used
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer ResourceQuota status did not initialize: %v", err)
		}
		select {
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *fixture) workspace(t *testing.T, parent, name string) string {
	t.Helper()
	client := f.client(t, parent).Resource(workspaces)
	workspace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": name}, "spec": map[string]any{"type": map[string]any{"name": "universal", "path": "root"}}}}
	_, err := client.Create(f.ctx, workspace, metav1.CreateOptions{})
	check(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, time.Minute)
	defer cancel()
	var lastErr error
	for {
		got, err := client.Get(ctx, name, metav1.GetOptions{})
		lastErr = err
		if err == nil {
			phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
			if phase == "Ready" {
				cluster, _, _ := unstructured.NestedString(got.Object, "spec", "cluster")
				if cluster == "" {
					t.Fatal("ready Workspace has no cluster identity")
				}
				return cluster
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Workspace %s/%s not ready: %v (last request: %v)", parent, name, ctx.Err(), lastErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *fixture) get(t *testing.T, url string, body io.Reader) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, url, body)
	check(t, err)
	response, err := f.http.Do(request)
	check(t, err)
	return response
}

func (f *fixture) waitGET(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		response := f.get(t, url, nil)
		body := readResponse(t, response)
		if response.StatusCode == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("VW GET returned %d: %s", response.StatusCode, body)
		}
		select {
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func configMap(name, data string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name, "namespace": "default"}, "data": map[string]any{"value": data}}}
}

// Quota status reconciliation can race a covered create. Retry only bounded
// resource-version conflicts against the same object and quota; quota
// exhaustion and every other API error remain visible to the test.
func createWithQuotaConflictRetry(ctx context.Context, resource dynamic.ResourceInterface, object *unstructured.Unstructured) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		_, err := resource.Create(ctx, object.DeepCopy(), metav1.CreateOptions{})
		return err
	})
}

func requireSizeDenial(t *testing.T, err error) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "serialized bytes") {
		t.Fatalf("expected native object-size denial, got %v", err)
	}
}

func requireQuotaDenial(t *testing.T, err error) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "consumer quota") {
		t.Fatalf("expected native consumer quota coverage denial, got %v", err)
	}
}

func readResponse(t *testing.T, response *http.Response) string {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	check(t, err)
	return string(body)
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func freePorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		check(t, err)
		listeners = append(listeners, listener)
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("unexpected listener address %T", listener.Addr())
		}
		ports = append(ports, address.Port)
	}
	return ports
}
