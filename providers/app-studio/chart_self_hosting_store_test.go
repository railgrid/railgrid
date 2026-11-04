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

package main

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelfHostingRequiresDatabaseURLSecretAndUsesItInDeployment(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}

	manifest, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read provider manifest: %v", err)
	}
	var entry struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Serving struct {
				SelfHosting struct {
					RequiredValues []struct {
						Name string `yaml:"name"`
					} `yaml:"requiredValues"`
				} `yaml:"selfHosting"`
			} `yaml:"serving"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(manifest, &entry); err != nil {
		t.Fatalf("parse provider manifest: %v", err)
	}
	if entry.Kind != "CatalogEntry" {
		t.Fatalf("manifest kind = %q, want CatalogEntry", entry.Kind)
	}
	const requiredDatabaseSecret = "store.databaseURLSecretRef.name"
	foundRequiredInput := false
	for _, required := range entry.Spec.Serving.SelfHosting.RequiredValues {
		if required.Name == requiredDatabaseSecret {
			foundRequiredInput = true
			break
		}
	}
	if !foundRequiredInput {
		t.Fatalf("selfHosting.requiredValues must include %q", requiredDatabaseSecret)
	}

	// The self-hosting recipe asks the installer to name a Secret. Helm only
	// needs the reference to render the Deployment; the Secret itself is
	// supplied by the installer in the provider namespace.
	const placeholderSecretName = "app-studio-self-hosting-db"
	output, err := exec.Command(helm, "template", "app-studio", "deploy/chart", "--set", requiredDatabaseSecret+"="+placeholderSecretName).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template with required value %q: %v\n%s", requiredDatabaseSecret, err, output)
	}

	type envVar struct {
		Name      string `yaml:"name"`
		Value     string `yaml:"value"`
		ValueFrom *struct {
			SecretKeyRef *struct {
				Name string `yaml:"name"`
				Key  string `yaml:"key"`
			} `yaml:"secretKeyRef"`
		} `yaml:"valueFrom"`
	}
	type deployment struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []envVar `yaml:"env"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(output)))
	var rendered *deployment
	for {
		var doc deployment
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered Helm document: %v", err)
		}
		if doc.Kind == "Deployment" {
			for _, container := range doc.Spec.Template.Spec.Containers {
				for _, env := range container.Env {
					if env.Name == "APP_STUDIO_DATABASE_URL" {
						rendered = &doc
						break
					}
				}
				if rendered != nil {
					break
				}
			}
			if rendered != nil {
				break
			}
		}
	}
	if rendered == nil {
		t.Fatal("helm output has no Deployment with APP_STUDIO_DATABASE_URL")
	}

	var databaseEnv []envVar
	for _, container := range rendered.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "APP_STUDIO_DATABASE_URL" {
				databaseEnv = append(databaseEnv, env)
			}
		}
	}
	if len(databaseEnv) != 1 {
		t.Fatalf("APP_STUDIO_DATABASE_URL entries = %d, want one", len(databaseEnv))
	}
	got := databaseEnv[0]
	if got.Value != "" {
		t.Fatalf("APP_STUDIO_DATABASE_URL has an inline value; want a Secret reference")
	}
	if got.ValueFrom == nil || got.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("APP_STUDIO_DATABASE_URL has no secretKeyRef")
	}
	if got.ValueFrom.SecretKeyRef.Name != placeholderSecretName || got.ValueFrom.SecretKeyRef.Key != "database-url" {
		t.Fatalf("APP_STUDIO_DATABASE_URL Secret reference = %q/%q, want %q/database-url",
			got.ValueFrom.SecretKeyRef.Name, got.ValueFrom.SecretKeyRef.Key, placeholderSecretName)
	}
}
