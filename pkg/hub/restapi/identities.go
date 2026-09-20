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

package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	rbacv1 "k8s.io/api/rbac/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// The scoped-identity REST surface. It is NOT mounted under the tenant-scoped
// subrouter: its callers are providers holding their own service-account
// token, not humans holding an org membership, so it authenticates itself the
// way the heartbeat does — bearer → TokenReview in the provider's workspace →
// subject must be that workspace's provider service account. A caller is
// therefore always exactly one provider, and that provider is the only one
// whose identities it can create, list or delete.
const (
	// PathIdentities is the collection: POST to create-or-refresh, GET to
	// list.
	PathIdentities = "/api/identities"
	// PathIdentity is one record by name: DELETE to revoke.
	PathIdentity = "/api/identities/{name}"

	maxIdentityBody = 128 << 10
)

// IdentityAttestor authenticates a provider-asserted identity request and
// returns the authenticated subject.
type IdentityAttestor interface {
	Attest(ctx context.Context, r *http.Request, provider string) (string, error)
}

// IdentityService is the seam the handler drives.
type IdentityService interface {
	Ensure(ctx context.Context, req identity.Request, mode tenancyv1alpha1.ScopedIdentityAttestationMode, subject string) (*identity.Token, error)
	ReleaseByName(ctx context.Context, provider, name string) error
	List(ctx context.Context, provider, clusterID string, owner *identity.Owner) ([]tenancyv1alpha1.ScopedIdentity, error)
}

// IdentityHandler serves /api/identities.
type IdentityHandler struct {
	service  IdentityService
	attestor IdentityAttestor
	log      logr.Logger
}

// NewIdentityHandler constructs the handler. Missing dependencies fail closed
// at request time with 503 rather than minting anything unverified.
func NewIdentityHandler(service IdentityService, attestor IdentityAttestor, log logr.Logger) *IdentityHandler {
	return &IdentityHandler{service: service, attestor: attestor, log: log}
}

// Register mounts the routes on router.
func (h *IdentityHandler) Register(router *mux.Router) {
	router.HandleFunc(PathIdentities, h.create).Methods(http.MethodPost)
	router.HandleFunc(PathIdentities, h.list).Methods(http.MethodGet)
	router.HandleFunc(PathIdentity, h.delete).Methods(http.MethodDelete)
}

// identityRequest is the POST body. The requesting provider names ITSELF in
// owner.provider and proves it with its own token; there is no separate
// "requester" field, because an identity is always for an object in the
// requester's own API group.
type identityRequest struct {
	Owner      identity.Owner      `json:"owner"`
	ClusterID  string              `json:"clusterID"`
	Rules      []rbacv1.PolicyRule `json:"rules"`
	TTLSeconds int64               `json:"ttlSeconds,omitempty"`
}

type identityResponse struct {
	Token          string    `json:"token"`
	TokenType      string    `json:"tokenType"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ServiceAccount string    `json:"serviceAccount"`
	Name           string    `json:"name"`
}

type identityListItem struct {
	Name           string              `json:"name"`
	Owner          identity.Owner      `json:"owner"`
	ClusterID      string              `json:"clusterID"`
	ServiceAccount string              `json:"serviceAccount"`
	Rules          []rbacv1.PolicyRule `json:"rules,omitempty"`
	TTLSeconds     int64               `json:"ttlSeconds"`
	Phase          string              `json:"phase,omitempty"`
	ExpiresAt      *time.Time          `json:"expiresAt,omitempty"`
}

func (h *IdentityHandler) create(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil || h.attestor == nil {
		writeIdentityError(w, http.StatusServiceUnavailable, "identity_service_unavailable", "the scoped identity service is unavailable")
		return
	}
	req, err := decodeIdentityRequest(w, r)
	if err != nil {
		writeIdentityError(w, http.StatusBadRequest, identity.CodeInvalidRequest, err.Error())
		return
	}
	provider := strings.TrimSpace(req.Owner.Provider)
	if provider == "" {
		writeIdentityError(w, http.StatusBadRequest, identity.CodeInvalidRequest, "owner.provider is required")
		return
	}
	subject, err := h.attestor.Attest(r.Context(), r, provider)
	if err != nil {
		status, code := identityAuthStatus(err)
		h.log.V(4).Info("scoped identity request not attested", "provider", provider, "err", err.Error())
		writeIdentityError(w, status, code, identityAuthMessage(status))
		return
	}

	token, err := h.service.Ensure(r.Context(), identity.Request{
		Owner: req.Owner, ClusterID: req.ClusterID, Rules: req.Rules, TTLSeconds: req.TTLSeconds,
	}, tenancyv1alpha1.ScopedIdentityAttestationProvider, subject)
	if err != nil {
		// A policy refusal is the caller's problem and names the offending
		// rule; anything else is the hub's and says nothing about tenant state.
		var refusal identity.Refusal
		if errors.As(err, &refusal) {
			writeIdentityError(w, http.StatusForbidden, refusal.Code, refusal.Reason)
			return
		}
		h.log.Error(err, "minting scoped identity", "provider", provider, "kind", req.Owner.Kind, "owner", req.Owner.Name)
		writeIdentityError(w, http.StatusBadGateway, "identity_issue_failed", "the scoped identity could not be issued")
		return
	}
	writeIdentityJSON(w, http.StatusOK, identityResponse{
		Token: token.Token, TokenType: token.TokenType, ExpiresAt: token.ExpiresAt.UTC(),
		ServiceAccount: token.ServiceAccount, Name: token.Name,
	})
}

func (h *IdentityHandler) list(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil || h.attestor == nil {
		writeIdentityError(w, http.StatusServiceUnavailable, "identity_service_unavailable", "the scoped identity service is unavailable")
		return
	}
	query := r.URL.Query()
	provider := strings.TrimSpace(query.Get("provider"))
	if provider == "" {
		writeIdentityError(w, http.StatusBadRequest, identity.CodeInvalidRequest, "provider is required")
		return
	}
	subject, err := h.attestor.Attest(r.Context(), r, provider)
	if err != nil {
		status, code := identityAuthStatus(err)
		writeIdentityError(w, status, code, identityAuthMessage(status))
		return
	}
	_ = subject

	clusterID := strings.TrimSpace(query.Get("clusterID"))
	var owner *identity.Owner
	// ?owner=<kind>/<name>[/<uid>] narrows to one object. The provider and
	// group are the caller's own, so they are not part of the selector.
	if raw := strings.TrimSpace(query.Get("owner")); raw != "" {
		parts := strings.Split(raw, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" || clusterID == "" {
			writeIdentityError(w, http.StatusBadRequest, identity.CodeInvalidRequest, "owner must be <kind>/<name>[/<uid>] and clusterID must be set")
			return
		}
		candidate := identity.Owner{
			Provider: provider, Kind: parts[0], Name: parts[1],
			Group: strings.TrimSpace(query.Get("group")),
		}
		if len(parts) > 2 {
			candidate.UID = parts[2]
		}
		owner = &candidate
	}

	records, err := h.service.List(r.Context(), provider, clusterID, owner)
	if err != nil {
		h.log.Error(err, "listing scoped identities", "provider", provider)
		writeIdentityError(w, http.StatusBadGateway, "identity_list_failed", "scoped identities could not be listed")
		return
	}
	items := make([]identityListItem, 0, len(records))
	for i := range records {
		items = append(items, identityListItemFor(&records[i]))
	}
	writeIdentityJSON(w, http.StatusOK, struct {
		Items []identityListItem `json:"items"`
	}{Items: items})
}

func (h *IdentityHandler) delete(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil || h.attestor == nil {
		writeIdentityError(w, http.StatusServiceUnavailable, "identity_service_unavailable", "the scoped identity service is unavailable")
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		writeIdentityError(w, http.StatusBadRequest, identity.CodeInvalidRequest, "provider is required")
		return
	}
	if _, err := h.attestor.Attest(r.Context(), r, provider); err != nil {
		status, code := identityAuthStatus(err)
		writeIdentityError(w, status, code, identityAuthMessage(status))
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		writeIdentityError(w, http.StatusBadRequest, identity.CodeInvalidRequest, "name is required")
		return
	}
	if err := h.service.ReleaseByName(r.Context(), provider, name); err != nil {
		h.log.Error(err, "releasing scoped identity", "provider", provider, "record", name)
		writeIdentityError(w, http.StatusBadGateway, "identity_release_failed", "the scoped identity could not be released")
		return
	}
	// Idempotent: a record that was never there, or belongs to another
	// provider, is reported the same way. Deleting twice is not an error and
	// a caller cannot probe for other providers' records.
	w.WriteHeader(http.StatusNoContent)
}

func identityListItemFor(record *tenancyv1alpha1.ScopedIdentity) identityListItem {
	item := identityListItem{
		Name: record.Name,
		Owner: identity.Owner{
			Provider: record.Spec.Owner.Provider, Kind: record.Spec.Owner.Kind,
			Group: record.Spec.Owner.Group, Version: record.Spec.Owner.Version,
			Resource: record.Spec.Owner.Resource, Name: record.Spec.Owner.Name,
			UID: record.Spec.Owner.UID, ClusterID: record.Spec.Owner.ClusterID,
		},
		ClusterID:      record.Spec.ClusterID,
		ServiceAccount: record.Spec.ServiceAccountName,
		Rules:          record.Spec.Rules,
		TTLSeconds:     record.Spec.TTLSeconds,
		Phase:          string(record.Status.Phase),
	}
	if record.Status.ExpiresAt != nil {
		expires := record.Status.ExpiresAt.UTC()
		item.ExpiresAt = &expires
	}
	return item
}

func decodeIdentityRequest(w http.ResponseWriter, r *http.Request) (identityRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxIdentityBody)
	var req identityRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return identityRequest{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return identityRequest{}, errors.New("request must contain exactly one JSON value")
	}
	return req, nil
}

// identityAuthStatus maps an attestation error onto a status, reusing the
// heartbeat's sentinels so one verifier means one answer.
func identityAuthStatus(err error) (int, string) {
	switch {
	case errors.Is(err, providers.ErrHeartbeatNoBearer):
		return http.StatusUnauthorized, "unauthenticated"
	case errors.Is(err, providers.ErrHeartbeatTokenRejected):
		return http.StatusUnauthorized, "unauthenticated"
	case errors.Is(err, providers.ErrHeartbeatWrongIdentity):
		return http.StatusForbidden, "wrong_identity"
	default:
		return http.StatusServiceUnavailable, "attestation_unavailable"
	}
}

// identityAuthMessage is the whole body for a refused request. Like the
// heartbeat's, it varies only with the status class: this endpoint is mounted
// with no auth middleware in front of it, and the authenticator's errors name
// logical clusters and expected usernames, which is a map of the deployment.
func identityAuthMessage(status int) string {
	switch status {
	case http.StatusForbidden:
		return "the bearer token is not this provider's service account"
	case http.StatusUnauthorized:
		return "a provider service-account bearer token is required"
	default:
		return "identity attestation is unavailable"
	}
}

func writeIdentityJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeIdentityError(w http.ResponseWriter, status int, code, message string) {
	writeIdentityJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}
