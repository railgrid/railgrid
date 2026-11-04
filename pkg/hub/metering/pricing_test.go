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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderPricesAreMatchedValidatedAndCached(t *testing.T) {
	b, _ := newBillingFixture(t, true, planObj("team", "Team", "price_team"))
	reads := 0
	price := SubscriptionPrice{ID: "price_team", Amount: "29.5", Currency: "EUR", Interval: "year", IntervalCount: 2}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer operator" {
			t.Error("operator token missing")
		}
		if r.URL.Path == "/billing/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true})
			return
		}
		if r.URL.Path != "/billing/prices" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		reads++
		_ = json.NewEncoder(w).Encode(map[string]any{"prices": map[string]SubscriptionPrice{"price_team": price, "price_other": {ID: "mismatch", Amount: "1", Currency: "USD", Interval: "month", IntervalCount: 1}}})
	}))
	defer srv.Close()
	b.discovery = discovered{PortalURL: srv.URL, token: "operator"}
	for i := 0; i < 2; i++ {
		summary, err := b.Summary(context.Background(), "org-1")
		if err != nil || len(summary.Plans) != 1 || summary.Plans[0].Price == nil || *summary.Plans[0].Price != price {
			t.Fatalf("summary: %+v, %v", summary, err)
		}
	}
	if reads != 1 {
		t.Fatalf("activation polling refetched prices %d times", reads)
	}
	price.Amount = "-1"
	b.pricingUntil = time.Time{}
	summary, err := b.Summary(context.Background(), "org-1")
	if err != nil || summary.Plans[0].Price != nil {
		t.Fatalf("invalid amount reached portal: %+v %v", summary, err)
	}
}

func TestMissingPriceEndpointPreservesPlanAndUsage(t *testing.T) {
	b, _ := newBillingFixture(t, true, planObj("team", "Team", "price_team"))
	summary, err := b.Summary(context.Background(), "org-1")
	if err != nil || len(summary.Plans) != 1 || summary.Plans[0].Price != nil || !summary.Plans[0].Sellable {
		t.Fatalf("older deployment must remain readable without inventing a price: %+v %v", summary, err)
	}
}

func TestCheckoutRejectsChangedReviewedPriceBeforeCreatingSession(t *testing.T) {
	b, dep := newBillingFixture(t, true, planObj("team", "Team", "price_new"))
	_, err := b.CheckoutQuoted(context.Background(), "org-1", "team", "price_old", "https://hub/ok", "https://hub/cancel")
	if err == nil || len(dep.requests) != 0 {
		t.Fatalf("changed price was purchased: %v %#v", err, dep.requests)
	}
	_, err = b.CheckoutQuoted(context.Background(), "org-1", "team", "price_new", "https://hub/ok", "https://hub/cancel")
	if err != nil || len(dep.requests) != 1 || dep.requests[0]["priceID"] != "price_new" {
		t.Fatalf("reviewed price not forwarded: %v %#v", err, dep.requests)
	}
}
