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
	_, err := ResolvePlan(providers.InstallInstructions{Warnings: []string{"missing chart"}})
	if err == nil {
		t.Fatal("incomplete recipe accepted")
	}
	_, err = ResolvePlan(providers.InstallInstructions{ChartVersion: "1.0.0", Values: []providers.ResolvedValue{{Name: "custom", Unresolved: true}}})
	if err == nil {
		t.Fatal("unresolved values accepted")
	}
	if _, err := ResolvePlan(providers.InstallInstructions{ChartRef: "oci://example.invalid/charts/provider", ChartVersion: ">=1.0.0"}); err == nil {
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
