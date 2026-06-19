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
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

func projectMessageScope(orgUUID, workspaceUUID, projectName string) store.Scope {
	return store.Scope{
		OrgUUID:       orgUUID,
		WorkspaceUUID: workspaceUUID,
		ProjectName:   projectName,
	}
}

func projectMessagesToAPI(items []store.Message) []aiv1alpha1.ProjectMessage {
	if len(items) == 0 {
		return nil
	}
	out := make([]aiv1alpha1.ProjectMessage, 0, len(items))
	for _, item := range items {
		out = append(out, projectMessageToAPI(item))
	}
	return out
}

func projectMessageToAPI(item store.Message) aiv1alpha1.ProjectMessage {
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
