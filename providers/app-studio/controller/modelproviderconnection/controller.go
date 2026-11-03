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

// Package modelproviderconnection owns saved model-provider connection
// readiness, safe generated-key lifetime, and migration of legacy direct
// Studio model records into reusable connection bindings.
package modelproviderconnection

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	api "github.com/railgrid/provider-app-studio/api"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	migratedFromStudioUIDAnnotation      = "ai.railgrid.ai/migrated-from-studio-uid"
	migratedFromStudioRevisionAnnotation = "ai.railgrid.ai/migrated-from-studio-revision"
	retainedConnectionUIDAnnotation      = "ai.railgrid.ai/retained-connection-uid"
	studioCredentialCollectorRequestName = "@studio-retained-model-credentials"
	conditionSecretResolved              = "SecretResolved"
	conditionReachable                   = "Reachable"
	conditionReady                       = "Ready"
)

var errRevisionHistoryFull = errors.New("studio model revision history is full")

type Reconciler struct {
	Manager mcmanager.Manager
	Now     func() time.Time
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("app-studio-modelproviderconnection").
		For(&aiv1alpha1.ModelProviderConnection{}).
		Watches(&aiv1alpha1.Studio{}, connectionsForStudio, mcbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, connectionsForSecret).
		Complete(r)
}

func connectionsForStudio(name multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var connections aiv1alpha1.ModelProviderConnectionList
		if err := cl.GetClient().List(ctx, &connections); err != nil {
			klog.FromContext(ctx).Error(err, "listing model provider connections for Studio event")
			return requestsForStudioEvent(nil, nil)
		}
		var studio *aiv1alpha1.Studio
		var current aiv1alpha1.Studio
		if err := cl.GetClient().Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &current); err == nil {
			studio = &current
		} else if !apierrors.IsNotFound(err) {
			klog.FromContext(ctx).Error(err, "getting Studio for connection event mapping")
		}
		return requestsForStudioEvent(studio, connections.Items)
	}))(name, cl)
}

func connectionsForSecret(name multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		if obj.GetNamespace() != "default" {
			return nil
		}
		var connections aiv1alpha1.ModelProviderConnectionList
		if err := cl.GetClient().List(ctx, &connections); err != nil {
			klog.FromContext(ctx).Error(err, "listing model provider connections for Secret event")
			return nil
		}
		var studio aiv1alpha1.Studio
		if err := cl.GetClient().Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil && !apierrors.IsNotFound(err) {
			klog.FromContext(ctx).Error(err, "getting Studio for legacy Secret migration event")
		}
		return requestsForSecretEvent(obj, &studio, connections.Items)
	}))(name, cl)
}

func requestsForSecretEvent(secret client.Object, studio *aiv1alpha1.Studio, connections []aiv1alpha1.ModelProviderConnection) []reconcile.Request {
	if secret == nil || secret.GetNamespace() != "default" {
		return nil
	}
	name := secret.GetName()
	seen := make(map[string]struct{}, len(connections))
	requests := make([]reconcile.Request, 0, len(connections)+2)
	appendName := func(connectionName string) {
		connectionName = strings.TrimSpace(connectionName)
		if connectionName == "" {
			return
		}
		if _, found := seen[connectionName]; found {
			return
		}
		seen[connectionName] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: connectionName}})
	}
	for i := range connections {
		connection := &connections[i]
		if connection.Spec.SecretRef.Name == name || connection.Annotations[aiv1alpha1.ModelProviderConnectionPendingKeySecretAnnotation] == name || hasConnectionOwner(secret, connection) {
			appendName(connection.Name)
		}
	}
	if isStudioOwnedGeneratedSecret(secret) {
		appendName(studioCredentialCollectorRequestName)
	}
	if studio != nil && studio.Spec.LLM != nil {
		for _, model := range studio.Spec.LLM.Models {
			if !model.Archived && model.ConnectionRef == "" && model.SecretRef != nil && model.SecretRef.Name == name {
				appendName(model.ID)
			}
		}
	}
	return requests
}

func studioCredentialCollectorRequest() reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: studioCredentialCollectorRequestName}}
}

func requestsForStudioEvent(studio *aiv1alpha1.Studio, connections []aiv1alpha1.ModelProviderConnection) []reconcile.Request {
	names := WatchMapperScope(studio, connections)
	requests := make([]reconcile.Request, 0, len(names)+1)
	for _, name := range names {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
	}
	// This synthetic key has an invalid Kubernetes object name, so it cannot
	// collide with a real ModelProviderConnection. It remains routable after
	// the last connection is gone.
	requests = append(requests, studioCredentialCollectorRequest())
	return requests
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()
	reader := cl.GetAPIReader()
	if reader == nil {
		reader = c
	}
	if req.Name == studioCredentialCollectorRequestName {
		return ctrl.Result{}, r.collectStudioOwnedGeneratedCredentials(ctx, c, reader)
	}
	var connection aiv1alpha1.ModelProviderConnection
	if err := reader.Get(ctx, req.NamespacedName, &connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err := r.collectStudioOwnedGeneratedCredentials(ctx, c, reader); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.migrateLegacyBindings(ctx, c, reader)
	}
	if !connection.DeletionTimestamp.IsZero() {
		secret, _, _, _, err := resolveSecret(ctx, reader, &connection)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := protectExternalCredentials(ctx, c, reader, &connection, secret); err != nil {
			return ctrl.Result{}, err
		}
		return r.finalize(ctx, c, reader, &connection)
	}
	if resumed, err := r.resumeLegacyMigration(ctx, c, reader, &connection); err != nil {
		return ctrl.Result{}, err
	} else if resumed {
		return ctrl.Result{Requeue: true}, nil
	}
	if !slices.Contains(connection.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer) {
		connection.Finalizers = append(connection.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer)
		if err := c.Update(ctx, &connection); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	secret, key, secretReason, secretMessage, err := resolveSecret(ctx, reader, &connection)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := protectExternalCredentials(ctx, c, reader, &connection, secret); err != nil {
		return ctrl.Result{}, err
	}
	status := connection.Status.DeepCopy()
	status.ObservedGeneration = connection.Generation
	now := r.now()
	setCondition(status, conditionSecretResolved, secretReason == "", reason(secretReason), secretMessage, connection.Generation, now)
	if secretReason == "" {
		status.SecretUID = string(secret.UID)
		status.SecretResourceVersion = secret.ResourceVersion
		status.CredentialMode = credentialMode(key)
		obj, convertErr := secretUnstructured(secret)
		if convertErr != nil {
			return ctrl.Result{}, convertErr
		}
		fingerprint, fingerprintErr := api.ModelProviderConnectionFingerprint(&connection, obj)
		if fingerprintErr != nil {
			return ctrl.Result{}, fingerprintErr
		}
		status.ConnectionFingerprint = fingerprint
		if !probeResultMatches(&connection.Status, fingerprint) {
			status.LastProbeTime = nil
			status.LastProbeFingerprint = ""
			status.Models = nil
			status.LastProbeError = "run authorized model discovery to verify this endpoint"
			setUnknownCondition(status, conditionReachable, "ExplicitDiscoveryRequired", status.LastProbeError, connection.Generation, now)
		}
	} else {
		status.SecretUID = ""
		status.SecretResourceVersion = ""
		status.CredentialMode = ""
		status.ConnectionFingerprint = ""
		status.LastProbeTime = nil
		status.LastProbeFingerprint = ""
		status.Models = nil
		status.LastProbeError = ""
		setCondition(status, conditionReachable, false, "SecretUnresolved", secretMessage, connection.Generation, now)
	}
	reachability := meta.FindStatusCondition(status.Conditions, conditionReachable)
	probeCurrent := secretReason == "" && probeResultMatches(status, status.ConnectionFingerprint)
	ready := probeCurrent && reachability != nil && (reachability.Status == metav1.ConditionTrue || (reachability.Status == metav1.ConditionUnknown && reachability.Reason == "DiscoveryUnavailable"))
	if ready {
		setCondition(status, conditionReady, true, "Ready", "The provider connection is ready.", connection.Generation, now)
	} else {
		setCondition(status, conditionReady, false, "NotReady", "The provider connection needs a usable key and a reachable endpoint.", connection.Generation, now)
	}
	var rolloverErr error
	if secretReason == "" {
		var admitted *aiv1alpha1.StudioLLMConnectionSnapshot
		admitted, rolloverErr = r.admitConnectionSnapshot(ctx, c, reader, &connection, secret)
		if rolloverErr == nil {
			status.ActiveSnapshot = admitted
			setCondition(status, "BindingsReady", true, "Activated", "Model bindings use the admitted provider configuration.", connection.Generation, now)
		} else {
			why := "RolloverPending"
			if errors.Is(rolloverErr, errRevisionHistoryFull) {
				why = "RevisionHistoryFull"
			}
			setCondition(status, "BindingsReady", false, why, "Provider changes are waiting for model binding rollover. The previous runtime configuration remains active.", connection.Generation, now)
			setCondition(status, conditionReady, false, why, "Provider changes are waiting for model binding rollover. The previous runtime configuration remains active.", connection.Generation, now)
		}
	}
	if !reflect.DeepEqual(connection.Status, *status) {
		connection.Status = *status
		if err := c.Status().Update(ctx, &connection); err != nil {
			return ctrl.Result{}, err
		}
	}
	if rolloverErr != nil {
		if errors.Is(rolloverErr, errRevisionHistoryFull) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, rolloverErr
	}
	return ctrl.Result{}, nil
}

// resumeLegacyMigration completes the Studio half of a migration when the
// connection create succeeded but the Studio update conflicted or the
// controller restarted between those writes. The migration annotations fence
// this path to the exact source Studio UID and model revision; an ordinary
// connection with the same name is never adopted.
func (r *Reconciler) resumeLegacyMigration(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection) (bool, error) {
	studioUID := strings.TrimSpace(connection.Annotations[migratedFromStudioUIDAnnotation])
	revisionID := strings.TrimSpace(connection.Annotations[migratedFromStudioRevisionAnnotation])
	if studioUID == "" || revisionID == "" {
		return false, nil
	}
	var studio aiv1alpha1.Studio
	if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if string(studio.UID) != studioUID || studio.Spec.LLM == nil {
		return false, nil
	}
	for _, model := range studio.Spec.LLM.Models {
		if model.ID != connection.Name || model.RevisionID != revisionID || model.Archived || model.ConnectionRef != "" || model.SecretRef == nil {
			continue
		}
		var sourceSecret corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: "default", Name: strings.TrimSpace(model.SecretRef.Name)}, &sourceSecret); err != nil {
			if apierrors.IsNotFound(err) {
				// Leave the destination parked until the source Secret watch
				// signals that the credential has arrived.
				return false, nil
			}
			return false, err
		}
		if len(strings.TrimSpace(string(sourceSecret.Data[aiv1alpha1.ModelProviderConnectionSecretKey]))) == 0 {
			return false, nil
		}
		return migrateLegacyModel(ctx, c, reader, &studio, model)
	}
	return false, nil
}

func (r *Reconciler) migrateLegacyBindings(ctx context.Context, c client.Client, reader client.Reader) error {
	var studio aiv1alpha1.Studio
	if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
		return client.IgnoreNotFound(err)
	}
	if studio.Spec.LLM == nil {
		return nil
	}
	if err := captureLegacyMigrationPlan(ctx, c, reader, &studio); err != nil {
		return err
	}
	for _, model := range studio.Spec.LLM.Models {
		if model.Archived || model.ConnectionRef != "" || model.SecretRef == nil || strings.TrimSpace(model.SecretRef.Name) == "" || strings.TrimSpace(model.Provider) == "harness" {
			continue
		}
		migrated, err := migrateLegacyModel(ctx, c, reader, &studio, model)
		if err != nil {
			if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
				return err
			}
			klog.FromContext(ctx).Error(err, "migrating legacy Studio model binding", "model", model.ID)
			return err
		}
		if !migrated {
			continue
		}
		// One transactional model rewrite at a time. A new Studio watch event or
		// the connection watch resumes remaining rows after this write.
		return nil
	}
	return nil
}

// Capture every eligible direct binding in one status CAS before promoting
// any shared key. A later caller-written spec entry cannot join that cohort.
func captureLegacyMigrationPlan(ctx context.Context, c client.Client, reader client.Reader, studio *aiv1alpha1.Studio) error {
	knownKeys := map[string]bool{}
	for _, entry := range studio.Status.ProtectedModelCredentials {
		knownKeys[entry.SecretName] = true
	}
	for _, entry := range studio.Status.LegacyModelMigrations {
		if entry.Model.SecretRef != nil {
			knownKeys[strings.TrimSpace(entry.Model.SecretRef.Name)] = true
		}
	}
	var connections aiv1alpha1.ModelProviderConnectionList
	if err := reader.List(ctx, &connections); err != nil {
		return err
	}
	for _, connection := range connections.Items {
		knownKeys[strings.TrimSpace(connection.Spec.SecretRef.Name)] = true
		if active := connection.Status.ActiveSnapshot; active != nil && active.SecretRef != nil {
			knownKeys[strings.TrimSpace(active.SecretRef.Name)] = true
		}
	}
	plan := append([]aiv1alpha1.StudioLegacyModelMigration(nil), studio.Status.LegacyModelMigrations...)
	for _, model := range studio.Spec.LLM.Models {
		if model.Archived || model.ConnectionRef != "" || model.SecretRef == nil || model.ConnectionSnapshot != nil {
			continue
		}
		name := strings.TrimSpace(model.SecretRef.Name)
		if name == "" || knownKeys[name] || strings.HasPrefix(name, aiv1alpha1.ModelProviderConnectionSecretPrefix) {
			continue
		}
		var secret corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if secret.UID == "" || len(strings.TrimSpace(string(secret.Data[aiv1alpha1.ModelProviderConnectionSecretKey]))) == 0 || secret.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != "" {
			continue
		}
		owned := false
		for _, owner := range secret.OwnerReferences {
			if owner.Kind == "ModelProviderConnection" && owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() {
				owned = true
			}
		}
		if owned {
			continue
		}
		if len(plan) >= 200 {
			return fmt.Errorf("legacy migration provenance is full")
		}
		plan = append(plan, aiv1alpha1.StudioLegacyModelMigration{Model: model, SecretUID: string(secret.UID)})
	}
	if reflect.DeepEqual(plan, studio.Status.LegacyModelMigrations) {
		return nil
	}
	studio.Status.LegacyModelMigrations = plan
	return c.Status().Update(ctx, studio)
}

func trustedLegacyMigration(studio *aiv1alpha1.Studio, model aiv1alpha1.StudioLLMModel, secretUID string) bool {
	for _, entry := range studio.Status.LegacyModelMigrations {
		prior := entry.Model
		if entry.SecretUID == secretUID && prior.ID == model.ID && prior.RevisionID == model.RevisionID && prior.SecretRef != nil && model.SecretRef != nil &&
			strings.TrimSpace(prior.SecretRef.Name) == strings.TrimSpace(model.SecretRef.Name) && prior.Provider == model.Provider &&
			prior.BaseURL == model.BaseURL && prior.AnthropicWorkspaceID == model.AnthropicWorkspaceID && prior.Model == model.Model && prior.ReasoningEffort == model.ReasoningEffort {
			return true
		}
	}
	return false
}

func migrateLegacyModel(ctx context.Context, c client.Client, reader client.Reader, studio *aiv1alpha1.Studio, model aiv1alpha1.StudioLLMModel) (bool, error) {
	if studio.UID == "" || strings.TrimSpace(model.RevisionID) == "" || strings.TrimSpace(model.ID) == "" {
		return false, fmt.Errorf("legacy model migration requires Studio UID, model ID, and revision ID")
	}
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: "default", Name: strings.TrimSpace(model.SecretRef.Name)}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(strings.TrimSpace(string(secret.Data[aiv1alpha1.ModelProviderConnectionSecretKey]))) == 0 {
		return false, nil
	}
	// A direct Studio entry must not launder a saved connection's key into a
	// new connection with a caller-chosen endpoint or a different use grant.
	if strings.HasPrefix(secret.Name, aiv1alpha1.ModelProviderConnectionSecretPrefix) || secret.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != "" {
		return false, nil
	}
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ModelProviderConnection" {
			return false, nil
		}
	}
	for _, entry := range studio.Status.ProtectedModelCredentials {
		if entry.SecretName == secret.Name && !trustedLegacyMigration(studio, model, string(secret.UID)) {
			return false, nil
		}
	}
	var connections aiv1alpha1.ModelProviderConnectionList
	if err := reader.List(ctx, &connections); err != nil {
		return false, err
	}
	for _, existing := range connections.Items {
		usesSecret := strings.TrimSpace(existing.Spec.SecretRef.Name) == secret.Name
		if active := existing.Status.ActiveSnapshot; active != nil && active.SecretRef != nil {
			usesSecret = usesSecret || strings.TrimSpace(active.SecretRef.Name) == secret.Name
		}
		if usesSecret && (existing.Name != model.ID || existing.Annotations[migratedFromStudioUIDAnnotation] != string(studio.UID) || existing.Annotations[migratedFromStudioRevisionAnnotation] != model.RevisionID) && !trustedLegacyMigration(studio, model, string(secret.UID)) {
			return false, nil
		}
	}
	provider := strings.TrimSpace(model.Provider)
	if provider == "" {
		provider = "openai-compatible"
	}
	legacySpec, err := normalizedLegacyConnectionSpec(aiv1alpha1.ModelProviderConnectionSpec{
		Provider: provider, BaseURL: strings.TrimSpace(model.BaseURL), AnthropicWorkspaceID: strings.TrimSpace(model.AnthropicWorkspaceID),
		SecretRef: *model.SecretRef, SecretKey: aiv1alpha1.ModelProviderConnectionSecretKey,
	})
	if err != nil {
		return false, fmt.Errorf("normalize legacy model provider settings: %w", err)
	}
	connection := &aiv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name: model.ID,
			Annotations: map[string]string{
				migratedFromStudioUIDAnnotation:                         string(studio.UID),
				migratedFromStudioRevisionAnnotation:                    model.RevisionID,
				aiv1alpha1.ModelProviderConnectionDisplayNameAnnotation: model.Name,
			},
			Finalizers: []string{aiv1alpha1.ModelProviderConnectionFinalizer},
		},
		Spec: legacySpec,
	}
	if err := c.Create(ctx, connection); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return false, err
		}
		if err := reader.Get(ctx, types.NamespacedName{Name: model.ID}, connection); err != nil {
			return false, err
		}
		if connection.Annotations[migratedFromStudioUIDAnnotation] != string(studio.UID) || connection.Annotations[migratedFromStudioRevisionAnnotation] != model.RevisionID || !sameMigratedConnectionConfiguration(connection.Spec, model) {
			return false, fmt.Errorf("provider connection %q already exists independently; direct model configuration was preserved", model.ID)
		}
	}
	if err := ensureConnectionFinalizer(ctx, c, reader, connection); err != nil {
		return false, err
	}
	secretObj, err := secretUnstructured(&secret)
	if err != nil {
		return false, err
	}
	snapshot, err := api.ModelProviderConnectionSnapshot(connection, secretObj)
	if err != nil {
		return false, err
	}
	current := studio.DeepCopy()
	if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, current); err != nil {
		return false, err
	}
	if current.Spec.LLM == nil {
		return false, nil
	}
	for i := range current.Spec.LLM.Models {
		entry := &current.Spec.LLM.Models[i]
		if entry.ID != model.ID || entry.RevisionID != model.RevisionID || entry.Archived || entry.ConnectionRef != "" {
			continue
		}
		if len(current.Spec.LLM.Models) >= 200 {
			// The controller-owned source record preserves pre-migration runs.
			// Admit the same logical revision in place when no archive slot is
			// available, rather than stranding a shared-key sibling.
			if !trustedLegacyMigration(current, model, string(secret.UID)) {
				return false, fmt.Errorf("legacy source provenance is missing at revision capacity")
			}
			entry.ConnectionRef = connection.Name
			entry.ConnectionSnapshot = snapshot
			return true, c.Update(ctx, current)
		}
		old := *entry
		entry.Archived = true
		newModel := old
		newModel.RevisionID = uuid.NewString()
		newModel.Archived = false
		newModel.ConnectionRef = connection.Name
		newModel.Provider = ""
		newModel.BaseURL = ""
		newModel.AnthropicWorkspaceID = ""
		newModel.SecretRef = nil
		newModel.ConnectionSnapshot = snapshot
		current.Spec.LLM.Models = append(current.Spec.LLM.Models, newModel)
		return true, c.Update(ctx, current)
	}
	return false, nil
}

func (r *Reconciler) admitConnectionSnapshot(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection, secret *corev1.Secret) (*aiv1alpha1.StudioLLMConnectionSnapshot, error) {
	if err := r.rolloverBindings(ctx, c, reader, connection, secret); err != nil {
		return nil, err
	}
	obj, err := secretUnstructured(secret)
	if err != nil {
		return nil, err
	}
	return api.ModelProviderConnectionSnapshot(connection, obj)
}

func (r *Reconciler) rolloverBindings(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection, secret *corev1.Secret) error {
	obj, err := secretUnstructured(secret)
	if err != nil {
		return err
	}
	snapshot, err := api.ModelProviderConnectionSnapshot(connection, obj)
	if err != nil {
		return err
	}
	var studio aiv1alpha1.Studio
	if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
		return client.IgnoreNotFound(err)
	}
	if studio.Spec.LLM == nil {
		return nil
	}
	changed := false
	models := append([]aiv1alpha1.StudioLLMModel(nil), studio.Spec.LLM.Models...)
	for i := range models {
		model := &models[i]
		if model.Archived || model.ConnectionRef != connection.Name {
			continue
		}
		if completeSnapshot(model.ConnectionSnapshot) && reflect.DeepEqual(model.ConnectionSnapshot, snapshot) {
			continue
		}
		if len(models) >= 200 {
			return fmt.Errorf("%w; cannot roll model %q to provider connection %q", errRevisionHistoryFull, model.ID, connection.Name)
		}
		old := *model
		model.Archived = true
		newModel := old
		newModel.RevisionID = uuid.NewString()
		newModel.Archived = false
		newModel.Provider = ""
		newModel.BaseURL = ""
		newModel.AnthropicWorkspaceID = ""
		newModel.SecretRef = nil
		newModel.ConnectionSnapshot = snapshot
		models = append(models, newModel)
		changed = true
	}
	if !changed {
		return nil
	}
	studio.Spec.LLM.Models = models
	return c.Update(ctx, &studio)
}

func completeSnapshot(snapshot *aiv1alpha1.StudioLLMConnectionSnapshot) bool {
	return snapshot != nil && snapshot.SecretRef != nil && strings.TrimSpace(snapshot.ConnectionRef) != "" &&
		strings.TrimSpace(snapshot.ConnectionUID) != "" && strings.TrimSpace(snapshot.SecretUID) != "" &&
		strings.TrimSpace(snapshot.SecretResourceVersion) != "" && strings.TrimSpace(snapshot.ConnectionFingerprint) != ""
}

func (r *Reconciler) finalize(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection) (ctrl.Result, error) {
	if !slices.Contains(connection.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer) {
		return ctrl.Result{}, nil
	}
	var studio aiv1alpha1.Studio
	studioErr := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio)
	if studioErr != nil && !apierrors.IsNotFound(studioErr) {
		return ctrl.Result{}, studioErr
	}
	if studioErr == nil && studio.Spec.LLM != nil {
		for _, model := range studio.Spec.LLM.Models {
			if !model.Archived && model.ConnectionRef == connection.Name {
				status := connection.Status.DeepCopy()
				setCondition(status, conditionReady, false, "ModelsStillReferenceConnection", "Remove or reassign active model bindings before deleting this connection.", connection.Generation, r.now())
				if !reflect.DeepEqual(connection.Status, *status) {
					connection.Status = *status
					if err := c.Status().Update(ctx, connection); err != nil {
						return ctrl.Result{}, err
					}
				}
				return ctrl.Result{}, nil
			}
		}
	}
	var studioPtr *aiv1alpha1.Studio
	if studioErr == nil {
		studioPtr = &studio
	}
	if err := retainAllGeneratedSecrets(ctx, c, reader, connection, studioPtr); err != nil {
		return ctrl.Result{}, err
	}
	connection.Finalizers = slices.DeleteFunc(connection.Finalizers, func(value string) bool { return value == aiv1alpha1.ModelProviderConnectionFinalizer })
	return ctrl.Result{}, c.Update(ctx, connection)
}

func retainAllGeneratedSecrets(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection, studio *aiv1alpha1.Studio) error {
	if studio == nil {
		return nil
	}
	if studio.UID == "" {
		return fmt.Errorf("refusing to retain generated Secrets for Studio %q without a UID", studio.Name)
	}
	if connection.UID == "" {
		return fmt.Errorf("refusing to retain generated Secrets for provider connection %q without a UID", connection.Name)
	}
	var secrets corev1.SecretList
	if err := reader.List(ctx, &secrets, client.InNamespace("default")); err != nil {
		return err
	}
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if !isIdentityValidatedGeneratedSecret(secret) || !secretOwnedByConnection(secret, connection) {
			continue
		}
		if secret.UID == "" || secret.ResourceVersion == "" {
			return fmt.Errorf("refusing to retain generated Secret %q without UID and resourceVersion", secret.Name)
		}
		if retainedUID := strings.TrimSpace(secret.Annotations[retainedConnectionUIDAnnotation]); retainedUID != "" && retainedUID != string(connection.UID) {
			return fmt.Errorf("generated Secret %q has conflicting retained connection identity", secret.Name)
		}
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}
		secret.Annotations[retainedConnectionUIDAnnotation] = string(connection.UID)
		if !secretOwnedByStudio(secret, studio) {
			secret.OwnerReferences = append(secret.OwnerReferences, metav1.OwnerReference{
				APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Studio", Name: studio.Name, UID: studio.UID,
			})
		}
		if err := c.Update(ctx, secret); err != nil {
			return err
		}
		var retained corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: "default", Name: secret.Name}, &retained); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if retained.UID != secret.UID || !isIdentityValidatedGeneratedSecret(&retained) || !secretOwnedByConnection(&retained, connection) || !secretOwnedByStudio(&retained, studio) || retained.Annotations[retainedConnectionUIDAnnotation] != string(connection.UID) {
			return fmt.Errorf("generated Secret %q was not durably transferred to Studio retention", secret.Name)
		}
	}
	return nil
}

// collectStudioOwnedGeneratedCredentials retires a generated Secret identity
// in Studio status before deleting it. The status update is a resourceVersion
// CAS against Studio spec writes; a conflict retries the reference calculation.
// Runtime snapshot resolution rejects retired identities, so no later model
// row can make a deleted key usable again.
func (r *Reconciler) collectStudioOwnedGeneratedCredentials(ctx context.Context, c client.Client, reader client.Reader) error {
	var studio aiv1alpha1.Studio
	if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
		return client.IgnoreNotFound(err)
	}
	if studio.UID == "" {
		return fmt.Errorf("refusing to collect generated Secrets for Studio %q without a UID", studio.Name)
	}
	var connections aiv1alpha1.ModelProviderConnectionList
	if err := reader.List(ctx, &connections); err != nil {
		return err
	}
	var secrets corev1.SecretList
	if err := reader.List(ctx, &secrets, client.InNamespace("default")); err != nil {
		return err
	}
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		generatedID, valid := generatedSecretIdentity(secret)
		if !valid || !secretOwnedByStudio(secret, &studio) || !onlyStudioAndSourceOwners(secret, &studio) {
			continue
		}
		if secret.UID == "" || secret.ResourceVersion == "" {
			continue
		}
		sourceUID := retainedSecretSourceUID(secret)
		if liveConnectionRetainsSecret(secret, generatedID, sourceUID, connections.Items) {
			continue
		}
		identity := aiv1alpha1.StudioModelCredentialIdentity{SecretName: secret.Name, SecretUID: string(secret.UID)}
		retired := slices.Contains(studio.Status.RetiredModelCredentials, identity)
		if !retired && studioReferencesGeneratedSnapshot(&studio, secret, generatedID) {
			continue
		}
		if !retired {
			if len(studio.Status.RetiredModelCredentials) >= 1000 {
				// A full tombstone ledger means no safe deletion is possible.
				continue
			}
			studio.Status.RetiredModelCredentials = append(studio.Status.RetiredModelCredentials, identity)
			if err := c.Status().Update(ctx, &studio); err != nil {
				return err
			}
			// Refresh the CAS token before processing another Secret.
			if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
				return err
			}
			if !slices.Contains(studio.Status.RetiredModelCredentials, identity) {
				return fmt.Errorf("studio did not persist retirement of generated Secret %q; keeping the key", secret.Name)
			}
		}
		uid, resourceVersion := secret.UID, secret.ResourceVersion
		if err := c.Delete(ctx, secret, client.Preconditions{UID: &uid, ResourceVersion: &resourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func generatedSecretIdentity(secret *corev1.Secret) (string, bool) {
	if secret == nil || secret.Namespace != "default" || secret.UID == "" || secret.ResourceVersion == "" {
		return "", false
	}
	id := strings.TrimSpace(secret.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation])
	return id, id != "" && validGeneratedSecretIdentity(secret.Name, id) &&
		secret.Labels[aiv1alpha1.ModelProviderConnectionSecretOwnerLabel] == aiv1alpha1.ModelProviderConnectionSecretOwner &&
		secret.Immutable != nil && *secret.Immutable
}

func isIdentityValidatedGeneratedSecret(secret *corev1.Secret) bool {
	_, valid := generatedSecretIdentity(secret)
	return valid
}

func isStudioOwnedGeneratedSecret(obj client.Object) bool {
	secret, ok := obj.(*corev1.Secret)
	if !ok || !isIdentityValidatedGeneratedSecret(secret) {
		return false
	}
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "Studio" && owner.UID != "" {
			return true
		}
	}
	return false
}

func onlyStudioAndSourceOwners(secret *corev1.Secret, studio *aiv1alpha1.Studio) bool {
	if secret == nil || studio == nil {
		return false
	}
	sourceUID := retainedSecretSourceUID(secret)
	studioFound := false
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "Studio" && owner.Name == studio.Name && owner.UID == studio.UID {
			studioFound = true
			continue
		}
		if owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ModelProviderConnection" && sourceUID != "" && string(owner.UID) == sourceUID {
			continue
		}
		// A generated key with another owner's claim remains under that owner's
		// lifecycle. The collector must not delete it on Studio's behalf.
		return false
	}
	return studioFound
}

func retainedSecretSourceUID(secret *corev1.Secret) string {
	if secret == nil {
		return ""
	}
	if stamped := strings.TrimSpace(secret.Annotations[retainedConnectionUIDAnnotation]); stamped != "" {
		return stamped
	}
	var found types.UID
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion != aiv1alpha1.SchemeGroupVersion.String() || owner.Kind != "ModelProviderConnection" || owner.UID == "" {
			continue
		}
		if found != "" && found != owner.UID {
			return ""
		}
		found = owner.UID
	}
	return string(found)
}

func studioReferencesGeneratedSnapshot(studio *aiv1alpha1.Studio, secret *corev1.Secret, generatedID string) bool {
	if studio == nil || secret == nil || studio.Spec.LLM == nil {
		return false
	}
	for _, model := range studio.Spec.LLM.Models {
		snapshot := model.ConnectionSnapshot
		if snapshot == nil || snapshot.SecretRef == nil {
			continue
		}
		if strings.TrimSpace(snapshot.SecretRef.Name) == secret.Name && snapshot.SecretUID == string(secret.UID) && snapshot.GeneratedSecretID == generatedID {
			return true
		}
	}
	return false
}

func liveConnectionRetainsSecret(secret *corev1.Secret, generatedID, sourceUID string, connections []aiv1alpha1.ModelProviderConnection) bool {
	for i := range connections {
		connection := &connections[i]
		if sourceUID != "" && string(connection.UID) == sourceUID {
			return true
		}
		if secretOwnedByConnection(secret, connection) {
			return true
		}
		if strings.TrimSpace(connection.Spec.SecretRef.Name) == secret.Name && strings.TrimSpace(connection.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation]) == generatedID {
			return true
		}
		if strings.TrimSpace(connection.Annotations[aiv1alpha1.ModelProviderConnectionPendingKeySecretAnnotation]) == secret.Name {
			return true
		}
		if snapshot := connection.Status.ActiveSnapshot; snapshot != nil && snapshot.SecretRef != nil &&
			strings.TrimSpace(snapshot.SecretRef.Name) == secret.Name && snapshot.SecretUID == string(secret.UID) && snapshot.GeneratedSecretID == generatedID {
			return true
		}
	}
	return false
}

func resolveSecret(ctx context.Context, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection) (*corev1.Secret, string, string, string, error) {
	name := strings.TrimSpace(connection.Spec.SecretRef.Name)
	if name == "" {
		return nil, "", "SecretMissing", "spec.secretRef.name is required", nil
	}
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", "SecretMissing", "the referenced key Secret does not exist in namespace default", nil
		}
		return nil, "", "", "", err
	}
	generatedID := strings.TrimSpace(connection.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation])
	keyName := strings.TrimSpace(connection.Spec.SecretKey)
	if keyName == "" {
		keyName = aiv1alpha1.ModelProviderConnectionSecretKey
	}
	key := strings.TrimSpace(string(secret.Data[keyName]))
	generated := generatedID != "" || strings.HasPrefix(secret.Name, aiv1alpha1.ModelProviderConnectionSecretPrefix) || secret.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != ""
	for _, owner := range secret.OwnerReferences {
		generated = generated || (owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ModelProviderConnection")
	}
	if generated && (!validGeneratedSecretIdentity(secret.Name, generatedID) || secret.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != generatedID || secret.Labels[aiv1alpha1.ModelProviderConnectionSecretOwnerLabel] != aiv1alpha1.ModelProviderConnectionSecretOwner || secret.Immutable == nil || !*secret.Immutable || !secretOwnedByConnection(&secret, connection)) {
		return nil, "", "SecretOwnershipInvalid", "the generated key Secret failed its immutable ownership check", nil
	}
	if key == "" {
		return nil, "", "SecretIncomplete", "the referenced key Secret has no nonempty credential entry", nil
	}
	return &secret, key, "", "the key Secret is available", nil
}

func ensureConnectionFinalizer(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection) error {
	if slices.Contains(connection.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer) {
		return nil
	}
	connection.Finalizers = append(connection.Finalizers, aiv1alpha1.ModelProviderConnectionFinalizer)
	if err := c.Update(ctx, connection); err != nil {
		return err
	}
	return reader.Get(ctx, types.NamespacedName{Name: connection.Name}, connection)
}

func secretUnstructured(secret *corev1.Secret) (*unstructured.Unstructured, error) {
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(secret)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: object}, nil
}

func setCondition(status *aiv1alpha1.ModelProviderConnectionStatus, conditionType string, ok bool, why, message string, generation int64, now time.Time) {
	conditionStatus := metav1.ConditionFalse
	if ok {
		conditionStatus = metav1.ConditionTrue
	}
	condition := metav1.Condition{Type: conditionType, Status: conditionStatus, Reason: why, Message: message, ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)}
	prior := meta.FindStatusCondition(status.Conditions, conditionType)
	if prior != nil && prior.Status == conditionStatus {
		condition.LastTransitionTime = prior.LastTransitionTime
	}
	meta.SetStatusCondition(&status.Conditions, condition)
}

func setUnknownCondition(status *aiv1alpha1.ModelProviderConnectionStatus, conditionType, why, message string, generation int64, now time.Time) {
	condition := metav1.Condition{Type: conditionType, Status: metav1.ConditionUnknown, Reason: why, Message: message, ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)}
	prior := meta.FindStatusCondition(status.Conditions, conditionType)
	if prior != nil && prior.Status == metav1.ConditionUnknown {
		condition.LastTransitionTime = prior.LastTransitionTime
	}
	meta.SetStatusCondition(&status.Conditions, condition)
}

func probeResultMatches(status *aiv1alpha1.ModelProviderConnectionStatus, fingerprint string) bool {
	return status != nil && fingerprint != "" && status.LastProbeFingerprint == fingerprint && status.LastProbeTime != nil
}

func reason(reason string) string {
	if reason == "" {
		return "Resolved"
	}
	return reason
}

func credentialMode(key string) string {
	if strings.TrimSpace(key) == "" {
		return ""
	}
	if _, serviceAccount, err := googleCredentialMode(key); err == nil && serviceAccount {
		return "service-account-json"
	}
	return "api-key"
}

func googleCredentialMode(key string) (bool, bool, error) {
	var credential struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if !strings.HasPrefix(strings.TrimSpace(key), "{") {
		return false, false, nil
	}
	if err := json.Unmarshal([]byte(key), &credential); err != nil {
		return false, false, err
	}
	service := credential.Type == "service_account" && credential.ProjectID != "" && credential.ClientEmail != "" && credential.PrivateKey != "" && credential.TokenURI != ""
	return true, service, nil
}

func sameMigratedConnectionConfiguration(spec aiv1alpha1.ModelProviderConnectionSpec, model aiv1alpha1.StudioLLMModel) bool {
	if model.SecretRef == nil {
		return false
	}
	provider := strings.TrimSpace(model.Provider)
	if provider == "" {
		provider = "openai-compatible"
	}
	expected, err := normalizedLegacyConnectionSpec(aiv1alpha1.ModelProviderConnectionSpec{
		Provider: provider, BaseURL: strings.TrimSpace(model.BaseURL), AnthropicWorkspaceID: strings.TrimSpace(model.AnthropicWorkspaceID),
		SecretRef: *model.SecretRef, SecretKey: aiv1alpha1.ModelProviderConnectionSecretKey,
	})
	if err != nil {
		return false
	}
	actual, err := normalizedLegacyConnectionSpec(spec)
	if err != nil {
		return false
	}
	return actual.Provider == expected.Provider && actual.BaseURL == expected.BaseURL &&
		actual.AnthropicWorkspaceID == expected.AnthropicWorkspaceID &&
		strings.TrimSpace(actual.SecretRef.Name) == strings.TrimSpace(expected.SecretRef.Name) &&
		actual.SecretKey == expected.SecretKey
}

func normalizedLegacyConnectionSpec(spec aiv1alpha1.ModelProviderConnectionSpec) (aiv1alpha1.ModelProviderConnectionSpec, error) {
	provider, baseURL, workspaceID, err := api.ResolveModelProviderConnectionSettings(&aiv1alpha1.ModelProviderConnection{
		Spec: spec,
	})
	if err != nil {
		return aiv1alpha1.ModelProviderConnectionSpec{}, err
	}
	spec.Provider = provider
	spec.BaseURL = baseURL
	spec.AnthropicWorkspaceID = workspaceID
	secretKey := strings.TrimSpace(spec.SecretKey)
	if secretKey == "" {
		secretKey = aiv1alpha1.ModelProviderConnectionSecretKey
	}
	spec.SecretKey = secretKey
	spec.SecretRef.Name = strings.TrimSpace(spec.SecretRef.Name)
	return spec, nil
}

func secretOwnedByConnection(secret *corev1.Secret, connection *aiv1alpha1.ModelProviderConnection) bool {
	for _, ref := range secret.OwnerReferences {
		if isConnectionOwner(ref, connection) {
			return true
		}
	}
	return false
}

func secretOwnedByStudio(secret *corev1.Secret, studio *aiv1alpha1.Studio) bool {
	if secret == nil || studio == nil {
		return false
	}
	for _, ref := range secret.OwnerReferences {
		if ref.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && ref.Kind == "Studio" && ref.Name == studio.Name && ref.UID == studio.UID {
			return true
		}
	}
	return false
}

func validGeneratedSecretIdentity(name, generatedID string) bool {
	if len(generatedID) != 32 || name != aiv1alpha1.ModelProviderConnectionSecretPrefix+generatedID {
		return false
	}
	decoded, err := hex.DecodeString(generatedID)
	return err == nil && len(decoded) == 16
}

func isConnectionOwner(ref metav1.OwnerReference, connection *aiv1alpha1.ModelProviderConnection) bool {
	return connection != nil && ref.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && ref.Kind == "ModelProviderConnection" && ref.Name == connection.Name && ref.UID == connection.UID
}

func hasConnectionOwner(obj client.Object, connection *aiv1alpha1.ModelProviderConnection) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if isConnectionOwner(ref, connection) {
			return true
		}
	}
	return false
}

// WatchMapperScope returns the connection names that one Studio event should
// enqueue; the caller's multicluster event handler supplies the tenant scope.
func WatchMapperScope(studio *aiv1alpha1.Studio, connections []aiv1alpha1.ModelProviderConnection) []string {
	seen := map[string]bool{}
	var names []string
	for i := range connections {
		if !seen[connections[i].Name] {
			seen[connections[i].Name] = true
			names = append(names, connections[i].Name)
		}
	}
	if studio != nil && studio.Spec.LLM != nil {
		for _, model := range studio.Spec.LLM.Models {
			if !model.Archived && model.ConnectionRef == "" && model.SecretRef != nil && model.ID != "" && !seen[model.ID] {
				seen[model.ID] = true
				names = append(names, model.ID)
			}
		}
	}
	return names
}

// Persist identities separately from caller-writable model rows and connection
// lifetime. No external Secret metadata or ownership is changed.
func protectExternalCredentials(ctx context.Context, c client.Client, reader client.Reader, connection *aiv1alpha1.ModelProviderConnection, secret *corev1.Secret) error {
	var identities []aiv1alpha1.StudioModelCredentialIdentity
	if active := connection.Status.ActiveSnapshot; active != nil && active.GeneratedSecretID == "" && active.SecretRef != nil && active.SecretUID != "" {
		identities = append(identities, aiv1alpha1.StudioModelCredentialIdentity{SecretName: active.SecretRef.Name, SecretUID: active.SecretUID})
	}
	if secret != nil && connection.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] == "" {
		identities = append(identities, aiv1alpha1.StudioModelCredentialIdentity{SecretName: secret.Name, SecretUID: string(secret.UID)})
	}
	if len(identities) == 0 {
		return nil
	}
	var studio aiv1alpha1.Studio
	if err := reader.Get(ctx, types.NamespacedName{Name: aiv1alpha1.StudioName}, &studio); err != nil {
		return err
	}
	changed := false
	for _, identity := range identities {
		if identity.SecretName == "" || identity.SecretUID == "" {
			return fmt.Errorf("external model credential identity is incomplete")
		}
		if slices.Contains(studio.Status.ProtectedModelCredentials, identity) {
			continue
		}
		if len(studio.Status.ProtectedModelCredentials) >= 1000 {
			return fmt.Errorf("protected model credential history is full")
		}
		studio.Status.ProtectedModelCredentials = append(studio.Status.ProtectedModelCredentials, identity)
		changed = true
	}
	if !changed {
		return nil
	}
	return c.Status().Update(ctx, &studio)
}
