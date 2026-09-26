// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import "k8s.io/apimachinery/pkg/runtime/schema"

// The two kinds an action may be bound to. The gate reads the addressed one
// as the provider, through its export virtual workspace
// (dataplane.ProviderCallerFactory.AsProvider), which is the one door where
// the provider has standing in a tenant workspace: there is no separate
// "authority" lookup any more, because that read IS the provider's own view.
var repositories = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositories"}
var connections = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "connections"}
