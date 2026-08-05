// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package project

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

func TestArtifactInstancesSkipsTheDevelopmentSandbox(t *testing.T) {
	p := &vibev1alpha1.Project{}
	p.Spec.Environments = []vibev1alpha1.ProjectEnvironmentSpec{
		{
			Name: "development", Mode: vibev1alpha1.ProjectEnvironmentModeLive,
			Bindings: []vibev1alpha1.ProjectProviderBindingSpec{{
				Name:        vibev1alpha1.BindingRuntime,
				ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{Name: "shop"},
			}},
		},
		{
			Name: "production", Mode: vibev1alpha1.ProjectEnvironmentModeArtifact,
			Bindings: []vibev1alpha1.ProjectProviderBindingSpec{{
				Name:        vibev1alpha1.BindingRuntime,
				ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{Name: "shop-prod"},
				Values: runtime.RawExtension{Raw: []byte(
					`{"name":"shop-prod","webImage":"ghcr.io/acme/shop/web@sha256:aaa"}`)},
			}},
		},
	}
	got := artifactInstances(p)
	if len(got) != 1 || got[0].instance != "shop-prod" {
		t.Fatalf("targets = %+v, want only the production instance", got)
	}
	// The sandbox runs platform dev images and needs no credential.
	if got[0].registry != "ghcr.io" {
		t.Errorf("registry = %q, want it read from the promoted image", got[0].registry)
	}
}

func TestRegistryOfValuesReadsTheHost(t *testing.T) {
	cases := map[string]string{
		`{"image":"registry.gitlab.com/acme/app@sha256:aa"}`: "registry.gitlab.com",
		`{"image":"localhost:5000/acme/app:v1"}`:             "localhost:5000",
		// A bare Docker Hub path names no host: the default applies.
		`{"image":"library/nginx:latest"}`: "ghcr.io",
		`{"name":"shop"}`:                  "ghcr.io",
		``:                                 "ghcr.io",
	}
	for raw, want := range cases {
		if got := registryOfValues([]byte(raw)); got != want {
			t.Errorf("registryOfValues(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestDockerConfigJSONAuthenticatesTheRegistry(t *testing.T) {
	payload, err := dockerConfigJSON("ghcr.io", "mjudeikis", "ghp_secret")
	if err != nil {
		t.Fatalf("dockerConfigJSON: %v", err)
	}
	var cfg struct {
		Auths map[string]struct {
			Username, Password, Auth string
		} `json:"auths"`
	}
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entry, ok := cfg.Auths["ghcr.io"]
	if !ok {
		t.Fatalf("no entry for the registry: %s", payload)
	}
	if entry.Username != "mjudeikis" || entry.Password != "ghp_secret" {
		t.Errorf("entry = %+v", entry)
	}
	decoded, _ := base64.StdEncoding.DecodeString(entry.Auth)
	if string(decoded) != "mjudeikis:ghp_secret" {
		t.Errorf("auth = %q, want user:token", decoded)
	}
}

func TestDockerConfigJSONFallsBackToAPlaceholderUser(t *testing.T) {
	// ghcr authenticates the token and ignores the username, but the field
	// cannot be empty.
	payload, err := dockerConfigJSON("", "", "tok")
	if err != nil {
		t.Fatalf("dockerConfigJSON: %v", err)
	}
	if !strings.Contains(string(payload), defaultRegistryUser) || !strings.Contains(string(payload), defaultRegistryHost) {
		t.Errorf("payload = %s", payload)
	}
}

func TestRegistryPullSecretNameMatchesTheBridgeConvention(t *testing.T) {
	// The infrastructure provider looks for exactly this name.
	if got := registryPullSecretName("shop-prod"); got != "shop-prod-registry" {
		t.Errorf("registryPullSecretName = %q", got)
	}
}
