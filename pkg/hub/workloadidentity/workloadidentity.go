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

// Package workloadidentity owns the production provider-action workload
// capability exchange. The hub does not parse or trust JWTs locally: an
// Attestor performs the provider-owned bootstrap review, after which this
// package asks the service-account manager for a short-lived, audience-bound
// TokenRequest credential.
package workloadidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
)

const (
	// PathExchange is the only production workload-identity route. There is
	// intentionally no compatibility alias for the prototype action endpoint.
	PathExchange = "/api/provider-actions/workload/exchange"

	// InfrastructureProviderName is the provider that owns bootstrap review and
	// runtime identity attestation. Its URL is looked up from the catalog, not
	// hardcoded in this package.
	InfrastructureProviderName = "infrastructure"

	workloadReviewPath   = "/workload-identities/review"
	maxExchangeBody      = 64 << 10
	maxReviewBody        = 64 << 10
	defaultReviewTimeout = 10 * time.Second
)

// ExchangeRequest is the frozen v1 body accepted by POST PathExchange. The
// body is strict: unknown fields are rejected so callers cannot smuggle an
// alternate audience, action, or resource selector into the review.
type ExchangeRequest struct {
	TenantPath  string `json:"tenantPath"`
	Project     string `json:"project"`
	ProjectUID  string `json:"projectUID"`
	Environment string `json:"environment"`
	Instance    string `json:"instance"`
}

// Review is the provider-owned bootstrap-attestation result. The provider
// must authenticate the bearer and verify every field in ExchangeRequest; the
// hub only accepts an authenticated result with a complete source identity.
type Review struct {
	Authenticated  bool   `json:"authenticated"`
	Subject        string `json:"subject"`
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"serviceAccount"`
}

// Attestor verifies the bootstrap bearer and identity tuple. Implementations
// must perform online/provider-owned verification; the hub never performs
// offline JWT signature or claim validation itself.
type Attestor interface {
	Verify(context.Context, string, ExchangeRequest) (Review, error)
}

// AttestorFunc adapts a function to Attestor and keeps focused handler tests
// independent of provider networking.
type AttestorFunc func(context.Context, string, ExchangeRequest) (Review, error)

// Verify implements Attestor.
func (f AttestorFunc) Verify(ctx context.Context, bearer string, req ExchangeRequest) (Review, error) {
	return f(ctx, bearer, req)
}

// TokenIssuer is the narrow service-account seam used by the exchange. The
// concrete serviceaccounts.Manager creates the deterministic SA, reconciles
// scoped GET-only RBAC, and calls TokenRequest with a fixed audience/TTL.
type TokenIssuer interface {
	EnsureWorkloadIdentity(context.Context, string, string, serviceaccounts.WorkloadIdentityScope) (*serviceaccounts.WorkloadIdentityToken, error)
}

// Options configures the exchange handler.
type Options struct {
	Attestor      Attestor
	Issuer        TokenIssuer
	ScopeResolver ProjectScopeResolver
	ReviewTimeout time.Duration
	Logger        logr.Logger
}

// Handler serves POST PathExchange.
type Handler struct {
	attestor      Attestor
	issuer        TokenIssuer
	scopeResolver ProjectScopeResolver
	reviewTimeout time.Duration
	log           logr.Logger
}

// New constructs an exchange handler. Missing dependencies fail closed at
// request time with 503 rather than silently minting an unverified token.
func New(opts Options) *Handler {
	timeout := opts.ReviewTimeout
	if timeout <= 0 || timeout > defaultReviewTimeout {
		timeout = defaultReviewTimeout
	}
	return &Handler{attestor: opts.Attestor, issuer: opts.Issuer, scopeResolver: opts.ScopeResolver, reviewTimeout: timeout, log: opts.Logger}
}

// ServeHTTP handles the workload identity exchange.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	bearer, ok := bearerCredential(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "a bearer credential is required")
		return
	}
	if h == nil || h.attestor == nil || h.issuer == nil || h.scopeResolver == nil {
		writeError(w, http.StatusServiceUnavailable, "workload_identity_unavailable", "workload identity exchange is unavailable")
		return
	}
	req, err := decodeExchangeRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	orgUUID, wsUUID, err := parseTenantPath(req.TenantPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateIdentityFields(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.reviewTimeout)
	defer cancel()
	review, err := h.attestor.Verify(ctx, bearer, req)
	if err != nil || !review.Authenticated || !completeReview(review) {
		// Do not reveal whether the provider rejected the bearer, the tuple, or
		// its own review operation. All are an attestation failure to callers.
		writeError(w, http.StatusForbidden, "attestation_failed", "bootstrap attestation was not accepted")
		return
	}

	scope, err := h.scopeResolver.Resolve(ctx, orgUUID, wsUUID, req)
	if err != nil {
		h.log.Error(err, "workload identity project scope verification failed", "tenantPath", req.TenantPath, "project", req.Project, "environment", req.Environment, "instance", req.Instance)
		writeError(w, http.StatusForbidden, "scope_denied", "workload identity scope was not accepted")
		return
	}
	token, err := h.issuer.EnsureWorkloadIdentity(ctx, orgUUID, wsUUID, scope)
	if err != nil {
		h.log.Error(err, "workload identity token issuance failed", "tenantPath", req.TenantPath, "project", req.Project, "environment", req.Environment, "instance", req.Instance)
		writeError(w, http.StatusBadGateway, "token_issue_failed", "workload identity could not be issued")
		return
	}
	if token == nil || strings.TrimSpace(token.Token) == "" || token.ExpiresAt.IsZero() || token.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusBadGateway, "token_issue_failed", "workload identity provider returned an invalid token")
		return
	}

	writeJSON(w, http.StatusOK, struct {
		Token     string    `json:"token"`
		TokenType string    `json:"tokenType"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{Token: token.Token, TokenType: "Bearer", ExpiresAt: token.ExpiresAt.UTC()})
}

func decodeExchangeRequest(w http.ResponseWriter, r *http.Request) (ExchangeRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxExchangeBody)
	var req ExchangeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return ExchangeRequest{}, fmt.Errorf("decode request: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ExchangeRequest{}, errors.New("request must contain exactly one JSON value")
		}
		return ExchangeRequest{}, fmt.Errorf("decode trailing request data: %w", err)
	}
	return req, nil
}

func validateIdentityFields(req ExchangeRequest) error {
	for field, value := range map[string]string{
		"tenantPath":  req.TenantPath,
		"project":     req.Project,
		"projectUID":  req.ProjectUID,
		"environment": req.Environment,
		"instance":    req.Instance,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s contains prohibited characters", field)
		}
	}
	for field, value := range map[string]string{"project": req.Project, "environment": req.Environment, "instance": req.Instance} {
		if errs := validation.IsDNS1123Subdomain(value); len(errs) != 0 {
			return fmt.Errorf("%s is not a valid resource name", field)
		}
	}
	return nil
}

func completeReview(review Review) bool {
	return strings.TrimSpace(review.Subject) != "" && strings.TrimSpace(review.Namespace) != "" && strings.TrimSpace(review.ServiceAccount) != ""
}

func parseTenantPath(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 5 || parts[0] != "root" || parts[1] != "faros" || parts[2] != "tenants" || parts[3] == "" || parts[4] == "" {
		return "", "", errors.New("tenantPath must be root:faros:tenants:<orgUUID>:<workspaceUUID>")
	}
	return parts[3], parts[4], nil
}

func bearerCredential(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= len("Bearer ") || !strings.EqualFold(trimmed[:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(trimmed[len("Bearer "):])
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", false
	}
	return "Bearer " + token, true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

// HTTPAttestor is the hub-side online attestor. It resolves the Infrastructure
// provider's declared virtual-workspace URL from the catalog and forwards the
// same bearer/body to /workload-identities/review. It does not parse JWTs or
// retain provider credentials.
type HTTPAttestor struct {
	registry ProviderLookup
	client   *http.Client
	timeout  time.Duration
}

// ProviderLookup is the minimal catalog seam needed by HTTPAttestor.
type ProviderLookup interface {
	Get(string) (providers.Provider, bool)
}

// HTTPAttestorOptions configures HTTPAttestor.
type HTTPAttestorOptions struct {
	Registry ProviderLookup
	Client   *http.Client
	Timeout  time.Duration
}

// NewHTTPAttestor constructs an online Infrastructure review client.
func NewHTTPAttestor(opts HTTPAttestorOptions) *HTTPAttestor {
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	// Never let the default client follow an Infrastructure redirect with the
	// bootstrap bearer attached. Clone the caller's client so this policy is
	// local to attestation and does not mutate a shared transport.
	clientCopy := *client
	previousRedirect := clientCopy.CheckRedirect
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && req.Header.Get("Authorization") != "" {
			return errors.New("attestation redirect would carry bootstrap bearer")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return http.ErrUseLastResponse
	}
	timeout := opts.Timeout
	if timeout <= 0 || timeout > defaultReviewTimeout {
		timeout = defaultReviewTimeout
	}
	return &HTTPAttestor{registry: opts.Registry, client: &clientCopy, timeout: timeout}
}

// Verify implements Attestor.
func (a *HTTPAttestor) Verify(ctx context.Context, bearer string, req ExchangeRequest) (Review, error) {
	if a == nil || a.registry == nil {
		return Review{}, errors.New("infrastructure attestor is unavailable")
	}
	provider, ok := a.registry.Get(InfrastructureProviderName)
	if !ok || !provider.Ready() || provider.BackendURL == nil {
		return Review{}, errors.New("infrastructure attestation endpoint is unavailable")
	}
	// The attestation endpoint lives on the provider's backend origin under a
	// hub-only reserved prefix: the backend proxy 404s /workload-identities/*
	// so a caller bearer can never reach it, and only the hub dials it here.
	target, err := reviewURL(provider.BackendURL)
	if err != nil {
		return Review{}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Review{}, fmt.Errorf("encode attestation request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	forward, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return Review{}, fmt.Errorf("build attestation request: %w", err)
	}
	forward.Header.Set("Accept", "application/json")
	forward.Header.Set("Content-Type", "application/json")
	forward.Header.Set("Authorization", bearer)
	response, err := a.client.Do(forward)
	if err != nil {
		return Review{}, fmt.Errorf("attestation request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxReviewBody+1))
	if err != nil || len(responseBody) > maxReviewBody {
		return Review{}, errors.New("attestation response could not be read")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Review{}, fmt.Errorf("attestation provider returned HTTP %d", response.StatusCode)
	}
	var review Review
	dec := json.NewDecoder(bytes.NewReader(responseBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&review); err != nil {
		return Review{}, fmt.Errorf("decode attestation response: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Review{}, errors.New("attestation response must contain exactly one JSON value")
	}
	return review, nil
}

func reviewURL(base *url.URL) (*url.URL, error) {
	if base == nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("infrastructure virtual workspace URL is invalid")
	}
	target := *base
	target.Path = strings.TrimRight(base.Path, "/") + workloadReviewPath
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	return &target, nil
}
