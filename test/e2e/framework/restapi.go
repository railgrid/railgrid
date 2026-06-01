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

// REST surface helpers shared by every tenancy e2e case. These wrap the
// /api/orgs/* surface the hub exposes so test cases can stay focused on
// assertions instead of HTTP plumbing.

package framework

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RESTClient is a TLS-skip HTTP client targeting the hub's self-signed
// dev cert. Shared across tenancy cases.
var RESTClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
	},
}

// DoRESTRequest performs a JSON request against the hub REST surface and
// returns the status code + response body. body may be nil for GET/DELETE.
// tenantHeaders carries X-Kedge-Org / X-Kedge-Workspace as needed.
func DoRESTRequest(
	ctx context.Context,
	method, url, bearer string,
	tenantHeaders map[string]string,
	body any,
) (int, []byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range tenantHeaders {
		req.Header.Set(k, v)
	}
	resp, err := RESTClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
}

// IsAuthRejectStatus is true for any status code the REST surface is
// allowed to return when an identity is denied. The hub may pick 401
// (no/invalid auth), 403 (auth ok, not a member), or 404 (refuse to
// confirm existence) depending on which check fires first. All three
// are acceptable for negative checks; 200/2xx is the bug.
func IsAuthRejectStatus(code int) bool {
	return code == http.StatusUnauthorized ||
		code == http.StatusForbidden ||
		code == http.StatusNotFound
}

// CreateOrgResponse / CreateWorkspaceResponse / SAResponse / TokenResponse
// are minimal projections of the REST replies — just enough for tests to
// pick the UUID and an optional display name out.

type CreateOrgResponse struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"displayName"`
	Personal    bool   `json:"personal"`
}

type CreateWorkspaceResponse struct {
	UUID        string `json:"uuid"`
	OrgUUID     string `json:"orgUUID"`
	DisplayName string `json:"displayName,omitempty"`
}

type SAResponse struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// CreateOrgViaREST creates a non-personal Organization under the given
// bearer identity and returns the created UUID. Fatal on non-201.
func CreateOrgViaREST(ctx context.Context, hubURL, bearer, displayName string) (CreateOrgResponse, error) {
	code, body, err := DoRESTRequest(ctx, http.MethodPost, hubURL+"/api/orgs", bearer, nil,
		map[string]string{"displayName": displayName})
	if err != nil {
		return CreateOrgResponse{}, fmt.Errorf("POST /api/orgs: %w", err)
	}
	if code != http.StatusCreated {
		return CreateOrgResponse{}, fmt.Errorf("POST /api/orgs: expected 201, got %d: %s", code, body)
	}
	var out CreateOrgResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return CreateOrgResponse{}, fmt.Errorf("decode org create: %w (body=%s)", err, body)
	}
	return out, nil
}

// CreateWorkspaceViaREST creates a Workspace under an Organization.
func CreateWorkspaceViaREST(ctx context.Context, hubURL, bearer, orgUUID, displayName string) (CreateWorkspaceResponse, error) {
	code, body, err := DoRESTRequest(ctx, http.MethodPost,
		hubURL+"/api/orgs/"+orgUUID+"/workspaces", bearer,
		map[string]string{"X-Kedge-Org": orgUUID},
		map[string]string{"displayName": displayName})
	if err != nil {
		return CreateWorkspaceResponse{}, fmt.Errorf("POST workspaces: %w", err)
	}
	if code != http.StatusCreated {
		return CreateWorkspaceResponse{}, fmt.Errorf("POST workspaces: expected 201, got %d: %s", code, body)
	}
	var out CreateWorkspaceResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return CreateWorkspaceResponse{}, fmt.Errorf("decode workspace create: %w (body=%s)", err, body)
	}
	return out, nil
}

// DeleteOrgViaREST soft-deletes an Org (best-effort; non-fatal).
func DeleteOrgViaREST(ctx context.Context, hubURL, bearer, orgUUID string) (int, error) {
	code, _, err := DoRESTRequest(ctx, http.MethodDelete,
		hubURL+"/api/orgs/"+orgUUID, bearer,
		map[string]string{"X-Kedge-Org": orgUUID}, nil)
	return code, err
}

// FindPersonalOrgUUID returns the personal-org UUID for the caller. Used
// by personal-org guardrail tests.
func FindPersonalOrgUUID(ctx context.Context, hubURL, bearer string) (string, error) {
	code, body, err := DoRESTRequest(ctx, http.MethodGet, hubURL+"/api/orgs", bearer, nil, nil)
	if err != nil {
		return "", fmt.Errorf("GET /api/orgs: %w", err)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("GET /api/orgs: expected 200, got %d: %s", code, body)
	}
	var list struct {
		Items []struct {
			UUID     string `json:"uuid"`
			Personal bool   `json:"personal"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("decode org list: %w (body=%s)", err, body)
	}
	for _, o := range list.Items {
		if o.Personal {
			return o.UUID, nil
		}
	}
	return "", fmt.Errorf("no personal org in list (got %d items)", len(list.Items))
}
