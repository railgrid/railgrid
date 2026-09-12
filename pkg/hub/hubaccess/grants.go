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

package hubaccess

import (
	"context"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	farosclient "github.com/faroshq/faros/pkg/client"
)

// DefaultGrantCacheTTL bounds how long a replica serves a cached grant (or
// the absence of one). Enable and Disable on the same replica update the
// cache immediately; another replica sees the change within this window.
const DefaultGrantCacheTTL = 15 * time.Second

// GrantKey identifies one provider in one workspace.
type GrantKey struct {
	OrgUUID, WorkspaceUUID, Provider, ProviderOrgUUID string
}

// Name is the grant's object name.
func (k GrantKey) Name() string {
	return GrantName(k.OrgUUID, k.WorkspaceUUID, k.Provider, k.ProviderOrgUUID)
}

// Store reads and writes provider Grants in root:faros:system:tenants, with a
// short cache on the read path the gate uses per request.
type Store struct {
	client *farosclient.Client
	ttl    time.Duration
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cachedGrant
}

type cachedGrant struct {
	grant   *tenancyv1alpha1.Grant // nil = none recorded
	expires time.Time
}

// NewStore returns a Store over the hub's tenancy client (the one bound to
// root:faros:system:tenants).
func NewStore(client *farosclient.Client) *Store {
	return &Store{client: client, ttl: DefaultGrantCacheTTL, now: time.Now, cache: map[string]cachedGrant{}}
}

// Get returns the grant for key, or nil when none is recorded.
func (s *Store) Get(ctx context.Context, key GrantKey) (*tenancyv1alpha1.Grant, error) {
	name := key.Name()
	s.mu.Lock()
	if c, ok := s.cache[name]; ok && s.now().Before(c.expires) {
		s.mu.Unlock()
		return c.grant, nil
	}
	s.mu.Unlock()

	grant, err := s.client.Grants().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		grant, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading provider access grant: %w", err)
	}
	// The object name is a hash; refuse one whose spec names another tuple
	// rather than trust it.
	if grant != nil && (grant.Spec.Subject.Kind != tenancyv1alpha1.GrantSubjectProvider ||
		grant.Spec.OrgUUID != key.OrgUUID || grant.Spec.WorkspaceUUID != key.WorkspaceUUID ||
		grant.Spec.Subject.Name != key.Provider || grant.Spec.Subject.OrgUUID != key.ProviderOrgUUID) {
		grant = nil
	}
	s.remember(name, grant)
	return grant, nil
}

// MergeFunc computes the decisions to record from the stored grant (nil when
// none exists).
type MergeFunc func(prev *tenancyv1alpha1.Grant) (accepted []tenancyv1alpha1.GrantedCapability, declined []tenancyv1alpha1.CapabilityRef)

// Record reads the stored grant for key straight from the API (not the
// cache), merges the new decisions into it with merge, and writes the result.
// When there is no stored grant and merge decides nothing, nothing is written:
// every capability stays undecided.
func (s *Store) Record(ctx context.Context, key GrantKey, merge MergeFunc, decidedBy string) (*tenancyv1alpha1.Grant, error) {
	name := key.Name()
	grants := s.client.Grants()
	existing, err := grants.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		existing, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading provider access grant: %w", err)
	}
	accepted, declined := merge(existing)
	if existing == nil && len(accepted) == 0 && len(declined) == 0 {
		s.remember(name, nil)
		return nil, nil
	}
	spec := tenancyv1alpha1.GrantSpec{
		Subject: tenancyv1alpha1.GrantSubject{
			Kind:    tenancyv1alpha1.GrantSubjectProvider,
			Name:    key.Provider,
			OrgUUID: key.ProviderOrgUUID,
		},
		OrgUUID:       key.OrgUUID,
		WorkspaceUUID: key.WorkspaceUUID,
		Capabilities:  accepted,
		Declined:      declined,
		AcceptedBy:    decidedBy,
		AcceptedAt:    metav1.NewTime(s.now()),
		Source:        tenancyv1alpha1.GrantSourceEnable,
	}
	labels := map[string]string{
		tenancyv1alpha1.LabelGrantOrg:         key.OrgUUID,
		tenancyv1alpha1.LabelGrantWorkspace:   key.WorkspaceUUID,
		tenancyv1alpha1.LabelGrantSubjectKind: string(tenancyv1alpha1.GrantSubjectProvider),
		tenancyv1alpha1.LabelGrantSubjectName: key.Provider,
	}
	if existing == nil {
		created, err := grants.Create(ctx, &tenancyv1alpha1.Grant{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: spec}, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("recording provider access grant: %w", err)
		}
		s.remember(name, created)
		return created, nil
	}
	existing.Labels = labels
	existing.Spec = spec
	updated, err := grants.Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("recording provider access grant: %w", err)
	}
	s.remember(name, updated)
	return updated, nil
}

// Put records exactly these decisions for key, replacing any earlier ones.
func (s *Store) Put(ctx context.Context, key GrantKey, accepted []tenancyv1alpha1.GrantedCapability, declined []tenancyv1alpha1.CapabilityRef, decidedBy string) (*tenancyv1alpha1.Grant, error) {
	return s.Record(ctx, key, func(*tenancyv1alpha1.Grant) ([]tenancyv1alpha1.GrantedCapability, []tenancyv1alpha1.CapabilityRef) {
		return accepted, declined
	}, decidedBy)
}

// Delete removes the grant for key. Deleting a grant that does not exist is
// not an error.
func (s *Store) Delete(ctx context.Context, key GrantKey) error {
	name := key.Name()
	if err := s.client.Grants().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing provider access grant: %w", err)
	}
	s.remember(name, nil)
	return nil
}

func (s *Store) remember(name string, grant *tenancyv1alpha1.Grant) {
	s.mu.Lock()
	s.cache[name] = cachedGrant{grant: grant, expires: s.now().Add(s.ttl)}
	s.mu.Unlock()
}
