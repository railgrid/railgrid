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

package admin

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/providers"
)

// Handler serves the /api/admin/* endpoints.
type Handler struct {
	svc        *Service
	userClient *kedgeclient.Client
	registry   *providers.Registry
}

// NewHandler builds an admin Handler.
func NewHandler(svc *Service, userClient *kedgeclient.Client, registry *providers.Registry) *Handler {
	return &Handler{svc: svc, userClient: userClient, registry: registry}
}

// Register mounts the admin routes on r (already gated by the admin Middleware).
func (h *Handler) Register(r *mux.Router) {
	// Cheap probe the portal calls to decide whether to show the /bonkers menu
	// item + allow the route. Reaching this handler means the caller passed the
	// admin gate; non-admins get 403, a disabled admin surface gives 404.
	r.HandleFunc("/access", h.access).Methods(http.MethodGet)
	r.HandleFunc("/users", h.listUsers).Methods(http.MethodGet)
	r.HandleFunc("/organizations", h.listOrganizations).Methods(http.MethodGet)
	r.HandleFunc("/providers", h.listProviders).Methods(http.MethodGet)
	r.HandleFunc("/identities", h.listIdentities).Methods(http.MethodGet)
	// Provisioning is declarative: creating a Provider object in
	// root:kedge:system:providers drives the Provider reconciler
	// (pkg/hub/providers/provider_controller.go) to create the sub-workspace +
	// ServiceAccount + kubeconfig Secret. These endpoints just create/delete
	// that object — they do no provisioning themselves.
	r.HandleFunc("/providers", h.createProvider).Methods(http.MethodPost)
	r.HandleFunc("/providers/{name}", h.deleteProvider).Methods(http.MethodDelete)
	r.HandleFunc("/providers/{name}/kubeconfig", h.providerKubeconfig).Methods(http.MethodGet)
}

type userDTO struct {
	Name         string `json:"name"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
	RBACIdentity string `json:"rbacIdentity"`
}

func (h *Handler) access(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]bool{"admin": true})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := h.userClient.Users().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]userDTO, 0, len(list.Items))
	for i := range list.Items {
		u := &list.Items[i]
		items = append(items, userDTO{
			Name:         u.Name,
			Email:        u.Spec.Email,
			DisplayName:  u.Spec.Name,
			RBACIdentity: u.Spec.RBACIdentity,
		})
	}
	writeJSON(w, map[string]any{"items": items})
}

type orgDTO struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	WorkspacePath string `json:"workspacePath"`
}

func (h *Handler) listOrganizations(w http.ResponseWriter, r *http.Request) {
	list, err := h.userClient.Organizations().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]orgDTO, 0, len(list.Items))
	for i := range list.Items {
		o := &list.Items[i]
		items = append(items, orgDTO{
			Name:          o.Name,
			DisplayName:   o.Spec.DisplayName,
			WorkspacePath: o.Status.WorkspacePath,
		})
	}
	writeJSON(w, map[string]any{"items": items})
}

type adminProviderDTO struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName"`
	Category         string `json:"category"`
	Version          string `json:"version"`
	Ready            bool   `json:"ready"`
	APIExportName    string `json:"apiExportName"`
	APIExportPath    string `json:"apiExportPath"`
	WorkspaceCluster string `json:"workspaceCluster"`
	// Registered: a CatalogEntry exists in the registry (chart installed).
	Registered bool `json:"registered"`
	// Onboarded: a provider workspace exists (admin onboarding has run).
	Onboarded bool `json:"onboarded"`
	// Builtin: a first-party provider compiled into the hub (kubernetesedges,
	// serveredges, mcp). Builtins are bootstrapped by the hub and must NOT be
	// onboarded via this flow — the UI hides the onboard action for them.
	Builtin bool `json:"builtin"`
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	// Merge two sources: registered CatalogEntries (chart installed → in the
	// registry) and onboarded workspaces (admin onboarding ran, possibly before
	// the chart exists). Keyed by name so a provider shows up after EITHER step.
	byName := map[string]*adminProviderDTO{}
	for _, p := range h.registry.List() {
		_, builtin := providers.BuiltinByName(p.Name)
		byName[p.Name] = &adminProviderDTO{
			Name:             p.Name,
			DisplayName:      p.DisplayName,
			Category:         p.Category,
			Version:          p.Version,
			Ready:            p.Ready(),
			APIExportName:    p.APIExportName,
			APIExportPath:    p.APIExportPath,
			WorkspaceCluster: p.WorkspaceCluster,
			Registered:       true,
			Builtin:          builtin,
		}
	}
	if ws, err := h.svc.ListOnboardedWorkspaces(r.Context()); err == nil {
		for _, o := range ws {
			d := byName[o.Name]
			if d == nil {
				_, builtin := providers.BuiltinByName(o.Name)
				d = &adminProviderDTO{Name: o.Name, DisplayName: o.Name, Builtin: builtin}
				byName[o.Name] = d
			}
			d.Onboarded = true
			if d.WorkspaceCluster == "" {
				d.WorkspaceCluster = o.Cluster
			}
		}
	}
	items := make([]adminProviderDTO, 0, len(byName))
	for _, d := range byName {
		items = append(items, *d)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) listIdentities(w http.ResponseWriter, r *http.Request) {
	ids, err := h.svc.ListRootIdentities(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": ids})
}

// createProviderRequest is the body of POST /api/admin/providers.
type createProviderRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// createProvider creates a Provider object in root:kedge:system:providers. The
// Provider reconciler then provisions the sub-workspace + ServiceAccount +
// kubeconfig Secret. Idempotent.
func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.svc.CreateProvider(r.Context(), req.Name, req.DisplayName); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"name": req.Name})
}

// deleteProvider removes a Provider object; the reconciler's finalizer tears
// down the provisioned sub-workspace.
func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, http.StatusBadRequest, "provider name is required")
		return
	}
	if err := h.svc.DeleteProvider(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// providerKubeconfig streams the minted kubeconfig for a provider, read from
// the Secret the Provider controller wrote into root:kedge:system:providers.
// 404 if the Provider isn't provisioned yet (no Secret).
func (h *Handler) providerKubeconfig(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, http.StatusBadRequest, "provider name is required")
		return
	}
	kc, err := h.svc.GetProviderKubeconfig(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(kc) == 0 {
		writeError(w, http.StatusNotFound, "kubeconfig not available yet — provider not provisioned")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-kubeconfig.yaml"`)
	_, _ = w.Write(kc)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
