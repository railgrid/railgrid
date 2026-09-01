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

package api

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

func TestEnsureProjectAttachmentAdmissionAddsScopeAndFinalizer(t *testing.T) {
	ctx := context.Background()
	project := publishingTestProjectTyped("demo", "project-uid", "")
	dynamicClient := attachmentAdmissionDynamic(t, project)
	client := asclient.NewFromDynamic(dynamicClient)
	server := NewWithWorkspace(nil, store.NewMemoryStore(), nil, "", false)
	id := identity{orgUUID: "org", workspaceUUID: "workspace", user: "alice"}

	admitted, err := server.ensureProjectAttachmentAdmission(ctx, client, id, project)
	if err != nil {
		t.Fatalf("ensureProjectAttachmentAdmission: %v", err)
	}
	if admitted == nil || admitted.Annotations[bindings.OrgUUIDAnnotation] != id.orgUUID || admitted.Annotations[bindings.WorkspaceUUIDAnnotation] != id.workspaceUUID || !hasAttachmentAdmissionFinalizer(admitted.Finalizers) {
		t.Fatalf("admitted Project metadata = %#v, want tenant annotations and attachment finalizer", admitted)
	}
	persisted, err := client.Projects().Get(ctx, project.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-read admitted Project: %v", err)
	}
	if !projectAttachmentAdmissionReady(persisted, id.orgUUID, id.workspaceUUID) {
		t.Fatalf("persisted Project metadata = %#v, want attachment admission ready", persisted)
	}
}

func TestEnsureProjectAttachmentAdmissionRejectsConflictingTenantMetadata(t *testing.T) {
	ctx := context.Background()
	project := publishingTestProjectTyped("demo", "project-uid", "")
	project.Annotations = map[string]string{
		bindings.OrgUUIDAnnotation:       "different-org",
		bindings.WorkspaceUUIDAnnotation: "workspace",
	}
	dynamicClient := attachmentAdmissionDynamic(t, project)
	client := asclient.NewFromDynamic(dynamicClient)
	server := NewWithWorkspace(nil, store.NewMemoryStore(), nil, "", false)
	_, err := server.ensureProjectAttachmentAdmission(ctx, client, identity{orgUUID: "org", workspaceUUID: "workspace"}, project)
	if !errors.Is(err, errProjectAttachmentScopeConflict) {
		t.Fatalf("conflicting metadata error = %v, want scope conflict", err)
	}
	for _, action := range dynamicClient.Actions() {
		if action.GetVerb() == "update" {
			t.Fatalf("conflicting metadata attempted Project update: %#v", action)
		}
	}
}

func TestEnsureProjectAttachmentAdmissionRetriesConflictWithFreshRead(t *testing.T) {
	ctx := context.Background()
	project := publishingTestProjectTyped("demo", "project-uid", "")
	dynamicClient := attachmentAdmissionDynamic(t, project)
	updates := 0
	dynamicClient.PrependReactor("update", "projects", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(asclient.ProjectGVR.GroupResource(), project.Name, errors.New("simulated write race"))
		}
		return false, nil, nil
	})
	client := asclient.NewFromDynamic(dynamicClient)
	server := NewWithWorkspace(nil, store.NewMemoryStore(), nil, "", false)
	if _, err := server.ensureProjectAttachmentAdmission(ctx, client, identity{orgUUID: "org", workspaceUUID: "workspace"}, project); err != nil {
		t.Fatalf("ensureProjectAttachmentAdmission after conflict: %v", err)
	}
	if updates != 2 {
		t.Fatalf("Project update attempts = %d, want one retry after conflict", updates)
	}
}

func TestCreateProjectAssistantAttachmentRejectsMetadataWriteFailureBeforeStore(t *testing.T) {
	project := publishingTestProjectTyped("demo", "project-uid", "")
	dynamicClient := attachmentAdmissionDynamic(t, project)
	dynamicClient.PrependReactor("update", "projects", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(asclient.ProjectGVR.GroupResource(), project.Name, errors.New("simulated write race"))
	})
	attachments := store.NewMemoryStore()
	client := asclient.NewFromDynamic(dynamicClient)
	server := NewWithWorkspace(nil, attachments, nil, "", false)
	server.projectClientFor = func(identity) (*asclient.Client, error) { return client, nil }
	router := mux.NewRouter()
	server.Register(router)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "screen.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/attachments", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-User", "alice")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("upload with metadata write failure status = %d: %s", response.Code, response.Body.String())
	}
	attachmentsForProject, err := attachments.ListAttachments(context.Background(), store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "project-uid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachmentsForProject) != 0 {
		t.Fatalf("attachments after metadata admission failure = %#v, want none", attachmentsForProject)
	}
}

func hasAttachmentAdmissionFinalizer(values []string) bool {
	for _, value := range values {
		if value == store.AttachmentStorageFinalizer {
			return true
		}
	}
	return false
}

func attachmentAdmissionDynamic(t *testing.T, project *aiv1alpha1.Project) *fake.FakeDynamicClient {
	t.Helper()
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(project)
	if err != nil {
		t.Fatalf("convert Project test object: %v", err)
	}
	return publishingTestDynamic(&unstructured.Unstructured{Object: object})
}
