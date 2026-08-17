/*
Copyright 2026 The Faros Authors.

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

package dataplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// ContractGetter returns the data-plane contract for a Template (by
// catalog name — the value an Instance carries in spec.template). Returns a
// nil contract with no error when the template declares no data plane, and
// an error when no such template exists.
type ContractGetter interface {
	For(ctx context.Context, templateName string) (*infrav1alpha1.TemplateDataPlane, error)
}

// DevelopmentGetter returns the platform-owned development component that
// corresponds to a data-plane component. Exec uses this separate lookup so
// image and working-directory policy remains in spec.development rather than
// being duplicated in the tenant-facing dataPlane block.
type DevelopmentGetter interface {
	DevelopmentFor(ctx context.Context, templateName, component string) (*infrav1alpha1.TemplateDevelopmentComponent, error)
}

var templateGVR = schema.GroupVersionResource{
	Group:    "infrastructure.faros.sh",
	Version:  "v1alpha1",
	Resource: "templates",
}

// TemplateContractGetter resolves a template's data-plane contract by reading
// Templates from the provider workspace with the provider's own (platform)
// kcp client. Templates are platform-owned and cluster-scoped, so reading them
// with the provider credential is correct — the caller's RBAC is enforced
// separately, on the instance (see handler.go). The template NAME comes from
// the instance's spec.template, which the caller was already authorized to
// read — never from a request field a caller could pick freely.
type TemplateContractGetter struct {
	templates dynamic.NamespaceableResourceInterface
}

// NewTemplateContractGetter builds a getter over the provider's dynamic client.
// Returns nil when client is nil (REST-only/dev serve), which the handler
// surfaces as "data plane unavailable".
func NewTemplateContractGetter(client dynamic.Interface) *TemplateContractGetter {
	if client == nil {
		return nil
	}
	return &TemplateContractGetter{templates: client.Resource(templateGVR)}
}

// For returns the named Template's dataPlane. Not cached: the Template set
// is tiny and a stale contract would silently mis-route a proxy, so we
// always read fresh.
func (g *TemplateContractGetter) For(ctx context.Context, templateName string) (*infrav1alpha1.TemplateDataPlane, error) {
	tmpl, err := g.templateByName(ctx, templateName)
	if err != nil {
		return nil, err
	}
	return dataPlaneFromTemplate(tmpl)
}

// DevelopmentFor returns the matching platform-owned development component.
// It intentionally reads the Template rather than trusting request fields so
// a caller cannot select an image or escape the component's working directory.
func (g *TemplateContractGetter) DevelopmentFor(ctx context.Context, templateName, component string) (*infrav1alpha1.TemplateDevelopmentComponent, error) {
	component = strings.TrimSpace(component)
	if component == "" {
		return nil, fmt.Errorf("component is required")
	}
	tmpl, err := g.templateByName(ctx, templateName)
	if err != nil {
		return nil, err
	}
	raw, found, err := unstructured.NestedMap(tmpl.Object, "spec", "development", "components", component)
	if err != nil {
		return nil, fmt.Errorf("template %q development component %q is malformed: %w", tmpl.GetName(), component, err)
	}
	if !found || len(raw) == 0 {
		return nil, fmt.Errorf("template %q has no development component %q", tmpl.GetName(), component)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("template %q development component %q re-encode: %w", tmpl.GetName(), component, err)
	}
	var development infrav1alpha1.TemplateDevelopmentComponent
	if err := json.Unmarshal(encoded, &development); err != nil {
		return nil, fmt.Errorf("template %q development component %q decode: %w", tmpl.GetName(), component, err)
	}
	return &development, nil
}

func (g *TemplateContractGetter) templateByName(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	if g == nil || g.templates == nil {
		return nil, fmt.Errorf("template contract getter is unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("instance names no template")
	}
	tmpl, err := g.templates.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading template %q: %w", name, err)
	}
	return tmpl, nil
}

// dataPlaneFromTemplate extracts and decodes spec.dataPlane from a Template's
// unstructured form. Returns (nil, nil) when the template declares no data
// plane.
func dataPlaneFromTemplate(tmpl *unstructured.Unstructured) (*infrav1alpha1.TemplateDataPlane, error) {
	raw, found, err := unstructured.NestedMap(tmpl.Object, "spec", "dataPlane")
	if err != nil {
		return nil, fmt.Errorf("template %q spec.dataPlane is malformed: %w", tmpl.GetName(), err)
	}
	if !found || len(raw) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("template %q spec.dataPlane re-encode: %w", tmpl.GetName(), err)
	}
	var contract infrav1alpha1.TemplateDataPlane
	if err := json.Unmarshal(encoded, &contract); err != nil {
		return nil, fmt.Errorf("template %q spec.dataPlane decode: %w", tmpl.GetName(), err)
	}
	return &contract, nil
}
