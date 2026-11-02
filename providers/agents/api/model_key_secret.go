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
	"crypto/rand"
	"encoding/hex"
	"fmt"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newGeneratedKeyID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generating model key identity: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func modelKeySecret(name, id string, owner metav1.Object, key, value string, kind string) *corev1.Secret {
	immutable := true
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: llm.SecretNamespace,
			Annotations: map[string]string{
				agentsv1alpha1.GeneratedKeySecretIDAnnotation: id,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: agentsv1alpha1.SchemeGroupVersion.String(),
				Kind:       kind,
				Name:       owner.GetName(),
				UID:        owner.GetUID(),
			}},
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: &immutable,
		Data:      map[string][]byte{key: []byte(value)},
	}
}

func createModelKeySecret(ctx context.Context, c *agentsclient.Client, credential *agentsv1alpha1.ModelCredential, id, name, key, value string) (*corev1.Secret, error) {
	if credential == nil || credential.UID == "" || name == "" || id == "" || key == "" {
		return nil, fmt.Errorf("creating generated model key requires its owner UID, Secret name, ID, and key name")
	}
	secret := modelKeySecret(name, id, credential, key, value, "ModelCredential")
	created, err := c.CreateSecret(ctx, secret)
	if err != nil {
		return nil, err
	}
	if created.Name != name || created.Namespace != secret.Namespace || created.UID == "" ||
		created.Immutable == nil || !*created.Immutable ||
		created.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != id || !modelSecretHasOwner(created, credential) {
		return nil, fmt.Errorf("creating generated model key Secret %q returned unexpected identity or owner", name)
	}
	return created, nil
}

func modelSecretHasOwner(secret *corev1.Secret, credential *agentsv1alpha1.ModelCredential) bool {
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == agentsv1alpha1.SchemeGroupVersion.String() &&
			owner.Kind == "ModelCredential" && owner.Name == credential.Name && owner.UID == credential.UID {
			return true
		}
	}
	return false
}
