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

// Package workspaceseeds applies WorkspaceSeeds (admin.railgrid.ai) to every
// tenant workspace: backend functionality that is bound and set up without
// anyone enabling it.
//
// Two controllers share a Store. The seed controller runs on the admin
// multicluster manager, validates each WorkspaceSeed and keeps the accepted
// ones in the Store. The workspace controller runs on the core multicluster
// manager, which engages every tenant workspace and serves its APIBindings,
// and brings each workspace to every seed in the Store whenever one of the
// workspace's bindings changes or the Store does.
package workspaceseeds

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

// Seed is one accepted WorkspaceSeed.
type Seed struct {
	Name string
	Spec adminv1alpha1.WorkspaceSeedSpec
}

// Store holds the accepted seeds and tells its listeners when the set
// changes.
type Store struct {
	mu        sync.RWMutex
	seeds     map[string]adminv1alpha1.WorkspaceSeedSpec
	listeners map[int]func()
	next      int
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{seeds: map[string]adminv1alpha1.WorkspaceSeedSpec{}, listeners: map[int]func(){}}
}

// Set records the accepted spec of a seed. Listeners run when it differs from
// what was held.
func (s *Store) Set(name string, spec adminv1alpha1.WorkspaceSeedSpec) {
	s.mu.Lock()
	current, held := s.seeds[name]
	if held && reflect.DeepEqual(current, spec) {
		s.mu.Unlock()
		return
	}
	s.seeds[name] = *spec.DeepCopy()
	s.mu.Unlock()
	s.notify()
}

// Delete drops a seed. Nothing it created in any workspace is removed;
// workspaces simply stop being brought to it.
func (s *Store) Delete(name string) {
	s.mu.Lock()
	_, held := s.seeds[name]
	delete(s.seeds, name)
	s.mu.Unlock()
	if held {
		s.notify()
	}
}

// List returns the accepted seeds, ordered by name.
func (s *Store) List() []Seed {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Seed, 0, len(s.seeds))
	for name, spec := range s.seeds {
		out = append(out, Seed{Name: name, Spec: *spec.DeepCopy()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// OnChange registers fn to run after every change of the set, on the
// goroutine that made the change. The returned function unregisters it.
func (s *Store) OnChange(fn func()) (unregister func()) {
	s.mu.Lock()
	id := s.next
	s.next++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

func (s *Store) notify() {
	s.mu.RLock()
	fns := make([]func(), 0, len(s.listeners))
	for _, fn := range s.listeners {
		fns = append(fns, fn)
	}
	s.mu.RUnlock()
	for _, fn := range fns {
		fn()
	}
}

// Validate reports what the schema cannot: a spec the hub refuses to apply to
// tenant workspaces.
func Validate(spec *adminv1alpha1.WorkspaceSeedSpec) error {
	var errs []error
	names := map[string]bool{}
	for i := range spec.Bindings {
		b := &spec.Bindings[i]
		at := fmt.Sprintf("bindings[%d]", i)
		if b.Name == kcp.CoreBindingName {
			errs = append(errs, fmt.Errorf("%s: the name %q is the hub's own binding", at, kcp.CoreBindingName))
		}
		if names[b.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name %q", at, b.Name))
		}
		names[b.Name] = true
		switch b.TenantAccess {
		case "", adminv1alpha1.SeedTenantAccessReadWrite, adminv1alpha1.SeedTenantAccessReadOnly, adminv1alpha1.SeedTenantAccessNone:
		default:
			errs = append(errs, fmt.Errorf("%s: unknown tenantAccess %q", at, b.TenantAccess))
		}
		for j := range b.PermissionClaims {
			pc := &b.PermissionClaims[j]
			claimAt := fmt.Sprintf("%s.permissionClaims[%d]", at, j)
			scoped := pc.Selector != nil && (len(pc.Selector.MatchLabels) > 0 || len(pc.Selector.MatchExpressions) > 0)
			if pc.Selector != nil {
				if list := metav1validation.ValidateLabelSelector(pc.Selector, metav1validation.LabelSelectorValidationOptions{}, field.NewPath("selector")); len(list) > 0 {
					errs = append(errs, fmt.Errorf("%s: %w", claimAt, list.ToAggregate()))
				}
			}
			// The same rule a provider's manifest is held to: nothing gets
			// every Secret or ConfigMap of a tenant workspace.
			if pc.Group == "" && (pc.Resource == "secrets" || pc.Resource == "configmaps") && !scoped {
				errs = append(errs, fmt.Errorf("%s: a claim on core %s must carry a selector", claimAt, pc.Resource))
			}
		}
	}
	for i := range spec.Objects {
		o := &spec.Objects[i]
		at := fmt.Sprintf("objects[%d]", i)
		var manifest struct {
			APIVersion string            `json:"apiVersion"`
			Kind       string            `json:"kind"`
			Metadata   metav1.ObjectMeta `json:"metadata"`
		}
		if err := json.Unmarshal(o.Manifest.Raw, &manifest); err != nil {
			errs = append(errs, fmt.Errorf("%s: manifest: %w", at, err))
			continue
		}
		if manifest.APIVersion == "" || manifest.Kind == "" || manifest.Metadata.Name == "" {
			errs = append(errs, fmt.Errorf("%s: manifest needs apiVersion, kind and metadata.name", at))
			continue
		}
		gv, err := schema.ParseGroupVersion(manifest.APIVersion)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: manifest apiVersion: %w", at, err))
			continue
		}
		// The hub writes these with its administrator credential. What a
		// seed may create is data of bound APIs and plain configuration, not
		// the workspace's own access control or its bindings.
		switch gv.Group {
		case "rbac.authorization.k8s.io", "apis.kcp.io", "tenancy.kcp.io", "core.kcp.io", "admissionregistration.k8s.io", "apiextensions.k8s.io":
			errs = append(errs, fmt.Errorf("%s: objects of group %s cannot be seeded", at, gv.Group))
		}
	}
	return errors.Join(errs...)
}
