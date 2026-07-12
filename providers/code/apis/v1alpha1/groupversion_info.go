/*
Copyright 2026 The Faros Authors.

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
	// GroupName is the API group for code-provider types. Tenant-authored
	// desired-state resources and observed helper resources share this group.
	GroupName = "code.kedge.faros.sh"
	// Version pins the served + storage version. Bumping to v1 will
	// require a conversion plan — keep all in-tree changes additive
	// until then.
	Version = "v1alpha1"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme
)

// Resource maps a string name (e.g. "repositories") to its
// schema.GroupResource so package callers don't have to repeat the
// GroupVersion literal.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&Connection{},
		&ConnectionList{},
		&Repository{},
		&RepositoryList{},
		&RepositoryCommit{},
		&RepositoryCommitList{},
		&RepositoryCheckout{},
		&RepositoryCheckoutList{},
		&RepositoryBuildStatus{},
		&RepositoryBuildStatusList{},
		&DeployKey{},
		&DeployKeyList{},
		&Collaborator{},
		&CollaboratorList{},
		&Package{},
		&PackageList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
