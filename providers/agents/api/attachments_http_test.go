// Copyright 2026 The Railgrid Authors.
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
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/attachments"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestAgentAttachmentHTTPUploadListDownloadDelete(t *testing.T) {
	shared := store.NewMemoryStore()
	const cluster = "aaaaaaaaaaaaaaaa"
	handler := newAttachmentHTTPHandler(t, shared, cluster, "alice", "agent-uid-upload", agentsv1alpha1.AgentBackendSpec{})

	body, contentType := attachmentMultipartBody(t, [][2]string{{"sessionID", "chat-one"}, {"clientAttachmentID", "upload-one"}}, "notes.txt", []byte("hello from an upload"))
	created := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("upload → %d: %s", created.Code, created.Body.String())
	}
	var receipt store.AgentAttachmentReceipt
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode upload receipt: %v (%s)", err, created.Body.String())
	}
	if receipt.ID != "upload-one" || receipt.Filename != "notes.txt" || receipt.SizeBytes != int64(len("hello from an upload")) || receipt.SHA256 == "" {
		t.Fatalf("upload receipt = %+v", receipt)
	}
	if created.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("upload Cache-Control = %q, want private, no-store", created.Header().Get("Cache-Control"))
	}

	listed := serveAttachmentHTTP(t, handler, http.MethodGet, attachmentCollectionPath(cluster, "chat-one"), nil, "", "alice")
	if listed.Code != http.StatusOK {
		t.Fatalf("list → %d: %s", listed.Code, listed.Body.String())
	}
	var response agentAttachmentListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode attachment list: %v (%s)", err, listed.Body.String())
	}
	if len(response.Items) != 1 || response.Items[0] != receipt {
		t.Fatalf("list items = %+v, want the uploaded immutable receipt %+v", response.Items, receipt)
	}
	if response.InputCapabilities == nil || !response.InputCapabilities.Attachments || !response.InputCapabilities.Skills || !response.InputCapabilities.Resources {
		t.Fatalf("model-backed Agent input capabilities = %+v, want all composer input kinds enabled", response.InputCapabilities)
	}

	detailPath := attachmentDetailPath(cluster, "upload-one", "chat-one")
	downloaded := serveAttachmentHTTP(t, handler, http.MethodGet, detailPath, nil, "", "alice")
	if downloaded.Code != http.StatusOK || downloaded.Body.String() != "hello from an upload" {
		t.Fatalf("download → %d body=%q, want original bytes", downloaded.Code, downloaded.Body.String())
	}
	if downloaded.Header().Get("Cache-Control") != "private, no-store" || downloaded.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("download headers = Cache-Control:%q X-Content-Type-Options:%q", downloaded.Header().Get("Cache-Control"), downloaded.Header().Get("X-Content-Type-Options"))
	}
	if !strings.HasPrefix(downloaded.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("download Content-Disposition = %q, want attachment", downloaded.Header().Get("Content-Disposition"))
	}

	deleted := serveAttachmentHTTP(t, handler, http.MethodDelete, detailPath, nil, "", "alice")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete draft → %d: %s", deleted.Code, deleted.Body.String())
	}
	missing := serveAttachmentHTTP(t, handler, http.MethodGet, detailPath, nil, "", "alice")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("download after delete → %d, want 404: %s", missing.Code, missing.Body.String())
	}
	listed = serveAttachmentHTTP(t, handler, http.MethodGet, attachmentCollectionPath(cluster, "chat-one"), nil, "", "alice")
	if listed.Code != http.StatusOK {
		t.Fatalf("list after delete → %d: %s", listed.Code, listed.Body.String())
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil || len(response.Items) != 0 {
		t.Fatalf("list after delete items=%+v err=%v", response.Items, err)
	}
}

func TestAgentAttachmentHTTPIsolation(t *testing.T) {
	shared := store.NewMemoryStore()
	const clusterA = "aaaaaaaaaaaaaaaa"
	const clusterB = "bbbbbbbbbbbbbbbb"
	alice := newAttachmentHTTPHandler(t, shared, clusterA, "alice", "agent-uid-original", agentsv1alpha1.AgentBackendSpec{})
	body, contentType := attachmentMultipartBody(t, [][2]string{{"sessionID", "chat"}, {"clientAttachmentID", "shared-id"}}, "private.txt", []byte("private"))
	created := serveAttachmentHTTP(t, alice, http.MethodPost, attachmentCollectionPath(clusterA, ""), body, contentType, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("upload as Alice → %d: %s", created.Code, created.Body.String())
	}

	for _, tc := range []struct {
		name    string
		cluster string
		user    string
		uid     string
		session string
	}{
		{name: "different actor", cluster: clusterA, user: "bob", uid: "agent-uid-original", session: "chat"},
		{name: "different session", cluster: clusterA, user: "alice", uid: "agent-uid-original", session: "other-chat"},
		{name: "recreated same-name Agent UID", cluster: clusterA, user: "alice", uid: "agent-uid-recreated", session: "chat"},
		{name: "same-name Agent in another tenant", cluster: clusterB, user: "alice", uid: "agent-uid-original", session: "chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newAttachmentHTTPHandler(t, shared, tc.cluster, tc.user, tc.uid, agentsv1alpha1.AgentBackendSpec{})
			list := serveAttachmentHTTP(t, handler, http.MethodGet, attachmentCollectionPath(tc.cluster, tc.session), nil, "", tc.user)
			if list.Code != http.StatusOK {
				t.Fatalf("list → %d: %s", list.Code, list.Body.String())
			}
			var response agentAttachmentListResponse
			if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode list: %v", err)
			}
			if len(response.Items) != 0 {
				t.Fatalf("cross-boundary list exposed receipts: %+v", response.Items)
			}
			detail := serveAttachmentHTTP(t, handler, http.MethodGet, attachmentDetailPath(tc.cluster, "shared-id", tc.session), nil, "", tc.user)
			if detail.Code != http.StatusNotFound {
				t.Fatalf("cross-boundary detail → %d, want 404: %s", detail.Code, detail.Body.String())
			}
		})
	}

	ownerDownload := serveAttachmentHTTP(t, alice, http.MethodGet, attachmentDetailPath(clusterA, "shared-id", "chat"), nil, "", "alice")
	if ownerDownload.Code != http.StatusOK || ownerDownload.Body.String() != "private" {
		t.Fatalf("original owner download → %d %q", ownerDownload.Code, ownerDownload.Body.String())
	}
}

func TestAgentAttachmentHTTPRejectsMalformedMultipartAndSessionFields(t *testing.T) {
	shared := store.NewMemoryStore()
	const cluster = "aaaaaaaaaaaaaaaa"
	handler := newAttachmentHTTPHandler(t, shared, cluster, "alice", "agent-uid-validation", agentsv1alpha1.AgentBackendSpec{})

	cases := []struct {
		name   string
		fields [][2]string
		file   bool
	}{
		{name: "duplicate session field", fields: [][2]string{{"sessionID", "one"}, {"sessionID", "two"}}, file: true},
		{name: "duplicate client id field", fields: [][2]string{{"sessionID", "one"}, {"clientAttachmentID", "id-one"}, {"clientAttachmentID", "id-two"}}, file: true},
		{name: "malformed client id", fields: [][2]string{{"sessionID", "one"}, {"clientAttachmentID", "../escape"}}, file: true},
		{name: "missing file field", fields: [][2]string{{"sessionID", "one"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filename := ""
			var data []byte
			if tc.file {
				filename, data = "input.txt", []byte("body")
			}
			body, contentType := attachmentMultipartBody(t, tc.fields, filename, data)
			rec := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, "alice")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid upload → %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}

	duplicateQuery := serveAttachmentHTTP(t, handler, http.MethodGet,
		attachmentCollectionPath(cluster, "one")+"&sessionID=two", nil, "", "alice")
	if duplicateQuery.Code != http.StatusBadRequest {
		t.Fatalf("duplicate session query → %d, want 400: %s", duplicateQuery.Code, duplicateQuery.Body.String())
	}

	list := serveAttachmentHTTP(t, handler, http.MethodGet, attachmentCollectionPath(cluster, "one"), nil, "", "alice")
	var response agentAttachmentListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil || len(response.Items) != 0 {
		t.Fatalf("rejected uploads left receipts %+v, err=%v", response.Items, err)
	}
}

func TestAgentAttachmentHTTPRejectsOversizedFileAndRequest(t *testing.T) {
	shared := store.NewMemoryStore()
	const cluster = "aaaaaaaaaaaaaaaa"
	handler := newAttachmentHTTPHandler(t, shared, cluster, "alice", "agent-uid-limits", agentsv1alpha1.AgentBackendSpec{})

	t.Run("file size", func(t *testing.T) {
		data := make([]byte, attachments.AttachmentMaxBytes+1)
		body, contentType := attachmentMultipartBody(t, [][2]string{{"sessionID", "chat"}}, "too-large.bin", data)
		rec := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, "alice")
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "25 MiB upload limit") {
			t.Fatalf("oversized file → %d %s, want file limit 413", rec.Code, rec.Body.String())
		}
	})

	t.Run("multipart request body", func(t *testing.T) {
		data := make([]byte, attachments.AttachmentMaxBytes+(512<<10))
		body, contentType := attachmentMultipartBody(t, [][2]string{{"sessionID", "chat"}}, "too-large.bin", data)
		rec := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, "alice")
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request exceeds the upload limit") {
			t.Fatalf("oversized request → %d %s, want multipart request limit 413", rec.Code, rec.Body.String())
		}
	})
}

func TestAgentAttachmentHTTPClientIDRetryAndBoundDelete(t *testing.T) {
	shared := store.NewMemoryStore()
	const cluster = "aaaaaaaaaaaaaaaa"
	const user = "alice"
	const agentUID = "agent-uid-bound"
	handler := newAttachmentHTTPHandler(t, shared, cluster, user, agentUID, agentsv1alpha1.AgentBackendSpec{})
	fields := [][2]string{{"sessionID", "chat"}, {"clientAttachmentID", "stable-id"}}
	body, contentType := attachmentMultipartBody(t, fields, "report.txt", []byte("same bytes"))
	first := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, user)
	if first.Code != http.StatusCreated {
		t.Fatalf("first upload → %d: %s", first.Code, first.Body.String())
	}
	var firstReceipt store.AgentAttachmentReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &firstReceipt); err != nil {
		t.Fatal(err)
	}

	body, contentType = attachmentMultipartBody(t, fields, "report.txt", []byte("same bytes"))
	retry := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, user)
	if retry.Code != http.StatusOK {
		t.Fatalf("same-ID retry → %d: %s", retry.Code, retry.Body.String())
	}
	var retriedReceipt store.AgentAttachmentReceipt
	if err := json.Unmarshal(retry.Body.Bytes(), &retriedReceipt); err != nil {
		t.Fatal(err)
	}
	if retriedReceipt != firstReceipt {
		t.Fatalf("idempotent retry receipt = %+v, want original %+v", retriedReceipt, firstReceipt)
	}

	body, contentType = attachmentMultipartBody(t, fields, "report.txt", []byte("different bytes"))
	conflict := serveAttachmentHTTP(t, handler, http.MethodPost, attachmentCollectionPath(cluster, ""), body, contentType, user)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same-ID changed-content retry → %d, want 409: %s", conflict.Code, conflict.Body.String())
	}

	scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: cluster, ClusterID: cluster, AgentName: conformanceAgent}
	now := time.Now().UTC()
	message := store.Message{
		ID: "accepted-message", AgentName: conformanceAgent, SessionID: "chat", RunID: "accepted-run",
		Role: "user", Content: "inspect this upload", CreatedAt: now,
	}
	run := store.Run{
		ID: "accepted-run", AgentName: conformanceAgent, SessionID: "chat", Trigger: "chat",
		Phase: store.RunPhasePending, CreatedAt: now, UpdatedAt: now,
	}
	if err := shared.CreateAgentChatRun(context.Background(), scope, agentUID, message, run, []store.AgentAttachmentReceipt{firstReceipt}, user, now); err != nil {
		t.Fatalf("admit chat run and bind upload atomically: %v", err)
	}
	deleted := serveAttachmentHTTP(t, handler, http.MethodDelete, attachmentDetailPath(cluster, firstReceipt.ID, "chat"), nil, "", user)
	if deleted.Code != http.StatusConflict {
		t.Fatalf("delete bound upload → %d, want immutable 409: %s", deleted.Code, deleted.Body.String())
	}
	downloaded := serveAttachmentHTTP(t, handler, http.MethodGet, attachmentDetailPath(cluster, firstReceipt.ID, "chat"), nil, "", user)
	if downloaded.Code != http.StatusOK || downloaded.Body.String() != "same bytes" {
		t.Fatalf("bound upload after rejected delete → %d body=%q", downloaded.Code, downloaded.Body.String())
	}
}

func TestAgentAttachmentHTTPReportsHarnessInputCapabilityFallback(t *testing.T) {
	shared := store.NewMemoryStore()
	const cluster = "aaaaaaaaaaaaaaaa"
	backend := agentsv1alpha1.AgentBackendSpec{
		Type: agentsv1alpha1.AgentBackendHarness,
		Harness: &agentsv1alpha1.AgentHarnessBackend{
			EdgeRef:       agentsv1alpha1.AgentHarnessEdgeRef{Kind: "LinuxServer", Name: "build-edge"},
			CredentialRef: "claude-credential",
		},
	}
	handler := newAttachmentHTTPHandler(t, shared, cluster, "alice", "agent-uid-harness", backend)
	rec := serveAttachmentHTTP(t, handler, http.MethodGet, attachmentCollectionPath(cluster, "chat"), nil, "", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("capability list → %d: %s", rec.Code, rec.Body.String())
	}
	var response agentAttachmentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode capability response: %v", err)
	}
	if response.InputCapabilities == nil || response.InputCapabilities.Attachments || response.InputCapabilities.Skills || !response.InputCapabilities.Resources || response.InputCapabilities.Reason == "" {
		t.Fatalf("harness without file-input runner capabilities = %+v", response.InputCapabilities)
	}
}

func newAttachmentHTTPHandler(t *testing.T, shared *store.MemoryStore, cluster, user, uid string, backend agentsv1alpha1.AgentBackendSpec) http.Handler {
	t.Helper()
	agent := &agentsv1alpha1.Agent{}
	agent.Name = conformanceAgent
	agent.UID = types.UID(uid)
	agent.Spec.Backend = backend
	obj := chatTestObject(t, agent, "agents.railgrid.ai/v1alpha1", "Agent")
	callers := &conformance.FakeCallers{
		Cluster: cluster,
		User:    user,
		Objects: []*unstructured.Unstructured{obj},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		},
	}
	s := newGatedTestServer(t, callers)
	s.store = shared
	return gatedHandler(t, s)
}

func attachmentCollectionPath(cluster, session string) string {
	path := verbBase(cluster, "agents", conformanceAgent) + "/attachments"
	if session != "" {
		path += "?sessionID=" + url.QueryEscape(session)
	}
	return path
}

func attachmentDetailPath(cluster, id, session string) string {
	return verbBase(cluster, "agents", conformanceAgent) + "/attachments/" + url.PathEscape(id) + "?sessionID=" + url.QueryEscape(session)
}

func serveAttachmentHTTP(t *testing.T, handler http.Handler, method, path string, body *bytes.Buffer, contentType, user string) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body.Bytes())
	}
	req := stamped(method, path, requestBody, user)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func attachmentMultipartBody(t *testing.T, fields [][2]string, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			t.Fatalf("write multipart field %q: %v", field[0], err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create multipart file: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("write multipart file: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	return &body, writer.FormDataContentType()
}
