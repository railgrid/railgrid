/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import "testing"

// The placeholder-namespace rewrite is the seam that replaced the fork's
// deploy-to-local-runtime localization: children lose the "default"
// placeholder (kro inherits the instance namespace), while RBAC
// ServiceAccount subjects — where RBAC demands an explicit namespace — are
// rewritten to the instance's own. Getting the subjects half wrong is
// silent until a credential-minting Job is Forbidden at runtime.
func TestStripPlaceholderNamespaces(t *testing.T) {
	resources := []any{
		map[string]any{
			"id": "deploy",
			"template": map[string]any{
				"kind":     "Deployment",
				"metadata": map[string]any{"name": "x", "namespace": "default"},
			},
		},
		map[string]any{
			"id": "binding",
			"template": map[string]any{
				"kind":     "RoleBinding",
				"metadata": map[string]any{"name": "x", "namespace": "default"},
				"subjects": []any{
					map[string]any{"kind": "ServiceAccount", "name": "sa", "namespace": "default"},
					map[string]any{"kind": "User", "name": "alice"},
				},
			},
		},
	}
	if err := stripPlaceholderNamespaces("t", resources); err != nil {
		t.Fatalf("stripPlaceholderNamespaces: %v", err)
	}

	deployMeta := resources[0].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)
	if _, still := deployMeta["namespace"]; still {
		t.Error("child metadata.namespace placeholder was not stripped")
	}
	bindingTemplate := resources[1].(map[string]any)["template"].(map[string]any)
	if _, still := bindingTemplate["metadata"].(map[string]any)["namespace"]; still {
		t.Error("binding metadata.namespace placeholder was not stripped")
	}
	subject := bindingTemplate["subjects"].([]any)[0].(map[string]any)
	if subject["namespace"] != "${schema.metadata.namespace}" {
		t.Errorf("SA subject namespace = %q, want ${schema.metadata.namespace}", subject["namespace"])
	}
	user := bindingTemplate["subjects"].([]any)[1].(map[string]any)
	if _, has := user["namespace"]; has {
		t.Errorf("non-SA subject gained a namespace: %v", user)
	}

	// Foreign namespaces are refused, on children and subjects alike.
	if err := stripPlaceholderNamespaces("t", []any{map[string]any{
		"id":       "bad",
		"template": map[string]any{"kind": "Deployment", "metadata": map[string]any{"namespace": "kube-system"}},
	}}); err == nil {
		t.Error("foreign metadata.namespace accepted")
	}
	if err := stripPlaceholderNamespaces("t", []any{map[string]any{
		"id": "badsubject",
		"template": map[string]any{
			"kind":     "RoleBinding",
			"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "sa", "namespace": "kube-system"}},
		},
	}}); err == nil {
		t.Error("foreign subject namespace accepted")
	}
}
