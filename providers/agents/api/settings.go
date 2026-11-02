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
	"fmt"
	"slices"
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

	// Bindings inherit their endpoint and key from a ModelProviderConnection.
	// They may change only their model; reject attempts to edit connection-owned
	// fields before any Secret reads or writes.
	existing, err := c.GetModelCredential(ctx, req.Name)
	switch {
	case err == nil && existing.Spec.ConnectionRef != "":
		if req.Provider != "" || req.BaseURL != "" || req.APIKey != "" || req.AnthropicWorkspaceID != "" {
			return nil, errBadRequest("a model connection binding can only change its model; edit the referenced provider connection to change its provider, base URL, key, or workspace ID")
		}
		updated, err := c.UpdateModelBinding(ctx, existing, req.Model)
		if err != nil {
			return nil, err
		}
		return &modelCredential{Name: updated.Name, Model: updated.Spec.Model}, nil
	case err != nil && !apierrors.IsNotFound(err):
		return nil, err
	}

	if req.Provider == "" {
		req.Provider = llm.ProviderOpenAICompatible
	}
	if req.BaseURL == "" {
		req.BaseURL = llm.DefaultBaseURL
	}

	// The existing object decides which Secret to write into: an edit must not
	// silently move a credential onto this provider's default name when it was
	// pointed somewhere else.
	secretName, secretKey := CredentialSecretName(req.Name), agentsv1alpha1.DefaultModelSecretKey
	if existing != nil {
		if n := strings.TrimSpace(existing.Spec.SecretRef.Name); n != "" {
			secretName = n
		}
		secretKey = llm.SecretKey(existing.Spec)
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
