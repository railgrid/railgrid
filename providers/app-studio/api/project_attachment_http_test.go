// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantAttachmentHTTPReceiptDownloadListAndOwnerDelete(t *testing.T) {
	project := publishingTestProject("demo", "project-uid", "")
	client := asclient.NewFromDynamic(publishingTestDynamic(project))
	server := NewWithWorkspace(nil, store.NewMemoryStore(), nil, "", false)
	server.projectClientFor = func(identity) (*asclient.Client, error) { return client, nil }
	router := mux.NewRouter()
	server.Register(router)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "screen.png")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x01}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/attachments", &body)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	upload.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	upload.Header.Set("X-Faros-Cluster", "cluster")
	upload.Header.Set("X-Faros-User", "alice")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, upload)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}
	var receipt struct {
		ID          string `json:"id"`
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		SizeBytes   int64  `json:"sizeBytes"`
		SHA256      string `json:"sha256"`
		CreatedAt   string `json:"createdAt"`
		Draft       bool   `json:"draft"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ID == "" || receipt.Filename != "screen.png" || receipt.ContentType != "image/png" || receipt.SizeBytes != int64(len(data)) || receipt.SHA256 == "" || receipt.CreatedAt == "" || !receipt.Draft || receipt.ExpiresAt == "" {
		t.Fatalf("upload receipt = %#v", receipt)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/attachments/"+receipt.ID, nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "bob")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("download = %d, headers=%v, body=%x", response.Code, response.Header(), response.Body.Bytes())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/attachments/"+receipt.ID, nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "alice")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), data) || response.Header().Get("ETag") == "" {
		t.Fatalf("owner download = %d, headers=%v, body=%x", response.Code, response.Header(), response.Body.Bytes())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/attachments", nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "alice")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}
	var listed ListResponse[attachmentReceiptResponse]
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 || listed.Items[0].ID != receipt.ID {
		t.Fatalf("listed attachments = %#v, %v", listed, err)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/attachments", nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "bob")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if err := json.Unmarshal(response.Body.Bytes(), &listed); response.Code != http.StatusOK || err != nil || len(listed.Items) != 0 {
		t.Fatalf("foreign actor list = %d, %#v, %v", response.Code, listed, err)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/projects/demo/assistant/attachments/"+receipt.ID, nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "bob")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong-owner delete status = %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/projects/demo/assistant/attachments/"+receipt.ID, nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "alice")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("owner delete status = %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/attachments/"+receipt.ID, nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "alice")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("download after delete status = %d: %s", response.Code, response.Body.String())
	}

}

func TestProjectAssistantAttachmentTurnAdmissionVerifiesAllBeforeBinding(t *testing.T) {
	ctx := context.Background()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "project-uid"}
	memory := store.NewMemoryStore()
	server := &Server{attachments: memory}
	now := time.Now().UTC().Truncate(time.Microsecond)
	makeAttachment := func(id string, data []byte) projectAssistantAttachmentReceipt {
		digest := sha256.Sum256(data)
		receipt := projectAssistantAttachmentReceipt{ID: id, Filename: id + ".txt", ContentType: "text/plain", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), CreatedAt: now}
		expires := now.Add(time.Hour)
		if _, err := memory.CreateAttachment(ctx, scope, store.Attachment{ID: id, ActorID: "alice", Filename: receipt.Filename, ContentType: receipt.ContentType, SizeBytes: receipt.SizeBytes, SHA256: receipt.SHA256, Draft: true, CreatedAt: now, ExpiresAt: &expires, Data: data}); err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	first := makeAttachment("att-first", []byte("first"))
	second := makeAttachment("att-second", []byte("second"))
	tampered := second
	tampered.SizeBytes++
	parts := []projectAssistantContentPart{projectAssistantContentPartAttachment(first), projectAssistantContentPartAttachment(tampered)}
	id := identity{orgUUID: "org", workspaceUUID: "workspace", user: "alice"}
	if err := server.bindProjectAssistantContentPartAttachments(ctx, id, project, parts); err == nil {
		t.Fatal("expected tampered receipt to reject turn admission")
	}
	stored, err := memory.GetAttachment(ctx, scope, first.ID)
	if err != nil || !stored.Draft {
		t.Fatalf("first attachment was partially bound: %#v, %v", stored, err)
	}
	parts[1] = projectAssistantContentPartAttachment(second)
	if err := server.bindProjectAssistantContentPartAttachments(ctx, id, project, parts); err != nil {
		t.Fatalf("bind verified attachments: %v", err)
	}
	for _, receipt := range []projectAssistantAttachmentReceipt{first, second} {
		stored, err = memory.GetAttachment(ctx, scope, receipt.ID)
		if err != nil || stored.Draft || stored.ExpiresAt != nil {
			t.Fatalf("bound attachment %s = %#v, %v", receipt.ID, stored, err)
		}
	}
}

func TestProjectAssistantStoreAttachmentReaderVerifiesScopedReceipt(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	data := []byte("# attachment\n")
	digest := sha256.Sum256(data)
	now := time.Now().UTC()
	memory := store.NewMemoryStore()
	created, err := memory.CreateAttachment(ctx, scope, store.Attachment{
		ID: "att-reader", ActorID: "alice", Filename: "notes.md", ContentType: "text/markdown",
		SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Draft: false, CreatedAt: now, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{attachments: memory}
	reader := server.projectAssistantAttachmentReader()
	receipt := projectAssistantAttachmentReceipt{ID: created.ID, Filename: created.Filename, ContentType: created.ContentType, SizeBytes: created.SizeBytes, SHA256: created.SHA256, CreatedAt: created.CreatedAt}
	read, err := reader.ReadAttachment(ctx, scope, receipt, "alice", 0, 64)
	if err != nil || !read.Complete || !bytes.Equal(read.Content, data) {
		t.Fatalf("verified attachment read = %#v, %v", read, err)
	}
	_, err = reader.ReadAttachment(ctx, scope, receipt, "bob", 0, 64)
	if !errors.Is(err, store.ErrAttachmentForbidden) {
		t.Fatalf("foreign actor read error = %v", err)
	}
}

func TestProjectAssistantAttachmentHTTPStableClientIDIsIdempotentAndDeletable(t *testing.T) {
	project := publishingTestProject("demo", "project-uid", "")
	client := asclient.NewFromDynamic(publishingTestDynamic(project))
	server := NewWithWorkspace(nil, store.NewMemoryStore(), nil, "", false)
	server.projectClientFor = func(identity) (*asclient.Client, error) { return client, nil }
	router := mux.NewRouter()
	server.Register(router)

	clientID := "attachment:stable-http-upload"
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x02}
	upload := func(filename, stableID, actor string) (int, attachmentReceiptResponse, string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if stableID != "" {
			if err := writer.WriteField("clientAttachmentID", stableID); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/attachments", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
		request.Header.Set("X-Faros-Cluster", "cluster")
		request.Header.Set("X-Faros-User", actor)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var receipt attachmentReceiptResponse
		if response.Code == http.StatusCreated || response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
				t.Fatalf("upload receipt: %v", err)
			}
		}
		return response.Code, receipt, response.Body.String()
	}

	status, first, body := upload("screen.png", clientID, "alice")
	if status != http.StatusCreated || first.ID != clientID {
		t.Fatalf("initial stable upload = %d, %#v, %s", status, first, body)
	}
	status, retry, body := upload("screen.png", clientID, "alice")
	if status != http.StatusOK || retry.ID != first.ID || retry.CreatedAt != first.CreatedAt {
		t.Fatalf("stable retry = %d, %#v, body=%s; first=%#v", status, retry, body, first)
	}
	status, _, body = upload("different.png", clientID, "alice")
	if status != http.StatusConflict {
		t.Fatalf("stable ID metadata mismatch = %d, body=%s", status, body)
	}
	status, _, body = upload("screen.png", clientID, "bob")
	if status != http.StatusConflict {
		t.Fatalf("stable ID actor mismatch = %d, body=%s", status, body)
	}
	status, _, body = upload("screen.png", "../invalid", "alice")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid stable ID = %d, body=%s", status, body)
	}
	status, _, body = upload("screen.png", "attachment?query", "alice")
	if status != http.StatusBadRequest {
		t.Fatalf("query-bearing stable ID = %d, body=%s", status, body)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/projects/demo/assistant/attachments/"+clientID, nil)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "alice")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete by stable ID = %d, body=%s", response.Code, response.Body.String())
	}
}
