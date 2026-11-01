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
	"net/http/httptest"
	"strings"
	"testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/metering"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

// fakeBilling is the BillingOps the routes see; it records what it was asked.
type fakeBilling struct {
	summary  metering.OrgBilling
	err      error
	checkout func(org, plan, successURL, cancelURL string) (string, error)
	portal   func(org, returnURL string) (string, error)
	calls    []string
}

func (f *fakeBilling) Summary(_ context.Context, org string) (metering.OrgBilling, error) {
	f.calls = append(f.calls, "summary "+org)
	return f.summary, f.err
}

func (f *fakeBilling) Checkout(_ context.Context, org, plan, successURL, cancelURL string) (string, error) {
	f.calls = append(f.calls, "checkout "+org+" "+plan)
	if f.checkout != nil {
		return f.checkout(org, plan, successURL, cancelURL)
	}
	return "https://checkout.example/" + plan + "?return=" + successURL, nil
}

func (f *fakeBilling) Portal(_ context.Context, org, returnURL string) (string, error) {
	f.calls = append(f.calls, "portal "+org)
	if f.portal != nil {
		return f.portal(org, returnURL)
	}
	return "https://portal.example/?return=" + returnURL, nil
}

// billingServer serves the tenant routes with fake as the billing seam (nil:
// a hub without metering) for a caller whose tenant context is tc.
func billingServer(t *testing.T, fake *fakeBilling, tc tenant.TenantContext) *httptest.Server {
	t.Helper()
	mgr, _, _ := newTestManager(t)
	if fake != nil {
		mgr.WithBilling(fake, "https://hub.example/")
	}
	srv := newTestServer(t, mgr, tc)
	t.Cleanup(srv.Close)
	return srv
}

// billingCall sends one request and decodes the JSON answer; "_raw" holds
// the body as sent.
func billingCall(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["_raw"] = string(raw)
	return resp.StatusCode, out
}

func bodyString(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

// A caller holding only a workspace-scope membership reaches org-scope
// routes with an empty organization role (tenant.Middleware admits them when
// they send both tenant headers). The billing summary is organization-wide
// data and is not theirs to read.
func TestOrgBilling_RequiresAnOrganizationMembership(t *testing.T) {
	fake := &fakeBilling{summary: metering.OrgBilling{Installed: true, Plan: "team"}}
	srv := billingServer(t, fake, tcWithOrgRole("mallory", "org-a", "ws-1", tenancyv1alpha1.MembershipRoleMember, ""))
	code, body := billingCall(t, srv, http.MethodGet, "/api/orgs/org-a/billing", "")
	if code != http.StatusForbidden {
		t.Fatalf("workspace-only read: status %d, body %s", code, bodyString(body, "_raw"))
	}
	if len(fake.calls) != 0 {
		t.Fatalf("billing was read for a workspace-only caller: %v", fake.calls)
	}
}

// Members read the summary; changing the plan is for organization admins,
// and the hub supplies the settings page as the return address.
func TestOrgBilling_MembersReadAdminsManage(t *testing.T) {
	fake := &fakeBilling{summary: metering.OrgBilling{Installed: true, Enabled: true, Plan: "team"}}
	member := billingServer(t, fake, memberTC("alice", "org-a", ""))
	if code, body := billingCall(t, member, http.MethodGet, "/api/orgs/org-a/billing", ""); code != http.StatusOK || body["plan"] != "team" {
		t.Fatalf("member read: %d %s", code, bodyString(body, "_raw"))
	}
	for _, path := range []string{"/api/orgs/org-a/billing/checkout", "/api/orgs/org-a/billing/portal"} {
		if code, _ := billingCall(t, member, http.MethodPost, path, `{"plan":"large"}`); code != http.StatusForbidden {
			t.Fatalf("member POST %s: status %d, want 403", path, code)
		}
	}

	admin := billingServer(t, fake, adminTC("alice", "org-a", ""))
	code, body := billingCall(t, admin, http.MethodPost, "/api/orgs/org-a/billing/checkout", `{"plan":"large"}`)
	if want := "https://checkout.example/large?return=https://hub.example/ui/org-a/settings/organizations?billing=success"; code != http.StatusOK || body["url"] != want {
		t.Fatalf("admin checkout: %d %s", code, bodyString(body, "_raw"))
	}
	if code, _ := billingCall(t, admin, http.MethodPost, "/api/orgs/org-a/billing/checkout", `{"plan":""}`); code != http.StatusBadRequest {
		t.Fatalf("checkout without a plan: status %d, want 400", code)
	}
	code, body = billingCall(t, admin, http.MethodPost, "/api/orgs/org-a/billing/portal", `{}`)
	if want := "https://portal.example/?return=https://hub.example/ui/org-a/settings/organizations?billing=portal"; code != http.StatusOK || body["url"] != want {
		t.Fatalf("admin portal: %d %s", code, bodyString(body, "_raw"))
	}
}

// A BillingError carries its own status and message. Any other failure is
// answered with a fixed sentence, and its text (kcp URLs, cluster paths)
// stays in the log, since every member can reach these routes.
func TestOrgBilling_ErrorsAreMappedWithoutInternalDetail(t *testing.T) {
	fake := &fakeBilling{err: errors.New(`Get "https://kcp.internal:6443/clusters/1abc/apis/metering.contrib.kcp.io/v1alpha1/plans": dial tcp: connection refused`)}
	member := billingServer(t, fake, memberTC("alice", "org-a", ""))
	code, body := billingCall(t, member, http.MethodGet, "/api/orgs/org-a/billing", "")
	if code != http.StatusBadGateway || bodyString(body, "message") != "billing information is temporarily unavailable" || strings.Contains(bodyString(body, "_raw"), "kcp.internal") {
		t.Fatalf("summary failure: %d %s", code, bodyString(body, "_raw"))
	}

	fake = &fakeBilling{checkout: func(string, string, string, string) (string, error) {
		return "", &metering.BillingError{Status: http.StatusConflict, Message: "the organization's plan is assigned by a platform administrator"}
	}}
	admin := billingServer(t, fake, adminTC("alice", "org-a", ""))
	code, body = billingCall(t, admin, http.MethodPost, "/api/orgs/org-a/billing/checkout", `{"plan":"large"}`)
	if code != http.StatusConflict || !strings.Contains(bodyString(body, "message"), "platform administrator") {
		t.Fatalf("billing error: %d %s", code, bodyString(body, "_raw"))
	}

	fake.checkout = func(string, string, string, string) (string, error) {
		return "", errors.New("Post https://metering.internal/billing/checkout: EOF")
	}
	code, body = billingCall(t, admin, http.MethodPost, "/api/orgs/org-a/billing/checkout", `{"plan":"large"}`)
	if code != http.StatusBadGateway || strings.Contains(bodyString(body, "_raw"), "metering.internal") {
		t.Fatalf("generic failure: %d %s", code, bodyString(body, "_raw"))
	}
}

// Without metering the summary says so and the hosted flows are unavailable.
func TestOrgBilling_WithoutMetering(t *testing.T) {
	srv := billingServer(t, nil, adminTC("alice", "org-a", ""))
	code, body := billingCall(t, srv, http.MethodGet, "/api/orgs/org-a/billing", "")
	if code != http.StatusOK || body["installed"] != false {
		t.Fatalf("summary without metering: %d %s", code, bodyString(body, "_raw"))
	}
	for _, path := range []string{"/api/orgs/org-a/billing/checkout", "/api/orgs/org-a/billing/portal"} {
		if code, _ := billingCall(t, srv, http.MethodPost, path, `{"plan":"large"}`); code != http.StatusServiceUnavailable {
			t.Fatalf("POST %s without metering: status %d, want 503", path, code)
		}
	}
}
