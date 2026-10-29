/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/railgrid/provider-sdk/usage"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// usageMeters are the resource counters this provider reports to metering
// (provider-sdk/usage): the number of Projects in a workspace. One UsageRecord per tenant workspace
// and window is written into the provider workspace once the hub has bound
// the metering-provider export there; nothing is emitted while it is not.
func usageMeters() []usage.Meter {
	return []usage.Meter{
		{Name: "projects", Kinds: []client.Object{&aiv1alpha1.Project{}}},
	}
}

// setupUsage registers the emitters on the leader-elected manager.
func setupUsage(mgr mcmanager.Manager, provider *rest.Config) error {
	return usage.Setup(mgr, usage.Options{Meters: usageMeters(), Provider: provider, Logger: ctrl.Log.WithName("usage")})
}
