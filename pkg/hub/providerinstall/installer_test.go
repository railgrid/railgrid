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
	"sync/atomic"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	kt "k8s.io/client-go/testing"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

type testWorkspaces struct{}

func (testWorkspaces) GetProviderEdgeRoute(context.Context, string, string) (*kcp.EdgeRoute, error) {
	return &kcp.EdgeRoute{WorkspaceUUID: "team", EdgeName: "devbox"}, nil
}
func (testWorkspaces) GetChildWorkspaceClusterName(context.Context, string, string) (string, error) {
	return "team-cluster", nil
}
func (testWorkspaces) ChildWorkspaceConfig(string, string) *rest.Config {
	return &rest.Config{Host: "https://kcp.invalid/clusters/team-cluster"}
}
func fixture(t *testing.T) (*Installer, *fake.Clientset, *fake.Clientset, *Plan) {
	t.Helper()
	edge, local := fake.NewClientset(), fake.NewClientset()
	ws := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "kuery", "uid": "provider-workspace-uid"}}}
	dy := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), ws)
	local.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "token" {
			return false, nil, nil
		}
		tr := a.(kt.CreateAction).GetObject().(*authnv1.TokenRequest)
		if *tr.Spec.ExpirationSeconds != 900 {
			t.Fatal("runner token must be short lived")
		}
		tr.Status.Token = "short-lived-runner-token"
		tr.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(15 * time.Minute))
		return true, tr, nil
	})
	local.PrependReactor("create", "subjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
		review := a.(kt.CreateAction).GetObject().(*authzv1.SubjectAccessReview)
		review.Status.Allowed = true
		return true, review, nil
	})
	i := New(&rest.Config{Host: "https://kcp.invalid/clusters/root"}, testWorkspaces{}, "https://hub.invalid", false)
	i.kubeClient = func(c *rest.Config) (kubernetes.Interface, error) {
		if strings.HasSuffix(c.Host, "/k8s") {
			return edge, nil
		}
		return local, nil
	}
	i.dynamicClient = func(*rest.Config) (dynamic.Interface, error) { return dy, nil }
	p := &Plan{Namespace: "railgrid-provider-kuery", Release: "kuery", Chart: "oci://ghcr.io/railgrid/charts/railgrid-kuery-provider", Version: "0.2.23", Values: map[string]any{"catalogEntry": map[string]any{"enabled": true}}}
	return i, edge, local, p
}
func jobCondition(t *testing.T, edge *fake.Clientset, s *Status, condition batchv1.JobConditionType) {
	t.Helper()
	job, err := edge.BatchV1().Jobs(s.Namespace).Get(context.Background(), s.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
	if _, err = edge.BatchV1().Jobs(s.Namespace).UpdateStatus(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
func TestDurableJobResumeRetryAndHealth(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	s, err := i.Start(ctx, "org", "kuery", p, []byte("provider-credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != "Queued" || s.Attempt != 1 {
		t.Fatalf("initial: %+v", s)
	}
	encoded, _ := json.Marshal(s)
	if strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "token") {
		t.Fatal("status exposed a credential")
	}
	job, _ := edge.BatchV1().Jobs(p.Namespace).Get(ctx, s.Job, metav1.GetOptions{})
	if *job.Spec.Template.Spec.AutomountServiceAccountToken || job.Spec.Template.Spec.ServiceAccountName != "" {
		t.Fatal("runner must not receive local cluster credentials")
	}
	if *job.Spec.ActiveDeadlineSeconds != 600 {
		t.Fatal("installation must be bounded")
	}
	runner, _ := edge.CoreV1().Secrets(p.Namespace).Get(ctx, s.Job+"-config", metav1.GetOptions{})
	if !strings.Contains(string(runner.Data["kubeconfig"]), "/clusters/team-cluster/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/devbox/k8s") {
		t.Fatal("runner credential escaped its Edge")
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil || plan.AttemptID == "" || job.Annotations[attemptIDAnnotation] != plan.AttemptID {
		t.Fatalf("attempt identity was not persisted on the Job: plan=%+v annotations=%v err=%v", plan, job.Annotations, err)
	}
	role, _ := local.RbacV1().ClusterRoles().Get(ctx, "railgrid-"+s.Job, metav1.GetOptions{})
	if len(role.Rules) != 1 || role.Rules[0].ResourceNames[0] != "devbox" || role.Annotations[attemptIDAnnotation] != plan.AttemptID {
		t.Fatal("runner role is not scoped to the selected Edge")
	}
	// A new server instance reads the same Job and sealed plan.
	resumed := *i
	again, err := resumed.Start(ctx, "org", "kuery", nil, []byte("provider-credential"), true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Job != s.Job {
		t.Fatal("retry duplicated a running installation")
	}
	jobCondition(t, edge, s, batchv1.JobFailed)
	failed, err := resumed.Status(ctx, "org", "kuery", false)
	if err != nil || !failed.CanRetry || failed.Phase != "Failed" {
		t.Fatalf("failed: %+v %v", failed, err)
	}
	next, err := resumed.Start(ctx, "org", "kuery", &Plan{Chart: "untrusted", Version: "other"}, []byte("provider-credential"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = edge.CoreV1().Secrets(p.Namespace).Get(ctx, s.Job+"-config", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("retry retained the prior execution credential")
	}
	if next.Attempt != 2 || next.Job == s.Job || next.Version != "0.2.23" {
		t.Fatalf("retry lost the pinned plan: %+v", next)
	}
	// A late cleanup for attempt 1 cannot revoke attempt 2's execution secret.
	resumed.Cleanup(ctx, "org", "kuery", s.Job)
	if _, err = edge.CoreV1().Secrets(p.Namespace).Get(ctx, next.Job+"-config", metav1.GetOptions{}); err != nil {
		t.Fatal("old cleanup revoked new attempt")
	}
	jobCondition(t, edge, next, batchv1.JobComplete)
	checking, err := resumed.Status(ctx, "org", "kuery", false)
	if err != nil || checking.Phase != "Checking" {
		t.Fatalf("Helm completion is not provider readiness: %+v %v", checking, err)
	}
	ready, err := resumed.Status(ctx, "org", "kuery", true)
	if err != nil || ready.Phase != "Ready" {
		t.Fatalf("ready: %+v %v", ready, err)
	}
	resumed.Cleanup(ctx, "org", "kuery", next.Job)
	if _, err = edge.CoreV1().Secrets(p.Namespace).Get(ctx, next.Job+"-config", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("runner execution secret was not removed")
	}
	if _, err = edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{}); err != nil {
		t.Fatal("cleanup removed the provider credential")
	}
}

func TestPlanOwnershipRequiresWorkspaceUID(t *testing.T) {
	i, _, _, _ := fixture(t)
	ws := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "kuery"}}}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), ws)
	i.dynamicClient = func(*rest.Config) (dynamic.Interface, error) { return dynamicClient, nil }
	if _, _, err := i.readPlan(context.Background(), "org", "kuery"); err == nil || !strings.Contains(err.Error(), "no UID") {
		t.Fatalf("installation plan accepted a workspace without durable ownership: %v", err)
	}
}

func TestFailedPreparationCanBeRetried(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	refuse := true
	edge.PrependReactor("create", "jobs", func(kt.Action) (bool, runtime.Object, error) {
		if refuse {
			return true, nil, fmt.Errorf("temporary connection failure")
		}
		return false, nil, nil
	})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil {
		t.Fatal("expected failure")
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, runnerSecret(plan), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("failed preparation retained its runner Secret: %v", err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+plan.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("failed preparation retained its runner ServiceAccount: %v", err)
	}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("failed preparation removed the provider runtime credential: %v", err)
	}
	s, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || !s.CanRetry || s.Phase != "Preparing" {
		t.Fatalf("partial preparation cannot resume: %+v %v", s, err)
	}
	refuse = false
	if _, err = i.Start(ctx, "org", "kuery", nil, []byte("credential"), true); err != nil {
		t.Fatal(err)
	}
	plan, _, err = i.readPlan(ctx, "org", "kuery")
	if err != nil || plan.Attempt != 2 {
		t.Fatalf("failed preparation was not fenced into a new attempt: %+v %v", plan, err)
	}
}

func TestRunnerTokenLifetimeResponseIsChecked(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	local.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "token" {
			return false, nil, nil
		}
		tr := a.(kt.CreateAction).GetObject().(*authnv1.TokenRequest)
		tr.Status.Token = "long-lived-runner-token"
		tr.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(24 * time.Hour))
		return true, tr, nil
	})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil || !strings.Contains(err.Error(), "bounded runner token lifetime") {
		t.Fatalf("overlong TokenRequest response accepted: %v", err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+plan.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rejected runner token left its ServiceAccount behind: %v", err)
	}
	if _, err := edge.CoreV1().Secrets(plan.Namespace).Get(ctx, runnerSecret(plan), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rejected runner token was persisted: %v", err)
	}
}

func TestCreateDoesNotAdoptSameNameJobFromAnotherAttempt(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	edge.PrependReactor("create", "jobs", func(a kt.Action) (bool, runtime.Object, error) {
		desired := a.(kt.CreateAction).GetObject().(*batchv1.Job)
		collision := desired.DeepCopy()
		collision.Namespace = p.Namespace
		collision.Annotations[attemptIDAnnotation] = "another-attempt"
		if err := edge.Tracker().Add(collision); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, desired.Name)
	})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil || !strings.Contains(err.Error(), "different ownership or configuration") {
		t.Fatalf("installer adopted a colliding Job: %v", err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := edge.CoreV1().Secrets(plan.Namespace).Get(ctx, runnerSecret(plan), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Job collision retained this attempt's runner Secret: %v", err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+plan.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Job collision retained this attempt's ServiceAccount: %v", err)
	}
}

func TestConcurrentStartUsesPreparationLease(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var tokenCalls atomic.Int32
	local.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "token" {
			return false, nil, nil
		}
		call := tokenCalls.Add(1)
		if call == 1 {
			close(entered)
			<-release
		}
		tr := a.(kt.CreateAction).GetObject().(*authnv1.TokenRequest)
		tr.Status.Token = fmt.Sprintf("preparation-token-%d", call)
		tr.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(15 * time.Minute))
		return true, tr, nil
	})
	result := make(chan error, 1)
	go func() {
		_, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
		result <- err
	}()
	var unblock sync.Once
	finishPreparation := func() { unblock.Do(func() { close(release) }) }
	defer finishPreparation()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initial Start did not reach token creation")
	}

	concurrent, err := i.Start(ctx, "org", "kuery", nil, []byte("credential"), true)
	if err != nil {
		t.Fatal(err)
	}
	if concurrent.Phase != "Preparing" || concurrent.CanRetry || concurrent.Attempt != 1 {
		t.Fatalf("concurrent request did not observe the active preparation lease: %+v", concurrent)
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("concurrent request created a second execution token: %d calls", tokenCalls.Load())
	}
	finishPreparation()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	jobs, err := edge.BatchV1().Jobs(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Name != concurrent.Job {
		t.Fatalf("concurrent starts did not converge on one Job: %+v %v", jobs, err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil || preparationLeaseActive(plan) {
		t.Fatalf("successful preparation retained its lease: %+v %v", plan, err)
	}
}

func TestPrepareDeleteDuringPreparationReleasesLeaseAndCancelsStart(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var unblock sync.Once
	finishPreparation := func() { unblock.Do(func() { close(release) }) }
	defer finishPreparation()
	local.PrependReactor("create", "serviceaccounts", func(action kt.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}
		close(entered)
		<-release
		request := action.(kt.CreateAction).GetObject().(*authnv1.TokenRequest)
		request.Status.Token = "short-lived-runner-token"
		request.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(15 * time.Minute))
		return true, request, nil
	})
	startResult := make(chan error, 1)
	go func() {
		_, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
		startResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not reach runner token creation")
	}
	deleteResult := make(chan error, 1)
	go func() { deleteResult <- i.PrepareDelete(ctx, "org", "kuery") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		plan, _, err := i.readPlan(ctx, "org", "kuery")
		if err != nil {
			t.Fatal(err)
		}
		if plan.Deleting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PrepareDelete did not publish deletion intent")
		}
		time.Sleep(10 * time.Millisecond)
	}
	finishPreparation()
	if err := <-startResult; err == nil {
		t.Fatal("Start succeeded after PrepareDelete fenced its attempt")
	}
	if err := <-deleteResult; err != nil {
		t.Fatalf("PrepareDelete remained blocked on the aborted preparation lease: %v", err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil || !plan.Deleting || !plan.CleanedUp || plan.PreparationID != "" {
		t.Fatalf("deletion did not complete after Start released its lease: plan=%+v err=%v", plan, err)
	}
	jobs, err := edge.BatchV1().Jobs(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("superseded Start created an installer Job: jobs=%v err=%v", jobs, err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+plan.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("superseded Start left its runner ServiceAccount: %v", err)
	}
}

func TestMissingJobWithActivePodCannotRetry(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.BatchV1().Jobs(p.Namespace).Delete(ctx, started.Job, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := edge.CoreV1().Pods(p.Namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      started.Job + "-orphan",
		Namespace: p.Namespace,
		Labels:    map[string]string{"batch.kubernetes.io/job-name": started.Job},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || status.Phase != "Installing" || status.CanRetry {
		t.Fatalf("orphan installer pod was not treated as active: %+v %v", status, err)
	}
	retried, err := i.Start(ctx, "org", "kuery", nil, []byte("credential"), true)
	if err != nil || retried.Phase != "Installing" || retried.Job != started.Job {
		t.Fatalf("retry overlapped an orphan installer pod: %+v %v", retried, err)
	}
	jobs, err := edge.BatchV1().Jobs(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("retry created a second Job while the old Pod was active: %+v %v", jobs, err)
	}
}

func TestFailedJobWithActivePodCannotRetryOrCleanup(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := edge.CoreV1().Pods(p.Namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      started.Job + "-still-running",
		Namespace: p.Namespace,
		Labels:    map[string]string{ownerLabel: "provider-workspace-uid", attemptLabel: started.Job},
	}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	jobCondition(t, edge, started, batchv1.JobFailed)
	status, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || status.Phase != "Installing" || status.CanRetry {
		t.Fatalf("failed Job with active Pod was marked retryable: %+v %v", status, err)
	}
	i.Cleanup(ctx, "org", "kuery", started.Job)
	if _, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, runnerSecret(&Plan{Job: started.Job}), metav1.GetOptions{}); err != nil {
		t.Fatalf("cleanup revoked credentials while the installer Pod was active: %v", err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+started.Job, metav1.GetOptions{}); err != nil {
		t.Fatalf("cleanup removed the runner identity while the installer Pod was active: %v", err)
	}
	retried, err := i.Start(ctx, "org", "kuery", nil, []byte("credential"), true)
	if err != nil || retried.Job != started.Job || retried.Attempt != started.Attempt {
		t.Fatalf("retry overlapped the still-running Job Pod: %+v %v", retried, err)
	}
}

func TestCleanupFailurePreventsRetry(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	jobCondition(t, edge, started, batchv1.JobFailed)
	failDelete := true
	local.PrependReactor("delete", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if failDelete && a.(kt.DeleteAction).GetName() == "railgrid-"+started.Job {
			return true, nil, fmt.Errorf("temporary revoke failure")
		}
		return false, nil, nil
	})
	if _, err := i.Start(ctx, "org", "kuery", nil, []byte("credential"), true); err == nil {
		t.Fatal("retry continued despite failure to revoke the old runner identity")
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil || plan.Attempt != 1 {
		t.Fatalf("failed cleanup advanced the durable attempt: %+v %v", plan, err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+started.Job, metav1.GetOptions{}); err != nil {
		t.Fatalf("failed cleanup unexpectedly removed the runner ServiceAccount: %v", err)
	}
	failDelete = false
	retried, err := i.Start(ctx, "org", "kuery", nil, []byte("credential"), true)
	if err != nil || retried.Attempt != 2 {
		t.Fatalf("retry did not proceed after cleanup recovered: %+v %v", retried, err)
	}
}

func TestPreexistingBroadRunnerBindingIsRejected(t *testing.T) {
	ctx := context.Background()
	i, _, local, p := fixture(t)
	local.PrependReactor("create", "clusterrolebindings", func(a kt.Action) (bool, runtime.Object, error) {
		desired := a.(kt.CreateAction).GetObject().(*rbacv1.ClusterRoleBinding)
		preexisting := desired.DeepCopy()
		preexisting.Annotations = nil
		preexisting.RoleRef.Name = "cluster-admin"
		if err := local.Tracker().Add(preexisting); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterrolebindings"}, desired.Name)
	})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil || !strings.Contains(err.Error(), "unexpected subjects") {
		t.Fatalf("preexisting broad binding was trusted: %v", err)
	}
	for _, action := range local.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "serviceaccounts" && action.GetSubresource() == "token" {
			t.Fatal("runner token was minted despite an untrusted ClusterRoleBinding")
		}
	}
}

func TestPreexistingAggregatedRunnerRoleIsRejected(t *testing.T) {
	ctx := context.Background()
	i, _, local, p := fixture(t)
	local.PrependReactor("create", "clusterroles", func(a kt.Action) (bool, runtime.Object, error) {
		desired := a.(kt.CreateAction).GetObject().(*rbacv1.ClusterRole)
		preexisting := desired.DeepCopy()
		preexisting.AggregationRule = &rbacv1.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"installer-escalation": "true"}}}}
		if err := local.Tracker().Add(preexisting); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterroles"}, desired.Name)
	})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil || !strings.Contains(err.Error(), "unexpected permissions") {
		t.Fatalf("preexisting aggregated role was trusted: %v", err)
	}
	for _, action := range local.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "serviceaccounts" && action.GetSubresource() == "token" {
			t.Fatal("runner token was minted despite an aggregated ClusterRole")
		}
	}
}

func TestNamespaceCollisionNeverOverwritesCredentials(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	_, _ = edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace}}, metav1.CreateOptions{})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil {
		t.Fatal("unowned namespace accepted")
	}
	secrets, _ := edge.CoreV1().Secrets(p.Namespace).List(ctx, metav1.ListOptions{})
	if len(secrets.Items) != 0 {
		t.Fatal("wrote a credential to an unowned namespace")
	}
}

func TestForeignProviderKubeconfigSecretIsNeverOverwritten(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	labels := map[string]string{ownerLabel: "provider-workspace-uid", orgLabel: "org", providerLabel: "kuery", edgeWorkspaceLabel: "team", edgeNameLabel: "devbox"}
	if _, err := edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: labels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: providers.KubeconfigSecretName, Namespace: p.Namespace, Labels: map[string]string{ownerLabel: "another-registration"}}, Data: map[string][]byte{"kubeconfig": []byte("foreign-credential")}}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil {
		t.Fatal("installer overwrote a foreign provider kubeconfig Secret")
	}
	stored, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if err != nil || stored.Labels[ownerLabel] != "another-registration" || string(stored.Data["kubeconfig"]) != "foreign-credential" {
		t.Fatalf("foreign provider kubeconfig Secret changed: secret=%+v err=%v", stored, err)
	}
}

func TestPrepareDeleteAndReregisterRetainedNamespace(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("old-provider-kubeconfig"), false)
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      started.Job + "-running",
		Namespace: p.Namespace,
		Labels:    map[string]string{ownerLabel: plan.Owner, attemptLabel: plan.Job},
		Annotations: map[string]string{
			attemptIDAnnotation: plan.AttemptID,
		},
	}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if _, err := edge.CoreV1().Pods(p.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := i.PrepareDelete(ctx, "org", "kuery"); err != nil {
		t.Fatalf("preparing provider deletion: %v", err)
	}
	if _, err := edge.BatchV1().Jobs(started.Namespace).Get(ctx, started.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("installer Job survived deletion: %v", err)
	}
	if _, err := edge.CoreV1().Pods(p.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("owned active installer Pod survived deletion: %v", err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+started.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("runner ServiceAccount survived deletion: %v", err)
	}
	if _, err := local.RbacV1().ClusterRoles().Get(ctx, "railgrid-"+started.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("runner ClusterRole survived deletion: %v", err)
	}
	if _, err := local.RbacV1().ClusterRoleBindings().Get(ctx, "railgrid-"+started.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("runner ClusterRoleBinding survived deletion: %v", err)
	}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, runnerSecret(plan), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("runner Secret survived deletion: %v", err)
	}
	providerSecret, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if err != nil || string(providerSecret.Data["kubeconfig"]) != "old-provider-kubeconfig" {
		t.Fatalf("provider kubeconfig should remain with the retained release: secret=%+v err=%v", providerSecret, err)
	}
	ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{orgLabel: "org", providerLabel: "kuery", edgeWorkspaceLabel: "team", edgeNameLabel: "devbox", ownerLabel: "provider-workspace-uid"} {
		if ns.Labels[key] != want {
			t.Fatalf("deletion did not stamp namespace identity %s=%q: labels=%v", key, want, ns.Labels)
		}
	}

	parent, err := i.parent("org")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Delete(ctx, "kuery", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	newWorkspace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": "kuery", "uid": "provider-workspace-new-uid"},
	}}
	if _, err := parent.Create(ctx, newWorkspace, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	newStarted, err := i.Start(ctx, "org", "kuery", p, []byte("new-provider-kubeconfig"), false)
	if err != nil {
		t.Fatalf("re-registering on the retained namespace failed: %v", err)
	}
	ns, err = edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels[ownerLabel] != "provider-workspace-new-uid" || ns.Labels[orgLabel] != "org" || ns.Labels[providerLabel] != "kuery" {
		t.Fatalf("namespace was not safely adopted by the new registration: %v", ns.Labels)
	}
	providerSecret, err = edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if err != nil || string(providerSecret.Data["kubeconfig"]) != "new-provider-kubeconfig" || providerSecret.Labels[ownerLabel] != "provider-workspace-new-uid" {
		t.Fatalf("provider kubeconfig was not rotated for the new registration: secret=%+v err=%v", providerSecret, err)
	}
	runnerSecretObject, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, runnerSecret(&Plan{Job: newStarted.Job}), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(runnerSecretObject.Data["values.json"], &values); err != nil {
		t.Fatal(err)
	}
	podAnnotations, ok := values["podAnnotations"].(map[string]any)
	if !ok || podAnnotations[registrationRolloutAnnotation] != "provider-workspace-new-uid" {
		t.Fatalf("registration UID did not force a provider workload rollout: %v", values["podAnnotations"])
	}
}

func TestNamespaceAdoptionFailsClosedForLegacyAndForeignIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
	}{
		{name: "legacy owner without stable identity", labels: map[string]string{ownerLabel: "old-registration"}},
		{name: "different organization", labels: map[string]string{ownerLabel: "old-registration", orgLabel: "foreign-org", providerLabel: "kuery", edgeWorkspaceLabel: "team", edgeNameLabel: "devbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			i, edge, _, p := fixture(t)
			parent, err := i.parent("org")
			if err != nil {
				t.Fatal(err)
			}
			if err := parent.Delete(ctx, "kuery", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			ws := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
				"metadata": map[string]any{"name": "kuery", "uid": "new-registration"},
			}}
			if _, err := parent.Create(ctx, ws, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: tc.labels}}, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := i.Start(ctx, "org", "kuery", p, []byte("new-kubeconfig"), false); err == nil {
				t.Fatal("installer adopted a legacy or foreign namespace")
			}
			ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
			if err != nil || ns.Labels[ownerLabel] != tc.labels[ownerLabel] {
				t.Fatalf("rejected namespace ownership was changed: labels=%v err=%v", ns.GetLabels(), err)
			}
			if _, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("installer wrote credentials into a rejected namespace: %v", err)
			}
		})
	}
}

func TestPrepareDeleteAfterRejectedNamespaceOnlyRevokesOwnedLocalCredentials(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	foreignLabels := map[string]string{"foreign": "keep"}
	if _, err := edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: foreignLabels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	foreignSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      providers.KubeconfigSecretName,
		Namespace: p.Namespace,
		Labels:    map[string]string{ownerLabel: "foreign-registration"},
	}, Data: map[string][]byte{"kubeconfig": []byte("foreign-kubeconfig")}}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Create(ctx, foreignSecret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil {
		t.Fatal("Start adopted an unlabelled namespace")
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NamespaceAdmissionKnown || plan.NamespaceAdmitted {
		t.Fatalf("failed namespace gate was not persisted: %+v", plan)
	}

	// These exact attempt-owned hub credentials are safe to revoke even though
	// the Edge namespace itself was rejected. The unrelated local identity and
	// all objects in the foreign namespace must remain untouched.
	identity := "railgrid-" + plan.Job
	meta := attemptObjectMeta(identity, plan)
	if _, err := local.CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{ObjectMeta: meta}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{ObjectMeta: meta}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: meta}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "unrelated-runner", Labels: map[string]string{ownerLabel: "other-registration"}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	foreignJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      plan.Job,
		Namespace: p.Namespace,
		Labels:    map[string]string{ownerLabel: "foreign-registration", attemptLabel: plan.Job},
	}, Status: batchv1.JobStatus{Active: 1}}
	if _, err := edge.BatchV1().Jobs(p.Namespace).Create(ctx, foreignJob, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := i.PrepareDelete(ctx, "org", "kuery"); err != nil {
		t.Fatalf("deleting a rejected registration should not need ownership of the foreign namespace: %v", err)
	}
	storedNS, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil || storedNS.Labels["foreign"] != "keep" || storedNS.Labels[ownerLabel] != "" || len(storedNS.Labels) != len(foreignLabels) {
		t.Fatalf("PrepareDelete changed the rejected namespace: labels=%v err=%v", storedNS.GetLabels(), err)
	}
	storedSecret, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if err != nil || storedSecret.Labels[ownerLabel] != "foreign-registration" || string(storedSecret.Data["kubeconfig"]) != "foreign-kubeconfig" {
		t.Fatalf("PrepareDelete changed the foreign provider credential: secret=%+v err=%v", storedSecret, err)
	}
	if _, err := edge.BatchV1().Jobs(p.Namespace).Get(ctx, plan.Job, metav1.GetOptions{}); err != nil {
		t.Fatalf("PrepareDelete touched a foreign installer Job: %v", err)
	}
	for _, resource := range []string{"serviceaccount", "clusterrole", "clusterrolebinding"} {
		t.Run(resource, func(t *testing.T) {
			var err error
			switch resource {
			case "serviceaccount":
				_, err = local.CoreV1().ServiceAccounts("default").Get(ctx, identity, metav1.GetOptions{})
			case "clusterrole":
				_, err = local.RbacV1().ClusterRoles().Get(ctx, identity, metav1.GetOptions{})
			case "clusterrolebinding":
				_, err = local.RbacV1().ClusterRoleBindings().Get(ctx, identity, metav1.GetOptions{})
			}
			if !apierrors.IsNotFound(err) {
				t.Fatalf("registration-owned %s was not revoked: %v", resource, err)
			}
		})
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "unrelated-runner", metav1.GetOptions{}); err != nil {
		t.Fatalf("PrepareDelete removed an unrelated local ServiceAccount: %v", err)
	}
	plan, _, err = i.readPlan(ctx, "org", "kuery")
	if err != nil || !plan.Deleting || !plan.CleanedUp {
		t.Fatalf("failed registration did not reach a durable cleaned state: plan=%+v err=%v", plan, err)
	}
}

func TestNamespaceAdoptionWaitsForPriorAttemptToStop(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	parent, err := i.parent("org")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Delete(ctx, "kuery", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
		"metadata": map[string]any{"name": "kuery", "uid": "new-registration"},
	}}
	if _, err := parent.Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{ownerLabel: "old-registration", orgLabel: "org", providerLabel: "kuery", edgeWorkspaceLabel: "team", edgeNameLabel: "devbox"}
	if _, err := edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: labels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	activeJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "old-installer",
		Namespace: p.Namespace,
		Labels:    map[string]string{ownerLabel: "old-registration", attemptLabel: "old-installer"},
		Annotations: map[string]string{
			attemptIDAnnotation: "old-attempt-id",
		},
	}, Status: batchv1.JobStatus{Active: 1}}
	if _, err := edge.BatchV1().Jobs(p.Namespace).Create(ctx, activeJob, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("new-kubeconfig"), false); err == nil {
		t.Fatal("installer adopted a namespace with a live prior attempt")
	}
	if _, err := edge.BatchV1().Jobs(p.Namespace).Get(ctx, activeJob.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("failed adoption changed the prior installer Job: %v", err)
	}
	ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil || ns.Labels[ownerLabel] != "old-registration" {
		t.Fatalf("failed adoption changed namespace ownership: labels=%v err=%v", ns.GetLabels(), err)
	}
}

func TestNamespaceAdoptionRetryAcceptsCurrentPriorSecretOwner(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	labels := map[string]string{
		ownerLabel:         "registration-b",
		previousOwnerLabel: "registration-a",
		orgLabel:           "org",
		providerLabel:      "kuery",
		edgeWorkspaceLabel: "team",
		edgeNameLabel:      "devbox",
	}
	if _, err := edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: labels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      providers.KubeconfigSecretName,
		Namespace: p.Namespace,
		Labels:    map[string]string{ownerLabel: "registration-b"},
	}, Data: map[string][]byte{"kubeconfig": []byte("registration-b-kubeconfig")}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	failFirstSecretUpdate := true
	edge.PrependReactor("update", "secrets", func(kt.Action) (bool, runtime.Object, error) {
		if failFirstSecretUpdate {
			failFirstSecretUpdate = false
			return true, nil, fmt.Errorf("temporary Secret update failure")
		}
		return false, nil, nil
	})
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("registration-c-kubeconfig"), false); err == nil {
		t.Fatal("first credential rotation should report the injected update failure")
	}
	ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels[ownerLabel] != "provider-workspace-uid" || ns.Labels[previousOwnerLabel] != "registration-b" {
		t.Fatalf("namespace did not retain the actual prior credential owner: %v", ns.Labels)
	}
	secret, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if err != nil || secret.Labels[ownerLabel] != "registration-b" {
		t.Fatalf("failed credential rotation changed the prior Secret owner: secret=%+v err=%v", secret, err)
	}
	if _, err := i.Start(ctx, "org", "kuery", nil, []byte("registration-c-kubeconfig"), false); err != nil {
		t.Fatalf("retry after the interrupted credential rotation failed: %v", err)
	}
	secret, err = edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if err != nil || secret.Labels[ownerLabel] != "provider-workspace-uid" || string(secret.Data["kubeconfig"]) != "registration-c-kubeconfig" {
		t.Fatalf("retry did not finish the credential rotation: secret=%+v err=%v", secret, err)
	}
	ns, err = edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if err != nil || ns.Labels[previousOwnerLabel] != "" {
		t.Fatalf("completed rotation retained stale namespace ownership: labels=%v err=%v", ns.GetLabels(), err)
	}
}

func TestPrepareDeleteFencesStartWithoutAPlan(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	if err := i.PrepareDelete(ctx, "org", "kuery"); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false); err == nil {
		t.Fatal("Start ignored durable deletion intent when no plan existed")
	}
	if _, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Start prepared resources after deletion intent: %v", err)
	}
}

func TestPrepareDeleteFailureRetainsWorkspaceAndAttemptOwnership(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ambiguous  bool
		failRunner bool
	}{
		{name: "credential revocation failure", failRunner: true},
		{name: "ambiguous pod is preserved", ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			i, edge, local, p := fixture(t)
			started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
			if err != nil {
				t.Fatal(err)
			}
			plan, _, err := i.readPlan(ctx, "org", "kuery")
			if err != nil {
				t.Fatal(err)
			}
			var ambiguousPod *corev1.Pod
			if tc.ambiguous {
				ambiguousPod = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Name:      started.Job + "-ambiguous",
					Namespace: p.Namespace,
					Labels:    map[string]string{"batch.kubernetes.io/job-name": started.Job},
				}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
				if _, err := edge.CoreV1().Pods(p.Namespace).Create(ctx, ambiguousPod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failRunner {
				local.PrependReactor("delete", "serviceaccounts", func(kt.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("revocation unavailable")
				})
			}
			if err := i.PrepareDelete(ctx, "org", "kuery"); err == nil {
				t.Fatal("provider deletion proceeded despite incomplete installer cleanup")
			}
			parent, err := i.parent("org")
			if err != nil {
				t.Fatal(err)
			}
			ws, err := parent.Get(ctx, "kuery", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("provider workspace was lost after partial cleanup: %v", err)
			}
			if ws.GetAnnotations()[deletingAnnotation] != plan.Owner {
				t.Fatal("partial delete did not retain its durable Start fence")
			}
			stored := &Plan{}
			if err := json.Unmarshal([]byte(ws.GetAnnotations()[planAnnotation]), stored); err != nil || !stored.Deleting || stored.CleanedUp || stored.Owner != plan.Owner {
				t.Fatalf("partial delete lost its owner-fenced plan: plan=%+v err=%v", stored, err)
			}
			if _, err := i.Start(ctx, "org", "kuery", nil, []byte("credential"), true); err == nil {
				t.Fatal("Start proceeded after deletion intent was recorded")
			}
			if tc.ambiguous {
				if _, err := edge.CoreV1().Pods(p.Namespace).Get(ctx, ambiguousPod.Name, metav1.GetOptions{}); err != nil {
					t.Fatalf("ambiguous Pod was deleted without exact ownership: %v", err)
				}
			}
		})
	}
}

func TestTerminalResultSurvivesJobTTLAndCleanupIsIdempotent(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := edge.BatchV1().Jobs(started.Namespace).Get(ctx, started.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 24*60*60 || !containsString(job.Finalizers, jobResultFinalizer) {
		t.Fatalf("installer Job lacks TTL/finalizer protection: ttl=%v finalizers=%v", job.Spec.TTLSecondsAfterFinished, job.Finalizers)
	}
	if _, _, err := i.readPlan(ctx, "org", "kuery"); err != nil {
		t.Fatal(err)
	}
	workspace, err := i.parent("org")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Get(ctx, "kuery", metav1.GetOptions{})
	if err != nil || !containsString(ws.GetFinalizers(), sealedWorkspaceFinalizer) {
		t.Fatalf("sealed provider workspace lacks cleanup finalizer: finalizers=%v err=%v", ws.GetFinalizers(), err)
	}
	jobCondition(t, edge, started, batchv1.JobComplete)
	if err := i.ReconcileCleanup(ctx, "org", "kuery"); err != nil {
		t.Fatal(err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil || plan.JobResult != string(batchv1.JobComplete) || plan.FinishedAt.IsZero() || !plan.CleanedUp || plan.JobUID != string(job.UID) {
		t.Fatalf("terminal result or cleanup marker was not persisted: plan=%+v err=%v", plan, err)
	}
	finishedJob, err := edge.BatchV1().Jobs(started.Namespace).Get(ctx, started.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if containsString(finishedJob.Finalizers, jobResultFinalizer) {
		t.Fatal("result finalizer remained after durable cleanup")
	}
	if err := edge.BatchV1().Jobs(started.Namespace).Delete(ctx, started.Job, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	jobGetsBefore := countActions(edge.Actions(), "get", "jobs")
	status, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || status.Phase != "Checking" {
		t.Fatalf("Job TTL changed the checking status: status=%+v err=%v", status, err)
	}
	ready, err := i.Status(ctx, "org", "kuery", true)
	if err != nil || ready.Phase != "Ready" {
		t.Fatalf("Job TTL changed readiness reporting: status=%+v err=%v", ready, err)
	}
	plan.FinishedAt = time.Now().Add(-6 * time.Minute)
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	ws, err = workspace.Get(ctx, "kuery", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	annotations := ws.GetAnnotations()
	annotations[planAnnotation] = string(encodedPlan)
	ws.SetAnnotations(annotations)
	if _, err := workspace.Update(ctx, ws, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	stale, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || stale.Phase != "Failed" || !stale.CanRetry {
		t.Fatalf("lost readiness was frozen as Ready after the checking timeout: status=%+v err=%v", stale, err)
	}
	stillReady, err := i.Status(ctx, "org", "kuery", true)
	if err != nil || stillReady.Phase != "Ready" {
		t.Fatalf("current provider readiness was not reflected after timeout: status=%+v err=%v", stillReady, err)
	}
	if err := i.ReconcileCleanup(ctx, "org", "kuery"); err != nil {
		t.Fatal(err)
	}
	if countActions(edge.Actions(), "get", "jobs") != jobGetsBefore {
		t.Fatal("read-only Status or repeated cleanup GET the expired installer Job")
	}
}

func TestExpiredPreparationWithMissingJobIsReconciledAsFailure(t *testing.T) {
	ctx := context.Background()
	i, edge, local, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.BatchV1().Jobs(started.Namespace).Delete(ctx, started.Job, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := i.ReconcileCleanup(ctx, "org", "kuery"); err != nil {
		t.Fatal(err)
	}
	plan, _, err := i.readPlan(ctx, "org", "kuery")
	if err != nil || plan.JobResult != string(batchv1.JobFailed) || plan.FinishedAt.IsZero() || !plan.CleanedUp {
		t.Fatalf("expired missing Job did not become a durable failed result: plan=%+v err=%v", plan, err)
	}
	if _, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, runnerSecret(plan), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("orphaned runner Secret was not cleaned: %v", err)
	}
	if _, err := local.CoreV1().ServiceAccounts("default").Get(ctx, "railgrid-"+started.Job, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("orphaned runner ServiceAccount was not cleaned: %v", err)
	}
	status, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || status.Phase != "Failed" || !status.CanRetry {
		t.Fatalf("missing Job failure did not survive through Status: status=%+v err=%v", status, err)
	}
}

func countActions(actions []kt.Action, verb, resource string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestInstallRequiresAllEdgeAPIVerbs(t *testing.T) {
	i, _, local, _ := fixture(t)
	local.PrependReactor("create", "subjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
		r := a.(kt.CreateAction).GetObject().(*authzv1.SubjectAccessReview)
		r.Status.Allowed = r.Spec.ResourceAttributes.Verb != "create"
		return true, r, nil
	})
	if err := i.Authorize(context.Background(), "org", "team", "devbox", "alice"); !apierrors.IsForbidden(err) {
		t.Fatalf("read-only access installed a provider: %v", err)
	}
}

func TestInstallAuthorizationFailsClosedOnSARDenialAndEvaluationError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status authzv1.SubjectAccessReviewStatus
	}{
		{name: "explicit denial", status: authzv1.SubjectAccessReviewStatus{Allowed: true, Denied: true}},
		{name: "evaluation error", status: authzv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "authorizer unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, _, local, _ := fixture(t)
			local.PrependReactor("create", "subjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
				review := a.(kt.CreateAction).GetObject().(*authzv1.SubjectAccessReview)
				review.Status = tc.status
				return true, review, nil
			})
			if err := i.Authorize(context.Background(), "org", "team", "devbox", "alice"); !apierrors.IsForbidden(err) {
				t.Fatalf("SAR with %s authorized installation: %v", tc.name, err)
			}
		})
	}
}
func TestResolvePlanRefusesUnresolvedValues(t *testing.T) {
	_, err := ResolvePlan(context.Background(), providers.InstallInstructions{Warnings: []string{"missing chart"}})
	if err == nil {
		t.Fatal("incomplete recipe accepted")
	}
	_, err = ResolvePlan(context.Background(), providers.InstallInstructions{ChartVersion: "1.0.0", Values: []providers.ResolvedValue{{Name: "custom", Unresolved: true}}})
	if err == nil {
		t.Fatal("unresolved values accepted")
	}
	if _, err := ResolvePlan(context.Background(), providers.InstallInstructions{ChartRef: "oci://example.invalid/charts/provider", ChartVersion: ">=1.0.0"}); err == nil {
		t.Fatal("a version range was accepted for a retry-pinned installation")
	}
}

func TestCompletedJobWithoutTransitionTimeDoesNotFailImmediately(t *testing.T) {
	ctx := context.Background()
	i, edge, _, p := fixture(t)
	started, err := i.Start(ctx, "org", "kuery", p, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := edge.BatchV1().Jobs(started.Namespace).Get(ctx, started.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if _, err := edge.BatchV1().Jobs(started.Namespace).UpdateStatus(ctx, job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err := i.Status(ctx, "org", "kuery", false)
	if err != nil || status.Phase != "Checking" || status.CanRetry {
		t.Fatalf("missing Job transition timestamp caused an early failure: %+v %v", status, err)
	}
}

func TestRetryAcceptsPlanStoredBeforePreparationLeaseWasAdded(t *testing.T) {
	ctx := context.Background()
	installer, edge, _, plan := fixture(t)
	first, err := installer.Start(ctx, "org", "kuery", plan, []byte("credential"), false)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := installer.parent("org")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := parent.Get(ctx, "kuery", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	annotations := ws.GetAnnotations()
	annotations[planAnnotation] = strings.ReplaceAll(annotations[planAnnotation], `,"preparationExpiresAt":"0001-01-01T00:00:00Z"`, "")
	ws.SetAnnotations(annotations)
	if _, err = parent.Update(ctx, ws, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	jobCondition(t, edge, first, batchv1.JobFailed)
	retried, err := installer.Start(ctx, "org", "kuery", nil, []byte("credential"), true)
	if err != nil {
		t.Fatalf("legacy plan could not retry: %v", err)
	}
	if retried.Attempt != 2 {
		t.Fatalf("unexpected attempt: %+v", retried)
	}
}
