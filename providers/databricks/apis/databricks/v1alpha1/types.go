// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type LocalSecretReference struct {
	// Name is the Secret name in the tenant workspace.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the Secret namespace in the tenant workspace. Defaults to default.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key is the optional key holding credential material. Defaults depend on
	// the auth type.
	// +optional
	Key string `json:"key,omitempty"`
}

// +kubebuilder:validation:Enum=pat
type ConnectionAuthType string

const (
	ConnectionAuthPAT ConnectionAuthType = "pat"
)

// Connection configures a Databricks workspace for one kedge tenant. It points
// at tenant-owned Secrets for auth material; credentials never live on the CR.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=dbxconn
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
// +kubebuilder:printcolumn:name="Auth",type=string,JSONPath=`.spec.authType`
// +kubebuilder:printcolumn:name="Validated",type=string,JSONPath=`.status.conditions[?(@.type=="Validated")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec"`
	Status ConnectionStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}

type ConnectionSpec struct {
	// Host is the Databricks workspace host, e.g. https://dbc-xyz.cloud.databricks.com.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https://[A-Za-z0-9.-]+(:[0-9]+)?/?$`
	Host string `json:"host"`

	// AuthType selects the credential model. PAT is the only supported model.
	// +required
	AuthType ConnectionAuthType `json:"authType"`

	// SecretRef points at tenant workspace credential/federation config.
	// +required
	SecretRef LocalSecretReference `json:"secretRef"`
}

type ConnectionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	WorkspaceID string `json:"workspaceID,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Warehouse binds a tenant-visible kedge handle to a Databricks SQL warehouse.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=dbxwh
// +kubebuilder:printcolumn:name="Connection",type=string,JSONPath=`.spec.connectionRef`
// +kubebuilder:printcolumn:name="Warehouse",type=string,JSONPath=`.spec.warehouseID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Warehouse struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WarehouseSpec   `json:"spec"`
	Status WarehouseStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WarehouseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Warehouse `json:"items"`
}

type WarehouseSpec struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	ConnectionRef string `json:"connectionRef"`
	// +required
	// +kubebuilder:validation:MinLength=1
	WarehouseID string `json:"warehouseID"`
}

type WarehouseStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Table imports a Databricks table into kedge as a stable resource handle. It
// is a governed pointer plus cached schema, not a copy of table data.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=dbxtbl
// +kubebuilder:printcolumn:name="Table",type=string,JSONPath=`.spec.table`
// +kubebuilder:printcolumn:name="Catalog",type=string,JSONPath=`.spec.catalog`
// +kubebuilder:printcolumn:name="Schema",type=string,JSONPath=`.spec.schema`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Table struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TableSpec   `json:"spec"`
	Status TableStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type TableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Table `json:"items"`
}

type TableSpec struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	ConnectionRef string `json:"connectionRef"`
	// +required
	// +kubebuilder:validation:MinLength=1
	WarehouseRef string `json:"warehouseRef"`
	// +required
	// +kubebuilder:validation:MinLength=1
	Catalog string `json:"catalog"`
	// +required
	// +kubebuilder:validation:MinLength=1
	Schema string `json:"schema"`
	// +required
	// +kubebuilder:validation:MinLength=1
	Table string `json:"table"`
}

type TableStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	RefreshedAt *metav1.Time `json:"refreshedAt,omitempty"`
	// Columns caches schema for App Studio authoring. It never stores row data.
	// +optional
	// +listType=map
	// +listMapKey=name
	Columns []Column `json:"columns,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type Column struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// +optional
	Nullable bool `json:"nullable,omitempty"`
	// +optional
	Comment string `json:"comment,omitempty"`
}

const (
	ConditionValidated       = "Validated"
	ConditionFederationReady = "FederationReady"
	ConditionReady           = "Ready"
)
