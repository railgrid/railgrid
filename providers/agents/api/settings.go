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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// CredentialSecretName is the Secret a ModelCredential written through this
// provider points at. It is a default for the objects this code creates, not a
// convention anything reads back: a ModelCredential says where its key is in
// spec.secretRef, and a hand-written one may name any Secret it likes.
func CredentialSecretName(name string) string { return agentsv1alpha1.ModelCredentialSecretName(name) }

// applyCredentialUpsert writes a ModelCredential and the Secret it points at
// (create-or-update), preserving an existing key when none is supplied.
//
// The Secret goes first, the object second — the same order
// applyConnectionCreate uses, and for the same reason: the reconciler reacts
// to the object, and a ModelCredential whose Secret does not exist yet parks
// with SecretResolved=False until one does. The other order would flag a
// credential that is about to be fine.
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

	// The existing object decides which Secret to write into: an edit must not
	// silently move a credential onto this provider's default name when it was
	// pointed somewhere else.
	secretName, secretKey := CredentialSecretName(req.Name), agentsv1alpha1.DefaultModelSecretKey
	existing, err := c.ModelCredentials().Get(ctx, req.Name, metav1.GetOptions{})
	switch {
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
		if n := strings.TrimSpace(existing.Spec.SecretRef.Name); n != "" {
			secretName = n
		}
		secretKey = llm.SecretKey(existing.Spec)
	case !apierrors.IsNotFound(err):
		return nil, err
	}
	if req.Provider == "" {
		req.Provider = llm.ProviderOpenAICompatible
	}
	if req.BaseURL == "" {
		req.BaseURL = llm.DefaultBaseURL
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
	sec := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: llm.SecretNamespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{secretKey: apiKey},
	}
	// ApplySecret stamps railgrid.ai/owner: agents, without which the
	// provider's label-scoped claim hides the Secret from every unattended run.
	if _, err := c.ApplySecret(ctx, sec); err != nil {
		return nil, err
	}

	cred := &agentsv1alpha1.ModelCredential{
		TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential"},
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.ModelCredentialSpec{
			Provider:             req.Provider,
			BaseURL:              req.BaseURL,
			AnthropicWorkspaceID: req.AnthropicWorkspaceID,
			Model:                req.Model,
			SecretRef:            agentsv1alpha1.ModelCredentialSecretRef{Name: secretName},
			SecretKey:            secretKey,
		},
	}
	if _, err := c.ModelCredentials().Update(ctx, cred, metav1.UpdateOptions{}); err != nil {
		return nil, err
	}
	return &modelCredential{
		Name: req.Name, Provider: req.Provider, BaseURL: req.BaseURL, Model: req.Model, AnthropicWorkspaceID: req.AnthropicWorkspaceID, HasAPIKey: true,
	}, nil
}

// deleteCredential removes a ModelCredential and the Secret it pointed at.
// Best effort on the Secret, and in this order: an orphaned Secret is
// harmless, an object pointing at a key that is gone is not.
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
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cred.UID, ResourceVersion: &cred.ResourceVersion}}
	if cred.Spec.ConnectionRef != "" {
		err := c.ModelCredentials().Delete(ctx, name, options)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	secretName := strings.TrimSpace(cred.Spec.SecretRef.Name)

	models, err := c.ModelCredentials().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, model := range models.Items {
		if model.Spec.ConnectionRef == name {
			return errBadRequest("remove the models using this connection before deleting it")
		}
	}
	if err := c.ModelCredentials().Delete(ctx, name, options); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if secretName == "" {
		return nil
	}
	secret, err := c.GetSecret(ctx, llm.SecretNamespace, secretName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(secret.OwnerReferences) != 1 {
		return nil
	}
	// No name-based fallback: a migrated connection may own the same-named key.
	// The resource version also protects ownership transfers after this read.
	for _, owner := range secret.OwnerReferences {
		if cred.UID != "" && owner.UID == cred.UID && owner.Kind == "ModelCredential" && owner.APIVersion == agentsv1alpha1.SchemeGroupVersion.String() {
			err := c.DeleteSecretWithOptions(ctx, llm.SecretNamespace, secretName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion}})
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}

	return nil
}

// credentialTestResult is the outcome of a live credential health probe.
type credentialTestResult struct {
	OK        bool     `json:"ok"`
	LatencyMS int64    `json:"latencyMS"`
	Error     string   `json:"error,omitempty"`
	Models    []string `json:"models,omitempty"` // ids the endpoint serves (discovery)
}
