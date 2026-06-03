// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NewStubClient returns a Client backed by an in-memory catalog and
// instance map. Used when KRO_KUBECONFIG is unset so phase 2 is demoable
// without provisioning a real central kro cluster. The stub is also
// useful in tests and in the portal's dev-mode where developers want
// to iterate on the UI without standing up infrastructure.
func NewStubClient() Client {
	return &stubClient{
		instances: map[string]map[string]*Instance{},
		nsByPath:  map[string]string{},
	}
}

type stubClient struct {
	mu sync.RWMutex
	// instances is keyed by tenantPath, then instance name.
	instances map[string]map[string]*Instance
	// nsByPath records the synthesized namespace name for each tenant
	// so EnsureTenantNamespace is stable across calls.
	nsByPath map[string]string
}

// stubTemplates is the curated catalog the stub serves. Crafted to
// exercise every input type the portal's DynamicForm supports so
// reviewers can poke the form without standing up kro. Keep this in
// sync with the SimpleSchema grammar parseSimpleSchemaLeaf supports.
var stubTemplates = func() []Template {
	apps, _ := ConvertSimpleSchema(map[string]any{
		"name":     "string | required=true",
		"replicas": "integer | default=2 | minimum=1 | maximum=10",
		"image":    "string | default=ghcr.io/example/app:1.0",
		"public":   "boolean | default=true",
	})
	postgres, postgresSamples := ConvertSimpleSchema(map[string]any{
		"name":    "string | required=true",
		"size":    `string | enum=small,medium,large | default=small`,
		"version": "string | default=16",
		"backups": "boolean | default=true",
	})
	bucket, bucketSamples := ConvertSimpleSchema(map[string]any{
		"name":   "string | required=true",
		"region": `string | enum=us-east-1,us-west-2,eu-west-1 | default=us-east-1 | description="AWS region"`,
		"public": "boolean | default=false",
	})
	return []Template{
		{
			Name:         "app",
			DisplayName:  "Application",
			Description:  "A 12-factor web app with a Deployment + Service + optional Ingress.",
			Category:     "Workloads",
			Cloud:        "k8s",
			Version:      "0.1.0",
			Backend:      "kro",
			InstanceKind: "Application",
			InstanceGVR: schema.GroupVersionResource{
				Group: "kro.run", Version: "v1alpha1", Resource: "applications",
			},
			InputsSchema: apps,
			SampleValues: map[string]any{
				"name": "hello", "replicas": 2, "image": "ghcr.io/example/app:1.0", "public": true,
			},
		},
		{
			Name:         "postgres",
			DisplayName:  "Postgres database",
			Description:  "Managed Postgres with daily backups and configurable size.",
			Category:     "Databases",
			Cloud:        "aws",
			Version:      "0.1.0",
			Backend:      "kro",
			InstanceKind: "Postgres",
			InstanceGVR: schema.GroupVersionResource{
				Group: "kro.run", Version: "v1alpha1", Resource: "postgres",
			},
			InputsSchema: postgres,
			SampleValues: postgresSamples,
		},
		{
			Name:         "s3-bucket",
			DisplayName:  "S3 bucket",
			Description:  "An AWS S3 bucket with sensible defaults and optional public access.",
			Category:     "Storage",
			Cloud:        "aws",
			Version:      "0.1.0",
			Backend:      "kro",
			InstanceKind: "Bucket",
			InstanceGVR: schema.GroupVersionResource{
				Group: "kro.run", Version: "v1alpha1", Resource: "buckets",
			},
			InputsSchema: bucket,
			SampleValues: bucketSamples,
		},
	}
}()

func (s *stubClient) ListTemplates(_ context.Context, filter TemplateFilter) ([]Template, error) {
	out := make([]Template, 0, len(stubTemplates))
	for _, t := range stubTemplates {
		if filter.Category != "" && t.Category != filter.Category {
			continue
		}
		if filter.Cloud != "" && t.Cloud != filter.Cloud {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *stubClient) GetTemplate(_ context.Context, name, version string) (*Template, error) {
	for i := range stubTemplates {
		t := stubTemplates[i]
		if t.Name != name {
			continue
		}
		if version != "" && t.Version != version {
			continue
		}
		return &t, nil
	}
	return nil, ErrTemplateNotFound
}

func (s *stubClient) EnsureTenantNamespace(_ context.Context, tenantPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ns, ok := s.nsByPath[tenantPath]; ok {
		return ns, nil
	}
	ns := tenantNamespaceName(tenantPath)
	s.nsByPath[tenantPath] = ns
	return ns, nil
}

func (s *stubClient) CreateInstance(_ context.Context, in CreateInstanceParams) (*Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.instances[in.TenantPath]
	if bucket == nil {
		bucket = map[string]*Instance{}
		s.instances[in.TenantPath] = bucket
	}
	if _, exists := bucket[in.InstanceName]; exists {
		return nil, fmt.Errorf("instance %q already exists", in.InstanceName)
	}
	inst := &Instance{
		Name:      in.InstanceName,
		Namespace: tenantNamespaceName(in.TenantPath),
		Template:  in.Template.Name,
		Phase:     "Ready", // stub: instant ready, no real reconcile
		Values:    in.Values,
		CreatedAt: time.Now().UTC(),
		Conditions: []InstanceCondition{{
			Type: "Ready", Status: "True", Reason: "StubReconciled",
			Message: "instance materialized by the stub client", Time: time.Now().UTC(),
		}},
	}
	bucket[in.InstanceName] = inst
	return inst, nil
}

func (s *stubClient) GetInstance(_ context.Context, tenantPath, name string) (*Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.instances[tenantPath]
	if inst, ok := bucket[name]; ok {
		return inst, nil
	}
	return nil, ErrInstanceNotFound
}

func (s *stubClient) ListInstances(_ context.Context, tenantPath string) ([]Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.instances[tenantPath]
	out := make([]Instance, 0, len(bucket))
	for _, inst := range bucket {
		out = append(out, *inst)
	}
	return out, nil
}

func (s *stubClient) DeleteInstance(_ context.Context, tenantPath, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.instances[tenantPath]
	if _, ok := bucket[name]; !ok {
		return ErrInstanceNotFound
	}
	delete(bucket, name)
	return nil
}
