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
	"errors"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/railgrid/railgrid/pkg/hub/metering"

	"k8s.io/klog/v2"
)

// Billing routes backed by the metering deployment:
//
//	GET  /api/billing/plans                signed-in user: plans and prices
//	GET  /api/orgs/{org}/billing            organization member: plan, limits, usage, subscription, plans on offer
//	POST /api/orgs/{org}/billing/checkout   org admin: {plan} -> {url} to redirect to
//	POST /api/orgs/{org}/billing/portal     org admin: {} -> {url} to redirect to
//
// The hub supplies the return addresses for billing settings and onboarding.

// BillingOps is the slice of pkg/hub/metering.Billing the routes need.
type BillingOps interface {
	Summary(ctx context.Context, org string) (metering.OrgBilling, error)
	Checkout(ctx context.Context, org, plan, successURL, cancelURL string) (string, error)
	Portal(ctx context.Context, org, returnURL string) (string, error)
}

// CheckoutRequest is the POST /api/orgs/{org}/billing/checkout body.
type CheckoutRequest struct {
	Plan       string `json:"plan"`
	PriceID    string `json:"priceID,omitempty"`
	Onboarding bool   `json:"onboarding,omitempty"`
}

// SessionResponse carries the hosted page's URL.
type SessionResponse struct {
	URL string `json:"url"`
}

func (h *Handler) getBillingPlans(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUser(w, r); !ok {
		return
	}
	catalog := struct {
		Enabled bool                 `json:"enabled"`
		Plans   []metering.PlanOffer `json:"plans"`
	}{Plans: []metering.PlanOffer{}}
	if h.mgr.billing != nil {
		summary, err := h.mgr.billing.Summary(r.Context(), "")
		if err != nil {
			writeBillingError(w, r, err)
			return
		}
		catalog.Enabled, catalog.Plans = summary.Enabled, summary.Plans
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, catalog)
}

func (h *Handler) getOrgBilling(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgMember(w, r)
	if !ok {
		return
	}
	if h.mgr.billing == nil {
		writeJSON(w, http.StatusOK, metering.OrgBilling{Limits: []metering.Limit{}, Usage: map[string]string{}, Plans: []metering.PlanOffer{}})
		return
	}
	summary, err := h.mgr.billing.Summary(r.Context(), tc.OrgUUID)
	if err != nil {
		writeBillingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) orgBillingCheckout(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, false, true)
	if !ok {
		return
	}
	var req CheckoutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Plan) == "" {
		writeError(w, newValidationError("plan is required"))
		return
	}
	if h.mgr.billing == nil {
		writeStatus(w, http.StatusServiceUnavailable, "BillingUnavailable", "metering is not configured on this hub")
		return
	}
	successURL := h.billingReturnURL(tc.OrgUUID, "success")
	cancelURL := h.billingReturnURL(tc.OrgUUID, "canceled")
	if req.Onboarding {
		if req.PriceID == "" {
			writeError(w, newValidationError("a reviewed price is required for onboarding checkout"))
			return
		}
		base := strings.TrimRight(h.mgr.hubExternalURL, "/") + "/ui/onboarding?org=" + neturl.QueryEscape(tc.OrgUUID)
		successURL = base + "&billing=success"
		cancelURL = base + "&billing=canceled"
	}
	var url string
	var err error
	if req.PriceID != "" {
		quoted, ok := h.mgr.billing.(interface {
			CheckoutQuoted(context.Context, string, string, string, string, string) (string, error)
		})
		if !ok {
			writeStatus(w, http.StatusServiceUnavailable, "BillingUnavailable", "the billing integration cannot confirm the reviewed price")
			return
		}
		url, err = quoted.CheckoutQuoted(r.Context(), tc.OrgUUID, req.Plan, req.PriceID, successURL+"&billingPlan="+neturl.QueryEscape(req.Plan), cancelURL+"&billingPlan="+neturl.QueryEscape(req.Plan))
	} else {
		url, err = h.mgr.billing.Checkout(r.Context(), tc.OrgUUID, req.Plan, successURL, cancelURL)
	}
	if err != nil {
		writeBillingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionResponse{URL: url})
}

func (h *Handler) orgBillingPortal(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, false, true)
	if !ok {
		return
	}
	if h.mgr.billing == nil {
		writeStatus(w, http.StatusServiceUnavailable, "BillingUnavailable", "metering is not configured on this hub")
		return
	}
	url, err := h.mgr.billing.Portal(r.Context(), tc.OrgUUID, h.billingReturnURL(tc.OrgUUID, "portal"))
	if err != nil {
		writeBillingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionResponse{URL: url})
}

// billingReturnURL is the organization's settings page, which reads the
// billing query parameter into a notice.
func (h *Handler) billingReturnURL(org, outcome string) string {
	return strings.TrimRight(h.mgr.hubExternalURL, "/") + "/ui/" + org + "/settings/organizations?billing=" + outcome + "&panel=billing"
}

// writeBillingError answers a billing failure. A BillingError carries a
// message meant for the caller. Anything else is a kcp or HTTP client error
// whose text (server URLs, cluster paths) stays in the log: every member of
// the organization can reach these routes.
func writeBillingError(w http.ResponseWriter, r *http.Request, err error) {
	var be *metering.BillingError
	if errors.As(err, &be) {
		writeStatus(w, be.Status, "Billing", be.Message)
		return
	}
	klog.FromContext(r.Context()).Error(err, "organization billing request failed", "path", r.URL.Path)
	writeStatus(w, http.StatusBadGateway, "BillingUnavailable", "billing information is temporarily unavailable")
}
