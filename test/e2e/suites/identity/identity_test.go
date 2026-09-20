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

package identity

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The suite works in one tenant workspace, created through the REST API the
// portal uses. It is created once (TestA…) and every later test reuses it: the
// composition acceptance is recorded per workspace, so a fresh workspace per
// test would hide the transition from refused to admitted.
var (
	orgUUID       string
	workspaceUUID string
	// tenantCluster is the workspace's kcp logical cluster — the `clusterID` a
	// provider sends and the `/clusters/{id}` segment a minted token is used
	// against.
	tenantCluster string
)

// ---------------------------------------------------------------------------
// The /api/identities wire types. Deliberately re-declared here rather than
// imported from pkg/hub/restapi: this suite is a client of that HTTP surface,
// and a field rename that breaks real providers must break this test too.
// ---------------------------------------------------------------------------

type policyRule struct {
	APIGroups     []string `json:"apiGroups,omitempty"`
	Resources     []string `json:"resources,omitempty"`
	Verbs         []string `json:"verbs,omitempty"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

type identityOwner struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	ClusterID string `json:"clusterID,omitempty"`
}

type identityRequest struct {
	Owner      identityOwner `json:"owner"`
	ClusterID  string        `json:"clusterID"`
	Rules      []policyRule  `json:"rules"`
	TTLSeconds int64         `json:"ttlSeconds,omitempty"`
}

type identityResponse struct {
	Token          string    `json:"token"`
	TokenType      string    `json:"tokenType"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ServiceAccount string    `json:"serviceAccount"`
	Name           string    `json:"name"`
	// Set on a refusal instead of the fields above.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// greetingOwner is the owner tuple for a Greeting in the suite's workspace.
func greetingOwner(name string) identityOwner {
	return identityOwner{
		Provider: providerName, Kind: "Greeting",
		Group: greetingGVR.Group, Version: greetingGVR.Version, Resource: greetingGVR.Resource,
		Name: name, ClusterID: tenantCluster,
	}
}

// mintIdentity POSTs /api/identities as the quickstart provider — the real
// credential, verified by the same TokenReview the heartbeat uses.
func mintIdentity(t *testing.T, req identityRequest) (int, identityResponse, string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal identity request: %v", err)
	}
	code, raw := hubDo(t, http.MethodPost, "/api/identities", providerToken, body)
	var out identityResponse
	_ = json.Unmarshal([]byte(raw), &out)
	return code, out, raw
}

// ---------------------------------------------------------------------------
// Tests. They are ordered by name because the workspace, the binding and the
// composition acceptance build on each other.
// ---------------------------------------------------------------------------

// TestATenantWorkspaceAndEnable builds the world the rest of the suite mints
// in: a workspace, the fixture dependency enabled in it, then quickstart
// enabled WITHOUT accepting the composition. Not accepting is the point — it is
// the state TestE measures the refusal in.
func TestATenantWorkspaceAndEnable(t *testing.T) {
	orgUUID = personalOrgUUID(t)
	workspaceUUID = createWorkspace(t, orgUUID, "identity-e2e")
	tenantCluster = workspaceCluster(t, orgUUID, workspaceUUID)
	t.Logf("org=%s workspace=%s cluster=%s", orgUUID, workspaceUUID, tenantCluster)
	if tenantCluster == "" {
		t.Fatal("workspace kubeconfig carried no /clusters/<id> segment")
	}

	// The dependency first: the Enable flow refuses a provider whose declared
	// dependencies are not enabled here.
	enableProvider(t, fixtureName, nil)
	enableProvider(t, providerName, nil)

	tenant := kcpDynamic(t, tenantCluster, staticToken)
	for _, name := range []string{fixtureName, providerName} {
		if !waitForCondition(t, 90*time.Second, func() (bool, string) {
			got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
			return phase == "Bound", "phase=" + phase
		}) {
			t.Fatalf("%s APIBinding never reached Bound", name)
		}
	}
	// Both APIs must actually answer before anything mints against them: a
	// binding reports Bound slightly before its kinds are servable, and an
	// identity minted for an owner the hub cannot yet read is refused as
	// unknown_owner rather than failing where the race is.
	for _, gvr := range []schema.GroupVersionResource{greetingGVR, widgetGVR} {
		if !waitForCondition(t, 90*time.Second, func() (bool, string) {
			_, err := tenant.Resource(gvr).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			return true, ""
		}) {
			t.Fatalf("%s never became servable in %s", gvr.Resource, tenantCluster)
		}
	}
}

// TestBMintForGreetingOwner is the happy path: a clause A request (the
// requesting provider's OWN exported group) yields a token, and that token
// really authorizes what was asked for — and only that — against
// /clusters/{id}.
func TestBMintForGreetingOwner(t *testing.T) {
	requireWorkspace(t)
	createGreeting(t, "identity-owner", "hello from the identity suite")

	code, resp, raw := mintIdentity(t, identityRequest{
		Owner:     greetingOwner("identity-owner"),
		ClusterID: tenantCluster,
		Rules: []policyRule{{
			APIGroups:     []string{greetingGVR.Group},
			Resources:     []string{greetingGVR.Resource},
			Verbs:         []string{"get", "update"},
			ResourceNames: []string{"identity-owner"},
		}},
		TTLSeconds: int64(shortTTL / time.Second),
	})
	if code != http.StatusOK {
		t.Fatalf("POST /api/identities = %d, want 200: %s", code, raw)
	}
	if resp.Token == "" || resp.TokenType != "Bearer" {
		t.Fatalf("response carries no bearer token: %s", raw)
	}
	if !strings.HasPrefix(resp.ServiceAccount, "railgrid-si-") {
		t.Errorf("serviceAccount = %q, want the railgrid-si- prefix that marks a provider-asserted identity", resp.ServiceAccount)
	}
	// The TTL ceiling is the whole reason these are not the standing tokens
	// providers used to mint for themselves.
	if until := time.Until(resp.ExpiresAt); until <= 0 || until > 24*time.Hour+time.Minute {
		t.Errorf("expiresAt = %s (%s from now); a scoped identity must expire, and never later than 24h", resp.ExpiresAt, until)
	}

	// The token is a kcp ServiceAccount token in the tenant workspace, so it
	// is used exactly like any other: against /clusters/{id}.
	minted := kcpDynamic(t, tenantCluster, resp.Token)
	got, err := minted.Resource(greetingGVR).Get(ctxWithTimeout(t, 15*time.Second), "identity-owner", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("minted token could not GET the Greeting it was minted for: %v", err)
	}
	if msg, _, _ := unstructured.NestedString(got.Object, "spec", "message"); msg != "hello from the identity suite" {
		t.Errorf("read back spec.message = %q", msg)
	}

	// Name scoping is real RBAC, not decoration: the rule named one Greeting,
	// so another one is forbidden even though it is the same kind in the same
	// workspace.
	createGreeting(t, "identity-other", "not yours")
	if _, err := minted.Resource(greetingGVR).Get(ctxWithTimeout(t, 15*time.Second), "identity-other", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("minted token read a Greeting outside its resourceNames: err = %v, want Forbidden", err)
	}
	// And nothing was granted on the fixture group, which the same workspace
	// serves: an identity reaches only what its rules named.
	if _, err := minted.Resource(widgetGVR).List(ctxWithTimeout(t, 15*time.Second), metav1.ListOptions{}); err == nil {
		t.Error("minted token listed the dependency's Widgets; clause A must not reach another provider's group")
	}

	// The mint above asked for shortTTL, which is BELOW the ten-minute minimum
	// kcp's TokenRequest admission enforces. The hub raises such an ask to its
	// floor rather than forwarding it: getting here at all means it did (a
	// forwarded five-minute request comes back as 502 identity_issue_failed),
	// and the expiry says which lifetime was actually issued.
	t.Run("a TTL below the mint floor is raised, not refused", func(t *testing.T) {
		if shortTTL >= mintFloorTTL {
			t.Fatalf("shortTTL = %s is not below the mint floor %s; this case no longer exercises the clamp", shortTTL, mintFloorTTL)
		}
		until := time.Until(resp.ExpiresAt)
		if until <= shortTTL+time.Minute {
			t.Fatalf("expiresAt = %s (%s from now): a %s request must be clamped UP to the %s floor, not passed through to a mint that refuses it",
				resp.ExpiresAt, until, shortTTL, mintFloorTTL)
		}
		if until > mintFloorTTL+time.Minute {
			t.Errorf("expiresAt = %s (%s from now): a below-floor request should get the floor (%s), not a longer lifetime",
				resp.ExpiresAt, until, mintFloorTTL)
		}
	})
}

// TestCRuleOutsidePolicyIsRefused walks the refusals a provider gets wrong in
// practice. Each asserts the CODE, because the code is the contract: the SDK's
// identity client switches on it, and a refusal that changes code silently
// changes every provider's error handling.
func TestCRuleOutsidePolicyIsRefused(t *testing.T) {
	requireWorkspace(t)
	createGreeting(t, "identity-owner", "hello from the identity suite")

	for _, tc := range []struct {
		name string
		rule policyRule
		code string
		why  string
	}{{
		name: "the core group is never minted",
		rule: policyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}, ResourceNames: []string{"anything"}},
		code: "core_group_forbidden",
		why:  "review X-4: Secrets are not cross-provider currency",
	}, {
		name: "a wildcard verb is never minted",
		rule: policyRule{APIGroups: []string{greetingGVR.Group}, Resources: []string{greetingGVR.Resource}, Verbs: []string{"*"}},
		code: "wildcard_forbidden",
		why:  "a wildcard is not a scope",
	}, {
		name: "an unnamed rule on another provider's group",
		rule: policyRule{APIGroups: []string{fixtureGroup}, Resources: []string{fixturePlainResource}, Verbs: []string{"get"}},
		code: "unnamed_foreign_rule",
		why:  "clause B is name-scoped",
	}, {
		name: "an unknown API group",
		rule: policyRule{APIGroups: []string{"nobody.example.com"}, Resources: []string{"things"}, Verbs: []string{"get"}, ResourceNames: []string{"a-thing"}},
		code: "unknown_group",
		why:  "no provider in the catalog exports it",
	}, {
		name: "a write on another provider's objects",
		rule: policyRule{APIGroups: []string{fixtureGroup}, Resources: []string{fixturePlainResource}, Verbs: []string{"delete"}, ResourceNames: []string{"a-gadget"}},
		code: "foreign_write_forbidden",
		why:  "only get is minted outside clause E",
	}, {
		name: "list on another provider's group",
		rule: policyRule{APIGroups: []string{fixtureGroup}, Resources: []string{fixturePlainResource}, Verbs: []string{"list"}, ResourceNames: []string{"a-gadget"}},
		code: "foreign_list_not_name_scoped",
		why:  "RBAC does not apply resourceNames to collections",
	}, {
		name: "two API groups in one rule",
		rule: policyRule{APIGroups: []string{greetingGVR.Group, fixtureGroup}, Resources: []string{greetingGVR.Resource}, Verbs: []string{"get"}, ResourceNames: []string{"identity-owner"}},
		code: "multi_group_rule",
		why:  "a mixed rule cannot be classified",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			code, resp, raw := mintIdentity(t, identityRequest{
				Owner:     greetingOwner("identity-owner"),
				ClusterID: tenantCluster,
				Rules:     []policyRule{tc.rule},
			})
			if code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s): %s", code, tc.why, raw)
			}
			if resp.Code != tc.code {
				t.Errorf("refusal code = %q, want %q (%s); message: %s", resp.Code, tc.code, tc.why, resp.Message)
			}
		})
	}

	t.Run("an owner that does not exist", func(t *testing.T) {
		code, resp, raw := mintIdentity(t, identityRequest{
			Owner:     greetingOwner("no-such-greeting"),
			ClusterID: tenantCluster,
			Rules: []policyRule{{
				APIGroups: []string{greetingGVR.Group}, Resources: []string{greetingGVR.Resource},
				Verbs: []string{"get"}, ResourceNames: []string{"no-such-greeting"},
			}},
		})
		if code != http.StatusForbidden || resp.Code != "unknown_owner" {
			t.Fatalf("status/code = %d/%q, want 403/unknown_owner: %s", code, resp.Code, raw)
		}
	})

	t.Run("a caller that is not the provider", func(t *testing.T) {
		body, _ := json.Marshal(identityRequest{
			Owner: greetingOwner("identity-owner"), ClusterID: tenantCluster,
			Rules: []policyRule{{
				APIGroups: []string{greetingGVR.Group}, Resources: []string{greetingGVR.Resource},
				Verbs: []string{"get"}, ResourceNames: []string{"identity-owner"},
			}},
		})
		// A tenant's own bearer is a valid hub credential and still must not
		// mint: attestation is "are you this provider", not "are you known".
		code, raw := hubDo(t, http.MethodPost, "/api/identities", staticToken, body)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("a tenant bearer minted an identity: status %d: %s", code, raw)
		}
		if strings.Contains(raw, tenantCluster) {
			t.Errorf("the refusal body leaks the tenant's logical cluster: %s", raw)
		}
	})
}

// TestDRefreshReturnsANewTokenForTheSameAccount pins the idempotency the whole
// design rests on: a long-running holder re-asks on a timer and must keep its
// account (and therefore its RBAC and its audit trail) while getting fresh
// credentials.
func TestDRefreshReturnsANewTokenForTheSameAccount(t *testing.T) {
	requireWorkspace(t)
	createGreeting(t, "identity-owner", "hello from the identity suite")

	req := identityRequest{
		Owner:     greetingOwner("identity-owner"),
		ClusterID: tenantCluster,
		Rules: []policyRule{{
			APIGroups: []string{greetingGVR.Group}, Resources: []string{greetingGVR.Resource},
			Verbs: []string{"get"}, ResourceNames: []string{"identity-owner"},
		}},
		TTLSeconds: int64(shortTTL / time.Second),
	}
	code, first, raw := mintIdentity(t, req)
	if code != http.StatusOK {
		t.Fatalf("first mint = %d: %s", code, raw)
	}
	code, second, raw := mintIdentity(t, req)
	if code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", code, raw)
	}

	if second.ServiceAccount != first.ServiceAccount {
		t.Errorf("refresh moved the identity to another ServiceAccount: %q -> %q", first.ServiceAccount, second.ServiceAccount)
	}
	if second.Name != first.Name {
		t.Errorf("refresh created a second ScopedIdentity record: %q -> %q", first.Name, second.Name)
	}
	if second.Token == first.Token {
		t.Error("refresh returned the same token; a refresh must mint a new one")
	}
	// Both are usable: a refresh must not revoke the credential the holder is
	// still running with.
	for label, token := range map[string]string{"first": first.Token, "refreshed": second.Token} {
		cl := kcpDynamic(t, tenantCluster, token)
		if _, err := cl.Resource(greetingGVR).Get(ctxWithTimeout(t, 15*time.Second), "identity-owner", metav1.GetOptions{}); err != nil {
			t.Errorf("%s token does not work: %v", label, err)
		}
	}
}

// TestEComposition is clause E end to end: declared but unaccepted is refused
// with composition_not_granted, accepting it at Enable admits exactly the
// declared verbs, and a verb the declaration omits is still refused.
func TestEComposition(t *testing.T) {
	requireWorkspace(t)
	createGreeting(t, "identity-owner", "hello from the identity suite")

	composed := policyRule{
		APIGroups: []string{fixtureGroup},
		Resources: []string{fixtureResource},
		// create/list/watch are the unnamed class: RBAC cannot name-scope them,
		// and the workspace is the bound scope.
		Verbs: []string{"create", "list", "watch"},
	}
	request := identityRequest{
		Owner:      greetingOwner("identity-owner"),
		ClusterID:  tenantCluster,
		Rules:      []policyRule{composed},
		TTLSeconds: int64(shortTTL / time.Second),
	}

	t.Run("refused until a workspace admin accepts it", func(t *testing.T) {
		code, resp, raw := mintIdentity(t, request)
		if code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 before acceptance: %s", code, raw)
		}
		if resp.Code != "composition_not_granted" {
			t.Fatalf("refusal code = %q, want composition_not_granted (message: %s)", resp.Code, resp.Message)
		}
	})

	t.Run("a verb the declaration omits stays refused", func(t *testing.T) {
		code, resp, raw := mintIdentity(t, identityRequest{
			Owner: request.Owner, ClusterID: tenantCluster,
			Rules: []policyRule{{
				APIGroups: []string{fixtureGroup}, Resources: []string{fixtureResource},
				Verbs: []string{"delete"}, ResourceNames: []string{"a-widget"},
			}},
		})
		if code != http.StatusForbidden || resp.Code != "composition_verb_not_declared" {
			t.Fatalf("status/code = %d/%q, want 403/composition_verb_not_declared: %s", code, resp.Code, raw)
		}
	})

	// Accept it, the way the Enable dialog does. Verbs are never sent: they
	// come from the declaration, read fresh at mint time.
	enableProvider(t, providerName, []acceptedComposition{{
		Provider: fixtureName, Group: fixtureGroup, Resource: fixtureResource,
	}})

	t.Run("admitted once accepted", func(t *testing.T) {
		var resp identityResponse
		if !waitForCondition(t, 60*time.Second, func() (bool, string) {
			code, out, raw := mintIdentity(t, request)
			if code == http.StatusOK {
				resp = out
				return true, ""
			}
			return false, fmt.Sprintf("status %d: %s", code, raw)
		}) {
			t.Fatal("the composition was accepted but never admitted")
		}

		// The capability is real: the identity creates an object of the
		// dependency's kind in this workspace, which is the thing clause E
		// exists to allow and clause B forbids.
		minted := kcpDynamic(t, tenantCluster, resp.Token)
		widget := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": fixtureGroup + "/v1alpha1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "composed-widget"},
			"spec":       map[string]any{"size": "small"},
		}}
		if !waitForCondition(t, 60*time.Second, func() (bool, string) {
			_, err := minted.Resource(widgetGVR).Create(ctxWithTimeout(t, 15*time.Second), widget, metav1.CreateOptions{})
			if err == nil || apierrors.IsAlreadyExists(err) {
				return true, ""
			}
			return false, err.Error()
		}) {
			t.Fatal("the composed identity could not create a Widget in the workspace")
		}
		t.Cleanup(func() {
			admin := kcpDynamic(t, tenantCluster, adminToken)
			_ = admin.Resource(widgetGVR).Delete(ctxWithTimeout(t, 10*time.Second), "composed-widget", metav1.DeleteOptions{})
		})

		// `delete` was never declared, so it was never granted — the grant
		// widens nothing beyond the declaration.
		err := minted.Resource(widgetGVR).Delete(ctxWithTimeout(t, 15*time.Second), "composed-widget", metav1.DeleteOptions{})
		if !apierrors.IsForbidden(err) {
			t.Errorf("the composed identity deleted a Widget: err = %v, want Forbidden (delete is not declared)", err)
		}
	})
}

// TestFOwnerDeletionCollectsTheIdentity is finding M7's closure: a credential
// minted for an object that is then deleted must not outlive it. The sweep
// (pkg/hub/identity/reconciler.go) runs on DefaultSweepInterval, so this test
// is paced by that interval, not by the token TTL — which is why the TTL here
// is deliberately short and the wait is deliberately longer than one sweep.
func TestFOwnerDeletionCollectsTheIdentity(t *testing.T) {
	requireWorkspace(t)
	createGreeting(t, "identity-ephemeral", "here for a moment")

	code, resp, raw := mintIdentity(t, identityRequest{
		Owner:     greetingOwner("identity-ephemeral"),
		ClusterID: tenantCluster,
		Rules: []policyRule{{
			APIGroups: []string{greetingGVR.Group}, Resources: []string{greetingGVR.Resource},
			Verbs: []string{"get"}, ResourceNames: []string{"identity-ephemeral"},
		}},
		TTLSeconds: int64(shortTTL / time.Second),
	})
	if code != http.StatusOK {
		t.Fatalf("mint = %d: %s", code, raw)
	}

	// The record lives in root:railgrid:system:tenants, which no tenant and no
	// provider can read — only the hub's own credential.
	records := kcpDynamic(t, "root:railgrid:system:tenants", adminToken)
	if _, err := records.Resource(scopedIDGVR).Get(ctxWithTimeout(t, 15*time.Second), resp.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("ScopedIdentity %s not recorded: %v", resp.Name, err)
	}
	tenant := kcpDynamic(t, tenantCluster, adminToken)
	if _, err := tenant.Resource(serviceAcctGVR).Namespace("default").
		Get(ctxWithTimeout(t, 15*time.Second), resp.ServiceAccount, metav1.GetOptions{}); err != nil {
		t.Fatalf("ServiceAccount %s not materialized: %v", resp.ServiceAccount, err)
	}
	// serviceaccounts.WorkloadIdentityRoleName: the account name plus "-access".
	roleName := resp.ServiceAccount + "-access"

	// Delete the owner. Nothing else is done: no provider calls DELETE
	// /api/identities, so what follows is the sweep alone.
	if err := tenant.Resource(greetingGVR).Delete(ctxWithTimeout(t, 15*time.Second), "identity-ephemeral", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete the owner Greeting: %v", err)
	}

	// One sweep interval is 2 minutes; allow three so a tick landing just
	// before the delete does not flake the suite.
	if !waitForCondition(t, 6*time.Minute, func() (bool, string) {
		_, err := records.Resource(scopedIDGVR).Get(ctxWithTimeout(t, 10*time.Second), resp.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, "record still present"
	}) {
		t.Fatalf("ScopedIdentity %s was not collected after its owner was deleted", resp.Name)
	}

	// Collecting the record must take the credential with it: the
	// ServiceAccount going away is what revokes every token already issued.
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := tenant.Resource(serviceAcctGVR).Namespace("default").
			Get(ctxWithTimeout(t, 10*time.Second), resp.ServiceAccount, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, "ServiceAccount still present"
	}) {
		t.Errorf("ServiceAccount %s survived collection; outstanding tokens are still valid", resp.ServiceAccount)
	}
	if _, err := tenant.Resource(clusterRoleGVR).Get(ctxWithTimeout(t, 10*time.Second), roleName, metav1.GetOptions{}); err == nil {
		t.Errorf("ClusterRole %s survived collection", roleName)
	}
}

// TestGWorkloadExchangeFixture documents the one item of the identity surface
// this suite cannot reach. The pod-attested workload exchange
// (POST /api/workload-identity/exchange, pkg/hub/workloadidentity) authenticates
// a bootstrap token issued by the App Studio provider's own virtual workspace
// for a Project, and neither the provider nor the Project kind exists in this
// bootstrap. Its coverage is pkg/hub/workloadidentity's unit tests plus the
// App Studio suite; if an App Studio fixture is ever added to this suite, the
// assertion belongs here, next to the provider-asserted path that shares the
// minter.
func TestGWorkloadExchangeFixture(t *testing.T) {
	requireWorkspace(t)
	// What IS assertable here: the endpoint exists and refuses an unattested
	// caller rather than minting on a bare bearer.
	code, raw := hubDo(t, http.MethodPost, "/api/provider-actions/workload/exchange", staticToken,
		[]byte(`{"projectId":"none","environment":"dev"}`))
	if code == http.StatusOK {
		t.Fatalf("the workload exchange minted for an unattested caller: %s", raw)
	}
	if code == http.StatusNotFound {
		t.Fatalf("the workload exchange is not mounted (404); the hub no longer serves the path this test pins: %s", raw)
	}
	t.Skip("the pod-attested workload exchange needs an App Studio Project fixture; only its refusal path is covered here")
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

// shortTTL is the token lifetime every mint in this suite asks for: short
// enough that the credentials the suite leaves behind expire on their own,
// long enough to survive a test.
//
// Five minutes is deliberately BELOW the floor kcp's TokenRequest admission
// enforces (it refuses `spec.expirationSeconds` under ten minutes), because
// the hub now clamps a short ask UP to that floor instead of forwarding a
// lifetime the mint can never satisfy. Every mint in this suite therefore
// exercises the clamp, and the token that comes back lives ten minutes, not
// five — see the "a TTL below the mint floor is raised, not refused" case in
// TestBMintForGreetingOwner, which pins exactly that. Asking for less than the
// floor used to come back as a 502 identity_issue_failed with no hint that the
// TTL was the problem; that was the hub bug this constant once documented.
//
// Collection is NOT paced by this: an identity is collected when its OWNER
// goes away, which the reconciler notices on its own sweep interval. See
// TestFOwnerDeletionCollectsTheIdentity.
const shortTTL = 5 * time.Minute

// mintFloorTTL is what a below-floor request is raised to: the hub's
// serviceaccounts.ScopedIdentityMinTokenTTL, which equals kcp's own minimum.
const mintFloorTTL = 10 * time.Minute

func requireWorkspace(t *testing.T) {
	t.Helper()
	if tenantCluster == "" {
		t.Fatal("no tenant workspace; TestATenantWorkspaceAndEnable must run first (do not use -run to select a later test alone)")
	}
}

type acceptedComposition struct {
	Provider string `json:"provider"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
}

// enableProvider drives POST .../providers/{name}/enable as the tenant, which
// is what creates the APIBinding and records the composition decisions in the
// workspace's Grant. Idempotent, as the endpoint is.
func enableProvider(t *testing.T, name string, compositions []acceptedComposition) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"acceptedClaims":       []any{},
		"acceptedCompositions": compositions,
	})
	if err != nil {
		t.Fatalf("marshal enable request: %v", err)
	}
	path := fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/%s/enable", orgUUID, workspaceUUID, name)
	var code int
	var raw string
	// The catalog controller resolves a provider's API groups on its own
	// cadence, and Enable refuses until the dependency is registered, so this
	// retries rather than failing on the first attempt after startup.
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		code, raw = hubDoAs(t, http.MethodPost, path, staticToken, orgUUID, workspaceUUID, body)
		return code == http.StatusOK || code == http.StatusCreated, fmt.Sprintf("status %d: %s", code, raw)
	}) {
		t.Fatalf("enable %s never succeeded: last status %d: %s", name, code, raw)
	}
}

// personalOrgUUID returns the caller's personal Organization, creating the user
// on the way in via the static-token login the portal performs.
func personalOrgUUID(t *testing.T) string {
	t.Helper()
	login(t)
	var orgs struct {
		Items []struct {
			UUID     string `json:"uuid"`
			Personal bool   `json:"personal"`
		} `json:"items"`
	}
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		code, raw := hubDo(t, http.MethodGet, "/api/orgs", staticToken, nil)
		if code != http.StatusOK {
			return false, fmt.Sprintf("status %d: %s", code, raw)
		}
		if err := json.Unmarshal([]byte(raw), &orgs); err != nil {
			return false, err.Error()
		}
		return len(orgs.Items) > 0, "no orgs yet"
	}) {
		t.Fatal("the static-token user never got an organization")
	}
	for _, org := range orgs.Items {
		if org.Personal {
			return org.UUID
		}
	}
	return orgs.Items[0].UUID
}

func createWorkspace(t *testing.T, org, displayName string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"displayName": displayName})
	code, raw := hubDoWithOrg(t, http.MethodPost, "/api/orgs/"+org+"/workspaces", staticToken, org, body)
	if code != http.StatusCreated {
		t.Fatalf("create workspace = %d: %s", code, raw)
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out.UUID == "" {
		t.Fatalf("decode workspace create: %v (body %s)", err, raw)
	}
	return out.UUID
}

// workspaceCluster resolves the workspace's kcp logical cluster — the
// `clusterID` a provider sends on an identity request and the `/clusters/{id}`
// segment a minted token is used against. It is read off the child Workspace's
// spec.cluster, the same field the hub's own bootstrapper reads
// (pkg/hub/kcp/bootstrap.go GetChildWorkspaceClusterName), rather than scraped
// out of a downloaded kubeconfig.
func workspaceCluster(t *testing.T, org, workspace string) string {
	t.Helper()
	orgClient := kcpDynamic(t, "root:railgrid:tenants:"+org, adminToken)
	var cluster string
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		ws, err := orgClient.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), workspace, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		cluster, _, _ = unstructured.NestedString(ws.Object, "spec", "cluster")
		return cluster != "", "spec.cluster still empty (workspace not Ready)"
	}) {
		t.Fatalf("workspace %s never reported spec.cluster", workspace)
	}
	return cluster
}

// login performs the static-token login, which is what materializes the user,
// their personal org and their default workspace.
func login(t *testing.T) {
	t.Helper()
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		code, raw := hubDo(t, http.MethodPost, "/auth/token-login", staticToken, nil)
		return code == http.StatusOK, fmt.Sprintf("status %d: %s", code, raw)
	}) {
		t.Fatal("token-login never succeeded")
	}
}

// createGreeting writes the owner object, as the tenant. Idempotent, because
// several tests want the same owner and they must not depend on each other's
// cleanup.
func createGreeting(t *testing.T, name, message string) {
	t.Helper()
	tenant := kcpDynamic(t, tenantCluster, staticToken)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": greetingGVR.Group + "/" + greetingGVR.Version,
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"message": message},
	}}
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		_, err := tenant.Resource(greetingGVR).Create(ctxWithTimeout(t, 10*time.Second), object, metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return false, err.Error()
	}) {
		t.Fatalf("create Greeting %s in %s never succeeded", name, tenantCluster)
	}
}

func hubDo(t *testing.T, method, path, token string, body []byte) (int, string) {
	t.Helper()
	return hubDoAs(t, method, path, token, "", "", body)
}

// hubDoWithOrg addresses an org-scoped route. hubDoAs additionally names the
// workspace: the tenant middleware takes BOTH from headers
// (X-Railgrid-Org / X-Railgrid-Workspace) and not from the path, so a
// workspace-scoped route called with only the path segment set is rejected for
// want of a workspace.
func hubDoWithOrg(t *testing.T, method, path, token, org string, body []byte) (int, string) {
	t.Helper()
	return hubDoAs(t, method, path, token, org, "", body)
}

func hubDoAs(t *testing.T, method, path, token, org, workspace string, body []byte) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), method, hubURL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Railgrid-Org", org)
	}
	if workspace != "" {
		req.Header.Set("X-Railgrid-Workspace", workspace)
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
		Timeout:   60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}
