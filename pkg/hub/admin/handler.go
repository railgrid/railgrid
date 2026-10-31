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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/hub/controllers/softdelete"
	"github.com/railgrid/railgrid/pkg/hub/metering"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// Handler serves the /api/admin/* endpoints.
type Handler struct {
	svc        *Service
	userClient *railgridclient.Client
	registry   *providers.Registry
	// gracePeriod mirrors the hub's --soft-delete-grace-period: how long
	// after a soft-delete the admin undelete routes still accept a
	// restore, and the window the listing reports as purgeAt. Zero means
	// softdelete.DefaultGracePeriod.
	gracePeriod time.Duration
	// planExists validates a plan name before it is written to
	// Organization.spec.plan. Nil means plan assignment is unavailable
	// (metering not configured) and the route answers 503.
	planExists func(ctx context.Context, name string) (bool, error)
}

// NewHandler builds an admin Handler.
func NewHandler(svc *Service, userClient *railgridclient.Client, registry *providers.Registry) *Handler {
	return &Handler{svc: svc, userClient: userClient, registry: registry}
}

// WithSoftDeleteGracePeriod sets the recovery window the undelete routes
// enforce and the listing reports. Wire it to the value the softdelete
// reconciler runs with; non-positive keeps softdelete.DefaultGracePeriod.
func (h *Handler) WithSoftDeleteGracePeriod(d time.Duration) *Handler {
	h.gracePeriod = d
	return h
}

// WithPlanLookup enables PUT /organizations/{name}/plan with the given
// existence check (metering.PlanExists in production).
func (h *Handler) WithPlanLookup(exists func(ctx context.Context, name string) (bool, error)) *Handler {
	h.planExists = exists
	return h
}

// GracePeriod returns the configured recovery window, falling back to
// softdelete.DefaultGracePeriod.
func (h *Handler) GracePeriod() time.Duration {
	if h.gracePeriod <= 0 {
		return softdelete.DefaultGracePeriod
	}
	return h.gracePeriod
}

// Register mounts the admin routes on r (already gated by the admin Middleware).
func (h *Handler) Register(r *mux.Router) {
	// Cheap probe the portal calls to decide whether to show the /bonkers menu
	// item + allow the route. Reaching this handler means the caller passed the
	// admin gate; non-admins get 403, a disabled admin surface gives 404.
	r.HandleFunc("/access", h.access).Methods(http.MethodGet)
	r.HandleFunc("/users", h.listUsers).Methods(http.MethodGet)
	r.HandleFunc("/organizations", h.listOrganizations).Methods(http.MethodGet)
	// Undelete is the admin-side twin of POST /api/orgs/{org}/undelete and
	// POST /api/orgs/{org}/workspaces/{ws}/undelete. Those require the
	// caller to hold admin in the Org; a platform admin usually is not a
	// member, so the admin area restores through kcp-admin credentials
	// instead. Same rule as the tenant routes: only the recovery window is
	// cancelled, nothing is rebuilt once the cascade has run, and a
	// restore past the window is refused with 409.
	r.HandleFunc("/organizations/{name}/undelete", h.undeleteOrganization).Methods(http.MethodPost)
	r.HandleFunc("/organizations/{name}/workspaces/{ws}/undelete", h.undeleteOrgWorkspace).Methods(http.MethodPost)
	// Plan assignment detaches an organization from its creator's pooled
	// metering account onto its own Entitlement on the named plan (empty
	// pools it back). The hub's metering reconciler does the moving; this
	// route only records intent on Organization.spec.plan, after checking
	// the plan exists.
	r.HandleFunc("/organizations/{name}/plan", h.setOrganizationPlan).Methods(http.MethodPut)
	r.HandleFunc("/providers", h.listProviders).Methods(http.MethodGet)
	// Provisioning is declarative: creating a Provider object in
	// root:railgrid:system:providers drives the Provider reconciler
	// (pkg/hub/providers/provider_controller.go) to create the sub-workspace +
	// ServiceAccount + kubeconfig Secret. These endpoints just create/delete
	// that object — they do no provisioning themselves.
	r.HandleFunc("/providers", h.createProvider).Methods(http.MethodPost)
	r.HandleFunc("/providers/{name}", h.deleteProvider).Methods(http.MethodDelete)
	r.HandleFunc("/providers/{name}/kubeconfig", h.providerKubeconfig).Methods(http.MethodGet)
	// Rotation is a POST, not another GET on the kubeconfig route: the
	// kubeconfig endpoint deliberately returns the SAME credential every time
	// (re-fetching must not multiply live tokens), so "give me a different one"
	// has to be a separate, explicit, non-idempotent call.
	r.HandleFunc("/providers/{name}/credentials/rotate", h.rotateProviderCredential).Methods(http.MethodPost)
}

type userDTO struct {
	Name         string `json:"name"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
	RBACIdentity string `json:"rbacIdentity"`
}

// accessDTO is the body of GET /api/admin/access. Reaching the handler already
// proves admin, so `admin` is always true; the portal only checks the status
// code. kubeconfigServers rides along so the providers table can offer just the
// download addresses this hub can actually produce.
type accessDTO struct {
	Admin             bool     `json:"admin"`
	KubeconfigServers []string `json:"kubeconfigServers"`
}

func (h *Handler) access(w http.ResponseWriter, _ *http.Request) {
	modes := h.svc.AvailableKubeconfigServerModes()
	names := make([]string, 0, len(modes))
	for _, m := range modes {
		names = append(names, string(m))
	}
	writeJSON(w, accessDTO{Admin: true, KubeconfigServers: names})
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
	Name          string         `json:"name"`
	DisplayName   string         `json:"displayName"`
	WorkspacePath string         `json:"workspacePath"`
	Workspaces    []workspaceDTO `json:"workspaces"`
	// DeletionRequestedAt is set while the Org is soft-deleted; PurgeAt is
	// when its cascade runs (DeletionRequestedAt + the grace period) and so
	// the last moment an undelete is accepted.
	DeletionRequestedAt *string `json:"deletionRequestedAt,omitempty"`
	PurgeAt             *string `json:"purgeAt,omitempty"`
	// Account is the metering Account the organization's usage pools into
	// (metering.OwnerAccount); OwnAccount says whether that account is the
	// organization's own rather than its creator's. Plan is spec.plan, set
	// when an admin detached it onto a plan.
	Account    string `json:"account"`
	OwnAccount bool   `json:"ownAccount"`
	Plan       string `json:"plan,omitempty"`
	CreatedBy  string `json:"createdBy,omitempty"`
}

type workspaceDTO struct {
	UUID                string   `json:"uuid"`
	DisplayName         string   `json:"displayName"`
	ClusterName         string   `json:"clusterName"`
	Providers           []string `json:"providers"`
	DeletionRequestedAt *string  `json:"deletionRequestedAt,omitempty"`
	PurgeAt             *string  `json:"purgeAt,omitempty"`
}

// softDeleteStamps renders a soft-delete timestamp and the matching purge
// time for a DTO.
func (h *Handler) softDeleteStamps(requestedAt time.Time) (requested, purge *string) {
	r := requestedAt.UTC().Format(time.RFC3339)
	p := requestedAt.Add(h.GracePeriod()).UTC().Format(time.RFC3339)
	return &r, &p
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
		dto := orgDTO{
			Name:          o.Name,
			DisplayName:   o.Spec.DisplayName,
			WorkspacePath: o.Status.WorkspacePath,
			Workspaces:    []workspaceDTO{},
		}
		if o.Status.DeletionRequestedAt != nil {
			dto.DeletionRequestedAt, dto.PurgeAt = h.softDeleteStamps(o.Status.DeletionRequestedAt.Time)
		}
		dto.Account, dto.OwnAccount = metering.OwnerAccount(o)
		dto.Plan = o.Spec.Plan
		dto.CreatedBy = o.Labels[tenancyv1alpha1.OrganizationCreatorLabel]
		// Best-effort: enumerate the org's child workspaces and their
		// enabled providers. An org whose workspace tree can't be read
		// (e.g. mid-provisioning) still renders with an empty list.
		if wss, err := h.svc.ListOrgWorkspaces(r.Context(), o.Name); err == nil {
			for _, ws := range wss {
				wd := workspaceDTO{
					UUID:        ws.UUID,
					DisplayName: ws.DisplayName,
					ClusterName: ws.ClusterName,
					Providers:   ws.Providers,
				}
				if wd.Providers == nil {
					wd.Providers = []string{}
				}
				if ws.DeletionRequestedAt != nil {
					wd.DeletionRequestedAt, wd.PurgeAt = h.softDeleteStamps(*ws.DeletionRequestedAt)
				}
				dto.Workspaces = append(dto.Workspaces, wd)
			}
		}
		items = append(items, dto)
	}
	writeJSON(w, map[string]any{"items": items})
}

// undeleteOrganization clears Organization.status.deletionRequestedAt while
// the recovery window is open. 200 with the Org's current soft-delete state
// (idempotent when it was never deleted), 409 once the window has elapsed:
// the reconciler is cascading by then, and clearing the stamp under it
// would leave a half-torn-down Org that looks alive.
func (h *Handler) undeleteOrganization(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, http.StatusBadRequest, "organization name is required")
		return
	}
	// The softdelete reconciler updates status concurrently (conditions,
	// UMI stamps), so a plain get-then-update races it; re-read and retry
	// on conflict, bounded.
	const maxAttempts = 5
	var lastErr error
	for range maxAttempts {
		org, err := h.userClient.Organizations().Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				writeError(w, http.StatusNotFound, fmt.Sprintf("organization %q not found", name))
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if org.Status.DeletionRequestedAt == nil {
			writeJSON(w, map[string]any{"name": org.Name, "restored": false})
			return
		}
		if !time.Now().UTC().Before(org.Status.DeletionRequestedAt.Add(h.GracePeriod())) {
			writeError(w, http.StatusConflict, "organization deletion grace period has expired; the cascade has started")
			return
		}
		org.Status.DeletionRequestedAt = nil
		if _, err := h.userClient.Organizations().UpdateStatus(r.Context(), org, metav1.UpdateOptions{}); err != nil {
			lastErr = err
			if apierrors.IsConflict(err) {
				continue
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"name": org.Name, "restored": true})
		return
	}
	writeError(w, http.StatusConflict, fmt.Sprintf("organization %q changed while restoring; retry: %v", name, lastErr))
}

// planRequest is the body of PUT /organizations/{name}/plan.
type planRequest struct {
	Plan string `json:"plan"`
}

// setOrganizationPlan records Organization.spec.plan. 200 with the new
// account/plan projection, 400 for an unknown plan, 404 for an unknown
// organization, 503 when metering is not configured.
func (h *Handler) setOrganizationPlan(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, http.StatusBadRequest, "organization name is required")
		return
	}
	if h.planExists == nil {
		writeError(w, http.StatusServiceUnavailable, "plan assignment needs metering, which is not configured on this hub")
		return
	}
	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Plan != "" {
		ok, err := h.planExists(r.Context(), req.Plan)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("plan %q does not exist", req.Plan))
			return
		}
	}
	const maxAttempts = 5
	var lastErr error
	for range maxAttempts {
		org, err := h.userClient.Organizations().Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				writeError(w, http.StatusNotFound, fmt.Sprintf("organization %q not found", name))
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if org.Spec.Plan != req.Plan {
			org.Spec.Plan = req.Plan
			if org, err = h.userClient.Organizations().Update(r.Context(), org, metav1.UpdateOptions{}); err != nil {
				lastErr = err
				if apierrors.IsConflict(err) {
					continue
				}
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		account, own := metering.OwnerAccount(org)
		writeJSON(w, map[string]any{"name": org.Name, "plan": org.Spec.Plan, "account": account, "ownAccount": own})
		return
	}
	writeError(w, http.StatusConflict, fmt.Sprintf("organization %q changed while updating; retry: %v", name, lastErr))
}

// undeleteOrgWorkspace clears the deletion-requested-at annotation on the
// child Workspace while its recovery window is open. 204 on success or when
// the workspace carries no soft-delete stamp (a workspace that does not
// exist reads as unstamped, which is the same answer: nothing to restore),
// 404 only if it vanished between the read and the clear, 409 once the
// window has elapsed.
func (h *Handler) undeleteOrgWorkspace(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	ws := mux.Vars(r)["ws"]
	if name == "" || ws == "" {
		writeError(w, http.StatusBadRequest, "organization name and workspace uuid are required")
		return
	}
	err := h.svc.UndeleteOrgWorkspace(r.Context(), name, ws, h.GracePeriod())
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrGracePeriodExpired):
		writeError(w, http.StatusConflict, "workspace deletion grace period has expired; the cascade has started")
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, fmt.Sprintf("workspace %q in organization %q not found", ws, name))
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
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
		// Platform providers only. This surface is about platform onboarding —
		// it pairs registry records with workspaces under root:railgrid:providers,
		// which org-owned providers have none of. Including them would also let
		// two Orgs that both named a provider "vault" collapse onto one row
		// whose contents depend on map iteration order. Org-owned providers are
		// managed by their Org at /api/orgs/{org}/providers.
		if p.OrgUUID != "" {
			continue
		}
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

// createProviderRequest is the body of POST /api/admin/providers.
type createProviderRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// createProvider creates a Provider object in root:railgrid:system:providers. The
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
// the Secret the Provider controller wrote into root:railgrid:system:providers.
// 404 if the Provider isn't provisioned yet (no Secret).
//
// The optional `server` query parameter re-points the server URL for this
// download only (the Secret is never modified): `internal` for a provider
// installed into this cluster, `external` for one running outside it. Omitted
// means "as minted". 400 on an unknown mode or one this hub has no URL for.
func (h *Handler) providerKubeconfig(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, http.StatusBadRequest, "provider name is required")
		return
	}
	mode, err := ParseKubeconfigServerMode(r.URL.Query().Get("server"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kc, err := h.svc.GetProviderKubeconfig(r.Context(), name, mode)
	if err != nil {
		// An unconfigured address is a bad request, not a server fault: the
		// caller asked for something this deployment cannot serve.
		if errors.Is(err, ErrServerModeUnavailable) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(kc) == 0 {
		writeError(w, http.StatusNotFound, "kubeconfig not available yet — provider not provisioned")
		return
	}
	// Distinct filenames so a downloads folder holding both stays legible.
	filename := name + "-kubeconfig.yaml"
	if mode != ServerModeAsMinted {
		filename = fmt.Sprintf("%s-kubeconfig-%s.yaml", name, mode)
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(kc)
}

// rotateProviderCredentialDTO is the body of
// POST /api/admin/providers/{name}/credentials/rotate.
type rotateProviderCredentialDTO struct {
	Name       string `json:"name"`
	Kubeconfig string `json:"kubeconfig"`
	// PreviousValidUntil is when the credential this call replaced stops
	// working, RFC3339. Empty when there was nothing to retire. It is the
	// deadline for rolling the provider onto the new kubeconfig — until then
	// both authenticate as the same ServiceAccount, so the provider keeps
	// working (its heartbeat included) on either.
	PreviousValidUntil string `json:"previousValidUntil,omitempty"`
	RotatedAt          string `json:"rotatedAt"`
}

// rotateProviderCredential mints a second ServiceAccount token for a platform
// provider, makes it the credential the hub hands out, and puts the previous
// one on a deletion clock. Returns the new kubeconfig in the response body —
// the only time it is shown, exactly like registration.
func (h *Handler) rotateProviderCredential(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, http.StatusBadRequest, "provider name is required")
		return
	}
	mode, err := ParseKubeconfigServerMode(r.URL.Query().Get("server"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kc, rotated, err := h.svc.RotateProviderCredential(r.Context(), name, mode)
	if err != nil {
		if errors.Is(err, ErrServerModeUnavailable) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
