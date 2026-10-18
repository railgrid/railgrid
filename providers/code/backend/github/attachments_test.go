// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

func TestUploadAttachmentWireContract(t *testing.T) {
	image := backend.Attachment{Path: "artifacts/screenshots/mobile.png", MediaType: "image/png", Data: []byte("image bytes")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/user-attachments/assets" || r.URL.Query().Get("repository_id") != "123" || r.URL.Query().Get("name") != "mobile.png" || r.URL.Query().Get("content_type") != "image/png" {
			t.Errorf("unexpected upload: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("missing upload headers")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != "image bytes" {
			t.Errorf("bad image: %v", err)
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"url":"https://github.com/user-attachments/assets/test"}`)
	}))
	defer server.Close()
	got, err := uploadAttachment(t.Context(), "test-token", 123, image, server.URL)
	if err != nil || got != "https://github.com/user-attachments/assets/test" {
		t.Fatalf("upload: %s %v", got, err)
	}
}
func TestUploadAttachmentRefusesRedirectAndInvalidURL(t *testing.T) {
	for _, mode := range []string{"redirect", "invalid-url", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, "https://example.invalid/collect", http.StatusTemporaryRedirect)
				case "forbidden":
					w.WriteHeader(403)
				default:
					_, _ = io.WriteString(w, `{"url":"https://example.invalid/image.png"}`)
				}
			}))
			defer server.Close()
			if _, err := uploadAttachment(t.Context(), "secret", 123, backend.Attachment{}, server.URL); err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
}
func TestInstallationTokenCannotUpload(t *testing.T) {
	a := backend.Attachment{Path: "artifacts/screenshots/a.png", MediaType: "image/png", Data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)}
	_, err := New().UploadAttachment(t.Context(), &api.Connection{}, backend.Credential{Token: "ghs_installation"}, &api.Repository{}, a)
	if err == nil || !strings.Contains(err.Error(), "installation tokens") {
		t.Fatalf("got %v", err)
	}
}

func TestAttachmentRateLimitRemainsRetryable(t *testing.T) {
	for _, limited := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limited {
				w.Header().Set("Retry-After", "60")
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		_, err := uploadAttachment(t.Context(), "test", 1, backend.Attachment{}, server.URL)
		server.Close()
		if err == nil || errors.Is(err, backend.ErrAttachmentsUnsupported) == limited {
			t.Fatalf("wrong retry classification: limited=%v err=%v", limited, err)
		}
	}
}
