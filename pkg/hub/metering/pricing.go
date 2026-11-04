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
	"io"
	"net/http"
	"regexp"
	"time"
)

// SubscriptionPrice is supplied by the metering billing provider, never by a
// hub-authored price label. Amount is a decimal in major currency units.
type SubscriptionPrice struct {
	ID            string `json:"id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Interval      string `json:"interval"`
	IntervalCount int64  `json:"intervalCount"`
	TaxBehavior   string `json:"taxBehavior,omitempty"`
}

var priceAmount = regexp.MustCompile(`^\d{1,16}(\.\d{1,15})?$`)
var priceCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func validSubscriptionPrice(price SubscriptionPrice) bool {
	if price.ID == "" || !priceAmount.MatchString(price.Amount) || !priceCurrency.MatchString(price.Currency) || price.IntervalCount < 1 || price.IntervalCount > 365 {
		return false
	}
	switch price.Interval {
	case "day", "week", "month", "year":
		return true
	}
	return false
}

func (b *Billing) subscriptionPrices(ctx context.Context, endpoint Endpoints) map[string]SubscriptionPrice {
	// Stripe Price IDs have immutable amounts. Cache briefly so polling for
	// activation does not repeatedly query the payment provider. Never reuse
	// prices after the discovered deployment or its credential changes.
	b.pricingMu.Lock()
	defer b.pricingMu.Unlock()
	key := endpoint.PortalURL + "\x00" + endpoint.token
	if key == b.pricingKey && time.Now().Before(b.pricingUntil) {
		return b.pricing
	}
	if endpoint.token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.PortalURL+"/billing/prices", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+endpoint.token)
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		b.logger.V(2).Info("billing prices unavailable", "error", err.Error())
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var answer struct {
		Prices map[string]SubscriptionPrice `json:"prices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&answer); err != nil {
		return nil
	}
	prices := map[string]SubscriptionPrice{}
	for id, price := range answer.Prices {
		if id == price.ID && validSubscriptionPrice(price) {
			prices[id] = price
		}
	}
	b.pricing, b.pricingKey, b.pricingUntil = prices, key, time.Now().Add(time.Minute)
	return prices
}
