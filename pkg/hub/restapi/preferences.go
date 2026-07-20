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

package restapi

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// Dashboard layout persistence. The portal remembers each user's
// customised dashboard (tile geometry, hidden set, chosen column count)
// server-side rather than in localStorage, so a layout follows the user
// across browsers and devices. State is stored on a single per-user
// UserPreferences CR (metadata.name = User CR name) in
// root:kedge:system:tenants, with one entry per workspace. These handlers
// project just the active workspace's slice in and out.

// dashboardTileBody is one tile's grid placement on the wire. Mirrors the
// portal's TileLayout: x/y are column/row units, w/h spans.
type dashboardTileBody struct {
	Name string `json:"name"`
	X    int32  `json:"x"`
	Y    int32  `json:"y"`
	W    int32  `json:"w"`
	H    int32  `json:"h"`
}

// dashboardLayoutBody is the GET response / PUT request for a single
// workspace's dashboard layout. Slices are always non-nil in responses so
// the portal can treat them uniformly.
type dashboardLayoutBody struct {
	GridColumns int32               `json:"gridColumns"`
	Tiles       []dashboardTileBody `json:"tiles"`
	Hidden      []string            `json:"hidden"`
	NoTile      []string            `json:"noTile"`
}

// getDashboardLayout: GET /{org}/workspaces/{ws}/dashboard/layout
//
// Returns the caller's remembered layout for this workspace, or an empty
// layout (200, all fields zero/empty) when the user has never customised
// it — the portal falls back to its default flow layout in that case.
func (h *Handler) getDashboardLayout(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	prefs, err := h.mgr.client.UserPreferences().Get(r.Context(), tc.User, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, emptyDashboardLayout())
			return
		}
		writeError(w, err)
		return
	}
	for i := range prefs.Spec.Dashboards {
		if prefs.Spec.Dashboards[i].WorkspaceUUID == tc.WorkspaceUUID {
			writeJSON(w, http.StatusOK, projectDashboard(&prefs.Spec.Dashboards[i]))
			return
		}
	}
	writeJSON(w, http.StatusOK, emptyDashboardLayout())
}

// putDashboardLayout: PUT /{org}/workspaces/{ws}/dashboard/layout
//
// Upserts this workspace's slice of the caller's UserPreferences,
// retrying on the get-modify-update conflict window.
func (h *Handler) putDashboardLayout(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	var body dashboardLayoutBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.GridColumns < 0 || body.GridColumns > 24 {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "gridColumns must be between 0 and 24")
		return
	}
	want := dashboardFromBody(tc.WorkspaceUUID, &body)
	if err := h.mgr.mutateUserPreferences(r.Context(), tc.User, func(p *tenancyv1alpha1.UserPreferences) bool {
		return upsertDashboard(p, want)
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectDashboard(&want))
}

// ===== projections =====

func emptyDashboardLayout() dashboardLayoutBody {
	return dashboardLayoutBody{Tiles: []dashboardTileBody{}, Hidden: []string{}, NoTile: []string{}}
}

func projectDashboard(d *tenancyv1alpha1.DashboardPreference) dashboardLayoutBody {
	out := dashboardLayoutBody{
		GridColumns: d.GridColumns,
		Tiles:       make([]dashboardTileBody, 0, len(d.Tiles)),
		Hidden:      append([]string{}, d.Hidden...),
		NoTile:      append([]string{}, d.NoTile...),
	}
	for _, t := range d.Tiles {
		out.Tiles = append(out.Tiles, dashboardTileBody{Name: t.Name, X: t.X, Y: t.Y, W: t.W, H: t.H})
	}
	return out
}

func dashboardFromBody(wsUUID string, b *dashboardLayoutBody) tenancyv1alpha1.DashboardPreference {
	d := tenancyv1alpha1.DashboardPreference{
		WorkspaceUUID: wsUUID,
		GridColumns:   b.GridColumns,
		Hidden:        append([]string{}, b.Hidden...),
		NoTile:        append([]string{}, b.NoTile...),
		UpdatedAt:     metav1.Now(),
	}
	for _, t := range b.Tiles {
		d.Tiles = append(d.Tiles, tenancyv1alpha1.DashboardTilePlacement{
			Name: t.Name, X: t.X, Y: t.Y, W: t.W, H: t.H,
		})
	}
	return d
}

// ===== UserPreferences mutation =====

// mutateUserPreferences fetches the caller's UserPreferences, applies
// mutator, and writes back. NotFound is treated as create-on-write (the
// user has no preferences object yet). Returns nil if mutator reports no
// change. Retries on conflict — a debounced portal save can race a prior
// in-flight write from the same user.
func (m *Manager) mutateUserPreferences(ctx context.Context, userName string, mutator func(*tenancyv1alpha1.UserPreferences) bool) error {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		prefs, err := m.client.UserPreferences().Get(ctx, userName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting UserPreferences %q: %w", userName, err)
		}
		if apierrors.IsNotFound(err) {
			prefs = &tenancyv1alpha1.UserPreferences{ObjectMeta: metav1.ObjectMeta{Name: userName}}
		}
		if !mutator(prefs) {
			return nil
		}
		prefs.Status.DashboardCount = int32(len(prefs.Spec.Dashboards))
		if prefs.ResourceVersion == "" {
			if _, err := m.client.UserPreferences().Create(ctx, prefs, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return fmt.Errorf("creating UserPreferences %q: %w", userName, err)
			}
			return nil
		}
		if _, err := m.client.UserPreferences().Update(ctx, prefs, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return fmt.Errorf("updating UserPreferences %q: %w", userName, err)
		}
		return nil
	}
	return fmt.Errorf("updating UserPreferences %q: gave up after %d conflicts", userName, maxAttempts)
}

// upsertDashboard replaces (or appends) the workspace entry in the
// UserPreferences spec. Returns true when the object changed.
func upsertDashboard(p *tenancyv1alpha1.UserPreferences, want tenancyv1alpha1.DashboardPreference) bool {
	for i := range p.Spec.Dashboards {
		if p.Spec.Dashboards[i].WorkspaceUUID == want.WorkspaceUUID {
			p.Spec.Dashboards[i] = want
			return true
		}
	}
	p.Spec.Dashboards = append(p.Spec.Dashboards, want)
	return true
}
