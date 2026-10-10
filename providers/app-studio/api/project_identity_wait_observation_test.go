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

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

type projectIdentityWaitRateLimiter struct {
	waits atomic.Int32
	delay time.Duration
}

func (limiter *projectIdentityWaitRateLimiter) Accept() {}

func (limiter *projectIdentityWaitRateLimiter) TryAccept() bool { return true }

func (limiter *projectIdentityWaitRateLimiter) Wait(ctx context.Context) error {
	limiter.waits.Add(1)
	timer := time.NewTimer(limiter.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (limiter *projectIdentityWaitRateLimiter) Stop() {}

func (*projectIdentityWaitRateLimiter) QPS() float32 { return 10 }

var _ flowcontrol.RateLimiter = (*projectIdentityWaitRateLimiter)(nil)

func TestProjectIntegrationBindingStatusSharesWaitObservationAcrossBothGets(t *testing.T) {
	limiter := &projectIdentityWaitRateLimiter{delay: 2 * time.Millisecond}
	var currentProjectGets atomic.Int32
	var targetGets atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects/project-a"):
			currentProjectGets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"apiVersion":%q,"kind":"Project","metadata":{"name":"project-a","uid":"project-uid","generation":9}}`, aiv1alpha1.GroupName+"/"+aiv1alpha1.Version)
		case strings.HasSuffix(r.URL.Path, "/widgets/widget-a"):
			targetGets.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer project-token" {
				t.Errorf("named provider GET Authorization = %q, want the Project scoped identity", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"apiVersion":"example.dev/v1","kind":"Widget","metadata":{"name":"widget-a"}}`))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer hub.Close()

	callers := newProjectIdentityTransportCallers(hub.URL, nil)
	callers.providerConfig.RateLimiter = limiter
	provider, err := dynamic.NewForConfig(&rest.Config{Host: hub.URL + "/clusters/cluster-a", RateLimiter: limiter})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{callers: callers, hubBase: hub.URL}
	server.projectIdentityTokenFor = func(ctx context.Context, id identity, project *aiv1alpha1.Project) (string, error) {
		if _, err := server.currentProjectForIdentity(ctx, id, project); err != nil {
			return "", err
		}
		return "project-token", nil
	}

	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-a", UID: "project-uid"}}
	grantedAt := metav1.Now()
	binding := aiv1alpha1.ProjectProviderBindingSpec{
		Kind: aiv1alpha1.ProjectBindingKindProviderReference,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name: "widget-a", APIVersion: "example.dev/v1", Kind: "Widget", Resource: "widgets",
		},
		AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{
			Name: "lookup", Version: "v1", GrantedBy: "alice", GrantedAt: &grantedAt,
		}},
	}
	id := identity{clusterID: "cluster-a", provider: provider}
	ctx, waits := projectAssistantObserveRateLimiterWaits(context.Background())
	status := server.projectIntegrationBindingStatus(ctx, nil, project, binding, id)
	if status.Phase != "Ready" {
		t.Fatalf("binding phase = %q, want Ready", status.Phase)
	}
	if got := currentProjectGets.Load(); got != 1 {
		t.Fatalf("fresh Project GET count = %d, want 1", got)
	}
	if got := targetGets.Load(); got != 1 {
		t.Fatalf("named provider GET count = %d, want 1", got)
	}
	if got := limiter.waits.Load(); got != 2 {
		t.Fatalf("rate limiter Wait count = %d, want one for each GET", got)
	}
	if got := waits.Snapshot(); got.calls != 2 || got.delayed != 2 || got.total < 4*time.Millisecond {
		t.Fatalf("shared wait observation = %#v, want both delayed GET waits", got)
	}
}

func TestCurrentProjectReadLogsWaitAndDurationWithoutProjectIdentity(t *testing.T) {
	limiter := &projectIdentityWaitRateLimiter{delay: 2 * time.Millisecond}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/projects/project-a") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"apiVersion":%q,"kind":"Project","metadata":{"name":"project-a","uid":"project-uid","generation":9}}`, aiv1alpha1.GroupName+"/"+aiv1alpha1.Version)
	}))
	defer hub.Close()
	provider, err := dynamic.NewForConfig(&rest.Config{Host: hub.URL + "/clusters/cluster-a", RateLimiter: limiter})
	if err != nil {
		t.Fatal(err)
	}

	var entries []string
	logger := funcr.New(func(prefix, args string) { entries = append(entries, prefix+args) }, funcr.Options{})
	ctx := klog.NewContext(context.Background(), logger)
	server := &Server{}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-a", UID: "project-uid"}}
	current, err := server.currentProjectForIdentity(ctx, identity{clusterID: "cluster-a", provider: provider}, project)
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation != 9 {
		t.Fatalf("current Project generation = %d, want 9", current.Generation)
	}
	if got := limiter.waits.Load(); got != 1 {
		t.Fatalf("rate limiter Wait count = %d, want 1", got)
	}
	if len(entries) != 1 {
		t.Fatalf("current Project read log entries = %d, want 1: %q", len(entries), entries)
	}
	logLine := entries[0]
	for _, fragment := range []string{"current_project_identity_read", "duration", "rateLimiterWaitCalls", "rateLimiterWaitDuration"} {
		if !strings.Contains(logLine, fragment) {
			t.Errorf("current Project read log %q is missing %q", logLine, fragment)
		}
	}
	if strings.Contains(logLine, "project-a") || strings.Contains(logLine, "cluster-a") || strings.Contains(logLine, "project-token") {
		t.Fatalf("current Project read log retained identity data: %q", logLine)
	}
}

func TestCurrentProjectReadUsesSharedProviderMetadataBudgetAndKeepsFreshChecks(t *testing.T) {
	var requests atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if !strings.Contains(r.URL.Path, "/clusters/cluster-a/") || !strings.HasSuffix(r.URL.Path, "/projects/project-a") {
			t.Errorf("current Project request path = %q, want scoped cluster-a Project GET", r.URL.Path)
		}
		uid := "project-uid"
		generation := requestNumber + 8
		deletion := ""
		switch requestNumber {
		case 3:
			uid = "replacement-project-uid"
		case 4:
			deletion = `,"deletionTimestamp":"2026-10-10T00:00:00Z"`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"apiVersion":%q,"kind":"Project","metadata":{"name":"project-a","uid":%q,"generation":%d%s}}`,
			aiv1alpha1.GroupName+"/"+aiv1alpha1.Version, uid, generation, deletion)
	}))
	defer hub.Close()

	factory := &projectAssistantWorkerReadCallerFactory{clientFor: func(clusterID string, limiter flowcontrol.RateLimiter) dynamic.Interface {
		client, err := dynamic.NewForConfig(&rest.Config{
			Host:        hub.URL + "/clusters/" + clusterID,
			RateLimiter: limiter,
		})
		if err != nil {
			t.Errorf("construct metadata client: %v", err)
			return nil
		}
		return client
	}}
	server := &Server{callers: factory}
	originalProvider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	id := identity{clusterID: "cluster-a", provider: originalProvider, user: "alice"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-a", UID: "project-uid"}}

	for index, wantGeneration := range []int64{9, 10} {
		current, err := server.currentProjectForIdentity(context.Background(), id, project)
		if err != nil {
			t.Fatalf("fresh Project read %d: %v", index+1, err)
		}
		if current.Generation != wantGeneration {
			t.Fatalf("fresh Project read %d generation = %d, want %d", index+1, current.Generation, wantGeneration)
		}
	}
	if _, err := server.currentProjectForIdentity(context.Background(), id, project); err == nil || !strings.Contains(err.Error(), "UID changed") {
		t.Fatalf("UID replacement result = %v, want fresh UID-change rejection", err)
	}
	if _, err := server.currentProjectForIdentity(context.Background(), id, project); err == nil || !strings.Contains(err.Error(), "deleting") {
		t.Fatalf("deletion result = %v, want fresh deletion rejection", err)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("fresh Project GET count = %d, want one per check", got)
	}
	if id.provider != originalProvider {
		t.Fatal("metadata client derivation replaced the Gate/review provider identity")
	}
	calls := factory.snapshot()
	sharedLimiter := server.assistantWorkerReadBudget.rateLimiter()
	if len(calls) != 4 {
		t.Fatalf("metadata client derivations = %d, want 4", len(calls))
	}
	for index, call := range calls {
		if call.cluster != id.clusterID || call.limiter != sharedLimiter {
			t.Fatalf("metadata client %d scope/budget = %#v, want cluster %q and shared limiter", index, call, id.clusterID)
		}
	}
}

func TestCurrentProjectReadPropagatesCanceledSharedBudgetWait(t *testing.T) {
	var requests atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request after canceled wait", http.StatusInternalServerError)
	}))
	defer hub.Close()
	factory := &projectAssistantWorkerReadCallerFactory{clientFor: func(clusterID string, limiter flowcontrol.RateLimiter) dynamic.Interface {
		client, err := dynamic.NewForConfig(&rest.Config{Host: hub.URL + "/clusters/" + clusterID, RateLimiter: limiter})
		if err != nil {
			t.Errorf("construct metadata client: %v", err)
			return nil
		}
		return client
	}}
	server := &Server{callers: factory}
	limiter := server.assistantWorkerReadBudget.rateLimiter()
	for token := 0; token < projectAssistantWorkerReadBurst; token++ {
		if !limiter.TryAccept() {
			t.Fatalf("shared limiter stopped before exhausting burst token %d", token+1)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-a", UID: "project-uid"}}
	_, err := server.currentProjectForIdentity(ctx, identity{clusterID: "cluster-a"}, project)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata budget wait = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("canceled metadata read made %d HTTP requests, want none", got)
	}
	calls := factory.snapshot()
	if len(calls) != 1 || calls[0].limiter != limiter {
		t.Fatalf("canceled metadata client derivation = %#v, want the shared server limiter", calls)
	}
}
