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

package restapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/faros-kedge/apis/ai/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	projectstore "github.com/faroshq/faros-kedge/providers/projects/store"
)

func (h *Handler) requireProjectMessagesStore(w http.ResponseWriter) (projectstore.Store, bool) {
	if h.mgr.projectMessages == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project message store not wired on this hub")
		return nil, false
	}
	return h.mgr.projectMessages, true
}

func projectMessageScope(orgUUID, workspaceUUID, projectName string) projectstore.Scope {
	return projectstore.Scope{
		OrgUUID:       orgUUID,
		WorkspaceUUID: workspaceUUID,
		ProjectName:   projectName,
	}
}

func projectMessagesToAPI(items []projectstore.Message) []aiv1alpha1.ProjectMessage {
	if len(items) == 0 {
		return nil
	}
	out := make([]aiv1alpha1.ProjectMessage, 0, len(items))
	for _, item := range items {
		out = append(out, projectMessageToAPI(item))
	}
	return out
}

func projectMessageToAPI(item projectstore.Message) aiv1alpha1.ProjectMessage {
	meta := metadataToAPI(item.Metadata)
	if len(meta) == 0 {
		meta = nil
	}
	return aiv1alpha1.ProjectMessage{
		ID:               item.ID,
		ProjectID:        item.ProjectName,
		Role:             item.Role,
		Content:          item.Content,
		ContentEncrypted: item.ContentEncrypted,
		ContentKeyID:     item.ContentKeyID,
		Metadata:         meta,
		CreatedAt:        metav1Time(item.CreatedAt),
	}
}

func metadataToAPI(src map[string]any) map[string]runtime.RawExtension {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]runtime.RawExtension, len(src))
	for k, v := range src {
		raw, err := json.Marshal(v)
		if err != nil {
			raw, _ = json.Marshal(fmt.Sprint(v))
		}
		dst[k] = runtime.RawExtension{Raw: raw}
	}
	return dst
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func metav1Time(t time.Time) metav1.Time {
	return metav1.NewTime(t.UTC())
}

func listLimitFromRequest(r *http.Request) int {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 250 {
		limit = 250
	}
	return limit
}

func (h *Handler) migrateLegacyProjectMessages(ctx context.Context, c *kedgeclient.Client, orgUUID, workspaceUUID string, p *aiv1alpha1.Project) error {
	raw, err := c.Dynamic().Resource(kedgeclient.ProjectGVR).Get(ctx, p.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	messages, found, err := unstructured.NestedSlice(raw.Object, "spec", "messages")
	if err != nil {
		return fmt.Errorf("read legacy project messages: %w", err)
	}
	if !found || len(messages) == 0 {
		return nil
	}
	store := h.mgr.projectMessages
	if store == nil {
		return fmt.Errorf("project message store not wired")
	}
	scope := projectMessageScope(orgUUID, workspaceUUID, p.Name)
	for _, item := range messages {
		payload, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal legacy project message: %w", err)
		}
		var msg aiv1alpha1.ProjectMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			return fmt.Errorf("decode legacy project message: %w", err)
		}
		if strings.TrimSpace(msg.ID) == "" {
			msg.ID = newMessageID()
		}
		if strings.TrimSpace(msg.Role) == "" {
			msg.Role = aiv1alpha1.ProjectMessageRoleUser
		}
		if strings.TrimSpace(msg.ProjectID) == "" {
			msg.ProjectID = p.Name
		}
		createdAt := msg.CreatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if err := store.AppendMessage(ctx, scope, projectstore.Message{
			ID:               msg.ID,
			ProjectName:      p.Name,
			Role:             msg.Role,
			Content:          msg.Content,
			ContentEncrypted: msg.ContentEncrypted,
			ContentKeyID:     msg.ContentKeyID,
			Metadata:         rawExtensionMapToAny(msg.Metadata),
			CreatedAt:        createdAt,
			UpdatedAt:        createdAt,
		}); err != nil {
			return err
		}
	}
	unstructured.RemoveNestedField(raw.Object, "spec", "messages")
	if _, err := c.Dynamic().Resource(kedgeclient.ProjectGVR).Update(ctx, raw, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("clear legacy project messages: %w", err)
	}
	return nil
}

func rawExtensionMapToAny(src map[string]runtime.RawExtension) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, raw := range src {
		var v any
		if len(raw.Raw) > 0 {
			if err := json.Unmarshal(raw.Raw, &v); err != nil {
				v = string(raw.Raw)
			}
		}
		dst[k] = v
	}
	return dst
}
