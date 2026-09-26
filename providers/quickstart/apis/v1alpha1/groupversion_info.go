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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// GroupName is the API group the quickstart APIExport serves, and what
	// manifest.yaml spec.export.resources[].apiVersion names: an export name is
	// not an API group, but here the export is quickstart.providers.railgrid.ai
	// and so is the group it serves.
	GroupName = "quickstart.providers.railgrid.ai"
	// Version is the served + storage version.
	Version = "v1alpha1"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme
)

// Resource maps a plural resource name to its schema.GroupResource, so callers
// do not repeat the GroupVersion literal.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// GreetingsResource is the GVR the data-plane verb and the portal address. The
// resource segment is the plural, never the Kind.
var GreetingsResource = SchemeGroupVersion.WithResource("greetings")

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&Greeting{},
		&GreetingList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
