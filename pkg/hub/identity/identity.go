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

// Package identity is the hub's scoped-identity service: the one place a
// runtime credential for a tenant object is created.
//
// Before this package there were three minters (cross-provider-simplification
// §"One identity service"): the hub workload exchange, the agents provider's
// per-agent ServiceAccounts, and the edges provider's per-edge ones. The last
// two minted NON-EXPIRING tokens from inside a provider, over API groups the
// provider did not own, with nothing collecting them when the agent or edge
// went away. That is finding M7, and it cannot be closed provider-side.
//
// What this package guarantees:
//
//   - Every identity has an OWNER: a real object in a tenant workspace. When
//     the owner is gone the identity is collected (reconciler.go).
//   - Every identity's rules passed a POLICY (policy.go). A provider cannot
//     mint itself, or anybody else, a capability over another provider's group
//     beyond named reads and declared verbs.
//   - Every token is TokenRequest-minted and TTL'd, at most 24h, and is never
//     persisted — not in the record, not in a Secret.
//   - Every identity is RECORDED as a ScopedIdentity in
//     root:railgrid:system:tenants, which no tenant or provider can read.
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const (
	// DefaultWorkloadTTL is the lifetime of a pod-attested workload token. It
	// is short because the runtime can always come back through the exchange.
	DefaultWorkloadTTL = 10 * time.Minute

	// DefaultProviderTTL is the lifetime of a provider-asserted token. It is
	// longer because the holder is a long-running agent process that refreshes
	// on a timer, and shorter than any credential these consumers hold today,
	// all of which never expire at all.
	DefaultProviderTTL = time.Hour

	// serviceAccountPrefix distinguishes provider-asserted identities from the
	// workload ones (railgrid-wi-), which keep their own deterministic name so
	// existing App Studio runtimes keep the account they already have.
	serviceAccountPrefix = "railgrid-si-"

	// recordPrefix is the ScopedIdentity object name prefix.
	recordPrefix = "si-"
)

// ErrNotFound is returned when no identity exists for an owner.
var ErrNotFound = errors.New("scoped identity not found")

// Owner is the tenant object an identity exists for.
type Owner struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	ClusterID string `json:"clusterID,omitempty"`
}

// Request is one create-or-refresh. It is idempotent on the owner tuple:
// asking twice returns the same ServiceAccount with a fresh token.
type Request struct {
	// Owner is the tenant object this identity is for.
	Owner Owner `json:"owner"`
	// ClusterID is the tenant workspace to materialize in. Empty means
	// Owner.ClusterID.
	ClusterID string `json:"clusterID,omitempty"`
	// Rules are the policy rules asked for; they are checked before anything
	// is written.
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`
	// TTLSeconds is the requested token lifetime, clamped to at most 24h.
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
}

// Token is what a caller gets back. The hub keeps no copy.
type Token struct {
	Token          string    `json:"token"`
	TokenType      string    `json:"tokenType"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ServiceAccount string    `json:"serviceAccount"`
	Name           string    `json:"name"`
}

// OwnerProbe answers whether an owner object still exists in a tenant
// workspace, with the UID the record was minted for. It is both the
// admission check on the provider-asserted path and the GC predicate.
type OwnerProbe interface {
	// Exists returns (found, uid, error). A found object with a different UID
	// is reported as found=false: the owner was deleted and recreated, and the
	// identity minted for the old one must not survive.
	Exists(ctx context.Context, clusterID string, owner Owner) (bool, string, error)
}

// RecordStore reads and writes ScopedIdentity records in
// root:railgrid:system:tenants.
type RecordStore interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*tenancyv1alpha1.ScopedIdentity, error)
	List(ctx context.Context, opts metav1.ListOptions) (*tenancyv1alpha1.ScopedIdentityList, error)
	Create(ctx context.Context, obj *tenancyv1alpha1.ScopedIdentity, opts metav1.CreateOptions) (*tenancyv1alpha1.ScopedIdentity, error)
	Update(ctx context.Context, obj *tenancyv1alpha1.ScopedIdentity, opts metav1.UpdateOptions) (*tenancyv1alpha1.ScopedIdentity, error)
	UpdateStatus(ctx context.Context, obj *tenancyv1alpha1.ScopedIdentity, opts metav1.UpdateOptions) (*tenancyv1alpha1.ScopedIdentity, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
}

// Options configures a Service.
type Options struct {
	// Records is the ScopedIdentity store.
	Records RecordStore
	// Clients builds a kube client for one tenant logical cluster.
	Clients serviceaccounts.ClusterClientFactory
	// Policy decides which rules a provider may ask for.
	Policy *Policy
	// Owners probes owner objects. Required for the provider-asserted path.
	Owners OwnerProbe
	// Now is overridable in tests.
	Now func() time.Time
}

// Service is the scoped-identity service.
type Service struct {
	records RecordStore
	clients serviceaccounts.ClusterClientFactory
	policy  *Policy
	owners  OwnerProbe
	now     func() time.Time
}

// New constructs a Service.
func New(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{records: opts.Records, clients: opts.Clients, policy: opts.Policy, owners: opts.Owners, now: now}
}

// ServiceAccountName is the deterministic account name for an owner tuple:
// a hash of (provider, kind, owner name, owner UID), per the §10 design.
// It is hash-only on purpose — owner names can carry information that should
// not be reflected in cluster-wide RBAC object names — and it includes the UID
// so a deleted-and-recreated owner never inherits its predecessor's identity.
func ServiceAccountName(owner Owner) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		owner.Provider, owner.Group, owner.Kind, owner.Name, owner.UID,
	}, "\x00")))
	return serviceAccountPrefix + hex.EncodeToString(sum[:20])
}

// RecordName is the deterministic ScopedIdentity object name: the tenant
// cluster plus the account name. It keys on the ACCOUNT rather than the owner
// because one owner can have more than one account — an App Studio Project
// has one workload identity per (environment, instance) — while every account
// has exactly one record. Both modes' account names are themselves hashes of
// their full identity tuple, so this is unique in both.
func RecordName(clusterID, serviceAccount string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{clusterID, serviceAccount}, "\x00")))
	return recordPrefix + hex.EncodeToString(sum[:20])
}

// OwnerHash is the label value records are listed and released by. It keys on
// the owner tuple, so releasing an owner releases every account minted for it.
func OwnerHash(clusterID string, owner Owner) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		clusterID, owner.Provider, owner.Group, owner.Kind, owner.Name, owner.UID,
	}, "\x00")))
	return hex.EncodeToString(sum[:20])
}

// Ensure creates or refreshes the identity for req, attested as mode by
// subject, and returns a fresh token. It is idempotent: the same owner tuple
// always yields the same ServiceAccount, and repeating the call with the same
// rules rewrites nothing.
func (s *Service) Ensure(ctx context.Context, req Request, mode tenancyv1alpha1.ScopedIdentityAttestationMode, subject string) (*Token, error) {
	if s == nil || s.records == nil || s.clients == nil {
		return nil, fmt.Errorf("identity service is unavailable")
	}
	clusterID := strings.TrimSpace(req.ClusterID)
	if clusterID == "" {
		clusterID = strings.TrimSpace(req.Owner.ClusterID)
	}
	if err := validateOwner(req.Owner, clusterID); err != nil {
		return nil, err
	}

	// The owner must exist, with the UID the caller named, BEFORE anything is
	// written. This is the provider-asserted equivalent of the workload path's
	// Project verification: a provider may attest itself, but it may not
	// invent the object it wants a credential for.
	if s.owners == nil {
		return nil, fmt.Errorf("identity service cannot verify owner objects")
	}
	found, uid, err := s.owners.Exists(ctx, clusterID, req.Owner)
	if err != nil {
		return nil, fmt.Errorf("verifying owner object: %w", err)
	}
	if !found {
		return nil, Refusal{Code: CodeUnknownOwner, Reason: fmt.Sprintf("%s %q does not exist in this workspace", req.Owner.Kind, req.Owner.Name)}
	}
	owner := req.Owner
	owner.ClusterID = clusterID
	if owner.UID == "" {
		owner.UID = uid
	}

	rules, err := s.policy.Authorize(req.Owner.Provider, clusterID, req.Rules)
	if err != nil {
		return nil, err
	}

	account := ServiceAccountName(owner)
	shape := serviceaccounts.ScopedIdentityShape{
		ServiceAccount: account,
		Annotations: map[string]string{
			AnnotationRecord:    RecordName(clusterID, account),
			AnnotationProvider:  owner.Provider,
			AnnotationOwnerKind: owner.Kind,
			AnnotationOwnerName: owner.Name,
			AnnotationOwnerUID:  owner.UID,
		},
		// The owner tuple is what the account IS. Reconciling one of these to
		// a different value would rebind a live credential to another object,
		// so it is refused; the name is a hash of the same tuple, so in
		// practice this fires only on a hash collision or a hand-made object.
		ImmutableAnnotations: []string{AnnotationProvider, AnnotationOwnerKind, AnnotationOwnerName, AnnotationOwnerUID},
		Rules:                rules,
		TokenTTL:             serviceaccounts.ClampScopedIdentityTTL(time.Duration(req.TTLSeconds)*time.Second, DefaultProviderTTL),
	}
	record, err := s.upsertRecord(ctx, clusterID, owner, mode, subject, shape)
	if err != nil {
		return nil, err
	}

	token, err := s.materialize(ctx, record)
	if err != nil {
		s.markFailed(ctx, record, err)
		return nil, err
	}
	return token, nil
}

// materialize reconciles the record's ServiceAccount, ClusterRole and binding
// in the tenant workspace and mints a token. It is shared by Ensure and by the
// reconciler, so a rule change reaches the workspace whether it arrived with a
// request or on a resync.
func (s *Service) materialize(ctx context.Context, record *tenancyv1alpha1.ScopedIdentity) (*Token, error) {
	cs, err := s.clients(record.Spec.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("reaching tenant workspace: %w", err)
	}
	issued, err := serviceaccounts.EnsureScopedIdentity(ctx, cs, Shape(record))
	if err != nil {
		return nil, err
	}
	s.markReady(ctx, record, issued)
	return &Token{
		Token:          issued.Token,
		TokenType:      "Bearer",
		ExpiresAt:      issued.ExpiresAt.UTC(),
		ServiceAccount: issued.ServiceAccountName,
		Name:           record.Name,
	}, nil
}

// Shape converts a record into the minter's shape. Everything the minter
// writes comes from here, and everything here comes from the record, so the
// record really is the full description of what exists in the tenant
// workspace — which is what lets garbage collection delete exactly the right
// objects without knowing which attestation mode created them.
func Shape(record *tenancyv1alpha1.ScopedIdentity) serviceaccounts.ScopedIdentityShape {
	return serviceaccounts.ScopedIdentityShape{
		ServiceAccount: record.Spec.ServiceAccountName,
		Labels: map[string]string{
			tenancyv1alpha1.LabelScopedIdentityProvider: record.Spec.Owner.Provider,
			tenancyv1alpha1.LabelScopedIdentityMode:     string(record.Spec.Attestation.Mode),
		},
		Annotations:          record.Spec.Annotations,
		ImmutableAnnotations: record.Spec.ImmutableAnnotationKeys,
		Rules:                record.Spec.Rules,
		TokenTTL:             time.Duration(record.Spec.TTLSeconds) * time.Second,
	}
}

// Annotations stamped on a provider-asserted identity's ServiceAccount. They
// are an audit marker: they say which record and which owner the account
// belongs to, so an operator reading a tenant workspace can tell what a
// railgrid-si-* account is for without hunting for the hash preimage.
const (
	AnnotationRecord    = "railgrid.ai/scoped-identity"
	AnnotationProvider  = "railgrid.ai/scoped-identity-provider"
	AnnotationOwnerKind = "railgrid.ai/scoped-identity-owner-kind"
	AnnotationOwnerName = "railgrid.ai/scoped-identity-owner-name"
	AnnotationOwnerUID  = "railgrid.ai/scoped-identity-owner-uid"
)

func (s *Service) upsertRecord(ctx context.Context, clusterID string, owner Owner, mode tenancyv1alpha1.ScopedIdentityAttestationMode, subject string, shape serviceaccounts.ScopedIdentityShape) (*tenancyv1alpha1.ScopedIdentity, error) {
	name := RecordName(clusterID, shape.ServiceAccount)
	spec := tenancyv1alpha1.ScopedIdentitySpec{
		Owner: tenancyv1alpha1.ScopedIdentityOwner{
			Provider: owner.Provider, Kind: owner.Kind, Group: owner.Group,
			Version: owner.Version, Resource: owner.Resource, Name: owner.Name,
			UID: owner.UID, ClusterID: clusterID,
		},
		ClusterID:               clusterID,
		ServiceAccountName:      shape.ServiceAccount,
		Annotations:             shape.Annotations,
		ImmutableAnnotationKeys: shape.ImmutableAnnotations,
		Attestation:             tenancyv1alpha1.ScopedIdentityAttestation{Mode: mode, Subject: subject},
		Rules:                   shape.Rules,
		TTLSeconds:              int64(shape.TokenTTL / time.Second),
	}
	labels := map[string]string{
		tenancyv1alpha1.LabelScopedIdentityProvider: owner.Provider,
		tenancyv1alpha1.LabelScopedIdentityOwner:    OwnerHash(clusterID, owner),
		tenancyv1alpha1.LabelScopedIdentityMode:     string(mode),
	}

	existing, err := s.records.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, err := s.records.Create(ctx, &tenancyv1alpha1.ScopedIdentity{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Spec:       spec,
		}, metav1.CreateOptions{})
		if err == nil {
			return created, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("recording scoped identity: %w", err)
		}
		existing, err = s.records.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("reading scoped identity record: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading scoped identity record: %w", err)
	}

	// The name is a hash, so a record under it that describes another tuple is
	// a collision or tampering, not a stale copy. Refuse rather than rewrite.
	if existing.Spec.Owner.Provider != owner.Provider || existing.Spec.Owner.Name != owner.Name ||
		existing.Spec.Owner.Kind != owner.Kind || existing.Spec.ClusterID != clusterID ||
		existing.Spec.ServiceAccountName != shape.ServiceAccount {
		return nil, fmt.Errorf("scoped identity record %q belongs to another owner", name)
	}
	if specEqual(existing.Spec, spec) {
		return existing, nil
	}
	updated := existing.DeepCopy()
	updated.Spec = spec
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for key, value := range labels {
		updated.Labels[key] = value
	}
	written, err := s.records.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("updating scoped identity record: %w", err)
	}
	return written, nil
}

// Release deletes the identity for an owner: the record, and with it the
// ServiceAccount (which revokes every outstanding token), the ClusterRole and
// the binding. It is idempotent.
func (s *Service) Release(ctx context.Context, clusterID string, owner Owner) error {
	if s == nil || s.records == nil {
		return fmt.Errorf("identity service is unavailable")
	}
	// An owner can have more than one account, so release everything labelled
	// for it rather than one name.
	records, err := s.List(ctx, owner.Provider, clusterID, &owner)
	if err != nil {
		return err
	}
	for i := range records {
		if err := s.deleteRecord(ctx, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseByName deletes one record by its object name, after checking that it
// belongs to provider. A provider may only delete its own identities.
func (s *Service) ReleaseByName(ctx context.Context, provider, name string) error {
	if s == nil || s.records == nil {
		return fmt.Errorf("identity service is unavailable")
	}
	record, err := s.records.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading scoped identity record: %w", err)
	}
	if record.Spec.Owner.Provider != provider {
		// Do not distinguish "not yours" from "not there": a provider must not
		// be able to probe for another provider's identities by name.
		return nil
	}
	return s.deleteRecord(ctx, record)
}

func (s *Service) deleteRecord(ctx context.Context, record *tenancyv1alpha1.ScopedIdentity) error {
	// Tear the credential down first. If the record were deleted first and the
	// teardown then failed, nothing would remember that the ServiceAccount is
	// still there — the standing credential would outlive its own audit trail,
	// which is the failure mode this service exists to end.
	if s.clients != nil {
		cs, err := s.clients(record.Spec.ClusterID)
		if err == nil {
			if err := serviceaccounts.DeleteScopedIdentity(ctx, cs, Shape(record).ServiceAccount); err != nil {
				return err
			}
		} else if !isWorkspaceGone(err) {
			return fmt.Errorf("reaching tenant workspace: %w", err)
		}
	}
	if err := s.records.Delete(ctx, record.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting scoped identity record: %w", err)
	}
	return nil
}

// List returns a provider's records, optionally narrowed to one owner. Tokens
// are never part of a listing: there is nothing to list, because none is kept.
func (s *Service) List(ctx context.Context, provider, clusterID string, owner *Owner) ([]tenancyv1alpha1.ScopedIdentity, error) {
	if s == nil || s.records == nil {
		return nil, fmt.Errorf("identity service is unavailable")
	}
	selector := tenancyv1alpha1.LabelScopedIdentityProvider + "=" + provider
	if owner != nil {
		selector += "," + tenancyv1alpha1.LabelScopedIdentityOwner + "=" + OwnerHash(clusterID, *owner)
	}
	list, err := s.records.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing scoped identities: %w", err)
	}
	items := make([]tenancyv1alpha1.ScopedIdentity, 0, len(list.Items))
	for _, item := range list.Items {
		// The label is tenant-invisible but the filter still verifies the spec:
		// a listing is an authorization decision, and a label is metadata.
		if item.Spec.Owner.Provider != provider {
			continue
		}
		if clusterID != "" && item.Spec.ClusterID != clusterID {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *Service) markReady(ctx context.Context, record *tenancyv1alpha1.ScopedIdentity, issued *serviceaccounts.ScopedIdentityToken) {
	updated := record.DeepCopy()
	expires := metav1.NewTime(issued.ExpiresAt)
	updated.Status = tenancyv1alpha1.ScopedIdentityStatus{
		Phase:              tenancyv1alpha1.ScopedIdentityReady,
		ServiceAccount:     issued.ServiceAccountName,
		ClusterRole:        issued.ClusterRoleName,
		ExpiresAt:          &expires,
		ObservedGeneration: record.Generation,
		Conditions: []metav1.Condition{{
			Type: tenancyv1alpha1.ScopedIdentityConditionReady, Status: metav1.ConditionTrue,
			Reason: "Materialized", Message: "ServiceAccount and RBAC match the record",
			LastTransitionTime: metav1.NewTime(s.now()), ObservedGeneration: record.Generation,
		}},
	}
	// Status is diagnostic. A failure to write it must not fail a request that
	// already produced a working credential; the next reconcile rewrites it.
	if written, err := s.records.UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err == nil && written != nil {
		*record = *written
	}
}

func (s *Service) markFailed(ctx context.Context, record *tenancyv1alpha1.ScopedIdentity, cause error) {
	updated := record.DeepCopy()
	updated.Status.Phase = tenancyv1alpha1.ScopedIdentityFailed
	updated.Status.ObservedGeneration = record.Generation
	updated.Status.Conditions = []metav1.Condition{{
		Type: tenancyv1alpha1.ScopedIdentityConditionReady, Status: metav1.ConditionFalse,
		Reason: "MaterializationFailed", Message: truncate(cause.Error(), 512),
		LastTransitionTime: metav1.NewTime(s.now()), ObservedGeneration: record.Generation,
	}}
	_, _ = s.records.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
}

func validateOwner(owner Owner, clusterID string) error {
	for field, value := range map[string]string{
		"clusterID":      clusterID,
		"owner.provider": owner.Provider,
		"owner.kind":     owner.Kind,
		"owner.version":  owner.Version,
		"owner.resource": owner.Resource,
		"owner.name":     owner.Name,
	} {
		if strings.TrimSpace(value) == "" {
			return Refusal{Code: CodeInvalidRequest, Reason: field + " is required"}
		}
		if strings.ContainsAny(value, "\r\n\x00 /") {
			return Refusal{Code: CodeInvalidRequest, Reason: field + " contains a prohibited character"}
		}
	}
	return nil
}

func specEqual(a, b tenancyv1alpha1.ScopedIdentitySpec) bool {
	if a.ClusterID != b.ClusterID || a.TTLSeconds != b.TTLSeconds ||
		a.Attestation.Mode != b.Attestation.Mode || a.Owner != b.Owner {
		return false
	}
	if len(a.Rules) != len(b.Rules) {
		return false
	}
	for i := range a.Rules {
		if ruleKey(a.Rules[i]) != ruleKey(b.Rules[i]) {
			return false
		}
	}
	return true
}

// isWorkspaceGone reports whether a workspace client could not be built
// because the workspace itself is gone. A deleted tenant workspace takes its
// ServiceAccounts with it, so the record should still be collected.
func isWorkspaceGone(err error) bool {
	return apierrors.IsNotFound(err)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
