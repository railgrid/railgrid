// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"testing"
)

type otherKey struct{}

func TestWithoutProxiedIdentityHidesOnlyTheCaller(t *testing.T) {
	ctx := context.WithValue(context.Background(), otherKey{}, "kept")
	ctx = WithProxiedIdentity(ctx, ProxiedIdentity{User: "alice"})
	if id, ok := ProxiedIdentityFrom(ctx); !ok || id.User != "alice" {
		t.Fatalf("stamped caller = %+v, %v", id, ok)
	}

	detached := WithoutProxiedIdentity(ctx)
	if id, ok := ProxiedIdentityFrom(detached); ok {
		t.Fatalf("detached context still reports caller %+v", id)
	}
	if detached.Value(otherKey{}) != "kept" {
		t.Fatal("detaching the caller dropped another context value")
	}
	if _, ok := ProxiedIdentityFrom(ctx); !ok {
		t.Fatal("detaching changed the original context")
	}

	plain := context.Background()
	if WithoutProxiedIdentity(plain) != plain {
		t.Fatal("a context with no caller should come back unchanged")
	}
}
