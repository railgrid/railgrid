// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package resulttypes

import (
	"fmt"
	"strings"
)

const (
	MaxAgentArtifactSources       = 32
	MaxAgentArtifactSourceActions = 8
)

// SourceObject is the exact tenant object a tool says it read. UID makes a
// delete-and-recreate at the same name a different source.
type SourceObject struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// SourceAccess is one published API action needed to use the source.
type SourceAccess struct {
	Group       string `json:"group,omitempty"`
	Version     string `json:"version"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name,omitempty"`
	Verb        string `json:"verb"`
}

// SourceReference is tool-authored provenance. It is never authority itself:
// a source-backed result reader rechecks every action and exact object UID
// against the current caller before returning captured bytes.
type SourceReference struct {
	Label  string         `json:"label,omitempty"`
	Object SourceObject   `json:"object"`
	Access []SourceAccess `json:"access"`
}

func (s SourceReference) Validate() error {
	if len(s.Label) > 512 {
		return fmt.Errorf("source label exceeds 512 bytes")
	}
	if len(s.Access) == 0 || len(s.Access) > MaxAgentArtifactSourceActions {
		return fmt.Errorf("source needs between one and %d required actions", MaxAgentArtifactSourceActions)
	}
	if strings.TrimSpace(s.Object.Version) == "" || strings.TrimSpace(s.Object.Resource) == "" ||
		strings.TrimSpace(s.Object.Name) == "" || strings.TrimSpace(s.Object.UID) == "" {
		return fmt.Errorf("source object needs version, resource, name and UID")
	}
	hasObjectGet := false
	for i, access := range s.Access {
		if strings.TrimSpace(access.Version) == "" || strings.TrimSpace(access.Resource) == "" || strings.TrimSpace(access.Name) == "" || strings.TrimSpace(access.Verb) == "" {
			return fmt.Errorf("source action %d needs version, resource, name and verb", i)
		}
		if len(access.Group) > 253 || len(access.Version) > 63 || len(access.Resource) > 253 || len(access.Subresource) > 253 ||
			len(access.Namespace) > 253 || len(access.Name) > 253 || len(access.Verb) > 32 {
			return fmt.Errorf("source action %d has an oversized permission coordinate", i)
		}
		if access.Group == s.Object.Group && access.Version == s.Object.Version && access.Resource == s.Object.Resource &&
			access.Namespace == s.Object.Namespace && access.Name == s.Object.Name && access.Verb == "get" && access.Subresource == "" {
			hasObjectGet = true
		}
	}
	if len(s.Object.Group) > 253 || len(s.Object.Version) > 63 || len(s.Object.Resource) > 253 || len(s.Object.Name) > 253 ||
		len(s.Object.Namespace) > 253 || len(s.Object.UID) > 128 {
		return fmt.Errorf("source object has an oversized identity coordinate")
	}
	if !hasObjectGet {
		return fmt.Errorf("source reference must require GET on its exact object")
	}
	return nil
}
