// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=skills,singular=skill,scope=Cluster
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Skill is a custom guidance package shared by agents in one workspace.
// Presence in the library grants no permissions and does not activate it.
type Skill struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SkillSpec `json:"spec"`
}

// SkillSpec holds the package document and bounded, named text resources.
type SkillSpec struct {
	// Document is a SKILL.md document with name/description frontmatter.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32768
	Document string `json:"document"`
	// Resources are supporting text files relative to this package.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Resources []SkillResource `json:"resources,omitempty"`
}

// SkillResource never names a host path; all reads stay inside its package.
type SkillResource struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=65536
	Content string `json:"content"`
}

// AgentSkillReference opts an agent into one qualified catalog identity.
type AgentSkillReference struct {
	// Name is a qualified identity such as workspace:incident-review.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Name string `json:"name"`
}

// +kubebuilder:object:root=true

// SkillList is a workspace skill package list.
type SkillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Skill `json:"items"`
}
