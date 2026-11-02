// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
)

// modelCredential is the key-free view of a ModelCredential the MCP tools
// render. The API key is write-only: accepted on save, never returned.
type modelCredential struct {
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	BaseURL   string `json:"baseURL,omitempty"`
	Model     string `json:"model,omitempty"`
	HasAPIKey bool   `json:"hasAPIKey"`
	// APIKey is write-only (accepted on save, never returned).
	APIKey               string `json:"apiKey,omitempty"`
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty"`
}

// CredentialSecretName is the legacy deterministic Secret name for a
// ModelCredential. New keys use a random generated name; existing objects
// still decide which Secret currently holds their key through spec.secretRef.
func CredentialSecretName(name string) string { return agentsv1alpha1.ModelCredentialSecretName(name) }

// applyCredentialUpsert creates or updates a direct ModelCredential and its
// key. New keys are immutable Secrets created after the protected parent; an
// update reserves a fresh Secret name, creates the owned key, then activates
// it with UID, resourceVersion, reservation, and original-spec tests. When no
// key is supplied on update, the existing value is copied into the new Secret.
func applyCredentialUpsert(ctx context.Context, c *agentsclient.Client, req *modelCredential) (*modelCredential, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Provider = strings.TrimSpace(req.Provider)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.Model = strings.TrimSpace(req.Model)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.AnthropicWorkspaceID = strings.TrimSpace(req.AnthropicWorkspaceID)
	if req.Name == "" {
		return nil, errBadRequest("name is required")
	}
	if req.Model == "" {
		return nil, errBadRequest("model is required")
	}

	// New keys get an unguessable, unique Secret name. Creating the credential
	// first makes its UID available for the Secret owner reference, so deleting
	// it before the first controller reconcile cannot orphan the key.
	existing, err := c.ModelCredentials().Get(ctx, req.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if req.Provider == "" {
			req.Provider = llm.ProviderOpenAICompatible
		}
		if req.BaseURL == "" {
			req.BaseURL = llm.DefaultBaseURL
		}
		if req.APIKey == "" {
			return nil, errBadRequest("apiKey is required")
		}
		id, err := newGeneratedKeyID()
		if err != nil {
			return nil, err
		}
		secretName := agentsv1alpha1.GeneratedModelCredentialSecretName(id)
		credential := &agentsv1alpha1.ModelCredential{
			TypeMeta: metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential"},
			ObjectMeta: metav1.ObjectMeta{
				Name:       req.Name,
				Finalizers: []string{agentsv1alpha1.ModelCredentialSecretProtectionFinalizer},
				Annotations: map[string]string{
					agentsv1alpha1.PendingKeySecretAnnotation: secretName,
				},
			},
			Spec: agentsv1alpha1.ModelCredentialSpec{
				Provider:             req.Provider,
				BaseURL:              req.BaseURL,
				AnthropicWorkspaceID: req.AnthropicWorkspaceID,
				Model:                req.Model,
				SecretRef:            agentsv1alpha1.ModelCredentialSecretRef{Name: secretName},
				SecretKey:            agentsv1alpha1.DefaultModelSecretKey,
			},
		}
		created, err := c.ModelCredentials().Create(ctx, credential, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
		if created.UID == "" {
			return nil, fmt.Errorf("creating ModelCredential %q returned no UID; refusing to write an unowned key", req.Name)
		}
		if _, err := createModelKeySecret(ctx, c, created, id, secretName, created.Spec.SecretKey, req.APIKey); err != nil {
			return nil, fmt.Errorf("creating key Secret for ModelCredential %q: %w", req.Name, err)
		}
		if _, err := c.ActivateModelCredentialKey(ctx, created, created.Spec, created.Spec, secretName, id); err != nil {
			return nil, fmt.Errorf("activating key Secret for ModelCredential %q: %w", req.Name, err)
		}
		return &modelCredential{
			Name: req.Name, Provider: req.Provider, BaseURL: req.BaseURL, Model: req.Model,
			AnthropicWorkspaceID: req.AnthropicWorkspaceID, HasAPIKey: true,
		}, nil
	case err == nil:
		if existing.Spec.ConnectionRef != "" {
			if req.Provider != "" || req.BaseURL != "" || req.APIKey != "" || req.AnthropicWorkspaceID != "" {
				return nil, errBadRequest("edit the provider connection to change its endpoint or key; omit provider configuration to update only the model")
			}
			updated, err := c.UpdateModelBinding(ctx, existing, req.Model)
			if err != nil {
				return nil, err
			}
			return &modelCredential{Name: updated.Name, Model: updated.Spec.Model}, nil
		}
	default:
		return nil, err
	}

	// An edit reads the existing key when one was omitted, then writes a fresh
	// immutable generated Secret. A custom tenant Secret remains unchanged and
	// unowned; the credential moves only after the staged key is ready.
	secretName := strings.TrimSpace(existing.Spec.SecretRef.Name)
	if secretName == "" {
		secretName = CredentialSecretName(req.Name)
	}
	secretKey := llm.SecretKey(existing.Spec)
	if req.Provider == "" {
		req.Provider = llm.ProviderOpenAICompatible
	}
	if req.BaseURL == "" {
		req.BaseURL = llm.DefaultBaseURL
	}

	protected, err := ensureModelCredentialSecretProtection(ctx, c, existing)
	if err != nil {
		return nil, err
	}

	// Preserve an existing key when updating without a new one.
	apiKey := req.APIKey
	if apiKey == "" {
		if sec, gerr := c.GetSecret(ctx, llm.SecretNamespace, secretName); gerr == nil {
			apiKey = llm.APIKeyFromSecret(sec, agentsv1alpha1.ModelCredentialSpec{SecretKey: secretKey})
		}
		if apiKey == "" {
			return nil, errBadRequest("apiKey is required")
		}
	}
	newSpec := agentsv1alpha1.ModelCredentialSpec{
		Provider:             req.Provider,
		BaseURL:              req.BaseURL,
		AnthropicWorkspaceID: req.AnthropicWorkspaceID,
		Model:                req.Model,
		SecretRef:            agentsv1alpha1.ModelCredentialSecretRef{Name: secretName},
		SecretKey:            secretKey,
	}
	id, err := newGeneratedKeyID()
	if err != nil {
		return nil, err
	}
	pendingName := agentsv1alpha1.GeneratedModelCredentialSecretName(id)
	reserved, err := reserveModelCredentialKey(ctx, c, protected, pendingName)
	if err != nil {
		return nil, errors.Join(err, releaseModelCredentialKeyReservation(ctx, c, req.Name, protected.UID, pendingName))
	}
	newSpec.SecretRef.Name = pendingName
	if _, err := createModelKeySecret(ctx, c, reserved, id, pendingName, secretKey, apiKey); err != nil {
		return nil, errors.Join(fmt.Errorf("creating rotated key Secret for ModelCredential %q: %w", req.Name, err), releaseModelCredentialKeyReservation(ctx, c, req.Name, reserved.UID, pendingName))
	}
	if _, err := c.ActivateModelCredentialKey(ctx, reserved, reserved.Spec, newSpec, pendingName, id); err != nil {
		return nil, errors.Join(fmt.Errorf("activating rotated key Secret for ModelCredential %q: %w", req.Name, err), releaseModelCredentialKeyReservation(ctx, c, req.Name, reserved.UID, pendingName))
	}
	return &modelCredential{
		Name: req.Name, Provider: req.Provider, BaseURL: req.BaseURL, Model: req.Model, AnthropicWorkspaceID: req.AnthropicWorkspaceID, HasAPIKey: true,
	}, nil
}

func reserveModelCredentialKey(ctx context.Context, c *agentsclient.Client, credential *agentsv1alpha1.ModelCredential, pendingName string) (*agentsv1alpha1.ModelCredential, error) {
	if credential == nil || credential.UID == "" || credential.ResourceVersion == "" || pendingName == "" {
		return nil, fmt.Errorf("ModelCredential key reservation requires name, UID, resourceVersion, and Secret name")
	}
	reserved := credential.DeepCopy()
	reserved.Finalizers = appendMissingFinalizer(reserved.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer)
	if reserved.Annotations == nil {
		reserved.Annotations = map[string]string{}
	}
	reserved.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] = pendingName
	updated, err := c.UpdateModelCredentialWithPreconditions(ctx, reserved)
	if err != nil {
		return nil, err
	}
	if updated.UID != credential.UID || updated.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] != pendingName ||
		!slices.Contains(updated.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
		return nil, fmt.Errorf("reserving key Secret for ModelCredential %q returned unexpected identity or protection", credential.Name)
	}
	return updated, nil
}

func releaseModelCredentialKeyReservation(ctx context.Context, c *agentsclient.Client, name string, uid types.UID, pendingName string) error {
	if name == "" || uid == "" || pendingName == "" {
		return nil
	}
	current, err := c.ModelCredentials().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.UID != uid || !current.DeletionTimestamp.IsZero() ||
		current.Spec.SecretRef.Name == pendingName ||
		current.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] != pendingName {
		return nil
	}
	updated := current.DeepCopy()
	delete(updated.Annotations, agentsv1alpha1.PendingKeySecretAnnotation)
	_, err = c.UpdateModelCredentialWithPreconditions(ctx, updated)
	return err
}

// deleteCredential removes a ModelCredential. A credential protection
// finalizer keeps the object present until the provider controller has
// preserved any Secret it shares with another provider connection or model.
// The API acts as the caller and deliberately does not read or delete Secrets.
func deleteCredential(ctx context.Context, c *agentsclient.Client, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errBadRequest("name is required")
	}
	cred, err := c.ModelCredentials().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cred.UID == "" || cred.ResourceVersion == "" {
		return fmt.Errorf("cannot safely delete ModelCredential %q without UID and resourceVersion", cred.Name)
	}
	if cred.Spec.ConnectionRef != "" {
		err := c.ModelCredentials().Delete(ctx, name, modelCredentialDeleteOptions(cred))
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	models, err := c.ModelCredentials().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, model := range models.Items {
		if model.Spec.ConnectionRef == name {
			return errBadRequest("remove the models using this connection before deleting it")
		}
	}
	cred, err = ensureModelCredentialSecretProtection(ctx, c, cred)
	if err != nil {
		return err
	}
	err = c.ModelCredentials().Delete(ctx, name, modelCredentialDeleteOptions(cred))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func appendMissingFinalizer(finalizers []string, finalizer string) []string {
	if slices.Contains(finalizers, finalizer) {
		return finalizers
	}
	return append(slices.Clone(finalizers), finalizer)
}

func ensureModelCredentialSecretProtection(ctx context.Context, c *agentsclient.Client, cred *agentsv1alpha1.ModelCredential) (*agentsv1alpha1.ModelCredential, error) {
	if slices.Contains(cred.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
		return cred, nil
	}
	if cred.UID == "" || cred.ResourceVersion == "" {
		return nil, fmt.Errorf("cannot protect ModelCredential %q without UID and resourceVersion", cred.Name)
	}
	protected := cred.DeepCopy()
	protected.Finalizers = appendMissingFinalizer(protected.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer)
	updated, err := c.UpdateModelCredentialWithPreconditions(ctx, protected)
	if err != nil {
		return nil, err
	}
	if updated.UID != cred.UID || updated.ResourceVersion == "" {
		return nil, fmt.Errorf("protecting ModelCredential %q returned unexpected UID or empty resourceVersion", cred.Name)
	}
	if !slices.Contains(updated.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
		return nil, fmt.Errorf("protecting ModelCredential %q returned without the required protection finalizer", cred.Name)
	}
	return updated, nil
}

func modelCredentialDeleteOptions(cred *agentsv1alpha1.ModelCredential) metav1.DeleteOptions {
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cred.UID, ResourceVersion: &cred.ResourceVersion}}
}

// credentialTestResult is the outcome of a live credential health probe.
type credentialTestResult struct {
	OK        bool     `json:"ok"`
	LatencyMS int64    `json:"latencyMS"`
	Error     string   `json:"error,omitempty"`
	Models    []string `json:"models,omitempty"` // ids the endpoint serves (discovery)
}
