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

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// maxSeedRequestBytes bounds a seed save. A seed holds at most 32 manifests;
// a megabyte is far more than any real one and well under what kcp stores.
const maxSeedRequestBytes = 1 << 20

// registerPlatformServices mounts the routes for platform services that are
// not providers.
//
// System workspaces mirror the provider routes one for one, because they are
// the same operation at a different place in the workspace tree: create the
// object, download or rotate its kubeconfig, delete it.
//
// Workspace seeds are edited as a whole spec. POST creates and refuses an
// existing name; PUT replaces and refuses a missing one, so a typo in the URL
// cannot create a second seed that reaches every tenant workspace.
func (h *Handler) registerPlatformServices(r *mux.Router) {
	r.HandleFunc("/system-workspaces", h.listSystemWorkspaces).Methods(http.MethodGet)
	r.HandleFunc("/system-workspaces", h.createSystemWorkspace).Methods(http.MethodPost)
	r.HandleFunc("/system-workspaces/{name}", h.deleteSystemWorkspace).Methods(http.MethodDelete)
	r.HandleFunc("/system-workspaces/{name}/kubeconfig", h.systemWorkspaceKubeconfig).Methods(http.MethodGet)
	r.HandleFunc("/system-workspaces/{name}/credentials/rotate", h.rotateSystemWorkspaceCredential).Methods(http.MethodPost)

	r.HandleFunc("/workspace-seeds", h.listWorkspaceSeeds).Methods(http.MethodGet)
	r.HandleFunc("/workspace-seeds", h.createWorkspaceSeed).Methods(http.MethodPost)
	r.HandleFunc("/workspace-seeds/{name}", h.updateWorkspaceSeed).Methods(http.MethodPut)
	r.HandleFunc("/workspace-seeds/{name}", h.deleteWorkspaceSeed).Methods(http.MethodDelete)
}

// writeServiceError maps a Service error to a status code. The message is
// passed through: this surface is platform-admin only, and an admin fixing a
// seed needs kcp's own words.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrServerModeUnavailable):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrSystemWorkspaceInUse), apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		writeError(w, http.StatusConflict, err.Error())
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (h *Handler) listSystemWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListSystemWorkspaces(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

// createSystemWorkspace creates a SystemWorkspace object. The reconciler then
// provisions root:railgrid:system:<name>. Idempotent.
func (h *Handler) createSystemWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.svc.CreateSystemWorkspace(r.Context(), req.Name, req.DisplayName); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"name": req.Name})
}

// deleteSystemWorkspace removes a SystemWorkspace object. 409 while a
// WorkspaceSeed still references an export in its workspace.
func (h *Handler) deleteSystemWorkspace(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteSystemWorkspace(r.Context(), mux.Vars(r)["name"]); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// systemWorkspaceKubeconfig streams the minted kubeconfig, with the same
// `server` query parameter as the provider download. 404 until provisioned.
func (h *Handler) systemWorkspaceKubeconfig(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	mode, err := ParseKubeconfigServerMode(r.URL.Query().Get("server"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kc, err := h.svc.GetSystemWorkspaceKubeconfig(r.Context(), name, mode)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if len(kc) == 0 {
		writeError(w, http.StatusNotFound, "kubeconfig not available yet: system workspace not provisioned")
		return
	}
	filename := name + "-system-kubeconfig.yaml"
	if mode != ServerModeAsMinted {
		filename = fmt.Sprintf("%s-system-kubeconfig-%s.yaml", name, mode)
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(kc)
}

// rotateSystemWorkspaceCredential issues a new credential and returns the new
// kubeconfig once, in the shape the provider rotation uses.
func (h *Handler) rotateSystemWorkspaceCredential(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	mode, err := ParseKubeconfigServerMode(r.URL.Query().Get("server"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kc, rotated, err := h.svc.RotateSystemWorkspaceCredential(r.Context(), name, mode)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	dto := rotateProviderCredentialDTO{
		Name:       name,
		Kubeconfig: string(kc),
		RotatedAt:  rotated.RotatedAt.UTC().Format(time.RFC3339),
	}
	if !rotated.PreviousValidUntil.IsZero() {
		dto.PreviousValidUntil = rotated.PreviousValidUntil.UTC().Format(time.RFC3339)
	}
	writeJSON(w, dto)
}

func (h *Handler) listWorkspaceSeeds(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListWorkspaceSeeds(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

// saveWorkspaceSeedRequest is the body of POST /workspace-seeds and
// PUT /workspace-seeds/{name}. Spec is the seed's spec as YAML; Name is read
// on POST only.
type saveWorkspaceSeedRequest struct {
	Name string `json:"name"`
	Spec string `json:"spec"`
}

func (h *Handler) decodeSeedRequest(w http.ResponseWriter, r *http.Request) (*saveWorkspaceSeedRequest, bool) {
	var req saveWorkspaceSeedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSeedRequestBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return nil, false
	}
	return &req, true
}

// createWorkspaceSeed creates a WorkspaceSeed. 400 when the hub would refuse
// the spec, 409 when the name is taken.
func (h *Handler) createWorkspaceSeed(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeSeedRequest(w, r)
	if !ok {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	info, err := h.svc.SaveWorkspaceSeed(r.Context(), req.Name, req.Spec, true)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, info)
}

// updateWorkspaceSeed replaces a WorkspaceSeed's spec. 400 when the hub would
// refuse it, 404 when there is no such seed, 409 when it changed underneath.
func (h *Handler) updateWorkspaceSeed(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeSeedRequest(w, r)
	if !ok {
		return
	}
	info, err := h.svc.SaveWorkspaceSeed(r.Context(), mux.Vars(r)["name"], req.Spec, false)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, info)
}

// deleteWorkspaceSeed removes a WorkspaceSeed. Nothing it created in tenant
// workspaces is removed.
func (h *Handler) deleteWorkspaceSeed(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteWorkspaceSeed(r.Context(), mux.Vars(r)["name"]); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
