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
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

const registrationRolloutAnnotation = "railgrid.ai/registration-uid"

const (
	deletePreparationWait = preparationTimeout + preparationCleanupTimeout + 5*time.Second
	deletePollInterval    = 100 * time.Millisecond
)

func validatePlanKey(p *Plan, org, provider string) error {
	if p == nil {
		return nil
	}
	if p.Org != "" && p.Org != org {
		return fmt.Errorf("installation belongs to a different organization")
	}
	if p.Provider != "" && p.Provider != provider {
		return fmt.Errorf("installation belongs to a different provider")
	}
	return nil
}

func (i *Installer) edgeClientsForPlan(ctx context.Context, org, name string, p *Plan) (kubernetes.Interface, kubernetes.Interface, *kcp.EdgeRoute, error) {
	if p == nil || p.EdgeWorkspaceUUID == "" || p.EdgeName == "" {
		return i.edgeClients(ctx, org, name)
	}
	cluster := p.Cluster
	if cluster == "" {
		var err error
		cluster, err = i.workspaces.GetChildWorkspaceClusterName(ctx, org, p.EdgeWorkspaceUUID)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	route := &kcp.EdgeRoute{WorkspaceUUID: p.EdgeWorkspaceUUID, EdgeName: p.EdgeName, Cluster: cluster}
	return i.edgeClientsForRoute(org, route)
}

func stampRegistrationRollout(p *Plan, registrationUID string) error {
	if p.Values == nil {
		p.Values = map[string]any{}
	}
	var annotations map[string]any
	switch raw := p.Values["podAnnotations"].(type) {
	case map[string]any:
		annotations = raw
	case map[string]string:
		annotations = make(map[string]any, len(raw)+1)
		for key, value := range raw {
			annotations[key] = value
		}
		p.Values["podAnnotations"] = annotations
	case nil:
		annotations = map[string]any{}
		p.Values["podAnnotations"] = annotations
	default:
		return fmt.Errorf("provider chart podAnnotations must be an object")
	}
	annotations[registrationRolloutAnnotation] = registrationUID
	return nil
}

func stableNamespaceIdentity(p *Plan) map[string]string {
	return map[string]string{
		orgLabel:           p.Org,
		providerLabel:      p.Provider,
		edgeWorkspaceLabel: p.EdgeWorkspaceUUID,
		edgeNameLabel:      p.EdgeName,
	}
}

// ensureInstallationNamespace permits adoption only for the same stable
// organization/provider/Edge identity. Owner UID alone is sufficient to stamp
// labels for the still-live registration; a new UID needs all stable labels.
func (i *Installer) ensureInstallationNamespace(ctx context.Context, edge kubernetes.Interface, p *Plan) ([]string, string, error) {
	identity := stableNamespaceIdentity(p)
	var previousOwners []string
	var namespaceUID string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			labels := map[string]string{ownerLabel: p.Owner}
			for key, value := range identity {
				labels[key] = value
			}
			created, createErr := edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: labels}}, metav1.CreateOptions{})
			err = createErr
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, p.Namespace, fmt.Errorf("namespace was concurrently created"))
			}
			if err == nil {
				namespaceUID = string(created.UID)
			}
			return err
		}
		if err != nil {
			return fmt.Errorf("preparing installation namespace: %w", err)
		}
		labels := ns.GetLabels()
		oldOwner := labels[ownerLabel]
		if oldOwner == "" {
			return fmt.Errorf("namespace %s is unlabelled and cannot be adopted; use manual installation or choose a clean namespace", p.Namespace)
		}
		if oldOwner != p.Owner {
			for key, value := range identity {
				if value == "" || labels[key] != value {
					return fmt.Errorf("namespace %s belongs to a different or legacy installation; use manual installation or choose a clean namespace", p.Namespace)
				}
			}
			if err := ensureNoActiveOldAttempt(ctx, edge, p.Namespace, oldOwner); err != nil {
				return err
			}
			previous := labels[previousOwnerLabel]
			secret, secretErr := edge.CoreV1().Secrets(p.Namespace).Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
			switch {
			case apierrors.IsNotFound(secretErr):
				previous = oldOwner
			case secretErr != nil:
				return secretErr
			default:
				if secret.Labels[attemptLabel] != "" || secret.Annotations[attemptIDAnnotation] != "" {
					return fmt.Errorf("provider kubeconfig Secret %s has installer-attempt ownership metadata", secret.Name)
				}
				secretOwner := secret.Labels[ownerLabel]
				if secretOwner != oldOwner && secretOwner != previous && secretOwner != p.Owner {
					return fmt.Errorf("provider kubeconfig Secret %s is not owned by the retained installation", secret.Name)
				}
				previous = secretOwner
			}
			if previous == "" {
				previous = oldOwner
			}
			labels[previousOwnerLabel] = previous
		}
		for key, value := range identity {
			if got := labels[key]; got != "" && got != value {
				return fmt.Errorf("namespace %s belongs to a different organization, provider, or Edge", p.Namespace)
			}
		}
		previousOwners = previousOwners[:0]
		if oldOwner != p.Owner {
			previousOwners = append(previousOwners, labels[previousOwnerLabel])
			labels[ownerLabel] = p.Owner
		}
		if previous := labels[previousOwnerLabel]; previous != "" && previous != p.Owner {
			previousOwners = appendUnique(previousOwners, previous)
		}
		for key, value := range identity {
			labels[key] = value
		}
		ns.SetLabels(labels)
		updated, updateErr := edge.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
		err = updateErr
		if err == nil {
			namespaceUID = string(updated.UID)
		}
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("preparing installation namespace: %w", err)
	}
	return previousOwners, namespaceUID, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendFinalizer(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func ensureNoActiveOldAttempt(ctx context.Context, edge kubernetes.Interface, namespace, oldOwner string) error {
	jobs, err := edge.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if jobs != nil {
		for idx := range jobs.Items {
			job := &jobs.Items[idx]
			if job.Labels[ownerLabel] == oldOwner && (job.Status.Active > 0 || !jobTerminal(job)) {
				return fmt.Errorf("namespace still has an active installer attempt; retry registration after it stops")
			}
		}
	}
	pods, err := edge.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if pods != nil {
		for idx := range pods.Items {
			pod := &pods.Items[idx]
			if pod.Labels[ownerLabel] == oldOwner && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				return fmt.Errorf("namespace still has an active installer Pod; retry registration after it stops")
			}
		}
	}
	return nil
}

func putProviderKubeconfig(ctx context.Context, edge kubernetes.Interface, namespace, owner string, previousOwners []string, credential []byte) error {
	secrets := edge.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, providers.KubeconfigSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: providers.KubeconfigSecretName, Labels: map[string]string{ownerLabel: owner}}, Data: map[string][]byte{"kubeconfig": credential}}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("provider kubeconfig Secret was concurrently created; retry installation")
		}
		if err != nil {
			return err
		}
		return clearPreviousOwnerLabel(ctx, edge, namespace, owner)
	}
	if err != nil {
		return err
	}
	if secret.Labels[attemptLabel] != "" || secret.Annotations[attemptIDAnnotation] != "" {
		return fmt.Errorf("provider kubeconfig Secret %s has installer-attempt ownership metadata", secret.Name)
	}
	secretOwner := secret.Labels[ownerLabel]
	owned := secretOwner == owner
	for _, previous := range previousOwners {
		owned = owned || (previous != "" && secretOwner == previous)
	}
	if !owned {
		return fmt.Errorf("provider kubeconfig Secret %s is not owned by this installation", secret.Name)
	}
	labels := secret.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ownerLabel] = owner
	secret.SetLabels(labels)
	secret.Data = map[string][]byte{"kubeconfig": credential}
	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return clearPreviousOwnerLabel(ctx, edge, namespace, owner)
}

// Kept local to avoid making namespace adoption depend on UI/provider packages.
func clearPreviousOwnerLabel(ctx context.Context, edge kubernetes.Interface, namespace, owner string) error {
	nss := edge.CoreV1().Namespaces()
	ns, err := nss.Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if ns.Labels[ownerLabel] != owner {
		return fmt.Errorf("installation namespace ownership changed while updating credentials")
	}
	if ns.Labels[previousOwnerLabel] == "" {
		return nil
	}
	labels := ns.GetLabels()
	delete(labels, previousOwnerLabel)
	ns.SetLabels(labels)
	_, err = nss.Update(ctx, ns, metav1.UpdateOptions{})
	return err
}

func terminalStatus(p *Plan, ready bool) *Status {
	out := &Status{Namespace: p.Namespace, Job: p.Job, Version: p.Version, Attempt: p.Attempt}
	if p.JobResult == string(batchv1.JobFailed) {
		out.Phase = "Failed"
		out.Message = "Helm installation failed. Retry the installation, or inspect the installer Job in your cluster."
		out.CanRetry = true
		return out
	}
	if p.JobResult == string(batchv1.JobComplete) {
		out.Phase = "Checking"
		out.Message = "Helm finished. Waiting for the provider to become healthy."
		if ready {
			out.Phase = "Ready"
			out.Message = "Your provider is ready."
		} else if !p.FinishedAt.IsZero() && time.Since(p.FinishedAt) > 5*time.Minute {
			out.Phase = "Failed"
			out.Message = "The provider is installed but has not become healthy. Check its pods and retry."
			out.CanRetry = true
		}
	}
	return out
}

func attemptStillActive(ctx context.Context, edge kubernetes.Interface, p *Plan) (bool, error) {
	job, err := edge.BatchV1().Jobs(p.Namespace).Get(ctx, p.Job, metav1.GetOptions{})
	if err == nil {
		if !jobBelongsToPlan(job, p) {
			return false, fmt.Errorf("installer Job ownership does not match")
		}
		if p.JobUID != "" && string(job.UID) != p.JobUID {
			return false, fmt.Errorf("installer Job UID does not match the terminal result")
		}
		if job.Status.Active > 0 {
			return true, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	return hasActiveAttemptPod(ctx, edge, p)
}

func (i *Installer) mutatePlan(ctx context.Context, org, name, owner, job, attemptID string, mutate func(*Plan) error) (*Plan, error) {
	cl, err := i.parent(org)
	if err != nil {
		return nil, err
	}
	var result *Plan
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := cl.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(ws.GetUID()) != owner {
			return fmt.Errorf("provider registration changed during installer cleanup")
		}
		annotations := ws.GetAnnotations()
		plan := &Plan{}
		if err := json.Unmarshal([]byte(annotations[planAnnotation]), plan); err != nil {
			return fmt.Errorf("invalid stored installation plan")
		}
		if marker := annotations[deletingAnnotation]; marker != "" {
			if marker != owner {
				return fmt.Errorf("provider registration deletion marker belongs to another registration")
			}
			plan.Deleting = true
		}
		if ws.GetDeletionTimestamp() != nil {
			plan.Deleting = true
		}
		if plan.Owner != owner || plan.Job != job || (attemptID != "" && plan.AttemptID != attemptID) {
			return fmt.Errorf("installation attempt changed during cleanup")
		}
		if err := validatePlanKey(plan, org, name); err != nil {
			return err
		}
		if err := mutate(plan); err != nil {
			return err
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[planAnnotation] = string(raw)
		ws.SetAnnotations(annotations)
		_, err = cl.Update(ctx, ws, metav1.UpdateOptions{})
		if err == nil {
			copy := *plan
			result = &copy
		}
		return err
	})
	return result, err
}

func (i *Installer) recordNamespaceAdmission(ctx context.Context, org, name string, p *Plan, namespaceUID string) error {
	_, err := i.mutatePlan(ctx, org, name, p.Owner, p.Job, p.AttemptID, func(current *Plan) error {
		if current.Deleting {
			return fmt.Errorf("provider registration is being deleted")
		}
		if current.NamespaceAdmitted && current.NamespaceUID != "" && namespaceUID != "" && current.NamespaceUID != namespaceUID {
			return fmt.Errorf("installation namespace changed during admission")
		}
		current.NamespaceAdmissionKnown = true
		current.NamespaceAdmitted = true
		current.NamespaceUID = namespaceUID
		return nil
	})
	return err
}

func (i *Installer) cleanupUnadmittedAttempt(ctx context.Context, org string, p *Plan) error {
	if p == nil || p.EdgeWorkspaceUUID == "" || p.Job == "" || p.AttemptID == "" {
		return fmt.Errorf("unadmitted installer attempt has incomplete ownership")
	}
	local, err := i.kubeClient(i.workspaces.ChildWorkspaceConfig(org, p.EdgeWorkspaceUUID))
	if err != nil {
		return err
	}
	return cleanupLocalAttemptResources(ctx, local, p)
}

func cleanupLocalAttemptResources(ctx context.Context, local kubernetes.Interface, p *Plan) error {
	identity := "railgrid-" + p.Job
	var errs []error
	if err := deleteOwnedClusterRoleBinding(ctx, local, identity, p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	if err := deleteOwnedClusterRole(ctx, local, identity, p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	if err := deleteOwnedServiceAccount(ctx, local, identity, p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (i *Installer) recordJobUID(ctx context.Context, org, name string, p *Plan, uid string) error {
	if uid == "" {
		return nil
	}
	_, err := i.mutatePlan(ctx, org, name, p.Owner, p.Job, p.AttemptID, func(current *Plan) error {
		if current.JobUID != "" && current.JobUID != uid {
			return fmt.Errorf("installer Job UID changed during preparation")
		}
		current.JobUID = uid
		return nil
	})
	return err
}

// ReconcileCleanup persists a terminal Job result before revoking runner
// credentials and releasing the Job finalizer. It is safe to call repeatedly.
func (i *Installer) ReconcileCleanup(ctx context.Context, org, name string) error {
	owner, deleteRequested, terminating, hasFinalizer, err := i.workspaceDeleteState(ctx, org, name)
	if err != nil {
		return err
	}
	p, planOwner, err := i.readPlan(ctx, org, name)
	if err != nil {
		return err
	}
	if planOwner != owner {
		return fmt.Errorf("provider registration changed during installer cleanup")
	}
	if p == nil {
		if hasFinalizer {
			return fmt.Errorf("installer cleanup finalizer exists without a durable plan")
		}
		if deleteRequested && !terminating {
			return i.finishWorkspaceDeleteIntent(ctx, org, name, owner)
		}
		return nil
	}
	if err := validatePlanKey(p, org, name); err != nil {
		return err
	}
	if !hasFinalizer && !deleteRequested {
		if err := i.ensureWorkspaceCleanupFinalizer(ctx, org, name, owner); err != nil {
			return err
		}
		hasFinalizer = true
	}
	if deleteRequested || p.Deleting {
		preparedOwner, err := i.PrepareDeleteWithUID(ctx, org, name, owner)
		if err != nil {
			return err
		}
		if preparedOwner != owner {
			return fmt.Errorf("provider registration changed during deletion cleanup")
		}
		if terminating {
			if hasFinalizer {
				return i.removeWorkspaceCleanupFinalizer(ctx, org, name, owner)
			}
			return nil
		}
		return i.finishWorkspaceDeleteIntent(ctx, org, name, owner)
	}
	if p.CleanedUp {
		return nil
	}
	if p.NamespaceAdmissionKnown && !p.NamespaceAdmitted {
		if preparationLeaseActive(p) {
			return nil
		}
		if p.JobResult == "" || p.FinishedAt.IsZero() {
			finishedAt := time.Now()
			_, err := i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
				if current.JobResult == "" || current.FinishedAt.IsZero() {
					current.JobResult = string(batchv1.JobFailed)
					current.FinishedAt = finishedAt
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if err := i.cleanupUnadmittedAttempt(ctx, org, p); err != nil {
			return err
		}
		_, err := i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
			current.CleanedUp = true
			return nil
		})
		return err
	}
	if p.JobResult == "" || p.FinishedAt.IsZero() {
		if preparationLeaseActive(p) {
			return nil
		}
		edge, _, _, err := i.edgeClientsForPlan(ctx, org, name, p)
		if err != nil {
			return err
		}
		job, err := edge.BatchV1().Jobs(p.Namespace).Get(ctx, p.Job, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			finishedAt := p.PreparationExpiresAt
			if finishedAt.IsZero() || finishedAt.After(time.Now()) {
				finishedAt = time.Now()
			}
			_, err = i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
				if current.JobResult == "" || current.FinishedAt.IsZero() {
					current.JobResult = string(batchv1.JobFailed)
					current.FinishedAt = finishedAt
				}
				return nil
			})
			if err != nil {
				return err
			}
			p, _, err = i.readPlan(ctx, org, name)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if !jobBelongsToPlan(job, p) {
				return fmt.Errorf("installer Job ownership does not match")
			}
			result, finishedAt, terminal := terminalJobResult(job)
			if !terminal {
				return nil
			}
			uid := string(job.UID)
			_, err = i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
				if current.JobResult == "" || current.FinishedAt.IsZero() {
					if current.JobUID != "" && uid != "" && current.JobUID != uid {
						return fmt.Errorf("installer Job UID changed before terminal result was saved")
					}
					current.JobResult = result
					current.FinishedAt = finishedAt
					current.JobUID = uid
				}
				return nil
			})
			if err != nil {
				return err
			}
			p, _, err = i.readPlan(ctx, org, name)
			if err != nil {
				return err
			}
		}
	}
	if p.JobResult == "" || p.FinishedAt.IsZero() {
		return nil
	}
	edge, local, _, err := i.edgeClientsForPlan(ctx, org, name, p)
	if err != nil {
		return err
	}
	done, err := cleanupTerminalResources(ctx, edge, local, p)
	if err != nil || !done {
		return err
	}
	if err := removeJobResultFinalizer(ctx, edge, p, p.JobUID); err != nil {
		return err
	}
	_, err = i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
		current.CleanedUp = true
		return nil
	})
	return err
}

func terminalJobResult(job *batchv1.Job) (string, time.Time, bool) {
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type != batchv1.JobComplete && condition.Type != batchv1.JobFailed {
			continue
		}
		finished := condition.LastTransitionTime.Time
		if finished.IsZero() {
			finished = time.Now()
		}
		return string(condition.Type), finished, true
	}
	return "", time.Time{}, false
}

func cleanupTerminalResources(ctx context.Context, edge, local kubernetes.Interface, p *Plan) (bool, error) {
	job, err := edge.BatchV1().Jobs(p.Namespace).Get(ctx, p.Job, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if err == nil {
		if !jobBelongsToPlan(job, p) {
			return false, fmt.Errorf("installer Job ownership does not match")
		}
		if p.JobUID != "" && string(job.UID) != p.JobUID {
			return false, fmt.Errorf("installer Job UID does not match the terminal result")
		}
		if !jobTerminal(job) || job.Status.Active > 0 {
			return false, nil
		}
	}
	active, err := hasActiveAttemptPod(ctx, edge, p)
	if err != nil || active {
		return false, err
	}
	if err := cleanupResources(ctx, edge, local, p); err != nil {
		return false, err
	}
	return true, nil
}

func removeJobResultFinalizer(ctx context.Context, edge kubernetes.Interface, p *Plan, expectedUID string) error {
	jobs := edge.BatchV1().Jobs(p.Namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		job, err := jobs.Get(ctx, p.Job, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !jobBelongsToPlan(job, p) || (expectedUID != "" && string(job.UID) != expectedUID) {
			return fmt.Errorf("installer Job ownership changed while removing its result finalizer")
		}
		filtered := make([]string, 0, len(job.Finalizers))
		for _, finalizer := range job.Finalizers {
			if finalizer != jobResultFinalizer {
				filtered = append(filtered, finalizer)
			}
		}
		if len(filtered) == len(job.Finalizers) {
			return nil
		}
		job.Finalizers = filtered
		_, err = jobs.Update(ctx, job, metav1.UpdateOptions{})
		return err
	})
}

func ensureJobResultFinalizer(ctx context.Context, edge kubernetes.Interface, existing *batchv1.Job, p *Plan) error {
	if !jobBelongsToPlan(existing, p) {
		return fmt.Errorf("installer Job ownership does not match")
	}
	for _, finalizer := range existing.Finalizers {
		if finalizer == jobResultFinalizer {
			return nil
		}
	}
	existing.Finalizers = append(existing.Finalizers, jobResultFinalizer)
	_, err := edge.BatchV1().Jobs(p.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (i *Installer) ensureWorkspaceCleanupFinalizer(ctx context.Context, org, name, owner string) error {
	cl, err := i.parent(org)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := cl.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(ws.GetUID()) != owner {
			return fmt.Errorf("provider registration changed while securing installer cleanup")
		}
		if !containsString(ws.GetFinalizers(), sealedWorkspaceFinalizer) && ws.GetDeletionTimestamp() == nil {
			ws.SetFinalizers(appendFinalizer(ws.GetFinalizers(), sealedWorkspaceFinalizer))
			_, err = cl.Update(ctx, ws, metav1.UpdateOptions{})
		}
		return err
	})
}

func (i *Installer) removeWorkspaceCleanupFinalizer(ctx context.Context, org, name, owner string) error {
	cl, err := i.parent(org)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := cl.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if string(ws.GetUID()) != owner {
			return fmt.Errorf("provider registration changed while releasing installer cleanup")
		}
		if ws.GetDeletionTimestamp() == nil && ws.GetAnnotations()[deletingAnnotation] != owner {
			return fmt.Errorf("provider registration deletion has not been accepted")
		}
		raw := ws.GetAnnotations()[planAnnotation]
		if raw == "" {
			return fmt.Errorf("installer cleanup finalizer exists without a durable plan")
		}
		plan := &Plan{}
		if err := json.Unmarshal([]byte(raw), plan); err != nil {
			return fmt.Errorf("invalid stored installation plan")
		}
		if plan.Owner != owner || !plan.Deleting || !plan.CleanedUp {
			return fmt.Errorf("installer cleanup is not complete")
		}
		finalizers := make([]string, 0, len(ws.GetFinalizers()))
		for _, finalizer := range ws.GetFinalizers() {
			if finalizer != sealedWorkspaceFinalizer {
				finalizers = append(finalizers, finalizer)
			}
		}
		if len(finalizers) == len(ws.GetFinalizers()) {
			return nil
		}
		ws.SetFinalizers(finalizers)
		_, err = cl.Update(ctx, ws, metav1.UpdateOptions{})
		return err
	})
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func (i *Installer) workspaceDeleteState(ctx context.Context, org, name string) (owner string, requested, terminating, hasFinalizer bool, err error) {
	cl, err := i.parent(org)
	if err != nil {
		return "", false, false, false, err
	}
	ws, err := cl.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", false, false, false, err
	}
	owner = string(ws.GetUID())
	if owner == "" {
		return "", false, false, false, fmt.Errorf("provider workspace has no UID for installation ownership")
	}
	marker := ws.GetAnnotations()[deletingAnnotation]
	if marker != "" && marker != owner {
		return "", false, false, false, fmt.Errorf("provider registration deletion marker belongs to another registration")
	}
	terminating = ws.GetDeletionTimestamp() != nil && !ws.GetDeletionTimestamp().IsZero()
	requested = marker != "" || terminating
	hasFinalizer = containsString(ws.GetFinalizers(), sealedWorkspaceFinalizer)
	return owner, requested, terminating, hasFinalizer, nil
}

type providerWorkspaceDeleter interface {
	DeleteOrgProviderWorkspace(context.Context, string, string) error
}

type providerWorkspaceUIDDeleter interface {
	DeleteOrgProviderWorkspaceIfUID(context.Context, string, string, string) error
}

func (i *Installer) finishWorkspaceDeleteIntent(ctx context.Context, org, name, owner string) error {
	if owner == "" {
		return nil
	}
	if deleter, ok := i.workspaces.(providerWorkspaceUIDDeleter); ok {
		err := deleter.DeleteOrgProviderWorkspaceIfUID(ctx, org, name, owner)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if _, ok := i.workspaces.(providerWorkspaceDeleter); ok {
		return fmt.Errorf("workspace deleter does not support UID-fenced provider deletion")
	}
	if owner == "" {
		return nil
	}
	return nil
}

// PrepareDelete fences future Start requests and removes only the current
// attempt's execution identity. It intentionally leaves the provider release,
// namespace, PVCs, and provider kubeconfig in place for a later registration.
func (i *Installer) PrepareDelete(ctx context.Context, org, name string) error {
	_, err := i.PrepareDeleteWithUID(ctx, org, name, "")
	return err
}

// PrepareDeleteWithUID returns the Workspace UID that was cleaned. Callers
// pass expectedOwner from an earlier observation when reconciling asynchronous
// deletion, so a stale reconcile cannot switch to a same-name replacement.
func (i *Installer) PrepareDeleteWithUID(ctx context.Context, org, name, expectedOwner string) (string, error) {
	p, owner, err := i.markRegistrationDeleting(ctx, org, name, expectedOwner)
	if apierrors.IsNotFound(err) && expectedOwner == "" {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if p == nil {
		return owner, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, deletePreparationWait)
	defer cancel()
	for preparationLeaseActive(p) {
		select {
		case <-waitCtx.Done():
			return "", fmt.Errorf("installer preparation did not stop before provider deletion: %w", waitCtx.Err())
		case <-time.After(deletePollInterval):
		}
		current, currentOwner, err := i.readPlan(waitCtx, org, name)
		if err != nil {
			return "", err
		}
		if currentOwner != owner || current == nil || current.Job != p.Job || current.AttemptID != p.AttemptID {
			return "", fmt.Errorf("installation attempt changed during provider deletion")
		}
		p = current
	}
	if p.PreparationID != "" && !preparationLeaseActive(p) {
		// An expired lease represents a crashed preparer. Deleting intent now
		// prevents it from passing assertPreparation if it resumes later.
		p.PreparationID = ""
	}
	if err := validatePlanKey(p, org, name); err != nil {
		return "", err
	}
	if p.NamespaceAdmissionKnown && !p.NamespaceAdmitted {
		if err := i.cleanupUnadmittedAttempt(ctx, org, p); err != nil {
			return "", err
		}
		_, err := i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
			current.Deleting = true
			current.CleanedUp = true
			current.PreparationID = ""
			current.PreparationExpiresAt = time.Time{}
			if current.JobResult == "" {
				current.JobResult = string(batchv1.JobFailed)
				current.FinishedAt = time.Now()
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		return owner, nil
	}
	edge, local, route, err := i.edgeClientsForPlan(ctx, org, name, p)
	if err != nil {
		return "", err
	}
	if p.Org == "" {
		p.Org = org
	}
	if p.Provider == "" {
		p.Provider = name
	}
	if p.EdgeWorkspaceUUID == "" {
		p.EdgeWorkspaceUUID = route.WorkspaceUUID
	}
	if p.EdgeName == "" {
		p.EdgeName = route.EdgeName
	}
	if p.Cluster == "" {
		p.Cluster = route.Cluster
	}
	if err := stampDeletionNamespace(ctx, edge, p); err != nil {
		return "", err
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, preparationCleanupTimeout)
	defer cleanupCancel()
	if err := stopAndRevokeAttempt(cleanupCtx, edge, local, p); err != nil {
		return "", err
	}
	_, err = i.mutatePlan(ctx, org, name, owner, p.Job, p.AttemptID, func(current *Plan) error {
		current.Deleting = true
		current.CleanedUp = true
		return nil
	})
	if err != nil {
		return "", err
	}
	return owner, nil
}

func (i *Installer) markRegistrationDeleting(ctx context.Context, org, name, expectedOwner string) (*Plan, string, error) {
	cl, err := i.parent(org)
	if err != nil {
		return nil, "", err
	}
	var plan *Plan
	var owner string
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := cl.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		owner = string(ws.GetUID())
		if owner == "" {
			return fmt.Errorf("provider workspace has no UID for installation ownership")
		}
		if expectedOwner != "" && expectedOwner != owner {
			return fmt.Errorf("provider registration changed before deletion cleanup")
		}
		annotations := ws.GetAnnotations()
		if marker := annotations[deletingAnnotation]; marker != "" && marker != owner {
			return fmt.Errorf("provider registration deletion marker belongs to another registration")
		}
		if raw := annotations[planAnnotation]; raw != "" {
			plan = &Plan{}
			if err := json.Unmarshal([]byte(raw), plan); err != nil {
				return fmt.Errorf("invalid stored installation plan")
			}
			if plan.Owner != owner {
				return fmt.Errorf("installation belongs to an earlier provider registration")
			}
			if err := validatePlanKey(plan, org, name); err != nil {
				return err
			}
			plan.Org, plan.Provider, plan.Deleting = org, name, true
			raw, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			annotations[planAnnotation] = string(raw)
			if ws.GetDeletionTimestamp() == nil {
				ws.SetFinalizers(appendFinalizer(ws.GetFinalizers(), sealedWorkspaceFinalizer))
			}
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[deletingAnnotation] = owner
		ws.SetAnnotations(annotations)
		_, err = cl.Update(ctx, ws, metav1.UpdateOptions{})
		return err
	})
	return plan, owner, err
}

func stampDeletionNamespace(ctx context.Context, edge kubernetes.Interface, p *Plan) error {
	nss := edge.CoreV1().Namespaces()
	ns, err := nss.Get(ctx, p.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	labels := ns.GetLabels()
	if labels[ownerLabel] != p.Owner {
		return fmt.Errorf("namespace %s is not owned by the current registration", p.Namespace)
	}
	if p.NamespaceUID != "" && string(ns.UID) != p.NamespaceUID {
		return fmt.Errorf("namespace %s changed after installer admission", p.Namespace)
	}
	for key, value := range stableNamespaceIdentity(p) {
		if existing := labels[key]; existing != "" && existing != value {
			return fmt.Errorf("namespace %s belongs to a different organization, provider, or Edge", p.Namespace)
		}
		labels[key] = value
	}
	ns.SetLabels(labels)
	_, err = nss.Update(ctx, ns, metav1.UpdateOptions{})
	return err
}

func stopAndRevokeAttempt(ctx context.Context, edge, local kubernetes.Interface, p *Plan) error {
	var errs []error
	jobs := edge.BatchV1().Jobs(p.Namespace)
	job, jobErr := jobs.Get(ctx, p.Job, metav1.GetOptions{})
	jobUID := p.JobUID
	if jobErr != nil && !apierrors.IsNotFound(jobErr) {
		errs = append(errs, jobErr)
	} else if jobErr == nil {
		if !jobBelongsToPlan(job, p) || (p.JobUID != "" && string(job.UID) != p.JobUID) {
			errs = append(errs, fmt.Errorf("installer Job ownership does not match; refusing to cancel it"))
		} else {
			jobUID = string(job.UID)
			zero := int64(0)
			options := metav1.DeleteOptions{GracePeriodSeconds: &zero}
			if job.UID != "" {
				options.Preconditions = &metav1.Preconditions{UID: &job.UID}
			}
			if err := jobs.Delete(ctx, p.Job, options); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}

	pods, podErr := edge.CoreV1().Pods(p.Namespace).List(ctx, metav1.ListOptions{})
	if podErr != nil && !apierrors.IsNotFound(podErr) {
		errs = append(errs, podErr)
	} else if pods != nil {
		for idx := range pods.Items {
			pod := &pods.Items[idx]
			if !podMatchesAttempt(pod, p.Job) {
				continue
			}
			if !podBelongsToPlan(pod, p) || !podHasExpectedJobOwner(pod, p, jobUID) {
				errs = append(errs, fmt.Errorf("installer Pod %s ownership is ambiguous; refusing to delete it", pod.Name))
				continue
			}
			zero := int64(0)
			options := metav1.DeleteOptions{GracePeriodSeconds: &zero}
			if pod.UID != "" {
				options.Preconditions = &metav1.Preconditions{UID: &pod.UID}
			}
			if err := edge.CoreV1().Pods(p.Namespace).Delete(ctx, pod.Name, options); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}
	// Revoke independently of cancellation success: a partially deleted Job
	// must never retain the capability while removal is retried.
	if err := cleanupResources(ctx, edge, local, p); err != nil {
		errs = append(errs, err)
	}
	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	if err := waitForNoActiveAttemptPods(ctx, edge, p); err != nil {
		return err
	}
	return removeJobResultFinalizer(ctx, edge, p, jobUID)
}

func podBelongsToPlan(pod *corev1.Pod, p *Plan) bool {
	return pod != nil && p != nil && pod.Labels[ownerLabel] == p.Owner && pod.Labels[attemptLabel] == p.Job && pod.Annotations[attemptIDAnnotation] == p.AttemptID
}

func podHasExpectedJobOwner(pod *corev1.Pod, p *Plan, jobUID string) bool {
	if pod == nil {
		return false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Kind != "Job" || owner.Name != p.Job || (owner.UID != "" && (jobUID == "" || string(owner.UID) != jobUID)) {
			return false
		}
	}
	return true
}

func waitForNoActiveAttemptPods(ctx context.Context, edge kubernetes.Interface, p *Plan) error {
	for {
		pods, err := edge.CoreV1().Pods(p.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		active := false
		if pods != nil {
			for idx := range pods.Items {
				pod := &pods.Items[idx]
				if !podMatchesAttempt(pod, p.Job) {
					continue
				}
				if !podBelongsToPlan(pod, p) {
					return fmt.Errorf("installer Pod %s ownership is ambiguous; refusing provider deletion", pod.Name)
				}
				if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
					active = true
				}
			}
		}
		if !active {
			return nil
		}
		timer := time.NewTimer(deletePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("installer Pod did not stop before provider deletion: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
