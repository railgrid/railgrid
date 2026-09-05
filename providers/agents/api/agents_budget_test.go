// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

func TestNormalizeAgentBudget(t *testing.T) {
	int64Ptr := func(value int64) *int64 { return &value }
	stringPtr := func(value string) *string { return &value }

	tests := []struct {
		name       string
		current    *agentsv1alpha1.AgentBudget
		tokenLimit *int64
		usdLimit   *string
		want       *agentsv1alpha1.AgentBudget
		wantErr    bool
	}{
		{name: "absent budget stays unlimited"},
		{name: "zero and blank are unlimited", tokenLimit: int64Ptr(0), usdLimit: stringPtr("  ")},
		{name: "zero decimal is unlimited", usdLimit: stringPtr(" 0.00 ")},
		{
			name:       "positive limits use monthly default",
			tokenLimit: int64Ptr(100),
			usdLimit:   stringPtr(" 1.25 "),
			want:       &agentsv1alpha1.AgentBudget{Window: "month", TokenLimit: 100, USDLimit: "1.25"},
		},
		{
			name:       "partial token update preserves usd and window",
			current:    &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 100, USDLimit: "2.50"},
			tokenLimit: int64Ptr(0),
			want:       &agentsv1alpha1.AgentBudget{Window: "day", USDLimit: "2.50"},
		},
		{
			name:     "partial blank usd removes only usd cap",
			current:  &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 100, USDLimit: "2.50"},
			usdLimit: stringPtr(""),
			want:     &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 100},
		},
		{
			name:       "clearing both caps removes budget",
			current:    &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 100, USDLimit: "2.50"},
			tokenLimit: int64Ptr(0),
			usdLimit:   stringPtr("0"),
		},
		{name: "negative tokens rejected", tokenLimit: int64Ptr(-1), wantErr: true},
		{name: "malformed usd rejected", usdLimit: stringPtr("twelve"), wantErr: true},
		{name: "hex usd rejected", usdLimit: stringPtr("0x10"), wantErr: true},
		{name: "negative usd rejected", usdLimit: stringPtr("-0.01"), wantErr: true},
		{name: "nan usd rejected", usdLimit: stringPtr("NaN"), wantErr: true},
		{name: "positive infinity rejected", usdLimit: stringPtr("+Inf"), wantErr: true},
		{name: "negative infinity rejected", usdLimit: stringPtr("-Inf"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAgentBudget(tt.current, tt.tokenLimit, tt.usdLimit)
			if tt.wantErr {
				if err == nil {
					t.Fatal("normalizeAgentBudget() error = nil, want validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAgentBudget() unexpected error: %v", err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("normalizeAgentBudget() = %#v, want nil", got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Fatalf("normalizeAgentBudget() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAgentBudgetMutationValidationPrecedesClientAccess(t *testing.T) {
	server := &Server{}
	negative := int64(-1)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "create",
			call: func() error {
				_, err := server.applyAgentCreate(context.Background(), nil, &createAgentRequest{
					Name:      "test-agent",
					BudgetUSD: "NaN",
				})
				return err
			},
		},
		{
			name: "update",
			call: func() error {
				_, err := server.applyAgentUpdate(context.Background(), nil, "test-agent", &updateAgentRequest{
					BudgetTokens: &negative,
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			requestErr, ok := err.(*requestError)
			if !ok {
				t.Fatalf("error = %T %v, want *requestError", err, err)
			}
			if requestErr.code != http.StatusBadRequest || requestErr.reason != "BadRequest" {
				t.Fatalf("request error = %#v, want HTTP 400 BadRequest", requestErr)
			}

			recorder := httptest.NewRecorder()
			writeUpdateError(recorder, err)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("HTTP status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}
