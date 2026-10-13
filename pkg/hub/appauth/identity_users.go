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

package appauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const identityUserLookupRoute = "/{org}/workspaces/{workspace}/app-auth/user"

// IdentityAccount is the minimal existing platform-user data needed to write
// a workspace-local app-access binding. Email and the User CR itself never
// leave the resolver.
type IdentityAccount struct {
	User         string
	Email        string
	RBACIdentity string
	Deleted      bool
}

// IdentityAccountLookup performs a direct lookup by stable User CR name. It
// must not list users, create users, or resolve arbitrary identifiers.
type IdentityAccountLookup func(context.Context, string) (IdentityAccount, error)

// IdentityWorkspaceCluster resolves an org/workspace UUID pair to its kcp
// logical-cluster ID after the caller has been admitted by tenant middleware.
type IdentityWorkspaceCluster func(context.Context, string, string) (string, error)

// IdentityUserLookupConfig assembles the narrow app-grant account resolver.
type IdentityUserLookupConfig struct {
	SARClient        SARFactory
	IdentityApp      IdentityAppResolver
	WorkspaceCluster IdentityWorkspaceCluster
	AccountLookup    IdentityAccountLookup
}

// IdentityUserLookupHandler returns an existing account's workspace RBAC
// identity only after an App Studio authentication-grant proof, an accepted
// hub-access capability, and a human SAR update on the exact enabled app
// instance.
type IdentityUserLookupHandler struct {
	sarClient        SARFactory
	identityApp      IdentityAppResolver
	workspaceCluster IdentityWorkspaceCluster
	accountLookup    IdentityAccountLookup
}

type identityUserLookupRequest struct {
	User     string `json:"user"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Name     string `json:"name"`
	AppID    string `json:"appId"`
}

type identityUserLookupResponse struct {
	User         string `json:"user"`
	RBACIdentity string `json:"rbacIdentity"`
}

// NewIdentityUserLookupHandler validates the dependencies for this one
// route. The route is mounted separately under the hub-access and tenant
// middlewares, so callers must register it only on that protected router.
func NewIdentityUserLookupHandler(cfg IdentityUserLookupConfig) (*IdentityUserLookupHandler, error) {
	if cfg.SARClient == nil || cfg.IdentityApp == nil || cfg.WorkspaceCluster == nil || cfg.AccountLookup == nil {
		return nil, fmt.Errorf("appauth: SAR, app descriptor, workspace cluster, and account lookup are required")
	}
	return &IdentityUserLookupHandler{
		sarClient:        cfg.SARClient,
		identityApp:      cfg.IdentityApp,
		workspaceCluster: cfg.WorkspaceCluster,
		accountLookup:    cfg.AccountLookup,
	}, nil
}

// Register mounts the account resolver on a /api/orgs subrouter that has
// both hubaccess.Gate and tenant.Middleware installed.
func (h *IdentityUserLookupHandler) Register(router *mux.Router) {
	if h == nil || router == nil {
		return
	}
	router.HandleFunc(identityUserLookupRoute, h.Handle).Methods(http.MethodPost)
}

// Handle resolves one existing User CR for an enabled production app. The
// action proof binds {user,appId}; this request adds the exact Infrastructure
// Instance coordinates that must pass a human update SAR.
func (h *IdentityUserLookupHandler) Handle(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		http.Error(w, "invalid app account lookup request", http.StatusBadRequest)
		return
	}
	tc, ok := tenant.FromContext(r.Context())
	call, callOK := tenant.DelegatedCallFrom(r.Context())
	if !ok || !callOK || !call.ActionProof || call.Provider != "app-studio" ||
		call.Capability != "app-auth.users.resolve" || call.Scope != "workspace" ||
		call.User == "" || call.User != tc.User || call.RBACIdentity == "" ||
		call.AppAccessOperation != "create" || call.AppAccessUser != call.ActionUser ||
		call.AppAccessProjectUID == "" || call.AppAccessProject == "" ||
		call.AppAccessGroup != "infrastructure.railgrid.ai" || call.AppAccessResource != "instances" ||
		call.AppAccessName == "" || call.AppAccessUID == "" ||
		tc.OrgUUID == "" || tc.WorkspaceUUID == "" {
		http.Error(w, "app account lookup is not authorized", http.StatusForbidden)
		return
	}
	vars := mux.Vars(r)
	if vars["org"] != tc.OrgUUID || vars["workspace"] != tc.WorkspaceUUID {
		http.Error(w, "app account lookup is not authorized", http.StatusForbidden)
		return
	}

	var req identityUserLookupRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "malformed app account lookup request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "malformed app account lookup request", http.StatusBadRequest)
		return
	}
	if !segmentRE.MatchString(req.User) || !validAppID(req.AppID) ||
		!strings.HasSuffix(req.AppID, "/production") || req.Group != "infrastructure.railgrid.ai" ||
		req.Resource != "instances" || !segmentRE.MatchString(req.Name) {
		http.Error(w, "invalid app account lookup request", http.StatusBadRequest)
		return
	}
	if call.ActionUser != req.User || call.ActionAppID != req.AppID ||
		req.Group != call.AppAccessGroup || req.Resource != call.AppAccessResource ||
		req.Name != call.AppAccessName || req.AppID != call.AppAccessProjectUID+"/production" {
		http.Error(w, "app account lookup is not authorized", http.StatusForbidden)
		return
	}

	cluster, err := h.workspaceCluster(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil || !segmentRE.MatchString(cluster) {
		http.Error(w, "app account lookup is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	ref := InstanceRef{Cluster: cluster, Group: req.Group, Resource: req.Resource, Name: req.Name}
	if ref.validate() != nil {
		http.Error(w, "invalid app account lookup request", http.StatusBadRequest)
		return
	}

	client, err := h.sarClient(cluster)
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	review, err := client.Create(r.Context(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   call.RBACIdentity,
			Groups: append([]string(nil), call.Groups...),
			Extra:  extraToSAR(call.Extra),
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:    ref.Group,
				Resource: ref.Resource,
				Name:     ref.Name,
				Verb:     "update",
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if !review.Status.Allowed {
		http.Error(w, "app account lookup is not authorized", http.StatusForbidden)
		return
	}

	app, err := h.identityApp(r.Context(), ref)
	if err != nil {
		http.Error(w, "app identity is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !validIdentityApp(app) || app.Environment != "production" || app.AppID != req.AppID ||
		app.InstanceUID != call.AppAccessUID {
		http.Error(w, "Railgrid Auth is not enabled for this production app", http.StatusForbidden)
		return
	}

	account, err := h.accountLookup(r.Context(), req.User)
	if apierrors.IsNotFound(err) || (err == nil && (account.User != req.User || account.Deleted || account.RBACIdentity == "")) {
		http.Error(w, "existing app account is unavailable", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "existing app account is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(identityUserLookupResponse{User: account.User, RBACIdentity: account.RBACIdentity})
}

func extraToSAR(extra map[string][]string) map[string]authorizationv1.ExtraValue {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]authorizationv1.ExtraValue, len(extra))
	for key, values := range extra {
		out[key] = authorizationv1.ExtraValue(append([]string(nil), values...))
	}
	return out
}
