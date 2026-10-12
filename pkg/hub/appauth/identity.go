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
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/browsersession"
)

const (
	// IdentityIssuerPath is the stable issuer URL suffix for Railgrid Auth.
	IdentityIssuerPath          = "/auth/apps/identity"
	identityAssertionTTL        = 15 * time.Minute
	identitySubjectKeyMinLength = 32
	identityPKCEMethodS256      = "S256"
	identityPKCEVerifierMin     = 43
	identityPKCEVerifierMax     = 128
)

var identityEnvironmentRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// identityAuthorizeInput is intentionally assembled from a fixed set of
// query parameters; duplicate parameters are rejected rather than silently
// choosing one value.
type identityAuthorizeInput struct {
	Ref           InstanceRef
	AppID         string
	RedirectURI   string
	Origin        string
	State         string
	CodeChallenge string
}

// identityExchangeRequest is the app proxy's server-to-server redemption.
// Every field is checked against the one-use code and the current instance.
type identityExchangeRequest struct {
	Code         string `json:"code"`
	Origin       string `json:"origin"`
	Cluster      string `json:"cluster"`
	Group        string `json:"group"`
	Resource     string `json:"resource"`
	Name         string `json:"name"`
	AppID        string `json:"app_id"`
	CodeVerifier string `json:"code_verifier"`
}

// IdentityExchangeResponse contains the signed assertion and the small
// display profile needed to establish an app-local session. No hub bearer,
// provider credential, RBAC identity, or platform User ID leaves the hub.
type IdentityExchangeResponse struct {
	Assertion string              `json:"assertion"`
	ExpiresIn int64               `json:"expiresIn"`
	User      IdentityUserProfile `json:"user"`
}

// IdentityUserProfile is the signed-in user's app-facing display profile.
type IdentityUserProfile struct {
	Name string `json:"name,omitempty"`
}

type identityJWTHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type identityJWTClaims struct {
	Issuer      string `json:"iss"`
	Audience    string `json:"aud"`
	Subject     string `json:"sub"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
	AppID       string `json:"railgrid_app_id"`
	Environment string `json:"railgrid_environment"`
	InstanceUID string `json:"railgrid_instance_uid"`
	Name        string `json:"name,omitempty"`
}

type identityJWKS struct {
	Keys []identityJWK `json:"keys"`
}

type identityJWK struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	Use     string `json:"use"`
	Alg     string `json:"alg"`
	KeyID   string `json:"kid"`
	X       string `json:"x"`
}

func (h *Handler) HandleIdentityAuthorize(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	input, err := parseIdentityAuthorizeInput(r.URL)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Invalid app sign-in request",
			"The application sign-in request was malformed. Return to the app and try again.")
		return
	}

	session, err := h.sessions.ResolveRequest(r)
	if err != nil {
		if r.URL.Query().Get(retriedParam) != "" {
			klog.FromContext(r.Context()).Info("app identity authorize: session still unresolved after login, refusing to bounce",
				"reason", resolveFailureReason(r, err), "app", input.Ref.Name)
			h.renderError(w, http.StatusUnauthorized, "Sign-in could not be completed",
				"The sign-in did not carry a usable session. Check that cookies are enabled for this site, then open the app again.")
			return
		}
		browsersession.ClearCookie(w)
		next := withRetriedMarker(r.URL)
		http.Redirect(w, r, h.loginPath+"?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	if session.Identity.UserID == "" || session.Identity.RBACIdentity == "" || session.Identity.AppScope != "" {
		h.renderError(w, http.StatusForbidden, "App sign-in denied",
			"This application requires a signed-in Railgrid account.")
		return
	}
	active, err := h.activeIdentityAccount(r.Context(), session.Identity)
	if err != nil {
		h.renderError(w, http.StatusServiceUnavailable, "App sign-in is unavailable",
			"The platform could not verify your account. Try again shortly.")
		return
	}
	if !active {
		h.renderError(w, http.StatusForbidden, "App sign-in denied",
			"This application requires an active Railgrid account.")
		return
	}

	// Always require the same explicit get <instance>/access SAR, including
	// public applications. Public visibility controls discovery, never identity
	// issuance or access to an account-backed app session.
	allowed, err := h.authorize(r.Context(), session.Identity, input.Ref)
	if err != nil {
		h.renderError(w, http.StatusServiceUnavailable, "App access is unavailable",
			"The platform could not evaluate access policy. Try again shortly.")
		return
	}
	if !allowed {
		h.renderError(w, http.StatusForbidden, "App access denied",
			"Your account does not have access to this application. Ask the app owner to share it with you.")
		return
	}

	app, err := h.identityApp(r.Context(), input.Ref)
	if err != nil {
		if errors.Is(err, ErrInstanceNotPublished) {
			h.renderError(w, http.StatusForbidden, "App sign-in is unavailable",
				"Railgrid Auth is not enabled for this application.")
			return
		}
		h.renderError(w, http.StatusServiceUnavailable, "App sign-in is unavailable",
			"The platform could not verify the application. Try again shortly.")
		return
	}
	if !validIdentityApp(app) || app.AppID != input.AppID {
		h.renderError(w, http.StatusForbidden, "App sign-in is unavailable",
			"Railgrid Auth is not enabled for this application.")
		return
	}
	uidAllowed, err := h.authorizeIdentityAccess(r.Context(), session.Identity, input.Ref, app.AppID, app.InstanceUID)
	if err != nil {
		h.renderError(w, http.StatusServiceUnavailable, "App access is unavailable",
			"The platform could not evaluate access policy. Try again shortly.")
		return
	}
	if !uidAllowed {
		h.renderError(w, http.StatusForbidden, "App access denied",
			"Your account does not have access to this application. Ask the app owner to share it with you.")
		return
	}
	member, err := h.identityEnvironmentAllowed(r.Context(), session.Identity, input.Ref, app)
	if err != nil {
		h.renderError(w, http.StatusServiceUnavailable, "App access is unavailable",
			"The platform could not verify workspace membership. Try again shortly.")
		return
	}
	if !member {
		h.renderError(w, http.StatusForbidden, "App access denied",
			"This environment is limited to workspace members.")
		return
	}
	if input.Origin != app.PublishedOrigin {
		h.renderError(w, http.StatusBadRequest, "Invalid app sign-in request",
			"The sign-in redirect does not match this application's published address.")
		return
	}

	code, err := h.mintIdentityCode(r.Context(), input.Ref, input.Origin, input.CodeChallenge, app, session.Identity)
	if err != nil {
		h.renderError(w, http.StatusInternalServerError, "App sign-in is unavailable",
			"The platform could not complete sign-in. Try again shortly.")
		return
	}
	target, _ := url.Parse(input.RedirectURI)
	query := url.Values{}
	query.Set("code", code)
	query.Set("state", input.State)
	target.RawQuery = query.Encode()
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (h *Handler) HandleIdentityExchange(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req identityExchangeRequest
	body := http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "malformed identity exchange request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "malformed identity exchange request", http.StatusBadRequest)
		return
	}
	ref := InstanceRef{Cluster: req.Cluster, Group: req.Group, Resource: req.Resource, Name: req.Name}
	origin, originErr := canonicalOriginOnly(req.Origin)
	if ref.validate() != nil || !strings.HasPrefix(req.Code, identityAppCodePrefix) || originErr != nil ||
		!validAppID(req.AppID) || !validIdentityCodeVerifier(req.CodeVerifier) {
		http.Error(w, "malformed identity exchange request", http.StatusBadRequest)
		return
	}

	record, ok := h.codes.Take(r.Context(), req.Code)
	if !ok || record.Purpose != IdentityPurpose || record.Ref.key() != ref.key() ||
		record.RedirectOrigin != origin || record.AppID != req.AppID || record.InstanceUID == "" ||
		!validIdentityCodeChallenge(record.CodeChallenge) || !identityCodeVerifierMatches(req.CodeVerifier, record.CodeChallenge) ||
		record.Identity.UserID == "" || record.Identity.RBACIdentity == "" || record.Identity.AppScope != "" ||
		!h.now().Before(record.ExpiresAt) {
		http.Error(w, "app sign-in expired", http.StatusGone)
		return
	}
	active, err := h.activeIdentityAccount(r.Context(), record.Identity)
	if err != nil {
		http.Error(w, "app account is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if !active {
		http.Error(w, "app account is no longer active", http.StatusForbidden)
		return
	}

	// A code is one-use even when its app was removed or its access grant was
	// revoked while the browser was at the callback. Re-read the descriptor and
	// rerun authorization before signing any assertion.
	app, err := h.identityApp(r.Context(), ref)
	if err != nil {
		if errors.Is(err, ErrInstanceNotPublished) {
			http.Error(w, "app sign-in expired", http.StatusGone)
			return
		}
		http.Error(w, "app sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if !validIdentityApp(app) || app.InstanceUID != record.InstanceUID || app.AppID != record.AppID ||
		app.Environment != record.Environment || app.PublishedOrigin != record.RedirectOrigin {
		http.Error(w, "app sign-in expired", http.StatusGone)
		return
	}
	member, err := h.identityEnvironmentAllowed(r.Context(), record.Identity, ref, app)
	if err != nil {
		http.Error(w, "app workspace membership is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if !member {
		http.Error(w, "workspace member access required", http.StatusForbidden)
		return
	}
	allowed, err := h.authorize(r.Context(), record.Identity, ref)
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		http.Error(w, "app access denied", http.StatusForbidden)
		return
	}
	uidAllowed, err := h.authorizeIdentityAccess(r.Context(), record.Identity, ref, app.AppID, app.InstanceUID)
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if !uidAllowed {
		http.Error(w, "app access denied", http.StatusForbidden)
		return
	}

	assertion, err := h.signIdentityAssertion(r.Context(), record.Identity, app)
	if err != nil {
		klog.FromContext(r.Context()).Error(err, "app identity assertion signing failed")
		http.Error(w, "app sign-in is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(IdentityExchangeResponse{
		Assertion: assertion,
		ExpiresIn: int64(identityAssertionTTL / time.Second),
		User: IdentityUserProfile{
			Name: safeIdentityDisplayName(record.Identity.Name),
		},
	})
}

func (h *Handler) HandleIdentityJWKS(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var privateKey ed25519.PrivateKey
	var publicKeys []ed25519.PublicKey
	var err error
	if h.identityKeys != nil {
		var keys IdentityKeySnapshot
		keys, err = h.identityKeySnapshot(r.Context())
		privateKey, publicKeys = keys.SigningKey, keys.PublicKeys
	} else {
		privateKey, err = h.currentIdentitySigningKey(r.Context())
		if err == nil {
			publicKeys, err = h.loadIdentityPublicKeys(r.Context())
		}
	}
	if err != nil {
		klog.FromContext(r.Context()).Error(err, "app identity JWKS public keys unavailable")
		http.Error(w, "app identity keys are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	currentPublicKey := privateKey.Public().(ed25519.PublicKey)
	if len(publicKeys) == 0 || len(publicKeys[0]) != ed25519.PublicKeySize || !ed25519.PublicKey(publicKeys[0]).Equal(currentPublicKey) {
		klog.FromContext(r.Context()).Error(fmt.Errorf("app identity JWKS does not start with the current signing key"), "app identity JWKS is inconsistent")
		http.Error(w, "app identity keys are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	keys := make([]identityJWK, 0, len(publicKeys))
	seen := make(map[string]struct{}, len(publicKeys))
	for _, publicKey := range publicKeys {
		if len(publicKey) != ed25519.PublicKeySize {
			http.Error(w, "app identity keys are temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		keyID := identityKeyID(publicKey)
		if _, exists := seen[keyID]; exists {
			continue
		}
		seen[keyID] = struct{}{}
		keys = append(keys, identityJWK{
			KeyType: "OKP",
			Curve:   "Ed25519",
			Use:     "sig",
			Alg:     "EdDSA",
			KeyID:   keyID,
			X:       base64.RawURLEncoding.EncodeToString(publicKey),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(identityJWKS{Keys: keys})
}

func parseIdentityAuthorizeInput(u *url.URL) (identityAuthorizeInput, error) {
	var input identityAuthorizeInput
	if u == nil {
		return input, fmt.Errorf("missing URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return input, fmt.Errorf("invalid authorize query")
	}
	for key, values := range q {
		switch key {
		case "cluster", "group", "resource", "name", "app_id", "redirect_uri", "state", "code_challenge", "code_challenge_method":
		case retriedParam:
			if len(values) != 1 || values[0] != "1" {
				return input, fmt.Errorf("invalid %s", retriedParam)
			}
		default:
			return input, fmt.Errorf("unexpected parameter %s", key)
		}
	}
	getOne := func(key string) (string, error) {
		values, ok := q[key]
		if !ok || len(values) != 1 || strings.TrimSpace(values[0]) != values[0] {
			return "", fmt.Errorf("invalid %s", key)
		}
		return values[0], nil
	}
	if input.Ref.Cluster, err = getOne("cluster"); err != nil {
		return input, err
	}
	if input.Ref.Group, err = getOne("group"); err != nil {
		return input, err
	}
	if input.Ref.Resource, err = getOne("resource"); err != nil {
		return input, err
	}
	if input.Ref.Name, err = getOne("name"); err != nil {
		return input, err
	}
	if input.AppID, err = getOne("app_id"); err != nil || !validAppID(input.AppID) {
		return input, fmt.Errorf("invalid app_id")
	}
	if input.RedirectURI, err = getOne("redirect_uri"); err != nil {
		return input, err
	}
	if input.State, err = getOne("state"); err != nil || !stateRE.MatchString(input.State) {
		return input, fmt.Errorf("invalid state")
	}
	if input.CodeChallenge, err = getOne("code_challenge"); err != nil || !validIdentityCodeChallenge(input.CodeChallenge) {
		return input, fmt.Errorf("invalid code_challenge")
	}
	challengeMethod, err := getOne("code_challenge_method")
	if err != nil || challengeMethod != "S256" {
		return input, fmt.Errorf("code_challenge_method must be S256")
	}
	if input.Ref.validate() != nil {
		return input, fmt.Errorf("invalid instance reference")
	}
	redirect, origin, err := validateIdentityRedirect(input.RedirectURI)
	if err != nil {
		return input, err
	}
	input.RedirectURI = redirect.String()
	input.Origin = origin
	return input, nil
}

func validateIdentityRedirect(raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" || u.EscapedPath() != IdentityCallbackPath {
		return nil, "", fmt.Errorf("invalid identity callback")
	}
	origin, err := canonicalHTTPSOrigin(u)
	if err != nil || raw != origin+IdentityCallbackPath {
		if err != nil {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("identity callback origin is not canonical")
	}
	return u, origin, nil
}

func canonicalOriginOnly(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("invalid origin")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", fmt.Errorf("invalid origin")
	}
	canonical, err := canonicalHTTPSOrigin(u)
	if err != nil || canonical != raw {
		return "", fmt.Errorf("origin is not canonical")
	}
	return canonical, nil
}

func canonicalHTTPSOrigin(u *url.URL) (string, error) {
	if u == nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid HTTPS origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "443" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return "https://" + host, nil
}

func validateIdentityIssuer(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Path != IdentityIssuerPath {
		return fmt.Errorf("issuer must be an absolute HTTPS URL ending in %s", IdentityIssuerPath)
	}
	origin, err := canonicalHTTPSOrigin(u)
	if err != nil || raw != origin+IdentityIssuerPath {
		return fmt.Errorf("issuer must use a canonical HTTPS origin")
	}
	return nil
}

func validAppID(appID string) bool {
	if len(appID) == 0 || len(appID) > 512 || strings.TrimSpace(appID) != appID || strings.ContainsAny(appID, "\\?#\x00\r\n\t ") {
		return false
	}
	projectUID, environment, ok := strings.Cut(appID, "/")
	return ok && segmentRE.MatchString(projectUID) && environment != "" && !strings.Contains(environment, "/") && identityEnvironmentRE.MatchString(environment)
}

func validIdentityCodeVerifier(verifier string) bool {
	if len(verifier) < identityPKCEVerifierMin || len(verifier) > identityPKCEVerifierMax {
		return false
	}
	for i := 0; i < len(verifier); i++ {
		c := verifier[i]
		if ('A' > c || c > 'Z') && ('a' > c || c > 'z') && ('0' > c || c > '9') &&
			c != '-' && c != '.' && c != '_' && c != '~' {
			return false
		}
	}
	return true
}

func validIdentityCodeChallenge(challenge string) bool {
	if len(challenge) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(decoded) == sha256.Size && base64.RawURLEncoding.EncodeToString(decoded) == challenge
}

func identityCodeVerifierMatches(verifier, challenge string) bool {
	if !validIdentityCodeVerifier(verifier) || !validIdentityCodeChallenge(challenge) {
		return false
	}
	digest := sha256.Sum256([]byte(verifier))
	actual := base64.RawURLEncoding.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(actual), []byte(challenge)) == 1
}

func validIdentityApp(app IdentityApp) bool {
	if !app.Enabled || app.InstanceUID == "" || app.AppID == "" || !validAppID(app.AppID) || app.PublishedOrigin == "" {
		return false
	}
	_, expectedEnvironment, _ := strings.Cut(app.AppID, "/")
	if app.Environment != expectedEnvironment || !identityEnvironmentRE.MatchString(app.Environment) {
		return false
	}
	origin, err := canonicalOriginOnly(app.PublishedOrigin)
	return err == nil && origin == app.PublishedOrigin
}

func (h *Handler) identityEnvironmentAllowed(ctx context.Context, identity browsersession.Identity, ref InstanceRef, app IdentityApp) (bool, error) {
	if app.Environment == "production" {
		return true, nil
	}
	if h.identityWorkspaceMember == nil {
		return false, nil
	}
	return h.identityWorkspaceMember(ctx, identity, ref)
}

// activeIdentityAccount re-reads the User object behind a browser session.
// Sessions intentionally outlive individual account API reads, so an account
// deleted after login must not receive a new app assertion.
func (h *Handler) activeIdentityAccount(ctx context.Context, identity browsersession.Identity) (bool, error) {
	if h.identityAccountLookup == nil || identity.UserID == "" || identity.RBACIdentity == "" {
		return false, nil
	}
	account, err := h.identityAccountLookup(ctx, identity.UserID)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return account.User == identity.UserID && !account.Deleted && account.RBACIdentity != "" &&
		account.RBACIdentity == identity.RBACIdentity, nil
}

// authorizeIdentityAccess adds a Project- and Instance-UID-specific SAR to
// the ordinary name-based access SAR. Old grants can remain in the workspace
// until garbage collection finishes, but cannot match either replacement UID.
func (h *Handler) authorizeIdentityAccess(ctx context.Context, identity browsersession.Identity, ref InstanceRef, appID, instanceUID string) (bool, error) {
	user := strings.TrimSpace(identity.RBACIdentity)
	projectUID, _, hasProjectUID := strings.Cut(appID, "/")
	subresource, validUIDs := identityAppAccessSubresource(projectUID, instanceUID)
	if user == "" || !validAppID(appID) || !hasProjectUID || !validUIDs {
		return false, nil
	}
	client, err := h.sarClient(ref.Cluster)
	if err != nil {
		return false, err
	}
	review, err := client.Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user,
			Groups: []string{"system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:       ref.Group,
				Resource:    ref.Resource,
				Subresource: subresource,
				Name:        ref.Name,
				Verb:        AccessVerb,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

func (h *Handler) mintIdentityCode(ctx context.Context, ref InstanceRef, redirectOrigin, codeChallenge string, app IdentityApp, identity browsersession.Identity) (string, error) {
	if !validIdentityCodeChallenge(codeChallenge) {
		return "", fmt.Errorf("identity code challenge is required")
	}
	buf := make([]byte, 32)
	if _, err := io.ReadFull(h.random, buf); err != nil {
		return "", err
	}
	code := identityAppCodePrefix + base64.RawURLEncoding.EncodeToString(buf)
	identity.Email = ""
	record := CodeRecord{
		Purpose:        IdentityPurpose,
		Ref:            ref,
		RedirectOrigin: redirectOrigin,
		AppID:          app.AppID,
		Environment:    app.Environment,
		InstanceUID:    app.InstanceUID,
		CodeChallenge:  codeChallenge,
		Identity:       identity,
		ExpiresAt:      h.now().Add(codeTTL),
	}
	if err := h.codes.Put(ctx, code, record); err != nil {
		return "", err
	}
	return code, nil
}

func (h *Handler) signIdentityAssertion(ctx context.Context, identity browsersession.Identity, app IdentityApp) (string, error) {
	if !validIdentityApp(app) || identity.UserID == "" {
		return "", fmt.Errorf("identity and app descriptor are required")
	}
	var privateKey ed25519.PrivateKey
	var subjectKey []byte
	var err error
	if h.identityKeys != nil {
		var keys IdentityKeySnapshot
		keys, err = h.identityKeySnapshot(ctx)
		privateKey, subjectKey = keys.SigningKey, keys.SubjectKey
	} else {
		privateKey, err = h.currentIdentitySigningKey(ctx)
		if err == nil {
			subjectKey, err = h.identitySubjectKey(ctx)
		}
	}
	if err != nil {
		return "", err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	issuedAt := h.now().UTC().Truncate(time.Second)
	header, err := json.Marshal(identityJWTHeader{Algorithm: "EdDSA", KeyID: identityKeyID(publicKey), Type: "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(identityJWTClaims{
		Issuer:      h.identityIssuer,
		Audience:    app.AppID,
		Subject:     identitySubject(subjectKey, identity.UserID, app.AppID),
		IssuedAt:    issuedAt.Unix(),
		ExpiresAt:   issuedAt.Add(identityAssertionTTL).Unix(),
		AppID:       app.AppID,
		Environment: app.Environment,
		InstanceUID: app.InstanceUID,
		Name:        safeIdentityDisplayName(identity.Name),
	})
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claims)
	message := encodedHeader + "." + encodedClaims
	signature := ed25519.Sign(privateKey, []byte(message))
	return message + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func safeIdentityDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n\t\v\f") {
		return ""
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return name
}

func (h *Handler) currentIdentitySigningKey(ctx context.Context) (ed25519.PrivateKey, error) {
	if h.identitySigningKey == nil {
		return nil, fmt.Errorf("app identity signing key is not configured")
	}
	key, err := h.identitySigningKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading app identity signing key: %w", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("app identity signing key has an invalid length")
	}
	return key, nil
}

func (h *Handler) identitySubjectKey(ctx context.Context) ([]byte, error) {
	if h.identitySubjectKeySource == nil {
		return nil, fmt.Errorf("app identity subject key is not configured")
	}
	key, err := h.identitySubjectKeySource(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading app identity subject key: %w", err)
	}
	if len(key) < identitySubjectKeyMinLength {
		return nil, fmt.Errorf("app identity subject key is too short")
	}
	return key, nil
}

func (h *Handler) loadIdentityPublicKeys(ctx context.Context) ([]ed25519.PublicKey, error) {
	if h.identityPublicKeys == nil {
		return nil, fmt.Errorf("app identity public keys are not configured")
	}
	keys, err := h.identityPublicKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading app identity public keys: %w", err)
	}
	return keys, nil
}

// DeriveIdentitySubjectKey derives a test or externally managed stable
// app-subject key. Production uses the separately persisted pseudonym key in
// serviceaccounts.AppIdentityKeyRing.
func DeriveIdentitySubjectKey(sharedSecret []byte) ([]byte, error) {
	return deriveIdentitySubkey(sharedSecret, "railgrid.app-identity.pseudonym.v1")
}

func deriveIdentitySubkey(sharedSecret []byte, purpose string) ([]byte, error) {
	if len(sharedSecret) < identitySubjectKeyMinLength {
		return nil, fmt.Errorf("hub shared secret is too short for app identity keys")
	}
	mac := hmac.New(sha256.New, sharedSecret)
	_, _ = io.WriteString(mac, purpose)
	_, _ = mac.Write([]byte{0})
	return mac.Sum(nil), nil
}

func identitySubject(key []byte, userID, appID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "railgrid.app-identity.subject.v1\x00")
	writeMACField(mac, userID)
	writeMACField(mac, appID)
	return "rg1_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func writeMACField(dst io.Writer, value string) {
	var length [4]byte
	length[0] = byte(len(value) >> 24)
	length[1] = byte(len(value) >> 16)
	length[2] = byte(len(value) >> 8)
	length[3] = byte(len(value))
	_, _ = dst.Write(length[:])
	_, _ = io.WriteString(dst, value)
}

func identityKeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return base64.RawURLEncoding.EncodeToString(digest[:12])
}
