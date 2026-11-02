// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package restapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

func TestAcceptedClaimDTOPreservesEffectiveVerbsAndSelector(t *testing.T) {
	scope := kcp.AcceptedClaimScope{Group: "code.railgrid.ai", Resource: "repositories", Verbs: []string{"get"}, Selector: kcp.AcceptedClaimSelector{MatchLabels: map[string]string{"owner": "previously-approved"}}}
	dto := claimStateOf(kcp.BindingClaimState{Accepted: []kcp.AcceptedClaimScope{scope}})
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"accepted": []any{map[string]any{"group": "code.railgrid.ai", "resource": "repositories", "verbs": []any{"get"}, "selector": map[string]any{"matchLabels": map[string]any{"owner": "previously-approved"}}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted DTO=%s", raw)
	}
}
