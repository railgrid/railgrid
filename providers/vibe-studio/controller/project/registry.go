// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package project

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

// Registry pull credentials.
//
// Production runs images the project's own CI published, and those packages
// are private — a fresh production deployment otherwise sits in
// ErrImagePull forever. The credential is the git-host token the project
// already has: on GitHub the same token that pushed the image can pull it.
//
// The convention is the infrastructure provider's, not ours: it bridges a
// tenant Secret named "<instance>-registry" into the runtime namespace and
// attaches it to that namespace's default ServiceAccount. Minting it here
// means the credential is re-derived every reconcile, so a rotated token
// reaches production without anyone re-promoting.
const (
	// credentialsNamespace is where the infrastructure provider looks for the
	// per-instance registry Secret (its CredentialsNamespace default).
	credentialsNamespace = "default"
	// defaultRegistryHost is used when the promoted images name no host.
	defaultRegistryHost = "ghcr.io"
	// defaultRegistryUser is a placeholder for hosts that authenticate the
	// token and ignore the username (ghcr does).
	defaultRegistryUser = "faros-vibe-studio"
)

// registryPullSecretName mirrors the infrastructure provider's convention.
func registryPullSecretName(instance string) string { return instance + "-registry" }

// ensureRegistryPullSecret mints the pull credential for every artifact-mode
// instance the Project runs. A project with no production environment, or no
// token to derive from, is a no-op.
func (r *Reconciler) ensureRegistryPullSecret(ctx context.Context, c client.Client, p *vibev1alpha1.Project) error {
	repo := p.Spec.Repository
	if repo == nil || repo.TokenSecret == nil || repo.TokenSecret.Name == "" {
		return nil
	}
	targets := artifactInstances(p)
	if len(targets) == 0 {
		return nil
	}

	ns := repo.TokenSecret.Namespace
	if ns == "" {
		ns = credentialsNamespace
	}
	var src corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: repo.TokenSecret.Name}, &src); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // the connection's Secret is gone; nothing to mint from
		}
		return fmt.Errorf("reading git token secret %s/%s: %w", ns, repo.TokenSecret.Name, err)
	}
	key := repo.TokenSecret.Key
	if key == "" {
		key = "token"
	}
	token := strings.TrimSpace(string(src.Data[key]))
	if token == "" {
		return nil
	}

	for _, t := range targets {
		payload, err := dockerConfigJSON(t.registry, repo.Login, token)
		if err != nil {
			return err
		}
		if err := r.applySecret(ctx, c, p, registryPullSecretName(t.instance), payload); err != nil {
			return fmt.Errorf("writing pull secret for %s: %w", t.instance, err)
		}
	}
	return nil
}

// artifactTarget is one instance that needs a pull credential.
type artifactTarget struct {
	instance string
	registry string
}

// artifactInstances lists the Project's artifact-mode instances and the
// registry each pulls from. Pure.
func artifactInstances(p *vibev1alpha1.Project) []artifactTarget {
	var out []artifactTarget
	for _, env := range p.Spec.Environments {
		if env.Mode != vibev1alpha1.ProjectEnvironmentModeArtifact {
			continue // a development sandbox runs platform images
		}
		for _, b := range env.Bindings {
			if b.ResourceRef == nil || b.ResourceRef.Name == "" {
				continue
			}
			out = append(out, artifactTarget{
				instance: b.ResourceRef.Name,
				registry: registryOfValues(b.Values.Raw),
			})
		}
	}
	return out
}

// registryOfValues reads the registry host out of the binding's pinned
// images, so a project on a host other than ghcr gets a credential for that
// host rather than the wrong one. Pure.
func registryOfValues(raw []byte) string {
	if len(raw) == 0 {
		return defaultRegistryHost
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return defaultRegistryHost
	}
	for _, v := range values {
		s, ok := v.(string)
		if !ok || !strings.Contains(s, "/") {
			continue
		}
		host := s[:strings.Index(s, "/")]
		// A registry host has a dot or a port; "library/nginx" does not.
		if strings.ContainsAny(host, ".:") {
			return host
		}
	}
	return defaultRegistryHost
}

// dockerConfigJSON builds a single-registry docker config. Pure.
func dockerConfigJSON(registry, login, token string) ([]byte, error) {
	if registry == "" {
		registry = defaultRegistryHost
	}
	user := strings.TrimSpace(login)
	if user == "" {
		user = defaultRegistryUser
	}
	return json.Marshal(map[string]any{
		"auths": map[string]any{
			registry: map[string]any{
				"username": user,
				"password": token,
				"auth":     base64.StdEncoding.EncodeToString([]byte(user + ":" + token)),
			},
		},
	})
}

// applySecret create-or-updates the dockerconfigjson Secret, and only writes
// when the content actually changed so a rotation is visible but a steady
// state is silent.
func (r *Reconciler) applySecret(ctx context.Context, c client.Client, p *vibev1alpha1.Project, name string, payload []byte) error {
	desired := &corev1.Secret{Type: corev1.SecretTypeDockerConfigJson}
	desired.Name = name
	desired.Namespace = credentialsNamespace
	desired.Labels = map[string]string{projectLabel: p.Name}
	desired.Data = map[string][]byte{corev1.DockerConfigJsonKey: payload}

	var existing corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Namespace: credentialsNamespace, Name: name}, &existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if string(existing.Data[corev1.DockerConfigJsonKey]) == string(payload) {
		return nil
	}
	existing.Type = corev1.SecretTypeDockerConfigJson
	existing.Data = desired.Data
	return c.Update(ctx, &existing)
}
