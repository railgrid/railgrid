// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package modelcatalog

import "testing"

func TestCurrentRecommendationsAndStandardRates(t *testing.T) {
	for _, tc := range []struct {
		id            string
		input, output float64
	}{
		{"gpt-6-astra", 10, 50}, {"gpt-6.1-sol", 2, 10}, {"gpt-6.1-terra", 1, 5}, {"gpt-6-luna", .1, .5},
		{"claude-fable-5-1", 10, 50}, {"claude-opus-5-5", 4, 20}, {"claude-sonnet-5-5", 2, 10}, {"claude-haiku-5-5", .1, .5},
	} {
		entry, ok := LookupModel(tc.id)
		if !ok || !entry.Recommended || entry.InputPer1M != tc.input || entry.OutputPer1M != tc.output || entry.PricingCheckedAt == "" || entry.PricingSource == "" {
			t.Errorf("%s: %#v", tc.id, entry)
		}
		if got := CostMicros(tc.id, 1000, 1000); got != int64((tc.input+tc.output)*1000) {
			t.Errorf("%s cost: %d", tc.id, got)
		}
	}
	for _, id := range []string{"gpt-4o", "gpt-5.4", "claude-sonnet-4", "claude-opus-4-5"} {
		if _, ok := LookupModel(id); !ok || IsRecommended(id) {
			t.Errorf("legacy pricing must remain available without recommendation: %s", id)
		}
	}
}
func TestModelVariantsDoNotInheritPricesOrRecommendations(t *testing.T) {
	for _, id := range []string{"gpt-5.99", "gpt-6.1-sol-custom", "claude-opus-5-50", "claude-opus-4-99"} {
		if _, ok := LookupModel(id); ok {
			t.Errorf("unknown variant received a price: %s", id)
		}
	}
	for _, id := range []string{"openai/gpt-6.1-sol", "gpt-6.1-sol-2026-10-01", "anthropic/claude-sonnet-5-5-20261001"} {
		if !IsRecommended(id) {
			t.Errorf("expected normalized recommendation: %s", id)
		}
	}
}

func TestHistoricalModelCostsRemainAvailable(t *testing.T) {
	for _, tc := range []struct {
		id            string
		input, output float64
	}{
		{"gpt-5.2", 1.75, 14}, {"gpt-5-chat-latest", 1.25, 10},
		{"claude-sonnet-4-5-latest", 3, 15}, {"claude-3-5-haiku-latest", .8, 4},
		{"gpt-4o-mini-search-preview", .15, .6}, {"o3-pro", 20, 80},
	} {
		if got := CostMicros(tc.id, 1000000, 1000000); got != int64((tc.input+tc.output)*1000000) {
			t.Errorf("%s cost = %d", tc.id, got)
		}
		if IsRecommended(tc.id) {
			t.Errorf("historical model recommended: %s", tc.id)
		}
	}
}

func TestRecommendationsHavePricingProvenance(t *testing.T) {
	for _, entry := range Catalog() {
		if entry.Recommended && (entry.PricingSource == "" || entry.PricingCheckedAt == "") {
			t.Errorf("recommendation missing pricing provenance: %s", entry.ID)
		}
	}
}
