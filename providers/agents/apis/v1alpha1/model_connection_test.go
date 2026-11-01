// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSharedConnectionResolution(t *testing.T) {
	connection := &ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "account"}, Spec: ModelCredentialSpec{
		Provider: ModelProviderOpenAI, BaseURL: "https://first.example/v1", SecretRef: ModelCredentialSecretRef{Name: "shared-key"},
	}}
	first := &ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "first"}, Spec: ModelCredentialSpec{ConnectionRef: "account", Model: "first-model"}}
	second := &ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "second"}, Spec: ModelCredentialSpec{ConnectionRef: "account", Model: "second-model"}}
	for _, model := range []*ModelCredential{first, second} {
		resolved, err := ResolveModelConnection(model, connection)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Spec.Model != model.Spec.Model || resolved.Spec.SecretRef.Name != "shared-key" || resolved.Spec.Provider != ModelProviderOpenAI {
			t.Fatalf("wrong resolved spec: %+v", resolved.Spec)
		}
	}
	connection.Spec.BaseURL = "https://rotated.example/v1"
	connection.Spec.SecretRef.Name = "rotated-key"
	resolved, err := ResolveModelConnection(first, connection)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Spec.BaseURL != connection.Spec.BaseURL || resolved.Spec.SecretRef.Name != "rotated-key" {
		t.Fatal("model did not follow its connection")
	}
	if first.Spec.SecretRef.Name != "" || first.Spec.BaseURL != "" {
		t.Fatal("resolution mutated the stored model")
	}
}

func TestSharedConnectionRejectsInvalidReferences(t *testing.T) {
	for _, name := range []string{"missing", "self", "chain", "harness", "terminating", "empty-model"} {
		t.Run(name, func(t *testing.T) {
			model := &ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "model"}, Spec: ModelCredentialSpec{ConnectionRef: "account", Model: "model-id"}}
			connection := &ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "account"}}
			switch name {
			case "missing":
				connection = nil
			case "empty-model":
				model.Spec.Model = ""
			case "self":
				model.Name = "account"
			case "chain":
				connection.Spec.ConnectionRef = "another-account"
			case "harness":
				connection.Spec.Provider = ModelProviderCodex
			case "terminating":
				now := metav1.Now()
				connection.DeletionTimestamp = &now
			}
			if _, err := ResolveModelConnection(model, connection); err == nil {
				t.Fatal("accepted an invalid connection")
			}
		})
	}
}
