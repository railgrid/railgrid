/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package repositorycheckout

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	codescheme "github.com/faroshq/provider-code/scheme"
)

func TestFailRecordsTerminalStatus(t *testing.T) {
	ctx := context.Background()
	checkout := &codev1alpha1.RepositoryCheckout{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-checkout"},
		Spec:       codev1alpha1.RepositoryCheckoutSpec{RepositoryRef: "demo"},
	}
	c := fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCheckout{}).
		WithObjects(checkout).
		Build()

	r := &Reconciler{}
	if err := r.fail(ctx, c, checkout, "git provider \"stub\" does not support reading files"); err != nil {
		t.Fatalf("fail returned error: %v", err)
	}

	var got codev1alpha1.RepositoryCheckout
	if err := c.Get(ctx, client.ObjectKey{Name: "demo-checkout"}, &got); err != nil {
		t.Fatalf("get checkout: %v", err)
	}
	if got.Status.Phase != codev1alpha1.RepositoryCheckoutPhaseFailed {
		t.Errorf("phase = %s, want Failed", got.Status.Phase)
	}
	if got.Status.CompletedAt == nil || got.Status.StartedAt == nil {
		t.Error("terminal status is missing timestamps")
	}
	var ready *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == codev1alpha1.ConditionReady {
			ready = &got.Status.Conditions[i]
		}
	}
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Message == "" {
		t.Errorf("Ready condition = %+v, want False with a message", ready)
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(codev1alpha1.RepositoryCheckoutPhaseRunning) || isTerminal("") {
		t.Error("non-terminal phases reported terminal")
	}
	if !isTerminal(codev1alpha1.RepositoryCheckoutPhaseSucceeded) || !isTerminal(codev1alpha1.RepositoryCheckoutPhaseFailed) {
		t.Error("terminal phases not reported terminal")
	}
}
