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

package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

// orgBackendKCPDispatcher returns the only native KCP dispatcher used by the
// opaque BYO broker. The REST handler has already checked the signed token,
// exact workspace/provider binding, and current permission gate. This adapter
// resolves the current Railgrid User and lets native KCP RBAC evaluate that
// user's current identity; no caller-supplied identity or credential header is
// forwarded.
func orgBackendKCPDispatcher(config *rest.Config, users *railgridclient.Client) func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) {
	return func(w http.ResponseWriter, r *http.Request, claims serviceaccounts.OrgBackendClaims) {
		if config == nil || users == nil || r == nil || claims.User == "" || claims.UserUID == "" || claims.ClusterID == "" {
			writeOrgBackendDispatchError(w, http.StatusServiceUnavailable, "provider authorization is unavailable")
			return
		}
		if !orgBackendRequestMatchesCluster(r, claims.ClusterID) {
			writeOrgBackendDispatchError(w, http.StatusForbidden, "provider request is outside its authorized workspace")
			return
		}
		user, err := users.Users().Get(r.Context(), claims.User, metav1.GetOptions{})
		if err != nil || user == nil || user.Name != claims.User || string(user.UID) != claims.UserUID || user.DeletionTimestamp != nil || user.Status.DeletionRequestedAt != nil || strings.TrimSpace(user.Spec.RBACIdentity) == "" {
			writeOrgBackendDispatchError(w, http.StatusForbidden, "provider caller is no longer available")
			return
		}

		impersonated := rest.CopyConfig(config)
		impersonated.Impersonate = rest.ImpersonationConfig{
			UserName: user.Spec.RBACIdentity,
			Groups:   []string{"system:authenticated"},
		}
		transport, err := rest.TransportFor(impersonated)
		if err != nil {
			writeOrgBackendDispatchError(w, http.StatusServiceUnavailable, "provider request dispatch is unavailable")
			return
		}
		target, err := url.Parse(config.Host)
		if err != nil || target.Scheme == "" || target.Host == "" {
			writeOrgBackendDispatchError(w, http.StatusServiceUnavailable, "provider request dispatch is unavailable")
			return
		}
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Director: func(out *http.Request) {
				out.URL.Scheme = target.Scheme
				out.URL.Host = target.Host
				out.URL.Path = r.URL.Path
				out.URL.RawPath = ""
				out.URL.RawQuery = r.URL.RawQuery
				out.Host = target.Host
				stripOrgBackendDispatchHeaders(out.Header)
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				writeOrgBackendDispatchError(w, http.StatusBadGateway, "provider request dispatch failed")
			},
		}
		proxy.ServeHTTP(w, r)
	}
}

func orgBackendRequestMatchesCluster(r *http.Request, clusterID string) bool {
	if r == nil || r.URL == nil || clusterID == "" || r.URL.EscapedPath() != r.URL.Path {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return len(parts) >= 3 && parts[0] == "clusters" && parts[1] == clusterID
}

func stripOrgBackendDispatchHeaders(header http.Header) {
	for name := range header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "cookie2" || lower == "sec-websocket-protocol" ||
			strings.HasPrefix(lower, "x-railgrid-") || strings.HasPrefix(lower, "x-remote-") || strings.HasPrefix(lower, "impersonate-") {
			header.Del(name)
		}
	}
}

func writeOrgBackendDispatchError(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
}

type orgBackendTokenIssuer interface {
	IssueOrgBackendToken(context.Context, string, string, serviceaccounts.Identity, serviceaccounts.DelegatedProvider) (string, time.Time, error)
}

// orgAwareDelegatedTokenIssuer keeps platform-provider compatibility while
// ensuring every org-owned backend, UI asset, and aggregate route gets only
// the opaque broker credential. Missing issuers fail closed by provider class.
func orgAwareDelegatedTokenIssuer(platform providers.DelegatedTokenIssuer, organization orgBackendTokenIssuer) providers.DelegatedTokenIssuer {
	return providers.DelegatedTokenIssuerFunc(func(ctx context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error) {
		if provider.OrgUUID != "" {
			if organization == nil {
				return "", time.Time{}, fmt.Errorf("organization provider identity issuer is unavailable")
			}
			return organization.IssueOrgBackendToken(ctx, orgUUID, wsUUID, user, provider)
		}
		if platform == nil {
			return "", time.Time{}, fmt.Errorf("platform provider delegated identity issuer is unavailable")
		}
		return platform.IssueDelegatedUserToken(ctx, orgUUID, wsUUID, user, provider)
	})
}
