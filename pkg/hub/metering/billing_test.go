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

package metering

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
)

// deployment stands in for the metering controller's /billing/ routes.
type deployment struct {
	t        *testing.T
	token    string
	enabled  bool
	emptyURL bool // answer the hosted-page routes with {"url":""}
	requests []map[string]string
	webhooks []string
}

func (d *deployment) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/billing/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+d.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !d.enabled {
			http.Error(w, "billing API disabled", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "provider": "stripe", "livemode": false, "defaultPlan": "free"})
	})
	mux.HandleFunc("/billing/checkout", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+d.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.requests = append(d.requests, body)
		if body["plan"] == "free" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": `plan "free" is not sold`})
			return
		}
		if body["plan"] == "boom" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "pq: connection to /var/run/postgresql refused"})
			return
		}
		if body["plan"] == "proxied" {
			// Something in front of metering answered, not the deployment.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><body><h1>403 Forbidden</h1><p>internal-ingress-7f: policy secret-policy-name denied</p></body></html>"))
			return
		}
		if body["plan"] == "redirected" {
			// A 3xx the client does not follow (no Location) comes back as is.
			w.WriteHeader(http.StatusMultipleChoices)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "choose a region"})
			return
		}
		if body["plan"] == "verbose" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "plan is not sold: " + strings.Repeat("x", 2000)})
			return
		}
		if d.emptyURL {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": ""})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://checkout.stripe.com/c/pay/cs_1"})
	})
	mux.HandleFunc("/billing/portal", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+d.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.requests = append(d.requests, body)
		if d.emptyURL {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": ""})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://billing.stripe.com/p/session/ps_1"})
	})
	mux.HandleFunc("/billing/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		d.webhooks = append(d.webhooks, r.Header.Get("Stripe-Signature")+" "+string(body))
		_ = json.NewEncoder(w).Encode(map[string]any{"received": true, "account": "org-1"})
	})
	return mux
}

// discovered is an Installed with a token, which installed(true) lacks.
type discovered Endpoints

func (d discovered) Current() (Endpoints, bool) { return Endpoints(d), true }

func newBillingFixture(t *testing.T, enabled bool, objects ...runtime.Object) (*Billing, *deployment) {
	t.Helper()
	dep := &deployment{t: t, token: "tok", enabled: enabled}
	srv := httptest.NewServer(dep.handler())
	t.Cleanup(srv.Close)
	store := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrPlans: "PlanList", gvrEntitlements: "EntitlementList",
	}, objects...)
	return NewBilling(discovered{PortalURL: srv.URL, token: "tok"}, store, klog.Background()), dep
}

func planObj(name, display, price string, limits ...map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Plan")
	u.SetName(name)
	spec := map[string]any{"displayName": display}
	if price != "" {
		spec["subscription"] = map[string]any{"priceID": price}
	}
	rows := make([]any, 0, len(limits))
	for _, l := range limits {
		rows = append(rows, l)
	}
	spec["limits"] = rows
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

func TestSummaryComposesTheOrganizationsBilling(t *testing.T) {
	ent := &unstructured.Unstructured{}
	ent.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	ent.SetKind("Entitlement")
	ent.SetName("org-1")
	ent.SetLabels(map[string]string{PlanSourceLabel: "billing"})
	_ = unstructured.SetNestedMap(ent.Object, map[string]any{"account": "org-1", "plan": "team"}, "spec")
	_ = unstructured.SetNestedField(ent.Object, map[string]any{
		"effectiveLimits": []any{map[string]any{"meter": "workspaces", "shape": "Level", "enforce": "Hard", "max": "25"}},
		"usage":           map[string]any{"workspaces": "3"},
		"window":          map[string]any{"start": "2026-11-01T00:00:00Z", "end": "2026-11-01T00:01:00Z"},
		"billing":         map[string]any{"entitledPlan": "team", "status": "active", "currentPeriodEnd": "2026-12-01T00:00:00Z", "cancelAtPeriodEnd": true},
		"conditions":      []any{map[string]any{"type": "OverHardLimit", "status": "False"}, map[string]any{"type": "OverSoftLimit", "status": "True"}},
	}, "status")
	b, _ := newBillingFixture(t, true,
		planObj("free", "Free", "", map[string]any{"meter": "workspaces", "enforce": "Hard", "max": "1"}),
		planObj("team", "Team", "price_team", map[string]any{"meter": "workspaces", "enforce": "Hard", "max": "25"}),
		ent,
	)
	got, err := b.Summary(context.Background(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Installed || !got.Enabled || got.Livemode || got.DefaultPlan != "free" {
		t.Fatalf("status: %+v", got)
	}
	if got.Plan != "team" || got.PlanDisplayName != "Team" || got.ManagedBy != "billing" {
		t.Fatalf("plan: %+v", got)
	}
	if len(got.Limits) != 1 || got.Limits[0].Meter != "workspaces" || got.Limits[0].Max != "25" || got.Usage["workspaces"] != "3" || got.Window == nil || got.Window.End == "" {
		t.Fatalf("limits/usage: %+v", got)
	}
	if !got.OverSoftLimit || got.OverHardLimit {
		t.Fatalf("conditions: %+v", got)
	}
	if got.Subscription == nil || got.Subscription.Status != "active" || !got.Subscription.CancelAtPeriodEnd || got.Subscription.CurrentPeriodEnd == "" {
		t.Fatalf("subscription: %+v", got.Subscription)
	}
	if len(got.Plans) != 2 || got.Plans[0].Name != "free" || got.Plans[0].Sellable || got.Plans[1].Name != "team" || !got.Plans[1].Sellable || !got.Plans[1].Current || got.Plans[0].Current {
		t.Fatalf("plans: %+v", got.Plans)
	}

	// No entitlement yet: the default plan, nothing used.
	got, err = b.Summary(context.Background(), "org-new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan != "free" || !got.Plans[0].Current || len(got.Limits) != 0 || got.Subscription != nil {
		t.Fatalf("new organization: %+v", got)
	}
}

func TestSummaryWithoutBillingOrMetering(t *testing.T) {
	b, _ := newBillingFixture(t, false, planObj("free", "Free", ""))
	got, err := b.Summary(context.Background(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Installed || got.Enabled || len(got.Plans) != 1 {
		t.Fatalf("billing off: %+v", got)
	}

	b.discovery = installed(false)
	got, err = b.Summary(context.Background(), "org-1")
	if err != nil || got.Installed || got.Enabled || got.Plans == nil || got.Limits == nil || got.Usage == nil {
		t.Fatalf("not installed: %+v %v", got, err)
	}
}

func TestCheckoutForwardsAndMapsErrors(t *testing.T) {
	b, dep := newBillingFixture(t, true)
	u, err := b.Checkout(context.Background(), "org-1", "team", "https://hub/ok", "https://hub/cancel")
	if err != nil || u != "https://checkout.stripe.com/c/pay/cs_1" {
		t.Fatalf("checkout: %q %v", u, err)
	}
	if req := dep.requests[0]; req["account"] != "org-1" || req["plan"] != "team" || req["successURL"] != "https://hub/ok" || req["cancelURL"] != "https://hub/cancel" {
		t.Fatalf("forwarded body = %v", req)
	}

	// The deployment's 4xx reaches the caller with its message.
	var be *BillingError
	_, err = b.Checkout(context.Background(), "org-1", "free", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadRequest || !strings.Contains(be.Message, "not sold") {
		t.Fatalf("unsold plan: %v", err)
	}

	// A wrong token is the hub's problem, reported as a gateway error.
	dep.token = "other"
	_, err = b.Checkout(context.Background(), "org-1", "team", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadGateway {
		t.Fatalf("bad token: %v", err)
	}

	b.discovery = installed(false)
	_, err = b.Checkout(context.Background(), "org-1", "team", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusServiceUnavailable {
		t.Fatalf("not installed: %v", err)
	}
}

func TestForwardWebhookRelaysBodyAndSignature(t *testing.T) {
	b, dep := newBillingFixture(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/billing/webhook", strings.NewReader(`{"id":"evt_1"}`))
	req.Header.Set("Stripe-Signature", "t=1,v1=abc")
	b.ForwardWebhook(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"received":true`) {
		t.Fatalf("relay: %d %s", rec.Code, rec.Body.String())
	}
	if len(dep.webhooks) != 1 || dep.webhooks[0] != `t=1,v1=abc {"id":"evt_1"}` {
		t.Fatalf("deployment saw %v", dep.webhooks)
	}

	b.discovery = installed(false)
	rec = httptest.NewRecorder()
	b.ForwardWebhook(rec, httptest.NewRequest(http.MethodPost, "/api/billing/webhook", strings.NewReader("{}")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not installed: %d", rec.Code)
	}
}

// A plan a platform administrator assigned (PlanSourceOperator on the
// Entitlement) is not for sale through Checkout: the deployment's billing
// reconciler leaves such a plan alone, so the organization would pay and
// stay where it is.
func TestCheckoutRefusedWhileAPlatformAdminOwnsThePlan(t *testing.T) {
	ent := &unstructured.Unstructured{}
	ent.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	ent.SetKind("Entitlement")
	ent.SetName("org-1")
	ent.SetLabels(map[string]string{PlanSourceLabel: PlanSourceOperator})
	_ = unstructured.SetNestedMap(ent.Object, map[string]any{"account": "org-1", "plan": "team"}, "spec")
	b, dep := newBillingFixture(t, true, ent)

	_, err := b.Checkout(context.Background(), "org-1", "large", "https://hub/ok", "https://hub/cancel")
	var be *BillingError
	if !errors.As(err, &be) || be.Status != http.StatusConflict || !strings.Contains(be.Message, "platform administrator") {
		t.Fatalf("checkout under an operator-assigned plan: %v", err)
	}
	if len(dep.requests) != 0 {
		t.Fatalf("the deployment was asked anyway: %v", dep.requests)
	}

	// Another organization, not operator-managed, still checks out.
	if u, err := b.Checkout(context.Background(), "org-2", "large", "https://hub/ok", "https://hub/cancel"); err != nil || u == "" {
		t.Fatalf("checkout for a subscription-managed organization: %q %v", u, err)
	}
}

// A 2xx without a URL is a bad gateway, not a redirect to "".
func TestCheckoutAndPortalRefuseAnEmptyURL(t *testing.T) {
	b, dep := newBillingFixture(t, true)
	dep.emptyURL = true
	var be *BillingError
	if _, err := b.Checkout(context.Background(), "org-1", "team", "https://hub/ok", "https://hub/cancel"); !errors.As(err, &be) || be.Status != http.StatusBadGateway {
		t.Fatalf("checkout with an empty url: %v", err)
	}
	if _, err := b.Portal(context.Background(), "org-1", "https://hub/back"); !errors.As(err, &be) || be.Status != http.StatusBadGateway {
		t.Fatalf("portal with an empty url: %v", err)
	}
	dep.emptyURL = false
	if u, err := b.Portal(context.Background(), "org-1", "https://hub/back"); err != nil || u != "https://billing.stripe.com/p/session/ps_1" {
		t.Fatalf("portal: %q %v", u, err)
	}
}

// The webhook route is registered on every hub; without metering the relay
// is nil and answers 503, so the provider retries later instead of taking a
// generic answer for a receipt.
func TestForwardWebhookWithoutMeteringAnswers503(t *testing.T) {
	var b *Billing
	rec := httptest.NewRecorder()
	b.ForwardWebhook(rec, httptest.NewRequest(http.MethodPost, "/api/billing/webhook", strings.NewReader(`{"id":"evt_1"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// What the hub cannot answer for itself stays out of the message a member
// sees: a transport failure comes back as a plain error naming the
// deployment's address, for the route to log, and the deployment's own 5xx
// text is logged here and replaced by a fixed sentence.
func TestTransportAndServerFailuresKeepDetailOutOfTheMessage(t *testing.T) {
	store := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrPlans: "PlanList", gvrEntitlements: "EntitlementList",
	})
	unreachable := NewBilling(discovered{PortalURL: "http://127.0.0.1:1", token: "tok"}, store, klog.Background())
	_, err := unreachable.Checkout(context.Background(), "org-1", "team", "https://hub/ok", "https://hub/cancel")
	var be *BillingError
	if err == nil || errors.As(err, &be) {
		t.Fatalf("a transport failure must be a plain error for the route to log, got %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("the plain error should keep the address for the log: %v", err)
	}

	b, _ := newBillingFixture(t, true)
	_, err = b.Checkout(context.Background(), "org-1", "boom", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadGateway || strings.Contains(be.Message, "postgresql") {
		t.Fatalf("a 5xx from the deployment: %v", err)
	}

	// The deployment's own 4xx answer is meant for the caller and passes.
	_, err = b.Checkout(context.Background(), "org-1", "free", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadRequest || !strings.Contains(be.Message, "not sold") {
		t.Fatalf("a 4xx from the deployment: %v", err)
	}
}

// Only a JSON error from the deployment reaches the caller, capped; a
// non-JSON 4xx (a proxy or an HTML error page in front of metering) is a
// 502 with a fixed message, and its excerpt is bounded for the log.
func TestOnlyTheDeploymentsOwnErrorReachesTheCaller(t *testing.T) {
	b, _ := newBillingFixture(t, true)
	var be *BillingError

	_, err := b.Checkout(context.Background(), "org-1", "proxied", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadGateway || strings.Contains(be.Message, "secret-policy") || strings.Contains(be.Message, "<") {
		t.Fatalf("an HTML 403 from a proxy: %v", err)
	}

	_, err = b.Checkout(context.Background(), "org-1", "verbose", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadRequest || !strings.HasPrefix(be.Message, "plan is not sold") {
		t.Fatalf("a long JSON error: %v", err)
	}
	if n := len([]rune(be.Message)); n > maxAnswerExcerpt+1 {
		t.Fatalf("the deployment's message was not capped: %d characters", n)
	}

	if got := excerpt("  " + strings.Repeat("é", 300) + "  "); len([]rune(got)) != maxAnswerExcerpt+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("excerpt = %d characters", len([]rune(got)))
	}
	if got := excerpt(" short "); got != "short" {
		t.Fatalf("excerpt(short) = %q", got)
	}

	// A 3xx is never a status the route may answer with, JSON error or not.
	_, err = b.Checkout(context.Background(), "org-1", "redirected", "https://hub/ok", "https://hub/cancel")
	if !errors.As(err, &be) || be.Status != http.StatusBadGateway || strings.Contains(be.Message, "region") {
		t.Fatalf("a 3xx with a JSON error: %v", err)
	}
}
