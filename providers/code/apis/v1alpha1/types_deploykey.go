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
)

// DeployKey grants SSH access to a single Repository. It is the cross-provider
// seam: when spec.publicKey is empty the controller generates a keypair,
// registers the public half on the repository, and writes the private half to
// a Secret in the tenant workspace (status.secretRef, owned by this CR so it
// is garbage-collected on delete). Another provider (e.g. infrastructure) can
// then mount that Secret to clone/push.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gkey
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="ReadOnly",type=boolean,JSONPath=`.spec.readOnly`
// +kubebuilder:printcolumn:name="KeyID",type=string,JSONPath=`.status.keyID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DeployKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeployKeySpec   `json:"spec"`
	Status DeployKeyStatus `json:"status,omitempty"`
}

// DeployKeyList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type DeployKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeployKey `json:"items"`
}

// DeployKeySpec is the desired state.
type DeployKeySpec struct {
	// RepositoryRef names the Repository (same workspace) this key is
	// installed on.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// Title is the human-readable label shown on the host. Empty uses
	// metadata.name.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Title string `json:"title,omitempty"`

	// PublicKey is an OpenSSH-format public key to register (bring your
	// own). When empty the controller generates an ed25519 keypair and
	// writes the private half to status.secretRef.
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	PublicKey string `json:"publicKey,omitempty"`

	// ReadOnly registers the key without write (push) access.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// DeployKeyStatus is the observed state.
type DeployKeyStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// KeyID is the host-side id of the registered key.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	KeyID string `json:"keyID,omitempty"`

	// SecretRef points at the Secret holding the generated private key
	// (key "ssh-privatekey"). Only set when the controller generated the
	// keypair (spec.publicKey was empty). The Secret is owned by this CR.
	// +optional
	SecretRef *LocalSecretReference `json:"secretRef,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// FinalizerDeployKey is added by the DeployKeyController so the host-side
// key is removed before the CR disappears.
const FinalizerDeployKey = "deploykeys.code.kedge.faros.sh/finalizer"
