/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package providerinstall

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestResolvePlanCancellationBoundsRemoteChartRequestsAndCleansCache(t *testing.T) {
	for _, chartRef := range []string{
		"oci://registry.test/railgrid/provider",
		"https://charts.test/provider.tgz",
	} {
		t.Run(chartRef, func(t *testing.T) {
			assertResolvePlanCanceled(t, providers.InstallInstructions{ChartRef: chartRef})
		})
	}
}

func TestResolvePlanPinnedVersionSkipsChartPull(t *testing.T) {
	transport := &countingRoundTripper{}
	plan, err := resolvePlan(context.Background(), providers.InstallInstructions{
		ChartRef:     "oci://registry.test/railgrid/provider",
		ChartVersion: "1.2.3",
	}, planResolverConfig{transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != "1.2.3" {
		t.Fatalf("pinned plan version = %q, want 1.2.3", plan.Version)
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("pinned chart version made %d HTTP requests, want none", got)
	}
}

func TestResolvePlanDownloadsDirectHTTPChartAndRemovesCache(t *testing.T) {
	archive, err := chartutil.Save(&chart.Chart{Metadata: &chart.Metadata{
		Name: "provider", Version: "2.3.4", APIVersion: chart.APIVersionV2,
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archiveData, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/provider-2.3.4.tgz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	tempParent := t.TempDir()
	plan, err := resolvePlan(context.Background(), providers.InstallInstructions{
		ChartRef: server.URL + "/provider-2.3.4.tgz",
	}, planResolverConfig{tempDir: tempParent})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != "2.3.4" {
		t.Fatalf("resolved chart version = %q, want 2.3.4", plan.Version)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP chart requests = %d, want 1", got)
	}
	entries, err := os.ReadDir(tempParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary Helm cache remains after resolution: %v", entries)
	}
}

func assertResolvePlanCanceled(t *testing.T, instructions providers.InstallInstructions) {
	t.Helper()
	tempParent := t.TempDir()
	transport := &blockingRoundTripper{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := resolvePlan(ctx, instructions, planResolverConfig{tempDir: tempParent, transport: transport})
		done <- err
	}()

	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("chart resolution did not reach the injected HTTP transport")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ResolvePlan error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResolvePlan did not stop after its context was canceled")
	}

	entries, err := os.ReadDir(tempParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary Helm cache remains after resolution: %v", entries)
	}
}

type blockingRoundTripper struct {
	started chan struct{}
	once    sync.Once
}

func (r *blockingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	r.once.Do(func() { close(r.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

type countingRoundTripper struct {
	calls atomic.Int32
}

func (r *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("unexpected HTTP request")
}
