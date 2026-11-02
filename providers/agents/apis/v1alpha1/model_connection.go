// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import "fmt"

// ResolveModelConnection overlays a model binding on its connection. The caller
// reads both objects in the same tenant workspace, using the same identity.
// Only the binding's model is retained; credentials and transport always come
// from the connection, so key rotation and endpoint edits have one authority.
func ResolveModelConnection(model *ModelCredential, connection *ModelProviderConnection) (*ModelCredential, error) {
	if model == nil || connection == nil || model.Spec.ConnectionRef == "" || model.Spec.Model == "" || model.Spec.ConnectionRef != connection.Name {
		return nil, fmt.Errorf("invalid provider connection reference")
	}
	if IsHarnessProvider(connection.Spec.Provider) || !connection.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("provider connection must be a direct, active API connection")
	}
	resolved := model.DeepCopy()
	resolved.Spec = connection.CredentialSpec()
	resolved.Spec.Model = model.Spec.Model
	resolved.Spec.ConnectionRef = connection.Name
	return resolved, nil
}
