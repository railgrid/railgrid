/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scheme builds the runtime.Scheme the code provider's controller
// manager and clients share: the provider's own code.kedge.faros.sh types
// plus core/v1 (the controllers read/write Secrets in tenant workspaces) and
// the kcp apis.kcp.io types the multicluster apiexport provider needs.
package scheme

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
)

// NewScheme returns a fully-populated scheme. Panics on registration error
// (a programming mistake, not a runtime condition) — same convention as the
// hub's NewScheme.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(corev1alpha1.AddToScheme(s))
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(s))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(s))
	utilruntime.Must(codev1alpha1.AddToScheme(s))
	return s
}
