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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/anthropicmodel"
	"github.com/railgrid/provider-sdk/dataplane"
)

type modelProviderConnectionTestRequest struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ModelProviderConnectionFingerprint hashes only non-secret configuration and
// the Secret's Kubernetes identity/version. It is a stale-draft check, not a
// credential verifier; key material is never included in the hash input.
func ModelProviderConnectionFingerprint(connection *aiv1alpha1.ModelProviderConnection, secret *unstructured.Unstructured) (string, error) {
	if connection == nil || secret == nil {
		return "", errors.New("connection and Secret are required for a fingerprint")
	}
	type material struct {
		UID                   string `json:"uid"`
		Generation            int64  `json:"generation"`
		Provider              string `json:"provider"`
		BaseURL               string `json:"baseURL"`
		AnthropicWorkspaceID  string `json:"anthropicWorkspaceID"`
		SecretName            string `json:"secretName"`
		SecretKey             string `json:"secretKey"`
		SecretUID             string `json:"secretUID"`
		SecretResourceVersion string `json:"secretResourceVersion"`
		GeneratedSecretID     string `json:"generatedSecretID"`
	}
	secretName := strings.TrimSpace(connection.Spec.SecretRef.Name)
	secretKey := strings.TrimSpace(connection.Spec.SecretKey)
	if secretKey == "" {
		secretKey = aiv1alpha1.ModelProviderConnectionSecretKey
	}
	value := material{
		UID:                   string(connection.UID),
		Generation:            connection.Generation,
		Provider:              canonicalModelProvider(connection.Spec.Provider),
		BaseURL:               strings.TrimRight(strings.TrimSpace(connection.Spec.BaseURL), "/"),
		AnthropicWorkspaceID:  strings.TrimSpace(connection.Spec.AnthropicWorkspaceID),
		SecretName:            secretName,
		SecretKey:             secretKey,
		SecretUID:             string(secret.GetUID()),
		SecretResourceVersion: secret.GetResourceVersion(),
		GeneratedSecretID:     strings.TrimSpace(connection.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation]),
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode model provider connection fingerprint: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalModelProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return defaultProjectLLMProvider
	}
	return strings.ToLower(provider)
}

func readModelProviderConnection(ctx context.Context, c *asclient.Client, name string) (*aiv1alpha1.ModelProviderConnection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, newValidationError("a provider connection is required")
	}
	resource, err := c.Resource(modelProviderConnectionResource, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var connection aiv1alpha1.ModelProviderConnection
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resource.Object, &connection); err != nil {
		return nil, fmt.Errorf("decode model provider connection %q: %w", name, err)
	}
	if !connection.DeletionTimestamp.IsZero() {
		return nil, apierrors.NewConflict(modelProviderConnectionGVR.GroupResource(), name, errors.New("provider connection is being removed"))
	}
	return &connection, nil
}

func connectionSettings(connection *aiv1alpha1.ModelProviderConnection) (projectLLMSettings, error) {
	if connection == nil {
		return projectLLMSettings{}, errors.New("provider connection is required")
	}
	provider := canonicalModelProvider(connection.Spec.Provider)
	baseURL := strings.TrimSpace(connection.Spec.BaseURL)
	if baseURL == "" {
		if provider == projectLLMProviderGoogle {
			baseURL = defaultProjectLLMGoogleBaseURL
		} else {
			baseURL = defaultProjectLLMBaseURL
		}
	}
	baseURL, err := normalizeLLMBaseURL(baseURL)
	if err != nil {
		return projectLLMSettings{}, err
	}
	if err := validateProjectLLMBaseURL(provider, baseURL); err != nil {
		return projectLLMSettings{}, err
	}
	workspaceID := strings.TrimSpace(connection.Spec.AnthropicWorkspaceID)
	if workspaceID != "" {
		if !anthropicmodel.UsesEndpoint(baseURL) || !validProjectLLMAnthropicWorkspaceID(workspaceID) {
			return projectLLMSettings{}, newValidationError("anthropicWorkspaceID requires a valid workspace ID and the official Anthropic Messages endpoint")
		}
	}
	return projectLLMSettings{
		Provider:             provider,
		BaseURL:              baseURL,
		AnthropicWorkspaceID: workspaceID,
	}, nil
}

func (s *Server) authorizeModelProviderConnectionUse(ctx context.Context, id identity, name string) error {
	allowed, err := s.authorizeCaller(ctx, id, dataplane.ResourceAttributes{
		Group:    modelProviderConnectionGVR.Group,
		Version:  modelProviderConnectionGVR.Version,
		Resource: modelProviderConnectionGVR.Resource,
		Name:     strings.TrimSpace(name),
		Verb:     "use",
	})
	if err != nil {
		return fmt.Errorf("authorize use of model provider connection %q: %w", name, err)
	}
	if !allowed {
		return apierrors.NewForbidden(modelProviderConnectionGVR.GroupResource(), name, errors.New("use permission is required to call this model provider connection"))
	}
	return nil
}

func readModelProviderConnectionSecret(ctx context.Context, c *asclient.Client, connection *aiv1alpha1.ModelProviderConnection) (*unstructured.Unstructured, string, error) {
	if connection == nil {
		return nil, "", errors.New("provider connection is required")
	}
	secretName := strings.TrimSpace(connection.Spec.SecretRef.Name)
	if secretName == "" {
		return nil, "", newValidationError("provider connection does not name a key Secret")
	}
	secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", newValidationError("provider connection key is missing")
		}
		return nil, "", err
	}
	if err := ValidateModelProviderConnectionSecret(connection, secret); err != nil {
		return nil, "", err
	}
	key := aiv1alpha1.ModelProviderConnectionSecretKey
	value := strings.TrimSpace(secretDataValue(secret, key))
	if value == "" {
		return nil, "", newValidationError("provider connection key Secret is empty")
	}
	return secret, value, nil
}

// ValidateModelProviderConnectionSecret checks the Secret-writer opt-in and
// fixed credential entry before a caller-writable reference may use the Secret.
func ValidateModelProviderConnectionSecret(connection *aiv1alpha1.ModelProviderConnection, secret *unstructured.Unstructured) error {
	if connection == nil || secret == nil {
		return newValidationError("provider connection and key Secret are required")
	}
	if err := validateModelProviderSecretKey(connection.Spec.SecretKey); err != nil {
		return err
	}
	secretName := strings.TrimSpace(connection.Spec.SecretRef.Name)
	if secretName == "" || secret.GetName() != secretName {
		return newValidationError("provider connection key Secret identity does not match")
	}
	generatedID := strings.TrimSpace(connection.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation])
	generated := generatedID != "" || strings.HasPrefix(secretName, aiv1alpha1.ModelProviderConnectionSecretPrefix) || secret.GetAnnotations()[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != ""
	for _, owner := range secret.GetOwnerReferences() {
		generated = generated || (owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ModelProviderConnection")
	}
	if generated {
		labels, _, _ := unstructured.NestedStringMap(secret.Object, "metadata", "labels")
		annotations, _, _ := unstructured.NestedStringMap(secret.Object, "metadata", "annotations")
		immutable, _, _ := unstructured.NestedBool(secret.Object, "immutable")
		if !validGeneratedSecretIdentity(secretName, generatedID) || labels[aiv1alpha1.ModelProviderConnectionSecretOwnerLabel] != aiv1alpha1.ModelProviderConnectionSecretOwner ||
			annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != generatedID || !immutable || !secretOwnedByConnection(secret, connection) {
			return newValidationError("provider connection key Secret ownership could not be confirmed")
		}
	}
	if !generated {
		return validateExternalModelProviderSecret(secret)
	}
	return nil
}

func validateModelProviderSecretKey(key string) error {
	if key = strings.TrimSpace(key); key != "" && key != aiv1alpha1.ModelProviderConnectionSecretKey {
		return newValidationError("provider connection credentials must use the apiKey Secret entry")
	}
	return nil
}

func validateExternalModelProviderSecret(secret *unstructured.Unstructured) error {
	if secret.GetLabels()[aiv1alpha1.ModelProviderConnectionSecretOwnerLabel] != aiv1alpha1.ModelProviderConnectionSecretOwner {
		return newValidationError("provider connection key Secret is not marked for App Studio use")
	}
	if strings.HasPrefix(secret.GetName(), aiv1alpha1.ModelProviderConnectionSecretPrefix) || secret.GetAnnotations()[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != "" {
		return newValidationError("generated provider key Secret requires its generated identity")
	}
	for _, owner := range secret.GetOwnerReferences() {
		if owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ModelProviderConnection" {
			return newValidationError("generated provider key Secret requires its generated identity")
		}
	}
	return nil
}

func secretOwnedByConnection(secret *unstructured.Unstructured, connection *aiv1alpha1.ModelProviderConnection) bool {
	if secret == nil || connection == nil || connection.UID == "" {
		return false
	}
	ownerRefs, _, _ := unstructured.NestedSlice(secret.Object, "metadata", "ownerReferences")
	for _, item := range ownerRefs {
		ref, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if ref["apiVersion"] == aiv1alpha1.SchemeGroupVersion.String() &&
			ref["kind"] == "ModelProviderConnection" && ref["name"] == connection.Name && ref["uid"] == string(connection.UID) {
			return true
		}
	}
	return false
}

func (s *Server) resolveSavedConnectionCredential(
	ctx context.Context,
	id identity,
	c *asclient.Client,
	name, expectedFingerprint, draftProvider, draftBaseURL, draftWorkspaceID string,
) (projectLLMSettings, error) {
	settings, _, err := s.resolveSavedConnectionCredentialForProbe(ctx, id, c, name, expectedFingerprint, draftProvider, draftBaseURL, draftWorkspaceID)
	return settings, err
}

type modelProviderConnectionProbeTarget struct {
	name          string
	connectionUID string
	generation    int64
	fingerprint   string
}

func (s *Server) resolveSavedConnectionCredentialForProbe(
	ctx context.Context,
	id identity,
	c *asclient.Client,
	name, expectedFingerprint, draftProvider, draftBaseURL, draftWorkspaceID string,
) (projectLLMSettings, *modelProviderConnectionProbeTarget, error) {
	connection, err := readModelProviderConnection(ctx, c, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return projectLLMSettings{}, nil, apierrors.NewNotFound(modelProviderConnectionGVR.GroupResource(), name)
		}
		return projectLLMSettings{}, nil, err
	}
	// Saved-model probes can keep using the controller-admitted transport
	// while a different desired configuration is waiting for revision space.
	// Such a probe validates only the admitted transport, not the pending spec.
	// The publisher intentionally declines the status write and logs that result
	// when the desired fingerprint differs.
	if active := connection.Status.ActiveSnapshot; active != nil && expectedFingerprint == active.ConnectionFingerprint && canonicalModelProvider(draftProvider) == active.Provider && strings.TrimRight(strings.TrimSpace(draftBaseURL), "/") == strings.TrimRight(active.BaseURL, "/") && strings.TrimSpace(draftWorkspaceID) == active.AnthropicWorkspaceID {
		key, err := s.readAuthorizedConnectionSnapshotCredential(ctx, id, c, connection.Name, active)
		if err != nil {
			return projectLLMSettings{}, nil, err
		}
		return projectLLMSettings{Provider: active.Provider, BaseURL: active.BaseURL, AnthropicWorkspaceID: active.AnthropicWorkspaceID, APIKey: key}, &modelProviderConnectionProbeTarget{
			name: connection.Name, connectionUID: string(connection.UID), generation: connection.Generation,
			fingerprint: active.ConnectionFingerprint,
		}, nil
	}
	stored, err := connectionSettings(connection)
	if err != nil {
		return projectLLMSettings{}, nil, err
	}
	baseURL, err := normalizeLLMBaseURL(draftBaseURL)
	if err != nil {
		return projectLLMSettings{}, nil, err
	}
	if canonicalModelProvider(draftProvider) != stored.Provider || strings.TrimRight(baseURL, "/") != strings.TrimRight(stored.BaseURL, "/") || strings.TrimSpace(draftWorkspaceID) != stored.AnthropicWorkspaceID {
		return projectLLMSettings{}, nil, newValidationError("enter a key before testing a changed provider, endpoint, or Anthropic workspace")
	}
	if err := s.authorizeModelProviderConnectionUse(ctx, id, connection.Name); err != nil {
		return projectLLMSettings{}, nil, err
	}
	secret, key, err := readModelProviderConnectionSecret(ctx, c, connection)
	if err != nil {
		return projectLLMSettings{}, nil, err
	}
	fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		return projectLLMSettings{}, nil, err
	}
	if expectedFingerprint == "" || expectedFingerprint != fingerprint || connection.Status.ConnectionFingerprint != fingerprint {
		return projectLLMSettings{}, nil, apierrors.NewConflict(modelProviderConnectionGVR.GroupResource(), connection.Name, errors.New("provider connection fingerprint is stale; refresh and verify again"))
	}
	stored.APIKey = key
	return stored, &modelProviderConnectionProbeTarget{
		name: connection.Name, connectionUID: string(connection.UID), generation: connection.Generation,
		fingerprint: fingerprint,
	}, nil
}

func readConnectionSnapshotCredential(ctx context.Context, c *asclient.Client, snapshot *aiv1alpha1.StudioLLMConnectionSnapshot) (string, error) {
	if snapshot == nil || snapshot.SecretRef == nil || strings.TrimSpace(snapshot.SecretRef.Name) == "" {
		return "", newValidationError("model revision does not name its pinned key Secret")
	}
	if strings.TrimSpace(snapshot.ConnectionUID) == "" || strings.TrimSpace(snapshot.SecretUID) == "" || strings.TrimSpace(snapshot.SecretResourceVersion) == "" || strings.TrimSpace(snapshot.ConnectionFingerprint) == "" {
		return "", newValidationError("model revision does not have a complete immutable provider connection snapshot")
	}
	secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, strings.TrimSpace(snapshot.SecretRef.Name), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", newValidationError("the pinned model credential version is no longer available")
		}
		return "", err
	}
	if snapshot.SecretUID != "" && string(secret.GetUID()) != snapshot.SecretUID {
		return "", newValidationError("the pinned model credential Secret was replaced")
	}
	if err := validateModelProviderSecretKey(snapshot.SecretKey); err != nil {
		return "", err
	}
	generatedID := strings.TrimSpace(snapshot.GeneratedSecretID)
	if generatedID != "" {
		labels, _, _ := unstructured.NestedStringMap(secret.Object, "metadata", "labels")
		annotations, _, _ := unstructured.NestedStringMap(secret.Object, "metadata", "annotations")
		immutable, _, _ := unstructured.NestedBool(secret.Object, "immutable")
		if !validGeneratedSecretIdentity(snapshot.SecretRef.Name, generatedID) || labels[aiv1alpha1.ModelProviderConnectionSecretOwnerLabel] != aiv1alpha1.ModelProviderConnectionSecretOwner ||
			annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != generatedID || !immutable {
			return "", newValidationError("the pinned model credential version failed its ownership check")
		}
		if !snapshotSecretOwnedByStudioOrConnection(secret, snapshot, ctx, c) {
			return "", newValidationError("the pinned model credential version is not owned by its provider connection or Studio")
		}
		// Immutable generated Secrets may receive metadata-only owner-reference
		// updates when a connection is removed. Their UID + immutable bit +
		// generated ID continue to identify the exact key bytes.
	} else {
		if err := validateExternalModelProviderSecret(secret); err != nil {
			return "", err
		}
		if snapshot.SecretResourceVersion != "" && string(secret.GetResourceVersion()) != snapshot.SecretResourceVersion {
			return "", newValidationError("the pinned model credential Secret changed after this revision was created")
		}
	}
	key := strings.TrimSpace(snapshot.SecretKey)
	if key == "" {
		key = aiv1alpha1.ModelProviderConnectionSecretKey
	}
	// Retirement is committed to controller-owned Studio status before key
	// deletion. Even a native Studio writer cannot reactivate a retired UID.
	if err := ensureModelCredentialNotRetired(ctx, c, snapshot.SecretRef.Name, snapshot.SecretUID); err != nil {
		return "", err
	}
	value := strings.TrimSpace(secretDataValue(secret, key))
	if value == "" {
		return "", newValidationError("the pinned model credential Secret is empty")
	}
	return value, nil
}

// readAuthorizedConnectionSnapshotCredential validates a newly selected
// model revision against the live, name-scoped connection after the caller's
// `use` SAR. Studio.spec is caller writable, so its snapshot is only a
// version token: it must match the currently authorized connection and the
// current Secret identity before a new run can use it. Resumable runs use the
// server-written snapshot in their audit and read through
// readConnectionSnapshotCredential instead.
func (s *Server) readAuthorizedConnectionSnapshotCredential(
	ctx context.Context,
	id identity,
	c *asclient.Client,
	connectionRef string,
	snapshot *aiv1alpha1.StudioLLMConnectionSnapshot,
) (string, error) {
	connectionRef = strings.TrimSpace(connectionRef)
	if connectionRef == "" || snapshot == nil || snapshot.SecretRef == nil {
		return "", newValidationError("model binding is missing its provider connection snapshot")
	}
	if snapshot.ConnectionRef != connectionRef {
		return "", newValidationError("model binding snapshot does not match its provider connection")
	}
	if err := s.authorizeModelProviderConnectionUse(ctx, id, connectionRef); err != nil {
		return "", err
	}
	connection, err := readModelProviderConnection(ctx, c, connectionRef)
	if err != nil {
		return "", err
	}
	if connection.UID == "" || string(connection.UID) != snapshot.ConnectionUID {
		return "", staleConnectionSnapshot(connectionRef)
	}
	if connection.Status.ActiveSnapshot == nil || !reflect.DeepEqual(connection.Status.ActiveSnapshot, snapshot) {
		return "", staleConnectionSnapshot(connectionRef)
	}
	key, err := readConnectionSnapshotCredential(ctx, c, snapshot)
	if err != nil {
		return "", err
	}
	latest, err := readModelProviderConnection(ctx, c, connectionRef)
	if err != nil {
		return "", err
	}
	if latest.UID != connection.UID || !reflect.DeepEqual(latest.Status.ActiveSnapshot, snapshot) {
		return "", staleConnectionSnapshot(connectionRef)
	}
	return key, nil
}

func staleConnectionSnapshot(name string) error {
	return apierrors.NewConflict(modelProviderConnectionGVR.GroupResource(), name, errors.New("model binding revision is stale and waiting for provider connection rollover"))
}

func snapshotSecretOwnedByStudioOrConnection(secret *unstructured.Unstructured, snapshot *aiv1alpha1.StudioLLMConnectionSnapshot, ctx context.Context, c *asclient.Client) bool {
	ownerRefs, _, _ := unstructured.NestedSlice(secret.Object, "metadata", "ownerReferences")
	for _, item := range ownerRefs {
		ref, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if snapshot.ConnectionUID != "" && ref["apiVersion"] == aiv1alpha1.SchemeGroupVersion.String() &&
			ref["kind"] == "ModelProviderConnection" && ref["name"] == snapshot.ConnectionRef && ref["uid"] == snapshot.ConnectionUID {
			return true
		}
	}
	studio, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, item := range ownerRefs {
		ref, ok := item.(map[string]interface{})
		if ok && ref["apiVersion"] == aiv1alpha1.SchemeGroupVersion.String() && ref["kind"] == "Studio" && ref["name"] == aiv1alpha1.StudioName && ref["uid"] == string(studio.GetUID()) {
			return true
		}
	}
	return false
}

// ModelProviderConnectionSnapshot creates a non-secret immutable binding
// snapshot from the connection and its resolved key Secret.
func ModelProviderConnectionSnapshot(connection *aiv1alpha1.ModelProviderConnection, secret *unstructured.Unstructured) (*aiv1alpha1.StudioLLMConnectionSnapshot, error) {
	if err := ValidateModelProviderConnectionSecret(connection, secret); err != nil {
		return nil, err
	}
	settings, err := connectionSettings(connection)
	if err != nil {
		return nil, err
	}
	fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		return nil, err
	}
	if connection.UID == "" || secret.GetUID() == "" || secret.GetResourceVersion() == "" {
		return nil, errors.New("connection and Secret must have Kubernetes UIDs and resourceVersions before snapshot creation")
	}
	secretKey := strings.TrimSpace(connection.Spec.SecretKey)
	if secretKey == "" {
		secretKey = aiv1alpha1.ModelProviderConnectionSecretKey
	}
	generatedID := strings.TrimSpace(connection.Annotations[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation])
	return &aiv1alpha1.StudioLLMConnectionSnapshot{
		ConnectionRef: connection.Name, ConnectionUID: string(connection.UID),
		Provider: settings.Provider, BaseURL: settings.BaseURL, AnthropicWorkspaceID: settings.AnthropicWorkspaceID,
		SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: strings.TrimSpace(connection.Spec.SecretRef.Name)},
		SecretKey: secretKey, ConnectionFingerprint: fingerprint, GeneratedSecretID: generatedID,
		SecretUID: string(secret.GetUID()), SecretResourceVersion: secret.GetResourceVersion(),
	}, nil
}

func validateProjectLLMCredentialMode(mode, provider, apiKey string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return nil
	}
	if mode != "api-key" && mode != "service-account-json" {
		return newValidationError("credentialMode must be api-key or service-account-json")
	}
	if mode == "service-account-json" {
		if !strings.EqualFold(strings.TrimSpace(provider), projectLLMProviderGoogle) {
			return newValidationError("service-account-json is supported only for Google AI Studio / Vertex connections")
		}
		if _, serviceAccount, err := googleServiceAccountCredentialFromJSON(apiKey); err != nil || !serviceAccount {
			return newValidationError("credentialMode service-account-json requires a valid Google service-account JSON key")
		}
	}
	return nil
}

func (s *Server) testModelProviderConnection(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request modelProviderConnectionTestRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	connection, err := readModelProviderConnection(r.Context(), c, name)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if err := s.authorizeModelProviderConnectionUse(r.Context(), id, connection.Name); err != nil {
		writeProjectError(w, err)
		return
	}
	secret, apiKey, err := readModelProviderConnectionSecret(r.Context(), c, connection)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	settings, err := connectionSettings(connection)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	settings.APIKey = apiKey
	settings.Model = strings.TrimSpace(request.Model)
	settings.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	if settings.Model == "" {
		writeProjectError(w, newValidationError("model is required to test a provider connection"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	testErr := verifyProjectLLMConnection(ctx, settings)
	probe := modelProviderConnectionProbeOutcome{
		reachable: metav1.ConditionTrue,
		reason:    "Reachable",
		message:   "The provider accepted the model connection check.",
	}
	if testErr != nil {
		probe = modelProviderConnectionProbeOutcome{
			reachable: metav1.ConditionFalse,
			reason:    "ProbeFailed",
			message:   "Provider model testing failed; check the model ID, endpoint, and credential.",
			lastError: "Provider model testing failed; check the model ID, endpoint, and credential.",
		}
	}
	target := newModelProviderConnectionProbeTarget(connection, fingerprint)
	if _, publishErr := publishModelProviderConnectionProbe(r.Context(), c, target, probe, time.Now()); publishErr != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "The model check ran, but its verification status could not be saved. Retry the check.")
		return
	}
	if testErr != nil {
		writeProjectLLMConnectionTestError(w, testErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) discoverModelProviderConnection(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request struct{}
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	connection, err := readModelProviderConnection(r.Context(), c, name)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if err := s.authorizeModelProviderConnectionUse(r.Context(), id, connection.Name); err != nil {
		writeProjectError(w, err)
		return
	}
	secret, apiKey, err := readModelProviderConnectionSecret(r.Context(), c, connection)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	settings, err := connectionSettings(connection)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	settings.APIKey = apiKey
	client := s.llmDiscoveryHTTPClient
	if client == nil {
		client = newProjectLLMDiscoveryHTTPClient()
	}
	response, err := discoverProjectLLMModels(r.Context(), client, settings)
	var probe modelProviderConnectionProbeOutcome
	if err != nil {
		probe = failedModelProviderDiscoveryOutcome(connection, apiKey, err)
		if _, publishErr := publishModelProviderConnectionProbe(r.Context(), c, newModelProviderConnectionProbeTarget(connection, fingerprint), probe, time.Now()); publishErr != nil {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Model discovery ran, but its verification status could not be saved. Retry discovery.")
			return
		}
		writeProjectLLMDiscoveryError(w, err)
		return
	}
	probe = modelProviderConnectionProbeOutcome{
		reachable:     metav1.ConditionTrue,
		reason:        "Reachable",
		message:       "The provider endpoint answered model discovery.",
		models:        discoveredModelIDs(response.Models),
		replaceModels: true,
	}
	if _, publishErr := publishModelProviderConnectionProbe(r.Context(), c, newModelProviderConnectionProbeTarget(connection, fingerprint), probe, time.Now()); publishErr != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Model discovery succeeded, but its verification status could not be saved. Retry discovery.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type modelProviderConnectionProbeOutcome struct {
	reachable     metav1.ConditionStatus
	reason        string
	message       string
	lastError     string
	models        []string
	replaceModels bool
}

func newModelProviderConnectionProbeTarget(connection *aiv1alpha1.ModelProviderConnection, fingerprint string) *modelProviderConnectionProbeTarget {
	if connection == nil {
		return nil
	}
	return &modelProviderConnectionProbeTarget{
		name: connection.Name, connectionUID: string(connection.UID), generation: connection.Generation,
		fingerprint: fingerprint,
	}
}

func failedModelProviderDiscoveryOutcome(connection *aiv1alpha1.ModelProviderConnection, apiKey string, err error) modelProviderConnectionProbeOutcome {
	if IsModelProviderDiscoveryUnsupported(err) || vertexServiceAccountDiscoveryUnavailable(connection, apiKey) {
		message := "Automatic model discovery is unavailable; enter a model ID and test it."
		if vertexServiceAccountDiscoveryUnavailable(connection, apiKey) {
			message = "Automatic model discovery is unavailable for Vertex service-account credentials; enter a model ID and test it."
		}
		return modelProviderConnectionProbeOutcome{
			reachable:     metav1.ConditionUnknown,
			reason:        "DiscoveryUnavailable",
			message:       message,
			lastError:     message,
			replaceModels: true,
		}
	}
	message := "Provider model discovery failed; check the endpoint and credential, or enter a model ID and test it."
	return modelProviderConnectionProbeOutcome{
		reachable:     metav1.ConditionFalse,
		reason:        "ProbeFailed",
		message:       message,
		lastError:     message,
		replaceModels: true,
	}
}

func vertexServiceAccountDiscoveryUnavailable(connection *aiv1alpha1.ModelProviderConnection, apiKey string) bool {
	if connection == nil || !strings.EqualFold(connection.Spec.Provider, projectLLMProviderGoogle) || !strings.Contains(strings.ToLower(connection.Spec.BaseURL), "aiplatform.googleapis.com") {
		return false
	}
	_, serviceAccount, err := googleServiceAccountCredentialFromJSON(apiKey)
	return err == nil && serviceAccount
}

func discoveredModelIDs(models []ProjectLLMDiscoveredModelView) []string {
	ids := make([]string, 0, min(len(models), 500))
	seen := make(map[string]struct{}, min(len(models), 500))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || len(id) > 253 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) == 500 {
			break
		}
	}
	return ids
}

func publishModelProviderConnectionProbe(
	ctx context.Context,
	c *asclient.Client,
	target *modelProviderConnectionProbeTarget,
	outcome modelProviderConnectionProbeOutcome,
	now time.Time,
) (bool, error) {
	if target == nil || strings.TrimSpace(target.name) == "" || len(target.fingerprint) != 64 {
		return false, errors.New("a versioned model provider connection probe target is required")
	}
	if c == nil || c.Dynamic() == nil {
		return false, errors.New("model provider connection status client is unavailable")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if outcome.reachable != metav1.ConditionTrue && outcome.reachable != metav1.ConditionFalse && outcome.reachable != metav1.ConditionUnknown {
		return false, errors.New("model provider connection probe status is invalid")
	}
	if strings.TrimSpace(outcome.reason) == "" || strings.TrimSpace(outcome.message) == "" {
		return false, errors.New("model provider connection probe status needs a reason and message")
	}

	resource := c.Dynamic().Resource(modelProviderConnectionsGVR)
	for attempt := 0; attempt < 4; attempt++ {
		object, err := c.Resource(modelProviderConnectionResource, "").Get(ctx, target.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			klog.FromContext(ctx).Info("Authorized model provider connection probe result was not recorded", "connection", target.name, "reason", "checked connection or credential version is no longer current")
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var current aiv1alpha1.ModelProviderConnection
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &current); err != nil {
			return false, fmt.Errorf("decode model provider connection %q before status update: %w", target.name, err)
		}
		if !current.DeletionTimestamp.IsZero() || string(current.UID) != target.connectionUID || current.Generation != target.generation {
			klog.FromContext(ctx).Info("Authorized model provider connection probe result was not recorded", "connection", target.name, "reason", "checked connection or credential version is no longer current")
			return false, nil
		}
		secretName := strings.TrimSpace(current.Spec.SecretRef.Name)
		if secretName == "" {
			klog.FromContext(ctx).Info("Authorized model provider connection probe result was not recorded", "connection", target.name, "reason", "checked connection or credential version is no longer current")
			return false, nil
		}
		secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, secretName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			klog.FromContext(ctx).Info("Authorized model provider connection probe result was not recorded", "connection", target.name, "reason", "checked connection or credential version is no longer current")
			return false, nil
		}
		if err != nil {
			return false, err
		}
		currentFingerprint, err := ModelProviderConnectionFingerprint(&current, secret)
		if err != nil {
			return false, err
		}
		if currentFingerprint != target.fingerprint {
			klog.FromContext(ctx).Info("Authorized model provider connection probe result was not recorded", "connection", target.name, "reason", "checked connection or credential version is no longer current")
			return false, nil
		}
		if current.ResourceVersion == "" {
			return false, errors.New("model provider connection status update requires a resourceVersion")
		}

		status := current.Status
		status.Conditions = append([]metav1.Condition(nil), current.Status.Conditions...)
		status.Models = append([]string(nil), current.Status.Models...)
		if status.LastProbeFingerprint != target.fingerprint {
			status.Models = nil
		}
		if outcome.replaceModels {
			status.Models = append([]string(nil), outcome.models...)
		}
		status.LastProbeFingerprint = target.fingerprint
		status.LastProbeTime = &metav1.Time{Time: now.UTC()}
		status.LastProbeError = outcome.lastError
		condition := metav1.Condition{
			Type: "Reachable", Status: outcome.reachable, Reason: outcome.reason,
			Message: outcome.message, ObservedGeneration: current.Generation,
			LastTransitionTime: metav1.NewTime(now.UTC()),
		}
		if previous := apiMeta.FindStatusCondition(status.Conditions, condition.Type); previous != nil && previous.Status == condition.Status {
			condition.LastTransitionTime = previous.LastTransitionTime
		}
		apiMeta.SetStatusCondition(&status.Conditions, condition)
		current.Status = status
		encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&current)
		if err != nil {
			return false, fmt.Errorf("encode model provider connection %q status: %w", target.name, err)
		}
		updated := &unstructured.Unstructured{Object: encoded}
		if _, err := resource.UpdateStatus(ctx, updated, metav1.UpdateOptions{}); apierrors.IsConflict(err) {
			continue
		} else if err != nil {
			return false, fmt.Errorf("publish authorized model provider connection probe: %w", err)
		}
		return true, nil
	}
	return false, apierrors.NewConflict(modelProviderConnectionGVR.GroupResource(), target.name, errors.New("connection status changed while publishing the probe result"))
}

// ResolveModelProviderConnectionSettings exposes only transport settings for
// controller probes. It never reads or returns credential data.
func ResolveModelProviderConnectionSettings(connection *aiv1alpha1.ModelProviderConnection) (provider, baseURL, workspaceID string, err error) {
	settings, err := connectionSettings(connection)
	if err != nil {
		return "", "", "", err
	}
	return settings.Provider, settings.BaseURL, settings.AnthropicWorkspaceID, nil
}

// DiscoverModelProviderConnection performs bounded discovery for a saved
// connection. The caller must authorize `use` and resolve the Secret first.
func DiscoverModelProviderConnection(ctx context.Context, client *http.Client, connection *aiv1alpha1.ModelProviderConnection, apiKey string) (ProjectLLMModelDiscoveryResponse, error) {
	if client == nil {
		client = newProjectLLMDiscoveryHTTPClient()
	}
	settings, err := connectionSettings(connection)
	if err != nil {
		return ProjectLLMModelDiscoveryResponse{}, err
	}
	settings.APIKey = apiKey
	return discoverProjectLLMModels(ctx, client, settings)
}

// IsModelProviderDiscoveryUnsupported reports provider responses that mean
// automatic listing is unavailable while a configured model may still work.
func IsModelProviderDiscoveryUnsupported(err error) bool {
	var upstream *projectLLMDiscoveryUpstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode == http.StatusNotFound || upstream.StatusCode == http.StatusMethodNotAllowed || upstream.StatusCode == http.StatusNotImplemented
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "automatic model discovery is not available")
}

func validGeneratedSecretIdentity(name, generatedID string) bool {
	if len(generatedID) != 32 || strings.TrimSpace(name) != aiv1alpha1.ModelProviderConnectionSecretPrefix+generatedID {
		return false
	}
	decoded, err := hex.DecodeString(generatedID)
	return err == nil && len(decoded) == 16
}

// VerifyModelProviderConnection verifies one model through the connection's
// configured provider protocol.
func VerifyModelProviderConnection(ctx context.Context, connection *aiv1alpha1.ModelProviderConnection, apiKey, model, reasoningEffort string) error {
	settings, err := connectionSettings(connection)
	if err != nil {
		return err
	}
	settings.APIKey = apiKey
	settings.Model = strings.TrimSpace(model)
	settings.ReasoningEffort = strings.TrimSpace(reasoningEffort)
	if settings.Model == "" {
		return newValidationError("model is required to test a provider connection")
	}
	return verifyProjectLLMConnection(ctx, settings)
}

func ensureModelCredentialNotRetired(ctx context.Context, c *asclient.Client, name, uid string) error {
	studio, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	identities, _, err := unstructured.NestedSlice(studio.Object, "status", "retiredModelCredentials")
	if err != nil {
		return err
	}
	for _, entry := range identities {
		identity, ok := entry.(map[string]interface{})
		if ok && identity["secretName"] == name && identity["secretUID"] == uid {
			return newValidationError("the pinned model credential version has been retired; select a current model configuration")
		}
	}
	return nil
}
