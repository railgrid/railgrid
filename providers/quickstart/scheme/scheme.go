/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scheme builds the runtime.Scheme the quickstart provider's
// controller manager and its clients share: this provider's own
// quickstart.providers.railgrid.ai types, the built-in Kubernetes types, and
// the kcp apis.kcp.io types provider-sdk/apiexportprovider needs to read the
// APIExportEndpointSlice it watches.
package scheme

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	quickstartv1alpha1 "github.com/railgrid/provider-quickstart/apis/v1alpha1"
)

// NewScheme returns a fully-populated scheme. It panics on a registration
// error, which is a programming mistake rather than a runtime condition.
func NewScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(corev1alpha1.AddToScheme(s))
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(s))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(s))
	utilruntime.Must(quickstartv1alpha1.AddToScheme(s))
	return s
}
