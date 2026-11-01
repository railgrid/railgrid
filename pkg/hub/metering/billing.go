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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// The hub's seam to subscription billing. The metering deployment sells
// plans (whatever provider it runs; the hub does not know); what the hub
// does is generic: it reads an organization's Entitlement for its plan,
// limits, usage and subscription state, forwards the two hosted flows
// (Checkout, Customer Portal) to the deployment's /billing/ routes with the
// operator token, and relays the provider's webhook to it unchanged. Which
// plan an organization is on stays what the Entitlement says.

const (
	// PlanSourceLabel is contrib-metering's record of who owns an
	// Entitlement's spec.plan. The hub writes PlanSourceOperator while an
	// admin's Organization.spec.plan stands, so the deployment's billing
	// reconciler leaves that plan alone, and clears it with the assignment.
	PlanSourceLabel = "metering.contrib.kcp.io/plan-source"
	// PlanSourceOperator is the PlanSourceLabel value for a plan set by hand.
	PlanSourceOperator = "operator"

	// maxWebhookBody bounds a relayed provider delivery.
	maxWebhookBody = 1 << 20
)

// Billing holds what the seam needs: discovery for the deployment's URL and
// token, and the kcp-admin client into the metering workspace for reads.
type Billing struct {
	discovery Installed
	store     dynamic.Interface
	client    *http.Client
	logger    klog.Logger
}

// NewBilling builds the seam over discovery (the hub's Discovery; tests
// substitute a constant) and the kcp-admin client into the metering
// workspace.
func NewBilling(d Installed, store dynamic.Interface, logger klog.Logger) *Billing {
	return &Billing{discovery: d, store: store, client: &http.Client{Timeout: 15 * time.Second}, logger: logger}
}

// BillingError carries the deployment's answer (or the seam's refusal) to
// the REST caller with its status.
type BillingError struct {
	Status  int
	Message string
}

func (e *BillingError) Error() string { return e.Message }

// OrgBilling is GET /api/orgs/{org}/billing: the organization's plan and
// what it buys, what it uses, the subscription behind it, and the plans on
// offer.
type OrgBilling struct {
	// Installed is whether metering is discovered at all; without it there
	// are no plans, limits or usage to show.
	Installed bool `json:"installed"`
	// Enabled is whether the deployment sells plans: Checkout and the
	// Customer Portal are available.
	Enabled bool `json:"enabled"`
	// Livemode is false when the deployment runs its provider's test
	// environment.
	Livemode    bool   `json:"livemode"`
	DefaultPlan string `json:"defaultPlan,omitempty"`
	// Plan is the organization's current plan (its Entitlement's spec.plan;
	// the default plan until the Entitlement exists).
	Plan            string `json:"plan,omitempty"`
	PlanDisplayName string `json:"planDisplayName,omitempty"`
	// ManagedBy is who set the plan: "billing" (a subscription), "operator"
	// (a platform admin), empty for the default plan.
	ManagedBy     string            `json:"managedBy,omitempty"`
	Limits        []Limit           `json:"limits"`
	Usage         map[string]string `json:"usage"`
	Window        *UsageWindow      `json:"window,omitempty"`
	OverSoftLimit bool              `json:"overSoftLimit"`
	OverHardLimit bool              `json:"overHardLimit"`
	// Subscription is the provider's view, when the deployment has synced
	// one.
	Subscription *OrgSubscription `json:"subscription,omitempty"`
	Plans        []PlanOffer      `json:"plans"`
}

// UsageWindow is the window the usage values cover.
type UsageWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// OrgSubscription is Entitlement.status.billing as the portal shows it.
type OrgSubscription struct {
	EntitledPlan      string `json:"entitledPlan,omitempty"`
	Status            string `json:"status,omitempty"`
	CurrentPeriodEnd  string `json:"currentPeriodEnd,omitempty"`
	CancelAtPeriodEnd bool   `json:"cancelAtPeriodEnd"`
	SyncedAt          string `json:"syncedAt,omitempty"`
}

// PlanOffer is one plan: its limits, whether the deployment sells it, and
// whether the organization is on it.
type PlanOffer struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"displayName,omitempty"`
	Sellable    bool    `json:"sellable"`
	Current     bool    `json:"current"`
	Limits      []Limit `json:"limits"`
}

type billingStatus struct {
	Enabled     bool   `json:"enabled"`
	Provider    string `json:"provider"`
	Livemode    bool   `json:"livemode"`
	DefaultPlan string `json:"defaultPlan"`
}

// Summary composes OrgBilling for org.
func (b *Billing) Summary(ctx context.Context, org string) (OrgBilling, error) {
	out := OrgBilling{Limits: []Limit{}, Usage: map[string]string{}, Plans: []PlanOffer{}}
	e, ok := b.discovery.Current()
	if !ok {
		return out, nil
	}
	out.Installed = true
	if st, err := b.status(ctx, e); err != nil {
		b.logger.V(2).Info("billing status unavailable", "error", err.Error())
	} else {
		out.Enabled, out.Livemode, out.DefaultPlan = st.Enabled, st.Livemode, st.DefaultPlan
	}

	plans, err := b.store.Resource(gvrPlans).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, fmt.Errorf("listing plans: %w", err)
	}
	display := map[string]string{}
	for i := range plans.Items {
		p := &plans.Items[i]
		offer := PlanOffer{Name: p.GetName(), Limits: limitsOf(p.Object, "spec", "limits")}
		offer.DisplayName, _, _ = unstructured.NestedString(p.Object, "spec", "displayName")
		price, _, _ := unstructured.NestedString(p.Object, "spec", "subscription", "priceID")
		offer.Sellable = price != ""
		display[offer.Name] = offer.DisplayName
		out.Plans = append(out.Plans, offer)
	}
	sort.Slice(out.Plans, func(i, j int) bool { return out.Plans[i].Name < out.Plans[j].Name })

	ent, err := b.store.Resource(gvrEntitlements).Get(ctx, org, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return out, fmt.Errorf("reading entitlement: %w", err)
		}
		out.Plan = out.DefaultPlan
	} else {
		out.Plan, _, _ = unstructured.NestedString(ent.Object, "spec", "plan")
		out.ManagedBy = ent.GetLabels()[PlanSourceLabel]
		out.Limits = limitsOf(ent.Object, "status", "effectiveLimits")
		if usage, ok, _ := unstructured.NestedStringMap(ent.Object, "status", "usage"); ok {
			out.Usage = usage
		}
		if w, ok, _ := unstructured.NestedStringMap(ent.Object, "status", "window"); ok {
			out.Window = &UsageWindow{Start: w["start"], End: w["end"]}
		}
		conditions, _, _ := unstructured.NestedSlice(ent.Object, "status", "conditions")
		for _, c := range conditions {
			m, _ := c.(map[string]any)
			t, _ := m["type"].(string)
			s, _ := m["status"].(string)
			switch {
			case t == "OverSoftLimit" && s == "True":
				out.OverSoftLimit = true
			case t == "OverHardLimit" && s == "True":
				out.OverHardLimit = true
			}
		}
		if bl, ok, _ := unstructured.NestedMap(ent.Object, "status", "billing"); ok {
			str := func(k string) string { v, _ := bl[k].(string); return v }
			cancel, _ := bl["cancelAtPeriodEnd"].(bool)
			out.Subscription = &OrgSubscription{
				EntitledPlan:      str("entitledPlan"),
				Status:            str("status"),
				CurrentPeriodEnd:  str("currentPeriodEnd"),
				CancelAtPeriodEnd: cancel,
				SyncedAt:          str("syncedAt"),
			}
		}
	}
	out.PlanDisplayName = display[out.Plan]
	for i := range out.Plans {
		out.Plans[i].Current = out.Plans[i].Name == out.Plan
	}
	return out, nil
}

// limitsOf reads a PlanLimit list at fields.
func limitsOf(obj map[string]any, fields ...string) []Limit {
	rows, _, _ := unstructured.NestedSlice(obj, fields...)
	out := make([]Limit, 0, len(rows))
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { v, _ := m[k].(string); return v }
		out = append(out, Limit{Meter: str("meter"), Shape: str("shape"), Enforce: str("enforce"), Max: str("max")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meter < out[j].Meter })
	return out
}

// Checkout asks the deployment for a Checkout URL that puts org on plan. An
// organization whose plan a platform administrator assigned
// (PlanSourceOperator on its Entitlement) is refused: the deployment's
// billing reconciler leaves such a plan alone, so paying would change
// nothing until the administrator releases it.
func (b *Billing) Checkout(ctx context.Context, org, plan, successURL, cancelURL string) (string, error) {
	ent, err := b.store.Resource(gvrEntitlements).Get(ctx, org, metav1.GetOptions{})
	switch {
	case err == nil && ent.GetLabels()[PlanSourceLabel] == PlanSourceOperator:
		return "", &BillingError{Status: http.StatusConflict, Message: "the organization's plan is assigned by a platform administrator; ask them to release it before subscribing"}
	case err != nil && !apierrors.IsNotFound(err):
		return "", fmt.Errorf("reading entitlement: %w", err)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := b.post(ctx, "/billing/checkout", map[string]string{"account": org, "plan": plan, "successURL": successURL, "cancelURL": cancelURL}, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		// A 2xx without a URL would send the browser to "" and reload the
		// settings page with no error; say what happened instead.
		return "", &BillingError{Status: http.StatusBadGateway, Message: "metering answered without a checkout URL"}
	}
	return out.URL, nil
}

// Portal asks the deployment for a Customer Portal URL for org.
func (b *Billing) Portal(ctx context.Context, org, returnURL string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := b.post(ctx, "/billing/portal", map[string]string{"account": org, "returnURL": returnURL}, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", &BillingError{Status: http.StatusBadGateway, Message: "metering answered without a billing portal URL"}
	}
	return out.URL, nil
}

// ForwardWebhook relays the provider's delivery to the deployment, body
// and signature header unchanged, and returns its answer. No hub-side
// authentication: the deployment verifies the provider's signature, which
// is the only credential a webhook carries. The route is registered on
// every hub, so a nil Billing (metering not configured) answers 503 and the
// provider retries later rather than taking a generic answer for a receipt.
func (b *Billing) ForwardWebhook(w http.ResponseWriter, r *http.Request) {
	if b == nil {
		http.Error(w, "metering is not configured on this hub", http.StatusServiceUnavailable)
		return
	}
	e, ok := b.discovery.Current()
	if !ok {
		http.Error(w, "metering is not installed", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxWebhookBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, e.PortalURL+"/billing/webhook", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for _, h := range []string{"Stripe-Signature", "Content-Type", "User-Agent"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.logger.Error(err, "relaying a billing webhook")
		http.Error(w, "metering unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		// A deployment without billing answers every unknown path with its
		// portal page; a 200 there would tell the provider the delivery
		// landed. Say it did not, so it retries once billing is deployed.
		http.Error(w, "the metering deployment does not serve billing", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<10))
}

func (b *Billing) status(ctx context.Context, e Endpoints) (billingStatus, error) {
	if e.token == "" {
		return billingStatus{}, errors.New("metering published no operator token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.PortalURL+"/billing/status", nil)
	if err != nil {
		return billingStatus{}, err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := b.client.Do(req)
	if err != nil {
		return billingStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusServiceUnavailable:
		// An older deployment, or one with billing off: no plans sold.
		return billingStatus{}, nil
	case resp.StatusCode != http.StatusOK:
		return billingStatus{}, fmt.Errorf("billing status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var st billingStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return billingStatus{}, err
	}
	return st, nil
}

// maxAnswerExcerpt bounds what of a deployment's error answer is kept: a
// message passed to the caller or a line in the log, never a 64 KiB page.
const maxAnswerExcerpt = 256

// excerpt trims s and keeps at most maxAnswerExcerpt characters of it.
func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxAnswerExcerpt {
		return string(r[:maxAnswerExcerpt]) + "…"
	}
	return s
}

// post calls a token-guarded billing route and maps the deployment's
// answer. A *BillingError carries a status and a message meant for the
// caller: the deployment's own JSON 4xx answer (capped), or a fixed
// sentence for the cases the hub names itself; a deployment without
// billing is a 503. A transport failure is a plain error whose detail the
// route keeps in the log, and an answer that is not the deployment's own
// (a proxy or an HTML error page) is a 502 with a fixed message.
func (b *Billing) post(ctx context.Context, path string, body, out any) error {
	e, ok := b.discovery.Current()
	if !ok {
		return &BillingError{Status: http.StatusServiceUnavailable, Message: "metering is not installed"}
	}
	if e.token == "" {
		return &BillingError{Status: http.StatusServiceUnavailable, Message: "metering published no operator token; set the chart's admin.tokenSecretRef"}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.PortalURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		// A transport failure names the deployment's URL and the connection
		// error; it goes back as a plain error so the route logs it and
		// answers with a fixed sentence.
		return fmt.Errorf("calling metering %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		// Only a JSON {"error": ...} is the deployment speaking to the
		// caller. Anything else, a proxy's or an HTML error page in front
		// of metering, is not for the organization's members: it stays a
		// bounded excerpt in the log, and both are capped where derived.
		var msg struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &msg)
		answer := excerpt(msg.Error)
		detail := excerpt(string(raw))
		switch {
		case resp.StatusCode == http.StatusNotFound && strings.Contains(detail, "page not found"):
			return &BillingError{Status: http.StatusServiceUnavailable, Message: "this metering deployment does not sell plans"}
		case resp.StatusCode == http.StatusServiceUnavailable:
			return &BillingError{Status: http.StatusServiceUnavailable, Message: "billing is not enabled on the metering deployment"}
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return &BillingError{Status: http.StatusBadGateway, Message: "metering rejected the hub's operator token"}
		case resp.StatusCode >= 500:
			// The deployment's own failure text is for its operator, not
			// for the organization's members.
			b.logger.Error(nil, "metering failed a billing request", "path", path, "status", resp.StatusCode, "answer", detail)
			return &BillingError{Status: http.StatusBadGateway, Message: "metering could not complete the request; try again later"}
		case answer != "" && resp.StatusCode >= 400 && resp.StatusCode < 500:
			// The deployment's own refusal, for the caller. A 3xx that
			// reached here (a 304, or one the client did not follow) is
			// not a status the route may write, so it takes the default.
			return &BillingError{Status: resp.StatusCode, Message: answer}
		default:
			b.logger.Error(nil, "metering answered a billing request with an error that is not its own", "path", path, "status", resp.StatusCode, "answer", detail)
			return &BillingError{Status: http.StatusBadGateway, Message: "metering did not accept the request"}
		}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return &BillingError{Status: http.StatusBadGateway, Message: "metering answered with something other than JSON"}
		}
	}
	return nil
}
