// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package harnesspolicy

import (
	"testing"

	api "github.com/railgrid/provider-agents/apis/v1alpha1"
)

func TestOnlyEnforcedHarnessCodeReviewGrantIsSupported(t *testing.T) {
	agent := &api.Agent{}
	agent.Spec.Tools.Interactive.Families = []string{"code"}
	agent.Spec.Tools.Background.Families = []string{"code"}
	if reason, message := UnsupportedFields(agent); reason != "" {
		t.Fatalf("scoped review grant rejected: %s %s", reason, message)
	}
	for _, family := range []string{"core", "code-write", "mcp", "web"} {
		agent.Spec.Tools.Background.Families = []string{family}
		if reason, _ := UnsupportedFields(agent); reason == "" {
			t.Fatalf("unsupported native policy accepted: %s", family)
		}
	}
	agent.Spec.Tools.Background.Families = []string{"code"}
	agent.Spec.Tools.Background.RequireApproval = []string{"*"}
	if reason, _ := UnsupportedFields(agent); reason == "" {
		t.Fatal("unenforced native approval grant accepted")
	}
}
