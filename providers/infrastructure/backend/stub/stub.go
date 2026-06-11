/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package stub is a no-op Backend implementation. Used by the
// platform's own tests + by dev-mode runs that don't want to spin up
// the kro backend. Operators registering Templates with
// spec.backend: "stub" will get them marked Ready=true with no
// side effects — useful for exercising the platform plumbing in
// isolation.
package stub

import (
	"context"
	"sync"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	infrastructurev1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/backend"
)

// Name registered with backend.Registry. Operators put this string
// in Template.spec.backend to opt their Template into the no-op flow.
const Name = "stub"

// Backend is the public type — embeddable in tests if a fake needs
// to extend behavior. The exported state lets tests observe what
// SetupTemplate / TeardownTemplate received.
type Backend struct {
	mu sync.Mutex

	// SeenSetups records every Template name passed through
	// SetupTemplate, in arrival order. Reset()-able from tests.
	SeenSetups []string
	// SeenTeardowns records the same for TeardownTemplate.
	SeenTeardowns []string

	// FailSetup, when true, makes SetupTemplate return an error.
	// Lets tests assert the controller's failure-handling path.
	FailSetup bool
	// FailTeardown does the same for the destruction path.
	FailTeardown bool
}

// New constructs an empty stub Backend.
func New() *Backend { return &Backend{} }

// Name returns "stub" so the registry can index this implementation.
func (b *Backend) Name() string { return Name }

// SetupTemplate records the call + optionally fails. Returns Ready=true
// + an empty message when not failing — the platform mirrors that
// onto Template.status.backend.Ready.
func (b *Backend) SetupTemplate(_ context.Context, tmpl *infrastructurev1alpha1.Template) (backend.TemplateStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.SeenSetups = append(b.SeenSetups, tmpl.Name)
	if b.FailSetup {
		return backend.TemplateStatus{Ready: false, Message: "stub: FailSetup=true"}, nil
	}
	return backend.TemplateStatus{Ready: true}, nil
}

// TeardownTemplate records the call + optionally fails.
func (b *Backend) TeardownTemplate(_ context.Context, tmpl *infrastructurev1alpha1.Template) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.SeenTeardowns = append(b.SeenTeardowns, tmpl.Name)
	if b.FailTeardown {
		return errStubTeardown
	}
	return nil
}

// Run blocks until ctx is cancelled. The stub doesn't have a watch
// loop — it logs once on start so tests can confirm Run was called
// and then sits idle.
func (b *Backend) Run(ctx context.Context, _ *rest.Config) error {
	klog.FromContext(ctx).WithName("backend.stub").Info("stub backend running (no-op)")
	<-ctx.Done()
	return nil
}

// Reset clears the seen-call state. Safe to call from a test cleanup
// hook so the next subtest starts from a clean slate.
func (b *Backend) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.SeenSetups = nil
	b.SeenTeardowns = nil
}

// errStubTeardown is a sentinel so tests can match on the exact
// error via errors.Is. Kept private — outside callers shouldn't
// depend on the stub's failure signaling.
var errStubTeardown = stubError("stub: FailTeardown=true")

type stubError string

func (e stubError) Error() string { return string(e) }
