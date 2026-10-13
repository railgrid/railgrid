/*
Copyright 2026 The Railgrid Authors.

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/hubapi"
)

const maxAppAccessBrokerResponseBytes = 1 << 20

type appAccessBrokerOperation string

const (
	appAccessBrokerList   appAccessBrokerOperation = "list"
	appAccessBrokerCreate appAccessBrokerOperation = "create"
	appAccessBrokerRevoke appAccessBrokerOperation = "revoke"
	appAccessBrokerReset  appAccessBrokerOperation = "reset"
)

// appAccessBrokerRequest is deliberately a closed, per-project tuple. It has
// no role, rule, subject kind, or arbitrary RBAC field: the hub validates the
// signed App Studio action, pins the named Project and managed Instance, then
// applies the fixed app-access grant shape.
type appAccessBrokerRequest struct {
	Operation   appAccessBrokerOperation `json:"operation"`
	Group       string                   `json:"group"`
	Resource    string                   `json:"resource"`
	Name        string                   `json:"name"`
	UID         string                   `json:"uid"`
	ProjectName string                   `json:"projectName"`
	ProjectUID  string                   `json:"projectUID"`
	User        string                   `json:"user,omitempty"`
	Grant       string                   `json:"grant,omitempty"`
}

type appAccessBrokerResponse struct {
	Items []projectPublishingGrantView `json:"items"`
}

func newAppAccessBrokerRequest(operation appAccessBrokerOperation, p *aiv1alpha1.Project, target projectPublishingTargetView, user, grant string) (appAccessBrokerRequest, error) {
	if p == nil || p.Name == "" || p.UID == "" || target.Name == "" || target.UID == "" {
		return appAccessBrokerRequest{}, fmt.Errorf("project and managed app identity are required for an app access grant")
	}
	group, _, ok := strings.Cut(target.APIVersion, "/")
	if !ok || group == "" || target.Resource == "" {
		return appAccessBrokerRequest{}, fmt.Errorf("managed app resource coordinates are incomplete")
	}
	req := appAccessBrokerRequest{
		Operation:   operation,
		Group:       group,
		Resource:    target.Resource,
		Name:        target.Name,
		UID:         target.UID,
		ProjectName: p.Name,
		ProjectUID:  string(p.UID),
		User:        strings.TrimSpace(user),
		Grant:       strings.TrimSpace(grant),
	}
	switch operation {
	case appAccessBrokerList, appAccessBrokerReset:
		if req.User != "" || req.Grant != "" {
			return appAccessBrokerRequest{}, fmt.Errorf("%s app access request cannot name a user or grant", operation)
		}
	case appAccessBrokerCreate:
		if req.User == "" || req.Grant != "" {
			return appAccessBrokerRequest{}, fmt.Errorf("create app access request must name exactly one user")
		}
	case appAccessBrokerRevoke:
		if req.Grant == "" || req.User != "" {
			return appAccessBrokerRequest{}, fmt.Errorf("revoke app access request must name exactly one grant")
		}
	default:
		return appAccessBrokerRequest{}, fmt.Errorf("unsupported app access operation %q", operation)
	}
	return req, nil
}

func (s *Server) callAppAccessBroker(ctx context.Context, id identity, req appAccessBrokerRequest) ([]projectPublishingGrantView, error) {
	if s.appAccessBroker != nil {
		return s.appAccessBroker(ctx, id, req)
	}
	if strings.TrimSpace(s.hubBase) == "" || strings.TrimSpace(s.hubToken) == "" {
		return nil, fmt.Errorf("hub access broker is not configured")
	}
	if strings.TrimSpace(id.actionProof) == "" {
		return nil, &hubStatusError{Status: http.StatusForbidden, Message: "a signed App Studio action is required for app access grants"}
	}
	if id.orgUUID == "" || id.workspaceUUID == "" {
		return nil, fmt.Errorf("trusted organization and workspace identity are required for app access grants")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode app access broker request: %w", err)
	}
	endpoint := strings.TrimRight(s.hubBase, "/") + hubapi.AppAccessGrantsPath(id.orgUUID, id.workspaceUUID)
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hubReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create app access broker request: %w", err)
	}
	hubReq.Header.Set("Content-Type", "application/json")
	s.setHubCallerHeaders(hubReq.Header, id)
	s.setHubActionProofHeader(hubReq.Header, id)
	response, err := s.hubHTTPClient(15 * time.Second).Do(hubReq)
	if err != nil {
		return nil, fmt.Errorf("calling app access broker: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAppAccessBrokerResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read app access broker response: %w", err)
	}
	if len(body) > maxAppAccessBrokerResponseBytes {
		return nil, fmt.Errorf("app access broker response exceeds %d bytes", maxAppAccessBrokerResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, newHubStatusError(response.StatusCode, body)
	}
	var decoded appAccessBrokerResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode app access broker response: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("decode app access broker response: trailing data")
	}
	return validateAppAccessBrokerItems(req.Name, decoded.Items)
}

func validateAppAccessBrokerItems(app string, items []projectPublishingGrantView) ([]projectPublishingGrantView, error) {
	clean := make([]projectPublishingGrantView, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.User) == "" || item.Publication != app {
			return nil, fmt.Errorf("app access broker returned an incomplete or mismatched grant")
		}
		clean = append(clean, item)
	}
	return clean, nil
}

func writeAppAccessBrokerError(w http.ResponseWriter, err error) {
	var hubErr *hubStatusError
	if !errors.As(err, &hubErr) {
		writeError(w, err)
		return
	}
	switch hubErr.Status {
	case http.StatusBadRequest:
		writeStatus(w, http.StatusBadRequest, "BadRequest", hubErr.Message)
	case http.StatusForbidden:
		writeStatus(w, http.StatusForbidden, "Forbidden", hubErr.Message)
	case http.StatusNotFound:
		writeStatus(w, http.StatusNotFound, "NotFound", hubErr.Message)
	case http.StatusConflict:
		writeStatus(w, http.StatusConflict, "Conflict", hubErr.Message)
	default:
		writeStatus(w, http.StatusBadGateway, "BadGateway", "the platform could not complete the app access request")
	}
}
