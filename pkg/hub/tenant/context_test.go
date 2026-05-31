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

package tenant

import (
	"context"
	"testing"
)

func TestWithContext_RoundTrip(t *testing.T) {
	tc := TenantContext{
		User:          "alice",
		OrgUUID:       "7f3a91d2",
		WorkspaceUUID: "9c4b8e1f",
		Role:          "admin",
	}
	ctx := WithContext(context.Background(), tc)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext returned ok=false after WithContext")
	}
	if got != tc {
		t.Errorf("FromContext: got %#v, want %#v", got, tc)
	}
}

func TestFromContext_Absent(t *testing.T) {
	got, ok := FromContext(context.Background())
	if ok {
		t.Errorf("FromContext on a bare ctx should return ok=false, got %#v", got)
	}
	if got != (TenantContext{}) {
		t.Errorf("FromContext on a bare ctx should return zero value, got %#v", got)
	}
}

// TestContextKey_Isolated ensures the context key is private to this
// package: a value stored under a foreign key with the same content
// shape doesn't satisfy FromContext, and vice-versa.
func TestContextKey_Isolated(t *testing.T) {
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, TenantContext{User: "alice"})

	if _, ok := FromContext(ctx); ok {
		t.Errorf("FromContext should not return a TenantContext stored under a foreign key")
	}
}
