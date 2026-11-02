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

package restapi

import (
	"context"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/railgrid/pkg/hub/providerinstall"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

type startInstallationRequest struct {
	Edge  *EdgeTargetRef `json:"edge,omitempty"`
	Retry bool           `json:"retry,omitempty"`
}

func (h *Handler) startOrgProviderInstallation(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgProviderAccess(w, r, true)
	if !ok {
		return
	}
	if h.mgr.providerInstaller == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "console installation is unavailable")
		return
	}
	name := mux.Vars(r)["name"]
	if err := h.validateOrgProviderName(name); err != nil {
		writeError(w, err)
		return
	}
	var req startInstallationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ws, err := h.mgr.orgProviders.GetOrgProviderWorkspace(r.Context(), tc.OrgUUID, name)
	if err != nil {
		writeError(w, err)
		return
	}
	var plan *providerinstall.Plan
	if ws != nil {
		target, err := h.mgr.providerInstaller.Target(r.Context(), tc.OrgUUID, name)
		if err != nil {
			writeError(w, err)
			return
		}
		if target == nil {
			writeError(w, newValidationError("this registration has no Edge; use manual installation"))
			return
		}
		if req.Edge != nil && (req.Edge.Name != target.EdgeName || req.Edge.Workspace != target.WorkspaceUUID) {
			writeStatus(w, http.StatusConflict, "Conflict", "resume installation on its original Edge")
			return
		}
		req.Edge = &EdgeTargetRef{Workspace: target.WorkspaceUUID, Name: target.EdgeName}
	}
	// A workspace-admin header must not enumerate the organization's other
	// workspaces. Resolve visibility using the caller's organization role.
	orgRole, roleErr := h.mgr.bootstrapper.GetOrgMembershipRole(r.Context(), tc.OrgUUID, tc.User)
	if roleErr != nil && !apierrors.IsNotFound(roleErr) {
		writeError(w, roleErr)
		return
	}
	edgeContext := tc
	edgeContext.Role = orgRole
	if !h.requireEdgeInstallTarget(w, r, edgeContext, req.Edge) {
		return
	}
	caller, err := h.mgr.client.Users().Get(r.Context(), tc.User, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if caller.Spec.RBACIdentity == "" {
		writeStatus(w, http.StatusForbidden, "Forbidden", "caller has no Kubernetes identity")
		return
	}
	if err := h.mgr.providerInstaller.Authorize(r.Context(), tc.OrgUUID, req.Edge.Workspace, req.Edge.Name, caller.Spec.RBACIdentity); err != nil {
		writeError(w, err)
		return
	}
	// Once authorized, finish submitting the durable Job even if a browser
	// refresh closes this HTTP connection. Preparation remains time-bounded.
	operationContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	r = r.WithContext(operationContext)
	needsPlan := ws == nil
	if ws != nil {
		status, err := h.mgr.providerInstaller.Status(r.Context(), tc.OrgUUID, name, h.orgProviderView(tc.OrgUUID, *ws).Ready)
		if err != nil {
			writeError(w, err)
			return
		}
		needsPlan = status.Phase == "NotStarted"
	}
	if needsPlan {
		recipe := h.installInstructions(r.Context(), tc.OrgUUID, name, name)
		if recipe == nil {
			writeError(w, newValidationError("this provider has no installable platform recipe"))
			return
		}
		plan, err = providerinstall.ResolvePlan(r.Context(), *recipe)
		if err != nil {
			writeError(w, newValidationError(err.Error()))
			return
		}
	}
	// Registration and credentials remain server side for console installation.
	// No workspace APIBinding or Enable endpoint is involved.
	if _, err = h.mgr.orgProviders.EnsureOrgProviderWorkspace(r.Context(), tc.OrgUUID, name); err != nil {
		writeError(w, err)
		return
	}
	if err = h.mgr.orgProviders.RecordProviderEdgeBinding(r.Context(), tc.OrgUUID, name, req.Edge.Workspace, req.Edge.Name); err != nil {
		writeError(w, err)
		return
	}
	path := kcppaths.OrgProviderPath(tc.OrgUUID, name)
	if err = h.mgr.providerCreds.EnsureProviderSAAtPath(r.Context(), path); err != nil {
		writeError(w, err)
		return
	}
	credential, err := h.mgr.providerCreds.MintProviderKubeconfigAtPath(r.Context(), path, h.mgr.kubeconfig.HubExternalURL)
	if err != nil {
		writeError(w, err)
		return
	}
	status, err := h.mgr.providerInstaller.Start(r.Context(), tc.OrgUUID, name, plan, credential, req.Retry)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}
func (h *Handler) getOrgProviderInstallation(w http.ResponseWriter, r *http.Request) {
	// Organization members can read progress without gaining permission to
	// start an installation. Status never returns credentials or pod logs.
	tc, ok := h.requireOrgProviderAccess(w, r, false)
	if !ok {
		return
	}
	if h.mgr.providerInstaller == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "console installation is unavailable")
		return
	}
	name := mux.Vars(r)["name"]
	ws, err := h.mgr.orgProviders.GetOrgProviderWorkspace(r.Context(), tc.OrgUUID, name)
	if err != nil {
		writeError(w, err)
		return
	}
	if ws == nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "provider is not registered")
		return
	}
	status, err := h.mgr.providerInstaller.Status(r.Context(), tc.OrgUUID, name, h.orgProviderView(tc.OrgUUID, *ws).Ready)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
