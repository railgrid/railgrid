/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package previewtoken

import (
	"net/url"
	"testing"
	"time"
)

func TestSignerSignsAndVerifiesHostBoundPayload(t *testing.T) {
	signer := NewSigner([]byte("test-secret"))
	payload := Payload{
		ProjectName:        "todo",
		TenantPath:         "root:org:workspace",
		ResourceName:       "kedge-sandbox-abc123",
		Subject:            "user@example.com",
		Host:               "preview.example.com",
		RuntimeNamespace:   "runtime-ns",
		PreviewServiceName: "runtime-svc",
		PreviewPortName:    "preview",
		AccessMode:         "private",
	}
	token, expiresAt, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	if token == "." {
		t.Fatal("token is malformed")
	}
	decoded, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if decoded.Host != payload.Host {
		t.Fatalf("Host = %q, want %q", decoded.Host, payload.Host)
	}
	if decoded.ProjectName != payload.ProjectName {
		t.Fatalf("ProjectName = %q, want %q", decoded.ProjectName, payload.ProjectName)
	}
	if decoded.TenantPath != payload.TenantPath {
		t.Fatalf("TenantPath = %q, want %q", decoded.TenantPath, payload.TenantPath)
	}
	if decoded.ResourceName != payload.ResourceName {
		t.Fatalf("ResourceName = %q, want %q", decoded.ResourceName, payload.ResourceName)
	}
	if decoded.RuntimeNamespace != payload.RuntimeNamespace {
		t.Fatalf("RuntimeNamespace = %q, want %q", decoded.RuntimeNamespace, payload.RuntimeNamespace)
	}
	if decoded.PreviewServiceName != payload.PreviewServiceName {
		t.Fatalf("PreviewServiceName = %q, want %q", decoded.PreviewServiceName, payload.PreviewServiceName)
	}
	if decoded.PreviewPortName != payload.PreviewPortName {
		t.Fatalf("PreviewPortName = %q, want %q", decoded.PreviewPortName, payload.PreviewPortName)
	}
	if decoded.ExpiresAt == 0 {
		t.Fatal("ExpiresAt is empty")
	}
	if decoded.ExpiresAt != expiresAt.Unix() {
		t.Fatalf("ExpiresAt = %d, want %d", decoded.ExpiresAt, expiresAt.Unix())
	}
}

func TestSignerRejectsExpiredPayload(t *testing.T) {
	signer := NewSigner([]byte("test-secret"))
	token, _, err := signer.Sign(Payload{
		ProjectName:        "todo",
		TenantPath:         "root:org:workspace",
		ResourceName:       "kedge-sandbox-abc123",
		Host:               "preview.example.com",
		RuntimeNamespace:   "runtime-ns",
		PreviewServiceName: "runtime-svc",
		PreviewPortName:    "preview",
		AccessMode:         "private",
		ExpiresAt:          time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}
	_, err = signer.Verify(token)
	if err == nil {
		t.Fatal("Verify returned nil, want expired error")
	}
}

func TestNormalizeHostTrimsSchemeAndPath(t *testing.T) {
	normalized := NormalizeHost("https://preview.example.com/my-app?token=abc")
	if want, got := "preview.example.com", normalized; got != want {
		t.Fatalf("NormalizeHost = %q, want %q", got, want)
	}
	u, _ := url.Parse("https://preview.example.com")
	if want, got := "preview.example.com", NormalizeHost(u.String()); got != want {
		t.Fatalf("NormalizeHost(URL) = %q, want %q", got, want)
	}
}

func TestCookieNameForHostStable(t *testing.T) {
	one := CookieNameForHost("preview.example.com")
	two := CookieNameForHost("https://preview.example.com")
	if one == "" || one != two {
		t.Fatalf("CookieNameForHost = %q and %q, want identical non-empty names", one, two)
	}
}
