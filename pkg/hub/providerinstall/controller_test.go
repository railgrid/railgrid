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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	controllerfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

func TestCleanupPlanRequiresWorkspaceUIDAndTrustedScope(t *testing.T) {
	plan := &Plan{Owner: "registration-uid", Org: "org-a", Provider: "installer", Namespace: "apps", Job: "job-one", AttemptID: "attempt-one"}
	workspace := cleanupWorkspaceForTest(t, "installer", "registration-uid", plan)

	got, ok := cleanupPlanForWorkspace("org-a", "installer", workspace)
	if !ok || got.Job != plan.Job {
		t.Fatalf("valid sealed plan was rejected: plan=%#v valid=%t", got, ok)
	}
	for _, test := range []struct {
		name       string
		org        string
		nameInPlan string
		uid        string
	}{
		{name: "workspace UID changed", org: "org-a", nameInPlan: "installer", uid: "new-registration-uid"},
		{name: "organization mismatch", org: "org-b", nameInPlan: "installer", uid: "registration-uid"},
		{name: "provider mismatch", org: "org-a", nameInPlan: "other", uid: "registration-uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			badPlan := *plan
			badPlan.Org = test.org
			badPlan.Provider = test.nameInPlan
			badWorkspace := cleanupWorkspaceForTest(t, "installer", test.uid, &badPlan)
			if _, ok := cleanupPlanForWorkspace("org-a", "installer", badWorkspace); ok {
				t.Fatal("untrusted plan was accepted")
			}
		})
	}

	legacy := &Plan{Owner: "registration-uid", Namespace: "apps", Job: "job-one", AttemptID: "attempt-one"}
	legacyWorkspace := cleanupWorkspaceForTest(t, "installer", "registration-uid", legacy)
	if _, ok := cleanupPlanForWorkspace("org-a", "installer", legacyWorkspace); !ok {
		t.Fatal("legacy plan without Org/Provider fields should use the scoped parent and Workspace name")
	}
}

func TestOrganizationWorkspaceInformerUsesScopedParentAndReplaysAfterRestart(t *testing.T) {
	plan := &Plan{Owner: "registration-uid", Org: "org-a", Provider: "installer", Namespace: "apps", Job: "install-job", AttemptID: "attempt-one"}
	workspace := cleanupWorkspaceForTest(t, "installer", "registration-uid", plan)
	for term := 1; term <= 2; term++ {
		t.Run(fmt.Sprintf("term-%d", term), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			queue := cleanupQueueForTest(t)
			installer := &Installer{admin: &rest.Config{Host: "https://kcp.invalid"}, workspaces: cleanupWorkspaceAccessForTest{}}
			source := newCleanupWatchSource(installer)
			var config *rest.Config
			source.dynamicClient = func(got *rest.Config) (dynamic.Interface, error) {
				config = rest.CopyConfig(got)
				return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), workspace.DeepCopy()), nil
			}
			if err := source.Start(ctx, queue); err != nil {
				t.Fatal(err)
			}
			if err := source.ensureOrganization("org-a"); err != nil {
				t.Fatal(err)
			}
			got := waitForCleanupRequest(t, queue)
			if got.Namespace != "org-a" || got.Name != "installer" {
				t.Fatalf("replayed Workspace event key = %s/%s, want org-a/installer", got.Namespace, got.Name)
			}
			wantHost := "https://kcp.invalid/clusters/root:railgrid:tenants:org-a:providers"
			if config == nil || config.Host != wantHost || config.Timeout != 0 {
				t.Fatalf("Workspace watch config = %#v, want host %q and no watch timeout", config, wantHost)
			}
			watch := source.orgs["org-a"]
			cancel()
			source.stopAll()
			select {
			case <-watch.done:
			case <-time.After(5 * time.Second):
				t.Fatal("Workspace informer did not stop with its leadership context")
			}
		})
	}
}

func TestTerminalAttemptEventsAreFencedAndOrgScoped(t *testing.T) {
	queue := cleanupQueueForTest(t)
	source := newCleanupWatchSource(&Installer{})
	source.queue = queue
	plan := &Plan{Namespace: "apps", Job: "install-job", Owner: "registration-uid", AttemptID: "attempt-one"}
	job := completeInstallerJobForTest(plan)

	inProgress := job.DeepCopy()
	inProgress.Status.Conditions = nil
	source.onAttemptObject("org-a", "installer", plan, inProgress)
	if queue.Len() != 0 {
		t.Fatal("nonterminal Job enqueued cleanup")
	}

	wrongAttempt := job.DeepCopy()
	wrongAttempt.Annotations[attemptIDAnnotation] = "stale-attempt"
	source.onAttemptObject("org-a", "installer", plan, wrongAttempt)
	if queue.Len() != 0 {
		t.Fatal("a stale attempt enqueued cleanup")
	}

	source.onAttemptObject("org-a", "installer", plan, job)
	if got := waitForCleanupRequest(t, queue); got.Namespace != "org-a" || got.Name != "installer" {
		t.Fatalf("terminal Job enqueued key %s/%s, want org-a/installer", got.Namespace, got.Name)
	}
	wrongOrgQueue := cleanupQueueForTest(t)
	other := newCleanupWatchSource(&Installer{})
	other.queue = wrongOrgQueue
	other.onAttemptObject("bad/org", "installer", plan, job)
	if wrongOrgQueue.Len() != 0 {
		t.Fatal("invalid org scope reached the workqueue")
	}
}

func TestWorkspaceQueueKeysPreserveOrganizationForSameProviderName(t *testing.T) {
	queue := cleanupQueueForTest(t)
	source := newCleanupWatchSource(&Installer{})
	source.queue = queue
	workspace := cleanupWorkspaceForTest(t, "installer", "registration-uid", nil)
	source.onWorkspace("org-a", workspace)
	source.onWorkspace("org-b", workspace)

	got := map[string]bool{}
	for range 2 {
		request := waitForCleanupRequest(t, queue)
		got[request.Namespace+"/"+request.Name] = true
	}
	if !got["org-a/installer"] || !got["org-b/installer"] {
		t.Fatalf("same-name provider queue keys lost organization scope: %v", got)
	}
}

func TestReconcileNotFoundStopsOnlyTheMatchingAttemptWatch(t *testing.T) {
	cancelled := false
	watchDone := make(chan struct{})
	close(watchDone)
	attempt := &attemptCleanupWatch{job: "install-job", attemptID: "attempt-one", owner: "registration-uid", cancel: func() { cancelled = true }, done: watchDone}
	organization := &organizationCleanupWatch{org: "org-a", attempts: map[string]*attemptCleanupWatch{"installer": attempt}}
	source := newCleanupWatchSource(&Installer{})
	source.orgs["org-a"] = organization
	installer := &Installer{
		admin:      &rest.Config{Host: "https://kcp.invalid"},
		workspaces: cleanupWorkspaceAccessForTest{},
		dynamicClient: func(*rest.Config) (dynamic.Interface, error) {
			return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil
		},
	}
	reconciler := &cleanupReconciler{installer: installer, source: source}
	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "org-a", Name: "installer"}})
	if err != nil {
		t.Fatalf("deleted Workspace reconcile: %v", err)
	}
	if !cancelled {
		t.Fatal("deleted provider Workspace did not stop its remote attempt watch")
	}
	if _, exists := organization.attempts["installer"]; exists {
		t.Fatal("deleted provider Workspace retained its attempt key")
	}
	if source.orgs["org-a"] != organization {
		t.Fatal("provider Workspace NotFound stopped the whole organization watch")
	}
}

func TestOrganizationNotFoundStopsScopedInformerAndAttempts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	organizationCanceled, attemptCanceled := false, false
	orgDone := make(chan struct{})
	close(orgDone)
	attemptDone := make(chan struct{})
	close(attemptDone)
	attempt := &attemptCleanupWatch{cancel: func() { attemptCanceled = true }, done: attemptDone}
	organization := &organizationCleanupWatch{
		org: "org-a", cancel: func() { organizationCanceled = true }, done: orgDone,
		attempts: map[string]*attemptCleanupWatch{"installer": attempt},
	}
	source := newCleanupWatchSource(&Installer{})
	source.orgs["org-a"] = organization
	reconciler := &cleanupReconciler{client: controllerfake.NewClientBuilder().WithScheme(scheme).Build(), source: source}
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "org-a"}})
	if err != nil {
		t.Fatalf("deleted Organization reconcile: %v", err)
	}
	if !organizationCanceled || !attemptCanceled {
		t.Fatalf("Organization stop callbacks = workspace:%t attempt:%t, want both", organizationCanceled, attemptCanceled)
	}
	if len(source.orgs) != 0 {
		t.Fatal("deleted Organization retained its scoped watcher")
	}
}

func TestCachedPlanKeepsDeletingWatchUntilCleanedUpWithoutFetching(t *testing.T) {
	ctx := context.Background()
	queue := cleanupQueueForTest(t)
	plan := &Plan{
		Owner: "registration-uid", Org: "org-a", Provider: "installer", Namespace: "apps",
		Job: "install-job", AttemptID: "attempt-one", Cluster: "edge-cluster", EdgeName: "edge-one", Deleting: true,
	}
	workspace := cleanupWorkspaceForTest(t, "installer", "registration-uid", plan)
	workspaceInformer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &unstructured.Unstructured{}, 0, cache.Indexers{})
	if err := workspaceInformer.GetStore().Add(workspace); err != nil {
		t.Fatal(err)
	}
	orgDone := make(chan struct{})
	close(orgDone)
	watchDone := make(chan struct{})
	close(watchDone)
	attempt := &attemptCleanupWatch{job: plan.Job, attemptID: plan.AttemptID, owner: plan.Owner, cancel: func() {}, done: watchDone}
	organization := &organizationCleanupWatch{org: "org-a", done: orgDone, workspaces: workspaceInformer, attempts: map[string]*attemptCleanupWatch{"installer": attempt}}
	source := newCleanupWatchSource(&Installer{})
	source.dynamicClient = func(*rest.Config) (dynamic.Interface, error) {
		return nil, fmt.Errorf("unexpected API client creation")
	}
	source.kubeClient = func(*rest.Config) (kubernetes.Interface, error) {
		return nil, fmt.Errorf("unexpected Edge API request")
	}
	source.queue = queue
	source.orgs["org-a"] = organization

	if delay, err := source.refreshAttemptFromCache(ctx, "org-a", "installer"); err != nil || delay != 0 {
		t.Fatalf("deleting plan refresh = delay %s, error %v", delay, err)
	}
	if organization.attempts["installer"] != attempt {
		t.Fatal("deletion intent stopped the Edge watch before cleanup finished")
	}

	plan.CleanedUp = true
	rawPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	workspace.SetAnnotations(map[string]string{planAnnotation: string(rawPlan)})
	if err := workspaceInformer.GetStore().Update(workspace); err != nil {
		t.Fatal(err)
	}
	if delay, err := source.refreshAttemptFromCache(ctx, "org-a", "installer"); err != nil || delay != 0 {
		t.Fatalf("cleaned plan refresh = delay %s, error %v", delay, err)
	}
	if _, exists := organization.attempts["installer"]; exists {
		t.Fatal("durable CleanedUp marker retained the Edge watch")
	}
}

func TestAttemptWatchReplaysTerminalJobReplacesAttemptAndStopsWithLeader(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := cleanupQueueForTest(t)
	first := &Plan{Namespace: "apps", Job: "install-job-one", Owner: "registration-uid", AttemptID: "attempt-one", Cluster: "edge-cluster", EdgeName: "edge-one"}
	client := kubefake.NewSimpleClientset(completeInstallerJobForTest(first))
	installer := &Installer{admin: &rest.Config{Host: "https://kcp.invalid"}}
	source := newCleanupWatchSource(installer)
	if err := source.Start(ctx, queue); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- (&cleanupSourceDrain{source: source}).Start(ctx) }()
	orgCtx, cancelOrg := context.WithCancel(ctx)
	orgDone := make(chan struct{})
	close(orgDone) // This test isolates attempt watcher cancellation from the parent informer.
	org := &organizationCleanupWatch{org: "org-a", ctx: orgCtx, cancel: cancelOrg, done: orgDone, attempts: map[string]*attemptCleanupWatch{}}
	source.mu.Lock()
	source.orgs["org-a"] = org
	source.mu.Unlock()
	var gotConfig *rest.Config
	source.kubeClient = func(config *rest.Config) (kubernetes.Interface, error) {
		gotConfig = rest.CopyConfig(config)
		return client, nil
	}
	if err := source.ensureAttemptWatch(ctx, "org-a", "installer", first); err != nil {
		t.Fatal(err)
	}
	firstWatch := org.attempts["installer"]
	if gotConfig == nil || gotConfig.Timeout != 0 || !strings.Contains(gotConfig.Host, "/clusters/edge-cluster/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/edge-one/k8s") {
		t.Fatalf("Edge watch config = %#v, want pinned cluster/Edge path and no client timeout", gotConfig)
	}
	if got := waitForCleanupRequest(t, queue); got.Namespace != "org-a" || got.Name != "installer" {
		t.Fatalf("initial Job list enqueued key %s/%s, want org-a/installer", got.Namespace, got.Name)
	}
	assertAttemptWatchScopes(t, client, first)

	second := *first
	second.Job = "install-job-two"
	second.AttemptID = "attempt-two"
	if err := source.ensureAttemptWatch(ctx, "org-a", "installer", &second); err != nil {
		t.Fatal(err)
	}
	secondWatch := org.attempts["installer"]
	if firstWatch == secondWatch {
		t.Fatal("attempt change retained the previous Job/Pod watches")
	}
	select {
	case <-firstWatch.done:
	case <-time.After(5 * time.Second):
		t.Fatal("superseded attempt watches did not stop")
	}

	cancel()
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup source did not drain after leadership ended")
	}
	select {
	case <-secondWatch.done:
	default:
		t.Fatal("attempt watch remained active after the leader drain completed")
	}
	if len(source.orgs) != 0 {
		t.Fatal("cleanup source retained organization watcher after leadership ended")
	}
}

func TestAttemptInformerReconnectsAfterClosedWatch(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := cleanupQueueForTest(t)
	client := kubefake.NewSimpleClientset()
	var mu sync.Mutex
	var jobWatches []*watch.FakeWatcher
	client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) {
		watcher := watch.NewFake()
		mu.Lock()
		jobWatches = append(jobWatches, watcher)
		count := len(jobWatches)
		mu.Unlock()
		if count == 1 {
			go func() {
				time.Sleep(1100 * time.Millisecond)
				watcher.Stop()
			}()
		}
		return true, watcher, nil
	})
	client.PrependWatchReactor("pods", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})
	plan := &Plan{Namespace: "apps", Job: "install-job", Owner: "registration-uid", AttemptID: "attempt-one", Cluster: "edge-cluster", EdgeName: "edge-one"}
	source := newCleanupWatchSource(&Installer{admin: &rest.Config{Host: "https://kcp.invalid"}})
	if err := source.Start(ctx, queue); err != nil {
		t.Fatal(err)
	}
	orgCtx, cancelOrg := context.WithCancel(ctx)
	orgDone := make(chan struct{})
	close(orgDone)
	org := &organizationCleanupWatch{org: "org-a", ctx: orgCtx, cancel: cancelOrg, done: orgDone, attempts: map[string]*attemptCleanupWatch{}}
	source.mu.Lock()
	source.orgs["org-a"] = org
	source.mu.Unlock()
	source.kubeClient = func(*rest.Config) (kubernetes.Interface, error) { return client, nil }
	if err := source.ensureAttemptWatch(ctx, "org-a", "installer", plan); err != nil {
		t.Fatal(err)
	}
	secondWatcher := waitForJobWatchCount(t, &mu, &jobWatches, 2)
	secondWatcher.Modify(completeInstallerJobForTest(plan))
	if got := waitForCleanupRequest(t, queue); got.Namespace != "org-a" || got.Name != "installer" {
		t.Fatalf("reconnected Job watch enqueued key %s/%s, want org-a/installer", got.Namespace, got.Name)
	}
	source.stopAttempt("org-a", "installer")
	cancelOrg()
	source.stopAll()
}

func cleanupWorkspaceForTest(t *testing.T, name, uid string, plan *Plan) *unstructured.Unstructured {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("tenancy.kcp.io/v1alpha1")
	object.SetKind("Workspace")
	object.SetName(name)
	object.SetUID(types.UID(uid))
	if plan != nil {
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		object.SetAnnotations(map[string]string{planAnnotation: string(raw)})
	}
	return object
}

func cleanupQueueForTest(t *testing.T) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	t.Helper()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	t.Cleanup(queue.ShutDown)
	return queue
}

func waitForCleanupRequest(t *testing.T, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) reconcile.Request {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for queue.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if queue.Len() == 0 {
		t.Fatal("timed out waiting for cleanup request")
	}
	item, shutdown := queue.Get()
	if shutdown {
		t.Fatal("cleanup queue shut down before delivering an event")
	}
	queue.Done(item)
	queue.Forget(item)
	return item
}

func waitForJobWatchCount(t *testing.T, mu *sync.Mutex, watches *[]*watch.FakeWatcher, count int) *watch.FakeWatcher {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		if len(*watches) >= count {
			watcher := (*watches)[count-1]
			mu.Unlock()
			return watcher
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d Job watch attempts", count)
	return nil
}

func assertAttemptWatchScopes(t *testing.T, client *kubefake.Clientset, plan *Plan) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var actions []clienttesting.Action
	for time.Now().Before(deadline) {
		actions = client.Actions()
		found := map[string]bool{}
		for _, action := range actions {
			resource := action.GetResource().Resource
			if (resource != "jobs" && resource != "pods") || action.GetNamespace() != plan.Namespace {
				continue
			}
			var restrictions clienttesting.ListRestrictions
			switch action := action.(type) {
			case clienttesting.ListAction:
				restrictions = action.GetListRestrictions()
			case clienttesting.WatchAction:
				watchRestrictions := action.GetWatchRestrictions()
				restrictions = clienttesting.ListRestrictions{Labels: watchRestrictions.Labels, Fields: watchRestrictions.Fields}
			default:
				continue
			}
			wantLabels := labels.Set{ownerLabel: plan.Owner, attemptLabel: plan.Job}.AsSelector().String()
			wantFields := ""
			if resource == "jobs" {
				wantFields = "metadata.name=" + plan.Job
			}
			if restrictions.Labels.String() != wantLabels || restrictions.Fields.String() != wantFields {
				t.Fatalf("%s %s scope = labels %q fields %q, want labels %q fields %q", action.GetVerb(), resource, restrictions.Labels, restrictions.Fields, wantLabels, wantFields)
			}
			found[resource+"/"+action.GetVerb()] = true
		}
		if found["jobs/list"] && found["jobs/watch"] && found["pods/list"] && found["pods/watch"] {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe scoped Job and Pod list/watch calls: %#v", actions)
}

func completeInstallerJobForTest(plan *Plan) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        plan.Job,
			Namespace:   plan.Namespace,
			Labels:      map[string]string{ownerLabel: plan.Owner, attemptLabel: plan.Job},
			Annotations: map[string]string{attemptIDAnnotation: plan.AttemptID},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
}

type cleanupWorkspaceAccessForTest struct{}

func (cleanupWorkspaceAccessForTest) GetProviderEdgeRoute(context.Context, string, string) (*kcp.EdgeRoute, error) {
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "workspaces"}, "provider")
}
func (cleanupWorkspaceAccessForTest) GetChildWorkspaceClusterName(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("unexpected child workspace cluster lookup")
}
func (cleanupWorkspaceAccessForTest) ChildWorkspaceConfig(string, string) *rest.Config {
	return &rest.Config{Host: "https://kcp.invalid"}
}
