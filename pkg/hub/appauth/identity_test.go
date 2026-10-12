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
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/railgrid/railgrid/pkg/browsersession"
)

type identityFixture struct {
	handler            *Handler
	sessions           *browsersession.Store
	now                time.Time
	app                IdentityApp
	account            IdentityAccount
	accountErr         error
	accountCalls       int
	allowed            bool
	uidAllowed         bool
	member             bool
	appErr             error
	sarErr             error
	sars               []*authorizationv1.SubjectAccessReview
	memberCalls        int
	privateKey         ed25519.PrivateKey
	previousPublicKeys []identityTestPublicKey
	codeStore          CodeStore
}

type identityTestPublicKey struct {
	key       ed25519.PublicKey
	expiresAt time.Time
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	f := &identityFixture{
		now:        time.Date(2026, time.October, 12, 10, 0, 0, 0, time.UTC),
		app:        IdentityApp{InstanceUID: "instance-uid-123", AppID: "project-1/production", Environment: "production", PublishedOrigin: "https://shop.example.test", Enabled: true},
		account:    IdentityAccount{User: "user-abc", RBACIdentity: "railgrid:abc@example.test"},
		allowed:    true,
		uidAllowed: true,
		member:     true,
		sessions:   browsersession.New(browsersession.Config{}),
	}
	seed := sha256.Sum256([]byte("first signing key seed for test"))
	f.privateKey = ed25519.NewKeyFromSeed(seed[:])
	secret := []byte("shared-key-material-for-railgrid-auth")
	subjectKey, err := DeriveIdentitySubjectKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	f.codeStore = newMemoryCodeStore(func() time.Time { return f.now })
	sarFactory := func(string) (authorizationv1client.SubjectAccessReviewInterface, error) {
		if f.sarErr != nil {
			return nil, f.sarErr
		}
		client := kubefake.NewClientset()
		client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
			f.sars = append(f.sars, sar)
			out := sar.DeepCopy()
			out.Status.Allowed = f.allowed
			if sar.Spec.ResourceAttributes.Subresource != AccessSubresource {
				out.Status.Allowed = f.uidAllowed
			}
			return true, out, nil
		})
		return client.AuthorizationV1().SubjectAccessReviews(), nil
	}
	h, err := New(Config{
		Sessions:     f.sessions,
		SARClient:    sarFactory,
		InstanceHost: func(context.Context, InstanceRef) (string, error) { return "shop.example.test", nil },
		IdentityApp: func(context.Context, InstanceRef) (IdentityApp, error) {
			return f.app, f.appErr
		},
		IdentityAccountLookup: func(context.Context, string) (IdentityAccount, error) {
			f.accountCalls++
			return f.account, f.accountErr
		},
		IdentityWorkspaceMember: func(context.Context, browsersession.Identity, InstanceRef) (bool, error) {
			f.memberCalls++
			return f.member, nil
		},
		IdentityIssuer: "https://hub.example.test" + IdentityIssuerPath,
		IdentitySigningKey: func(context.Context) (ed25519.PrivateKey, error) {
			return append(ed25519.PrivateKey(nil), f.privateKey...), nil
		},
		IdentityPublicKeys: func(context.Context) ([]ed25519.PublicKey, error) {
			keys := []ed25519.PublicKey{append(ed25519.PublicKey(nil), f.privateKey.Public().(ed25519.PublicKey)...)}
			for _, prior := range f.previousPublicKeys {
				if f.now.Before(prior.expiresAt) {
					keys = append(keys, append(ed25519.PublicKey(nil), prior.key...))
				}
			}
			return keys, nil
		},
		IdentitySubjectKey: func(context.Context) ([]byte, error) { return append([]byte(nil), subjectKey...), nil },
		Codes:              f.codeStore,
		Now:                func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.handler = h
	return f
}

func (f *identityFixture) authorizeRequest(t *testing.T, redirect string, appID string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, err := f.sessions.IssueHTTP(context.Background(), rec, browsersession.Identity{
		UserID: "user-abc", Email: "abc@example.test", Name: "Ab C", RBACIdentity: "railgrid:abc@example.test",
	}); err != nil {
		t.Fatalf("issue session: %v", err)
	}
	query := url.Values{
		"cluster":      {"abc123cluster"},
		"group":        {"infrastructure.railgrid.ai"},
		"resource":     {"instances"},
		"name":         {"shop"},
		"app_id":       {appID},
		"redirect_uri": {redirect},
		"state":        {"state_123"},
	}
	req := httptest.NewRequest(http.MethodGet, IdentityAuthorizePath+"?"+query.Encode(), nil)
	for _, cookie := range rec.Result().Cookies() {
		req.AddCookie(cookie)
	}
	return req
}

func (f *identityFixture) authorize(t *testing.T) (string, *url.URL) {
	t.Helper()
	request := f.authorizeRequest(t, f.app.PublishedOrigin+IdentityCallbackPath, f.app.AppID)
	recorder := httptest.NewRecorder()
	f.handler.HandleIdentityAuthorize(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	redirect, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	code := redirect.Query().Get("code")
	if code == "" || redirect.Query().Get("state") != "state_123" {
		t.Fatalf("callback redirect = %q", redirect.String())
	}
	return code, redirect
}

func (f *identityFixture) exchange(t *testing.T, code, origin, appID string) (*httptest.ResponseRecorder, IdentityExchangeResponse) {
	t.Helper()
	body, err := json.Marshal(identityExchangeRequest{
		Code: code, Origin: origin, Cluster: "abc123cluster", Group: "infrastructure.railgrid.ai",
		Resource: "instances", Name: "shop", AppID: appID,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	f.handler.HandleIdentityExchange(recorder, httptest.NewRequest(http.MethodPost, IdentityExchangePath, bytes.NewReader(body)))
	var response IdentityExchangeResponse
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode exchange response: %v", err)
		}
	}
	return recorder, response
}

func TestIdentityAuthorizeExchangeSignsBoundShortLivedAssertion(t *testing.T) {
	f := newIdentityFixture(t)
	code, redirect := f.authorize(t)
	if !strings.HasPrefix(code, identityAppCodePrefix) {
		t.Fatalf("identity code = %q", code)
	}
	if len(f.sars) != 2 {
		t.Fatalf("authorize SARs=%d, want base and UID-fenced checks", len(f.sars))
	}
	attrs := f.sars[0].Spec.ResourceAttributes
	if f.sars[0].Spec.User != "railgrid:abc@example.test" || attrs.Group != "infrastructure.railgrid.ai" ||
		attrs.Resource != "instances" || attrs.Name != "shop" || attrs.Subresource != AccessSubresource || attrs.Verb != AccessVerb {
		t.Fatalf("authorize SAR = %+v", f.sars[0].Spec)
	}
	uidAttrs := f.sars[1].Spec.ResourceAttributes
	projectUID, _, _ := strings.Cut(f.app.AppID, "/")
	identityFence, validFence := identityAppAccessSubresource(projectUID, f.app.InstanceUID)
	if uidAttrs.Group != attrs.Group || uidAttrs.Resource != attrs.Resource || uidAttrs.Name != attrs.Name ||
		!validFence || uidAttrs.Subresource != identityFence || uidAttrs.Verb != AccessVerb {
		t.Fatalf("UID-fenced authorize SAR = %+v", f.sars[1].Spec)
	}
	recorder, response := f.exchange(t, code, f.app.PublishedOrigin, f.app.AppID)
	if recorder.Code != http.StatusOK || response.Assertion == "" || response.ExpiresIn != int64(identityAssertionTTL/time.Second) ||
		response.User.Name != "Ab C" {
		t.Fatalf("exchange status=%d response=%+v body=%s", recorder.Code, response, recorder.Body.String())
	}
	if len(f.sars) != 4 {
		t.Fatalf("authorize plus exchange SARs=%d, want two checks per request", len(f.sars))
	}
	header, claims := decodeIdentityJWT(t, response.Assertion, f.privateKey.Public().(ed25519.PublicKey))
	if header.Algorithm != "EdDSA" || header.KeyID != identityKeyID(f.privateKey.Public().(ed25519.PublicKey)) ||
		claims["iss"] != "https://hub.example.test"+IdentityIssuerPath || claims["aud"] != "project-1/production" ||
		claims["railgrid_app_id"] != "project-1/production" || claims["railgrid_environment"] != "production" ||
		claims["railgrid_instance_uid"] != "instance-uid-123" || claims["name"] != "Ab C" {
		t.Fatalf("JWT claims = %#v header=%+v", claims, header)
	}
	if _, includesEmail := claims["email"]; includesEmail {
		t.Fatalf("identity assertion exposed email: %#v", claims)
	}
	if sub, ok := claims["sub"].(string); !ok || !strings.HasPrefix(sub, "rg1_") || strings.Contains(sub, "abc") || strings.Contains(sub, "@") {
		t.Fatalf("subject = %#v, want opaque app-scoped pseudonym", claims["sub"])
	}
	if _, exists := claims["userID"]; exists {
		t.Fatal("platform User ID escaped in assertion")
	}
	if _, exists := claims["rbacIdentity"]; exists {
		t.Fatal("RBAC identity escaped in assertion")
	}
	if redirect.Host != "shop.example.test" || redirect.Path != IdentityCallbackPath {
		t.Fatalf("callback redirect host/path = %s%s", redirect.Host, redirect.Path)
	}

	replay, _ := f.exchange(t, code, f.app.PublishedOrigin, f.app.AppID)
	if replay.Code != http.StatusGone {
		t.Fatalf("replay status=%d, want 410", replay.Code)
	}
}

func TestIdentityAuthorizeRevalidatesTheLiveBrowserAccount(t *testing.T) {
	tests := []struct {
		name       string
		deleted    bool
		lookupErr  error
		wantStatus int
	}{
		{name: "deleted account", deleted: true, wantStatus: http.StatusForbidden},
		{name: "missing account", lookupErr: apierrors.NewNotFound(schema.GroupResource{Resource: "users"}, "user-abc"), wantStatus: http.StatusForbidden},
		{name: "lookup unavailable", lookupErr: errors.New("user store unavailable"), wantStatus: http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture(t)
			request := f.authorizeRequest(t, fIdentityOrigin+IdentityCallbackPath, f.app.AppID)
			f.account.Deleted = tc.deleted
			f.accountErr = tc.lookupErr
			recorder := httptest.NewRecorder()
			f.handler.HandleIdentityAuthorize(recorder, request)
			if recorder.Code != tc.wantStatus || f.accountCalls != 1 || len(f.sars) != 0 {
				t.Fatalf("status=%d account lookups=%d SARs=%d body=%s; want %d, one lookup, no SAR", recorder.Code, f.accountCalls, len(f.sars), recorder.Body.String(), tc.wantStatus)
			}
		})
	}
	t.Run("changed RBAC identity", func(t *testing.T) {
		f := newIdentityFixture(t)
		f.account.RBACIdentity = "railgrid:replacement@example.test"
		recorder := httptest.NewRecorder()
		f.handler.HandleIdentityAuthorize(recorder, f.authorizeRequest(t, fIdentityOrigin+IdentityCallbackPath, f.app.AppID))
		if recorder.Code != http.StatusForbidden || f.accountCalls != 1 || len(f.sars) != 0 {
			t.Fatalf("status=%d account lookups=%d SARs=%d body=%s", recorder.Code, f.accountCalls, len(f.sars), recorder.Body.String())
		}
	})
}

func TestIdentityExchangeRejectsAccountDeletedAfterCodeIssue(t *testing.T) {
	f := newIdentityFixture(t)
	code, _ := f.authorize(t)
	f.account.Deleted = true
	recorder, response := f.exchange(t, code, f.app.PublishedOrigin, f.app.AppID)
	if recorder.Code != http.StatusForbidden || response.Assertion != "" || f.accountCalls != 2 {
		t.Fatalf("exchange status=%d assertion=%t account lookups=%d body=%s", recorder.Code, response.Assertion != "", f.accountCalls, recorder.Body.String())
	}
	if len(f.sars) != 2 {
		t.Fatalf("deleted account reached exchange SARs: %d", len(f.sars))
	}
	replay, _ := f.exchange(t, code, f.app.PublishedOrigin, f.app.AppID)
	if replay.Code != http.StatusGone {
		t.Fatalf("deleted-account code replay status=%d, want 410", replay.Code)
	}
}

func TestIdentityExchangeFailsClosedWhenAccountLookupIsUnavailable(t *testing.T) {
	f := newIdentityFixture(t)
	code, _ := f.authorize(t)
	f.accountErr = errors.New("user store unavailable")
	recorder, response := f.exchange(t, code, f.app.PublishedOrigin, f.app.AppID)
	if recorder.Code != http.StatusServiceUnavailable || response.Assertion != "" || f.accountCalls != 2 {
		t.Fatalf("exchange status=%d assertion=%t account lookups=%d body=%s", recorder.Code, response.Assertion != "", f.accountCalls, recorder.Body.String())
	}
	replay, _ := f.exchange(t, code, f.app.PublishedOrigin, f.app.AppID)
	if replay.Code != http.StatusGone {
		t.Fatalf("unavailable-account code replay status=%d, want 410", replay.Code)
	}
}

func TestIdentityAndOrdinaryAppCodesCannotCrossRedeem(t *testing.T) {
	f := newIdentityFixture(t)
	identityCode, _ := f.authorize(t)
	ordinaryRequest := exchangeRequest{
		Code: identityCode, Host: "shop.example.test", Cluster: "abc123cluster",
		Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "shop",
	}
	body, _ := json.Marshal(ordinaryRequest)
	ordinaryRecorder := httptest.NewRecorder()
	f.handler.HandleExchange(ordinaryRecorder, httptest.NewRequest(http.MethodPost, ExchangePath, bytes.NewReader(body)))
	if ordinaryRecorder.Code != http.StatusGone {
		t.Fatalf("ordinary app exchange redeemed identity code: status=%d body=%s", ordinaryRecorder.Code, ordinaryRecorder.Body.String())
	}
	identityRecorder, _ := f.exchange(t, identityCode, fIdentityOrigin, f.app.AppID)
	if identityRecorder.Code != http.StatusGone {
		t.Fatalf("identity exchange replayed a code taken by the other purpose: %d", identityRecorder.Code)
	}

	ordinaryCode, err := f.handler.mintCode(context.Background(), InstanceRef{Cluster: "abc123cluster", Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "shop"}, "shop.example.test", browsersession.Identity{UserID: "user-abc"})
	if err != nil {
		t.Fatal(err)
	}
	wrongPurpose, _ := f.exchange(t, ordinaryCode, fIdentityOrigin, f.app.AppID)
	if wrongPurpose.Code != http.StatusBadRequest {
		t.Fatalf("identity endpoint accepted ordinary code: status=%d body=%s", wrongPurpose.Code, wrongPurpose.Body.String())
	}
	if _, ok := f.codeStore.Take(context.Background(), ordinaryCode); !ok {
		t.Fatal("identity endpoint consumed ordinary code on its prefix rejection")
	}
}

func TestIdentityAuthorizeRequiresExactCallbackAndPublishedOrigin(t *testing.T) {
	tests := []struct {
		name     string
		redirect string
		want     int
	}{
		{name: "wrong callback path", redirect: fIdentityOrigin + "/callback", want: http.StatusBadRequest},
		{name: "wrong origin", redirect: "https://other.example.test" + IdentityCallbackPath, want: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture(t)
			request := f.authorizeRequest(t, tc.redirect, f.app.AppID)
			recorder := httptest.NewRecorder()
			f.handler.HandleIdentityAuthorize(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.want)
			}
			if len(f.sars) != 0 && tc.name == "wrong callback path" {
				t.Fatalf("malformed callback caused SAR: %+v", f.sars)
			}
		})
	}
}

const fIdentityOrigin = "https://shop.example.test"

func TestIdentityExchangeRejectsOriginAudienceAndDescriptorChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*identityFixture)
		origin string
		appID  string
	}{
		{name: "origin mismatch", origin: "https://attacker.example.test", appID: "project-1/production"},
		{name: "app audience mismatch", origin: fIdentityOrigin, appID: "project-2/production"},
		{name: "app disabled", origin: fIdentityOrigin, appID: "project-1/production", mutate: func(f *identityFixture) { f.app.Enabled = false }},
		{name: "instance recreated", origin: fIdentityOrigin, appID: "project-1/production", mutate: func(f *identityFixture) { f.app.InstanceUID = "new-uid" }},
		{name: "environment changed", origin: fIdentityOrigin, appID: "project-1/production", mutate: func(f *identityFixture) { f.app.Environment = "development" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture(t)
			code, _ := f.authorize(t)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			recorder, _ := f.exchange(t, code, tc.origin, tc.appID)
			if recorder.Code != http.StatusGone {
				t.Fatalf("exchange status=%d body=%s, want 410", recorder.Code, recorder.Body.String())
			}
			replay, _ := f.exchange(t, code, fIdentityOrigin, "project-1/production")
			if replay.Code != http.StatusGone {
				t.Fatalf("failed exchange did not consume code: replay status=%d", replay.Code)
			}
		})
	}
}

func TestIdentityFlowRechecksSARAndRejectsDisabledApps(t *testing.T) {
	t.Run("disabled before authorize", func(t *testing.T) {
		f := newIdentityFixture(t)
		f.app.Enabled = false
		recorder := httptest.NewRecorder()
		f.handler.HandleIdentityAuthorize(recorder, f.authorizeRequest(t, fIdentityOrigin+IdentityCallbackPath, f.app.AppID))
		if recorder.Code != http.StatusForbidden || len(f.sars) != 1 {
			t.Fatalf("status=%d SARs=%d body=%s", recorder.Code, len(f.sars), recorder.Body.String())
		}
	})
	t.Run("SAR denied before authorize", func(t *testing.T) {
		f := newIdentityFixture(t)
		f.allowed = false
		recorder := httptest.NewRecorder()
		f.handler.HandleIdentityAuthorize(recorder, f.authorizeRequest(t, fIdentityOrigin+IdentityCallbackPath, f.app.AppID))
		if recorder.Code != http.StatusForbidden || len(f.sars) != 1 {
			t.Fatalf("status=%d SARs=%d body=%s", recorder.Code, len(f.sars), recorder.Body.String())
		}
	})
	t.Run("legacy access does not substitute for current UID grant", func(t *testing.T) {
		f := newIdentityFixture(t)
		f.uidAllowed = false
		recorder := httptest.NewRecorder()
		f.handler.HandleIdentityAuthorize(recorder, f.authorizeRequest(t, fIdentityOrigin+IdentityCallbackPath, f.app.AppID))
		projectUID, _, _ := strings.Cut(f.app.AppID, "/")
		identityFence, validFence := identityAppAccessSubresource(projectUID, f.app.InstanceUID)
		if recorder.Code != http.StatusForbidden || len(f.sars) != 2 ||
			f.sars[0].Spec.ResourceAttributes.Subresource != AccessSubresource ||
			!validFence || f.sars[1].Spec.ResourceAttributes.Subresource != identityFence {
			t.Fatalf("status=%d SARs=%#v body=%s", recorder.Code, f.sars, recorder.Body.String())
		}
	})
	t.Run("access revoked at exchange", func(t *testing.T) {
		f := newIdentityFixture(t)
		code, _ := f.authorize(t)
		f.allowed = false
		recorder, _ := f.exchange(t, code, fIdentityOrigin, "project-1/production")
		if recorder.Code != http.StatusForbidden || len(f.sars) != 3 {
			t.Fatalf("status=%d SARs=%d body=%s", recorder.Code, len(f.sars), recorder.Body.String())
		}
		replay, _ := f.exchange(t, code, fIdentityOrigin, "project-1/production")
		if replay.Code != http.StatusGone {
			t.Fatalf("revoked code replay status=%d, want 410", replay.Code)
		}
	})
}

func TestIdentityEnvironmentMembershipPolicy(t *testing.T) {
	environments := []string{"development", "test", "staging", "production"}
	for _, environment := range environments {
		for _, member := range []bool{true, false} {
			caseName := environment + "/non-member"
			if member {
				caseName = environment + "/member"
			}
			t.Run(caseName, func(t *testing.T) {
				f := newIdentityFixture(t)
				f.app.AppID = "project-1/" + environment
				f.app.Environment = environment
				f.member = member
				request := f.authorizeRequest(t, fIdentityOrigin+IdentityCallbackPath, f.app.AppID)
				authorize := httptest.NewRecorder()
				f.handler.HandleIdentityAuthorize(authorize, request)

				allowed := member || environment == "production"
				wantAuthorizeStatus := http.StatusForbidden
				if allowed {
					wantAuthorizeStatus = http.StatusFound
				}
				if authorize.Code != wantAuthorizeStatus {
					t.Fatalf("authorize status=%d, want %d", authorize.Code, wantAuthorizeStatus)
				}
				if !allowed {
					if f.memberCalls != 1 || len(f.sars) != 2 {
						t.Fatalf("denied authorize membership checks=%d SARs=%d, want base and UID-fenced checks plus one membership check", f.memberCalls, len(f.sars))
					}
					return
				}

				callback, err := url.Parse(authorize.Header().Get("Location"))
				if err != nil {
					t.Fatalf("parse callback redirect: %v", err)
				}
				code := callback.Query().Get("code")
				if code == "" {
					t.Fatal("authorize redirect has no code")
				}
				exchange, _ := f.exchange(t, code, fIdentityOrigin, f.app.AppID)
				if exchange.Code != http.StatusOK {
					t.Fatalf("exchange status=%d, want %d; body=%s", exchange.Code, http.StatusOK, exchange.Body.String())
				}
				wantMembershipChecks := 2
				if environment == "production" {
					wantMembershipChecks = 0
				}
				if f.memberCalls != wantMembershipChecks || len(f.sars) != 4 {
					t.Fatalf("membership checks=%d SARs=%d, want %d membership checks and four access SARs", f.memberCalls, len(f.sars), wantMembershipChecks)
				}
			})
		}

		if environment == "production" {
			continue
		}
		t.Run(environment+"/membership-revoked-before-exchange", func(t *testing.T) {
			f := newIdentityFixture(t)
			f.app.AppID = "project-1/" + environment
			f.app.Environment = environment
			f.member = true
			code, _ := f.authorize(t)
			f.member = false
			exchange, _ := f.exchange(t, code, fIdentityOrigin, f.app.AppID)
			if exchange.Code != http.StatusForbidden || f.memberCalls != 2 || len(f.sars) != 2 {
				t.Fatalf("exchange status=%d membership checks=%d SARs=%d, want membership refusal after authorize", exchange.Code, f.memberCalls, len(f.sars))
			}
		})
	}
}

func TestIdentitySigningKeyRotationKeepsAppSubjectStable(t *testing.T) {
	f := newIdentityFixture(t)
	code, _ := f.authorize(t)
	firstResponse, first := f.exchange(t, code, fIdentityOrigin, f.app.AppID)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first exchange status=%d", firstResponse.Code)
	}
	firstHeader, firstClaims := decodeIdentityJWT(t, first.Assertion, f.privateKey.Public().(ed25519.PublicKey))

	secondSeed := sha256.Sum256([]byte("rotated signing key seed for test"))
	f.previousPublicKeys = append(f.previousPublicKeys, identityTestPublicKey{
		key:       append(ed25519.PublicKey(nil), f.privateKey.Public().(ed25519.PublicKey)...),
		expiresAt: f.now.Add(identityAssertionTTL),
	})
	f.privateKey = ed25519.NewKeyFromSeed(secondSeed[:])
	code, _ = f.authorize(t)
	secondResponse, second := f.exchange(t, code, fIdentityOrigin, f.app.AppID)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second exchange status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	secondHeader, secondClaims := decodeIdentityJWT(t, second.Assertion, f.privateKey.Public().(ed25519.PublicKey))
	if firstClaims["sub"] != secondClaims["sub"] || firstHeader.KeyID == secondHeader.KeyID {
		t.Fatalf("rotation changed subject or kept key id: first=%#v/%+v second=%#v/%+v", firstClaims["sub"], firstHeader, secondClaims["sub"], secondHeader)
	}

	jwks := httptest.NewRecorder()
	f.handler.HandleIdentityJWKS(jwks, httptest.NewRequest(http.MethodGet, IdentityJWKSPath, nil))
	var keys identityJWKS
	if err := json.Unmarshal(jwks.Body.Bytes(), &keys); err != nil || len(keys.Keys) != 2 || keys.Keys[0].KeyID != secondHeader.KeyID || keys.Keys[0].Alg != "EdDSA" || keys.Keys[1].KeyID != firstHeader.KeyID {
		t.Fatalf("JWKS after rotation = %+v, err=%v", keys, err)
	}
	oldPublicKeyBytes, err := base64.RawURLEncoding.DecodeString(keys.Keys[1].X)
	if err != nil {
		t.Fatalf("decode previous JWKS key: %v", err)
	}
	decodeIdentityJWT(t, first.Assertion, ed25519.PublicKey(oldPublicKeyBytes))
	f.now = f.now.Add(identityAssertionTTL)
	rotatedJWKS := httptest.NewRecorder()
	f.handler.HandleIdentityJWKS(rotatedJWKS, httptest.NewRequest(http.MethodGet, IdentityJWKSPath, nil))
	if err := json.Unmarshal(rotatedJWKS.Body.Bytes(), &keys); err != nil || len(keys.Keys) != 1 || keys.Keys[0].KeyID != secondHeader.KeyID {
		t.Fatalf("JWKS after overlap expiry = %+v, err=%v", keys, err)
	}
}

func decodeIdentityJWT(t *testing.T, token string, public ed25519.PublicKey) (identityJWTHeader, map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts", len(parts))
	}
	decode := func(part string) []byte {
		value, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatalf("decode JWT part: %v", err)
		}
		return value
	}
	var header identityJWTHeader
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	claimBytes := decode(parts[1])
	claims := map[string]any{}
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), decode(parts[2])) {
		t.Fatal("JWT signature did not verify")
	}
	return header, claims
}
