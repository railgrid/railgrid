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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:resource:scope=Cluster,shortName=uprefs
// +kubebuilder:printcolumn:name="Dashboards",type="integer",JSONPath=".status.dashboardCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserPreferences is the per-User bucket of UI state the portal wants to
// remember across browsers and devices: today the customised dashboard
// layout (tile geometry, hidden set, chosen column count) for every
// workspace the user visits. metadata.name matches the User's
// metadata.name; one UserPreferences exists per User, and it lives in the
// same hub-mediated workspace (root:kedge:system:tenants) as the User and
// UserMembershipIndex CRs.
//
// It is deliberately generic — a single per-user object the portal
// upserts through the hub REST surface (GET/PUT
// /api/orgs/{org}/workspaces/{ws}/dashboard/layout) — so other browser-
// local UI state (theme, dock, tenant selection) can migrate onto it later
// without a second CRD. The content is advisory: the set of tiles a user
// may actually see is still gated by the live provider catalog and the
// workspace's enablement bindings; this object only remembers arrangement.
type UserPreferences struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserPreferencesSpec   `json:"spec,omitempty"`
	Status            UserPreferencesStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserPreferencesList is a list of UserPreferences resources.
type UserPreferencesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UserPreferences `json:"items"`
}

// UserPreferencesSpec is the user-authored preference state.
type UserPreferencesSpec struct {
	// Dashboards holds the customised dashboard layout per workspace, keyed
	// by the workspace UUID. One entry per workspace the user has arranged;
	// workspaces the user never customised simply have no entry and fall
	// back to the default flow layout in the portal.
	//
	// +optional
	// +listType=map
	// +listMapKey=workspaceUUID
	Dashboards []DashboardPreference `json:"dashboards,omitempty"`
}

// UserPreferencesStatus is the observed state of a UserPreferences object.
type UserPreferencesStatus struct {
	// DashboardCount mirrors len(spec.dashboards) so `kubectl get` can show
	// it as a column without server-side computation.
	//
	// +optional
	DashboardCount int32 `json:"dashboardCount,omitempty"`
}

// DashboardPreference is one workspace's remembered dashboard arrangement.
type DashboardPreference struct {
	// WorkspaceUUID is the child Workspace this layout applies to
	// (Workspace.metadata.name / the portal's workspace UUID).
	//
	// +kubebuilder:validation:Required
	WorkspaceUUID string `json:"workspaceUUID"`

	// GridColumns is the column count the user last laid the grid out at.
	// Zero means "portal default" (the portal derives a responsive column
	// count from viewport width when this is unset).
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=24
	GridColumns int32 `json:"gridColumns,omitempty"`

	// Tiles is the placed geometry for each visible dashboard tile. The
	// tile Name is the provider name (the grid item id).
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	Tiles []DashboardTilePlacement `json:"tiles,omitempty"`

	// Hidden is the set of provider names the user explicitly removed from
	// the dashboard. They stay available to add back from the "Add tile"
	// menu while they remain live in the catalog.
	//
	// +optional
	// +listType=set
	Hidden []string `json:"hidden,omitempty"`

	// NoTile is the set of provider names that were probed and found to
	// ship no dashboard-tile element. Persisting it (rather than
	// re-probing each load) is what stops empty providers from flashing
	// into the grid and vanishing on every page load. Cleared when a
	// provider's bundle version changes, since a later version may add a
	// tile.
	//
	// +optional
	// +listType=set
	NoTile []string `json:"noTile,omitempty"`

	// UpdatedAt is when this workspace's layout was last written, used for
	// last-writer diagnostics.
	//
	// +optional
	UpdatedAt metav1.Time `json:"updatedAt,omitempty"`
}

// DashboardTilePlacement is one tile's placement in the grid. X/Y are
// column/row units; W/H are spans in those units — the same coordinate
// space the portal's grid uses.
type DashboardTilePlacement struct {
	// Name is the provider name; it is the grid item id.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// X is the column position (0-based).
	X int32 `json:"x"`

	// Y is the row position (0-based).
	Y int32 `json:"y"`

	// W is the width in column units.
	//
	// +kubebuilder:validation:Minimum=1
	W int32 `json:"w"`

	// H is the height in row units.
	//
	// +kubebuilder:validation:Minimum=1
	H int32 `json:"h"`
}
