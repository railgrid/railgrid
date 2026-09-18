// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package scheme builds the runtime.Scheme the agents provider's controller
// manager shares with its reconcilers: the provider's own agents.railgrid.ai
// types plus core/v1 (the reconcilers read and write the per-connection
// credential Secrets in tenant workspaces through the APIExport claim) and the
// kcp apis.kcp.io types the multicluster apiexport provider needs.
package scheme

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// NewScheme returns a fully-populated scheme. Panics on registration error
// (a programming mistake, not a runtime condition) — same convention as the
// code provider and the hub.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(corev1alpha1.AddToScheme(s))
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(s))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(s))
	utilruntime.Must(agentsv1alpha1.AddToScheme(s))
	return s
}
