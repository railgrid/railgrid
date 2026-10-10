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

package appauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/browsersession"
)

// PreviewHandoffPath exchanges a scoped workload credential for one-app browser access.
const PreviewHandoffPath = "/auth/apps/preview-handoff"
const previewCookieName = "__Host-railgrid-preview"
const previewHandoffPurpose = "preview-handoff"

// ErrInvalidPreviewIdentity marks a caller credential that was absent,
// malformed, unauthenticated, or resolved to an identity outside the requested
// workspace service-account contract. Other resolver errors represent an
// identity-verification dependency failure and must not be charged as a bad
// credential.
var ErrInvalidPreviewIdentity = errors.New("invalid preview identity")

// NewKCPPreviewIdentityResolver authenticates in the requested workspace. A
// provider account from another cluster must never become a tenant-local SA
// merely because both accounts have the same username.
func NewKCPPreviewIdentityResolver(config *rest.Config) func(*http.Request, InstanceRef) (browsersession.Identity, error) {
	return func(r *http.Request, ref InstanceRef) (browsersession.Identity, error) {
		token, ok := bearerFromRequest(r)
		if !ok {
			return browsersession.Identity{}, ErrInvalidPreviewIdentity
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, ref.Cluster)
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return browsersession.Identity{}, err
		}
		review, err := client.AuthenticationV1().TokenReviews().Create(r.Context(), &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
		if err != nil {
			// A caller-selected workspace can make kcp reject TokenReview with a
			// client error. Treat those as invalid identity so they are rate-limited
			// and do not expose whether the workspace exists. Server errors and
			// transport failures remain retryable dependency failures.
			if apierrors.IsBadRequest(err) || apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
				return browsersession.Identity{}, ErrInvalidPreviewIdentity
			}
			return browsersession.Identity{}, err
		}
		clusters := review.Status.User.Extra["authentication.kcp.io/cluster-name"]
		if !review.Status.Authenticated || len(clusters) != 1 || clusters[0] != ref.Cluster || !strings.HasPrefix(review.Status.User.Username, "system:serviceaccount:") {
			return browsersession.Identity{}, ErrInvalidPreviewIdentity
		}
		return browsersession.Identity{UserID: review.Status.User.Username, RBACIdentity: review.Status.User.Username, AuthType: "workload-preview", AppScope: ref.key()}, nil
	}
}

// HandlePreviewHandoff issues a short-lived, one-app browser capability. It
// never creates a portal session or accepts an asserted end-user identity.
// Existing name-scoped instances/proxy rights authorize the same app here.
func (h *Handler) HandlePreviewHandoff(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodGet {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if !strings.HasPrefix(code, previewHandoffCodePrefix) {
			http.Error(w, "preview handoff expired", http.StatusGone)
			return
		}
		record, ok := h.previewCodes.Take(r.Context(), code)
		if !ok || record.Purpose != previewHandoffPurpose || record.Identity.AppScope != record.Ref.key() || !h.now().Before(record.ExpiresAt) {
			http.Error(w, "preview handoff expired", http.StatusGone)
			return
		}
		ttl := record.Identity.AppExpiresAt.Sub(h.now())
		value, _, err := h.previewSessions.IssueTransient(r.Context(), record.Identity, ttl)
		if err != nil {
			http.Error(w, "preview session unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: previewCookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: max(1, int(ttl/time.Second))})
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "preview session ready")
		return
	}
	source := h.clientIP(r)
	if h.verifyFailures.blocked(source) {
		writeThrottled(w)
		return
	}
	var req MintRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req) != nil {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}
	ref := InstanceRef{Cluster: req.Cluster, Group: req.Group, Resource: req.Resource, Name: req.Name}
	if ref.validate() != nil || ref.Group != "infrastructure.railgrid.ai" || ref.Resource != "instances" {
		http.Error(w, "invalid preview target", http.StatusBadRequest)
		return
	}
	identity, err := h.previewIdentity(r, ref)
	if err != nil {
		if errors.Is(err, ErrInvalidPreviewIdentity) {
			h.rejectBearer(w, source)
			return
		}
		// Transient TokenReview API, configuration, and request-context failures are
		// not evidence that the caller supplied a bad credential. Preserve the
		// retryable dependency-failure response without spending their budget.
		http.Error(w, "preview identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	identity.AppScope = ref.key()
	identity.AppExpiresAt = h.now().Add(sessionTTL)
	token, _ := bearerFromRequest(r)
	if exp, ok := jwtExpiry(token); ok && exp.Before(identity.AppExpiresAt) {
		identity.AppExpiresAt = exp
	}
	if !h.now().Before(identity.AppExpiresAt) {
		h.rejectBearer(w, source)
		return
	}
	allowed, err := h.authorize(r.Context(), identity, ref)
	if err != nil {
		http.Error(w, "preview policy unavailable", http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		http.Error(w, "preview access denied", http.StatusForbidden)
		return
	}
	if _, err := h.instanceHost(r.Context(), ref); err != nil {
		http.Error(w, "preview host unavailable", http.StatusServiceUnavailable)
		return
	}
	expires := h.now().Add(time.Minute)
	if identity.AppExpiresAt.Before(expires) {
		expires = identity.AppExpiresAt
	}
	buf := make([]byte, 32)
	if _, err := io.ReadFull(h.random, buf); err != nil {
		http.Error(w, "preview handoff unavailable", http.StatusServiceUnavailable)
		return
	}
	code := previewHandoffCodePrefix + base64.RawURLEncoding.EncodeToString(buf)
	if err := h.previewCodes.Put(r.Context(), code, CodeRecord{Purpose: previewHandoffPurpose, Ref: ref, Identity: identity, ExpiresAt: expires}); err != nil {
		http.Error(w, "preview handoff unavailable", http.StatusServiceUnavailable)
		return
	}
	writeVerifyJSON(w, http.StatusOK, map[string]any{
		"path":      PreviewHandoffPath + "?code=" + url.QueryEscape(code),
		"expiresAt": identity.AppExpiresAt.Unix(),
	})
}
