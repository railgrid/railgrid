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

package v1alpha1_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// The CEL rules on Addon are the only thing standing between a user and a
// configuration the agent would refuse anyway — the difference is whether they
// find out at kubectl apply or by reading a status condition later. These tests
// evaluate the rules from the GENERATED CRD, so an edit to a marker that does
// not regenerate is caught here rather than in a cluster.
const addonCRDPath = "../../config/crds/edges.railgrid.ai_addons.yaml"

var (
	validatorOnce sync.Once
	addonSchema   *structuralschema.Structural
	validatorErr  error
)

func addonValidator(t *testing.T) *cel.Validator {
	t.Helper()
	validatorOnce.Do(func() {
		raw, err := os.ReadFile(addonCRDPath)
		if err != nil {
			validatorErr = err
			return
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			validatorErr = err
			return
		}
		var props apiextensions.JSONSchemaProps
		if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
			crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &props, nil); err != nil {
			validatorErr = err
			return
		}
		addonSchema, validatorErr = structuralschema.NewStructural(&props)
	})
	if validatorErr != nil {
		t.Fatalf("building the Addon schema validator: %v", validatorErr)
	}
	return cel.NewValidator(addonSchema, true, celconfig.PerCallLimit)
}

// validate returns the CEL errors for one candidate object.
func validate(t *testing.T, obj map[string]any) field.ErrorList {
	t.Helper()
	errs, _ := addonValidator(t).Validate(context.Background(), field.NewPath(""), addonSchema, obj, nil, celconfig.RuntimeCELCostBudget)
	return errs
}

func addonObject(spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "edges.railgrid.ai/v1alpha1",
		"kind":       "Addon",
		"metadata":   map[string]any{"name": "code"},
		"spec":       spec,
	}
}

func runnerSpec(mutate ...func(map[string]any)) map[string]any {
	runner := map[string]any{
		"port":            int64(8787),
		"maximumCapacity": int64(1),
		"repositories": map[string]any{
			"app": map[string]any{"source": "/srv/repos/app"},
		},
		"codex": map[string]any{
			"binary":        "codex",
			"authSecretRef": map[string]any{"name": "codex-auth", "namespace": "default"},
		},
	}
	spec := map[string]any{
		"edgeRef": map[string]any{"kind": "LinuxServer", "name": "build-01"},
		"type":    "runner",
		"runner":  runner,
	}
	for _, m := range mutate {
		m(spec)
	}
	return spec
}

func TestAddonCELAcceptsAValidRunner(t *testing.T) {
	if errs := validate(t, addonObject(runnerSpec())); len(errs) > 0 {
		t.Fatalf("a valid runner Addon was rejected: %v", errs)
	}
}

// TestAddonCELRejectsKubernetesClusterEdges: cluster edges have no host
// loopback and no agent-side add-on plane. The enum still carries the value so
// supporting them later is a rule change, not a schema migration.
func TestAddonCELRejectsKubernetesClusterEdges(t *testing.T) {
	spec := runnerSpec(func(s map[string]any) {
		s["edgeRef"] = map[string]any{"kind": "KubernetesCluster", "name": "prod"}
	})
	errs := validate(t, addonObject(spec))
	if len(errs) == 0 {
		t.Fatal("a KubernetesCluster edgeRef was accepted")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "KubernetesCluster edges cannot host add-ons") {
		t.Errorf("unexpected message: %v", errs.ToAggregate())
	}
}

// TestAddonCELRequiresTheRunnerBlock: spec.type alone says nothing about how
// the add-on should be configured.
func TestAddonCELRequiresTheRunnerBlock(t *testing.T) {
	spec := runnerSpec(func(s map[string]any) { delete(s, "runner") })
	errs := validate(t, addonObject(spec))
	if len(errs) == 0 {
		t.Fatal("spec.type=runner without spec.runner was accepted")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "spec.runner is required") {
		t.Errorf("unexpected message: %v", errs.ToAggregate())
	}
}

// TestAddonCELPinsMaximumCapacity: the runner is single-execution and refuses a
// capacity above 1 at load time; the API says so first.
func TestAddonCELPinsMaximumCapacity(t *testing.T) {
	spec := runnerSpec(func(s map[string]any) {
		s["runner"].(map[string]any)["maximumCapacity"] = int64(4)
	})
	errs := validate(t, addonObject(spec))
	if len(errs) == 0 {
		t.Fatal("maximumCapacity 4 was accepted")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "maximumCapacity must be 1") {
		t.Errorf("unexpected message: %v", errs.ToAggregate())
	}
}

// TestAddonCELRejectsRelativeRepositorySources: the runner resolves sources on
// the EDGE host, where a relative path means whatever the child's working
// directory happened to be.
func TestAddonCELRejectsRelativeRepositorySources(t *testing.T) {
	spec := runnerSpec(func(s map[string]any) {
		s["runner"].(map[string]any)["repositories"] = map[string]any{
			"app": map[string]any{"source": "repos/app"},
		}
	})
	errs := validate(t, addonObject(spec))
	if len(errs) == 0 {
		t.Fatal("a relative repository source was accepted")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "absolute path") {
		t.Errorf("unexpected message: %v", errs.ToAggregate())
	}
}

// TestAddonCELRejectsBadRepositoryIDs: the ID becomes a runner identifier and
// is named by every start request.
func TestAddonCELRejectsBadRepositoryIDs(t *testing.T) {
	spec := runnerSpec(func(s map[string]any) {
		s["runner"].(map[string]any)["repositories"] = map[string]any{
			"bad id": map[string]any{"source": "/srv/repos/app"},
		}
	})
	if errs := validate(t, addonObject(spec)); len(errs) == 0 {
		t.Fatal("an invalid repository ID was accepted")
	}
}

// TestAddonCELConstrainsFetchRemoteURLs: fetchRemoteURL is operator-only
// enrollment data handed straight to git. The agent re-validates it fully; this
// rule rejects the shapes that are obviously not one of the four allowed forms,
// and anything carrying query or fragment options.
func TestAddonCELConstrainsFetchRemoteURLs(t *testing.T) {
	withRemote := func(remote string) map[string]any {
		return runnerSpec(func(s map[string]any) {
			s["runner"].(map[string]any)["repositories"] = map[string]any{
				"app": map[string]any{"source": "/srv/repos/app", "fetchRemoteURL": remote},
			}
		})
	}

	for _, ok := range []string{
		"/srv/mirrors/app.git",
		"file:///srv/mirrors/app.git",
		"https://example.com/acme/app.git",
		"ssh://git@example.com/acme/app.git",
		"git@example.com:acme/app.git",
	} {
		if errs := validate(t, addonObject(withRemote(ok))); len(errs) > 0 {
			t.Errorf("fetchRemoteURL %q was rejected: %v", ok, errs.ToAggregate())
		}
	}

	for _, bad := range []string{
		"--upload-pack=/bin/sh",
		"ext::sh -c whoami",
		"https://example.com/acme/app.git?depth=1",
		"ssh://git@example.com/acme/app.git#frag",
	} {
		if errs := validate(t, addonObject(withRemote(bad))); len(errs) == 0 {
			t.Errorf("fetchRemoteURL %q was accepted", bad)
		}
	}
}
