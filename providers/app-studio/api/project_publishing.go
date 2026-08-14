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

package api

// This file owns the App Studio side of template-native app access.
//
// A promoted production instance is always served on its own URL through the
// platform access gate embedded in its template graph. Publishing is
// therefore not a separate deployment plane: it is exactly two native
// mechanisms —
//
//   - visibility: the instance's `access` value (public|private), stored in
//     the production binding values and reconciled onto the live instance by
//     the Project reconciler like every other binding value; and
//   - invitations: plain kcp RBAC in the tenant workspace. A member may open
//     a private app when they hold `get` on the instance's `access`
//     subresource; the share dialog writes one ClusterRole per app plus one
//     ClusterRoleBinding per invited member, and revoking is deleting the
//     binding. The hub evaluates this with a SubjectAccessReview at sign-in
//     time; the access gate never re-checks per request.
//
// No PublishedApp/AppAccessGrant records exist. The API below is a thin
// veneer that keeps the portal's publishing vocabulary (public/restricted)
// over those two mechanisms.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

const (
	// accessValueField is the template schema field carrying visibility.
	accessValueField = "access"
	// accessPublic / accessPrivate are the template access vocabulary.
	accessPublic  = "public"
	accessPrivate = "private"
	// appAccessRolePrefix names the per-app ClusterRole and its bindings.
	appAccessRolePrefix = "faros-app-access"
	// appAccessLabel marks every RBAC object this API manages with the
	// instance it grants access to, so grants are enumerable by selector.
	appAccessLabel = "faros.sh/app-access"
	// appAccessProjectLabel traces the grant back to its App Studio project.
	appAccessProjectLabel = "app-studio.faros.sh/project"
	// appAccessUserLabel records the granted platform User name.
	appAccessUserLabel = "app-studio.faros.sh/user"
)

var (
	clusterRoleResource = tenant.Resource{
		GVR: schema.GroupVersionResource{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles",
		},
		Kind: "ClusterRole", Plural: "ClusterRoles", Namespaced: false,
	}
	clusterRoleBindingResource = tenant.Resource{
		GVR: schema.GroupVersionResource{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings",
		},
		Kind: "ClusterRoleBinding", Plural: "ClusterRoleBindings", Namespaced: false,
	}
)

type publishingMember struct {
	User string `json:"user"`
	// RBACIdentity is the member's kcp username ("faros:<email>") — the ONLY
	// subject string tenant-workspace RBAC evaluates. Grants must bind it;
	// the User CR name appears in no kcp binding.
	RBACIdentity string `json:"rbacIdentity,omitempty"`
	Role         string `json:"role,omitempty"`
}

type publishingMembersResponse struct {
	Items []publishingMember `json:"items"`
}

type projectPublishingRequest struct {
	// Mode accepts public/restricted plus the legacy aliases members/private;
	// everything non-public normalizes to the instance access value
	// "private".
	Mode string `json:"mode,omitempty"`
}

type projectPublishingGrantRequest struct {
	// User is the stable tenancy User metadata.name of an existing member.
	// With Invite set it may instead be an email address of someone who is
	// not on the platform yet.
	User string `json:"user"`
	// Invite invites a non-member by email: the hub pre-provisions a pending
	// User and org membership (adopted at their first sign-in), and the app
	// grant is written against that stable identity immediately. The
	// caller's own org-admin rights authorize the membership write.
	Invite bool `json:"invite,omitempty"`
}

type projectPublishingTargetView struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

type projectPublishingPublicationView struct {
	Name   string                      `json:"name"`
	UID    string                      `json:"uid"`
	Mode   string                      `json:"mode"`
	Host   string                      `json:"host,omitempty"`
	URL    string                      `json:"url,omitempty"`
	Ready  bool                        `json:"ready"`
	Phase  string                      `json:"phase,omitempty"`
	Error  string                      `json:"error,omitempty"`
	Target projectPublishingTargetView `json:"target"`
}

type projectPublishingGrantView struct {
	Name        string `json:"name"`
	UID         string `json:"uid,omitempty"`
	User        string `json:"user"`
	Publication string `json:"publication"`
	Revoked     bool   `json:"revoked"`
	Phase       string `json:"phase,omitempty"`
}

type projectPublishingResponse struct {
	Published   bool                              `json:"published"`
	Publication *projectPublishingPublicationView `json:"publication,omitempty"`
	Grants      []projectPublishingGrantView      `json:"grants,omitempty"`
}

func newPublishingHTTPClient() *http.Client {
	transport := http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FAROS_HUB_INSECURE")), "true") {
		if base, ok := transport.(*http.Transport); ok {
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit local/dev opt-in
		}
	}
	return &http.Client{Transport: transport}
}

// appAccessRuntimeResolver names a sharing channel. Passing one of these into
// the shared grant handlers is what lets production and the development preview
// share the entire invite/list/revoke implementation.
type appAccessRuntimeResolver func(*Server, context.Context, *asclient.Client, *aiv1alpha1.Project) (appAccessRuntime, error)

// appAccessRuntime resolves one sharing channel's instance: its resource
// coordinates, live object, and the desired access value recorded on the
// binding. Everything a sharing surface shows or mutates hangs off it, for
// production and for the development preview alike — the grant machinery below
// is parameterised by this struct and never by which channel it came from.
type appAccessRuntime struct {
	target        projectPublishingTargetView
	instance      *unstructured.Unstructured
	desiredAccess string
}

func (s *Server) getProjectPublishing(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	_ = id
	response, err := s.projectPublishingResponse(r.Context(), c, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// publishProject is POST /publishing {mode}. It writes the access value onto
// the production binding (the Project reconciler converges the live
// instance) and returns the current observed view.
func (s *Server) publishProject(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	_ = id
	var req projectPublishingRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid publishing request: "+err.Error())
			return
		}
	}
	access, err := requestedAccessValue(req.Mode, p)
	if err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.setProductionAccess(r.Context(), c, p, access)
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := s.projectPublishingResponse(r.Context(), c, updated)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// unpublishProject is DELETE /publishing. Production is always reachable on
// its URL by design, so "unpublish" means: private access plus no grants —
// only workspace members can open the app.
func (s *Server) unpublishProject(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	_ = id
	updated, err := s.setProductionAccess(r.Context(), c, p, accessPrivate)
	if err != nil {
		writeError(w, err)
		return
	}
	runtime, err := s.productionRuntime(r.Context(), c, updated)
	if err == nil {
		if err := s.deleteAllAppAccessGrants(r.Context(), c, runtime.target.Name); err != nil {
			writeError(w, err)
			return
		}
	}
	response, err := s.projectPublishingResponse(r.Context(), c, updated)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listProjectPublishingMembers(w http.ResponseWriter, r *http.Request) {
	_, id, _, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	members, err := s.currentPublishingMembers(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	sort.Slice(members, func(i, j int) bool { return members[i].User < members[j].User })
	writeJSON(w, http.StatusOK, ListResponse[publishingMember]{Items: members})
}

func (s *Server) listProjectPublishingGrants(w http.ResponseWriter, r *http.Request) {
	s.listAppAccessGrants(w, r, (*Server).productionRuntime)
}

// listAppAccessGrants serves one channel's grant list. resolve picks the
// channel; everything downstream is identical, because a grant is a
// ClusterRoleBinding against an instance name and nothing about it knows
// whether that instance is production or a preview.
func (s *Server) listAppAccessGrants(w http.ResponseWriter, r *http.Request, resolve appAccessRuntimeResolver) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	_ = id
	runtime, err := resolve(s, r.Context(), c, p)
	if err != nil {
		// No production yet — an empty grant list, not an error.
		writeJSON(w, http.StatusOK, ListResponse[projectPublishingGrantView]{Items: nil})
		return
	}
	grants, err := s.appAccessGrantViews(r.Context(), c, runtime.target.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[projectPublishingGrantView]{Items: grants})
}

// createProjectPublishingGrant invites one workspace/org member: it ensures
// the per-app ClusterRole and creates the member's ClusterRoleBinding. The
// caller's own workspace RBAC authorizes the write — App Studio adds only the
// membership validation and naming convention.
func (s *Server) createProjectPublishingGrant(w http.ResponseWriter, r *http.Request) {
	s.createAppAccessGrant(w, r, (*Server).productionRuntime)
}

// createAppAccessGrant invites one member to whichever channel resolve names.
func (s *Server) createAppAccessGrant(w http.ResponseWriter, r *http.Request, resolve appAccessRuntimeResolver) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	runtime, err := resolve(s, r.Context(), c, p)
	if err != nil {
		writeError(w, err)
		return
	}
	if runtime.desiredAccess != accessPrivate {
		writeStatus(w, http.StatusBadRequest, "BadRequest",
			"grants require private access; the app is currently public — switch it to invite-only first")
		return
	}
	var req projectPublishingGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid grant request: "+err.Error())
		return
	}
	user := strings.TrimSpace(req.User)
	if user == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "user is required")
		return
	}
	subject := ""
	if req.Invite && strings.Contains(user, "@") {
		// Invite-by-email: the hub resolves-or-creates the pending User and
		// org membership under the caller's own authority, returning the
		// stable User name plus the kcp RBAC identity the grant binds.
		invited, err := s.invitePublishingMember(r.Context(), id, user)
		if err != nil {
			writeError(w, err)
			return
		}
		user = invited.User
		subject = invited.RBACIdentity
	} else {
		if strings.Contains(user, "@") {
			writeStatus(w, http.StatusBadRequest, "BadRequest",
				"user must be the stable platform User name; set invite to share with a new email")
			return
		}
		members, err := s.currentPublishingMembers(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		isMember := false
		for _, member := range members {
			if member.User == user {
				isMember = true
				subject = member.RBACIdentity
				break
			}
		}
		if !isMember {
			writeStatus(w, http.StatusBadRequest, "BadRequest",
				"user is not a member of this organization or workspace")
			return
		}
	}
	if strings.TrimSpace(subject) == "" {
		// Never fall back to the User CR name: kcp evaluates no binding
		// against it, so a grant bound to it would silently deny.
		writeStatus(w, http.StatusBadGateway, "BadGateway",
			"the platform did not report the member's RBAC identity; update the faros hub")
		return
	}
	if err := s.ensureAppAccessRole(r.Context(), c, p, runtime.target); err != nil {
		writeError(w, err)
		return
	}
	if err := s.ensureAppAccessBinding(r.Context(), c, p, runtime.target.Name, user, subject); err != nil {
		writeError(w, err)
		return
	}
	grants, err := s.appAccessGrantViews(r.Context(), c, runtime.target.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[projectPublishingGrantView]{Items: grants})
}

// revokeProjectPublishingGrant is POST /publishing/grants/{grant}. Revoking
// is deleting the member's ClusterRoleBinding; it is allowed in every access
// mode so a stale grant can always be cleaned up.
func (s *Server) revokeProjectPublishingGrant(w http.ResponseWriter, r *http.Request) {
	s.revokeAppAccessGrant(w, r, (*Server).productionRuntime)
}

// revokeAppAccessGrant deletes one grant from whichever channel resolve names.
// The label check below is what keeps a preview revoke from touching a
// production grant and vice versa.
func (s *Server) revokeAppAccessGrant(w http.ResponseWriter, r *http.Request, resolve appAccessRuntimeResolver) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	_ = id
	grantName := strings.TrimSpace(mux.Vars(r)["grant"])
	if grantName == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "grant name is required")
		return
	}
	runtime, err := resolve(s, r.Context(), c, p)
	if err != nil {
		writeError(w, err)
		return
	}
	binding, err := c.Resource(clusterRoleBindingResource, "").Get(r.Context(), grantName, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if binding.GetLabels()[appAccessLabel] != runtime.target.Name {
		writeStatus(w, http.StatusConflict, "Conflict", "grant does not belong to this project's app")
		return
	}
	if err := c.Resource(clusterRoleBindingResource, "").Delete(r.Context(), grantName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		writeError(w, err)
		return
	}
	grants, err := s.appAccessGrantViews(r.Context(), c, runtime.target.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[projectPublishingGrantView]{Items: grants})
}

// --- view assembly ---

func (s *Server) projectPublishingResponse(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (projectPublishingResponse, error) {
	runtime, err := s.productionRuntime(ctx, c, p)
	if err != nil {
		// Not promoted yet: nothing is published and there is nothing to show.
		return projectPublishingResponse{Published: false}, nil
	}
	view := publicationViewFromRuntime(runtime)
	grants, err := s.appAccessGrantViews(ctx, c, runtime.target.Name)
	if err != nil {
		return projectPublishingResponse{}, err
	}
	return projectPublishingResponse{Published: true, Publication: &view, Grants: grants}, nil
}

func publicationViewFromRuntime(rt appAccessRuntime) projectPublishingPublicationView {
	observedAccess, _, _ := unstructured.NestedString(rt.instance.Object, "spec", accessValueField)
	if observedAccess == "" {
		observedAccess = accessPublic
	}
	host, _, _ := unstructured.NestedString(rt.instance.Object, "status", "host")
	appURL, _, _ := unstructured.NestedString(rt.instance.Object, "status", "url")
	converged := observedAccess == rt.desiredAccess
	ready := converged && appURL != ""
	phase := "Ready"
	if !ready {
		phase = "Pending"
	}
	return projectPublishingPublicationView{
		Name:   rt.target.Name,
		UID:    rt.target.UID,
		Mode:   portalModeString(rt.desiredAccess),
		Host:   host,
		URL:    appURL,
		Ready:  ready,
		Phase:  phase,
		Target: rt.target,
	}
}

// portalModeString maps the template access vocabulary onto the portal's
// publishing vocabulary (public/restricted).
func portalModeString(access string) string {
	if access == accessPublic {
		return "public"
	}
	return "restricted"
}

func (s *Server) productionRuntime(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (appAccessRuntime, error) {
	binding := findProjectProductionBinding(p)
	if binding == nil || binding.ResourceRef == nil {
		return appAccessRuntime{}, newValidationError("publishing requires a promoted production instance")
	}
	ref := binding.ResourceRef
	gvr, err := projectProviderResourceGVR(ref)
	if err != nil {
		return appAccessRuntime{}, err
	}
	values, err := projectProviderBindingValues(*binding)
	if err != nil {
		return appAccessRuntime{}, err
	}
	name := strings.TrimSpace(ref.Name)
	if binding.Kind == aiv1alpha1.ProjectBindingKindProviderResource {
		name = projectProviderBindingResourceName(p, *binding, values, identity{})
	}
	if name == "" {
		return appAccessRuntime{}, newValidationError("production binding has no runtime resource name")
	}
	obj, err := c.Resource(tenant.Resource{GVR: gvr, Kind: ref.Kind, Plural: ref.Resource}, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return appAccessRuntime{}, err
	}
	desired, _ := values[accessValueField].(string)
	if desired == "" {
		desired = accessPublic
	}
	return appAccessRuntime{
		target: projectPublishingTargetView{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Resource:   ref.Resource,
			Name:       name,
			UID:        string(obj.GetUID()),
		},
		instance:      obj,
		desiredAccess: desired,
	}, nil
}

// setProductionAccess merges the access value into the production binding's
// values and updates the Project. The Project reconciler applies the changed
// binding to the live instance; the template gate picks the value up as an
// in-place env change.
func (s *Server) setProductionAccess(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, access string) (*aiv1alpha1.Project, error) {
	next := p.DeepCopy()
	binding := findProjectProductionBinding(next)
	if binding == nil || binding.ResourceRef == nil {
		return nil, newValidationError("publishing requires a promoted production instance")
	}
	values, err := projectProviderBindingValues(*binding)
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]any{}
	}
	if current, _ := values[accessValueField].(string); current == access {
		return next, nil
	}
	values[accessValueField] = access
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	binding.Values = runtime.RawExtension{Raw: raw}
	return c.Projects().Update(ctx, next, metav1.UpdateOptions{})
}

func requestedAccessValue(requested string, p *aiv1alpha1.Project) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(requested))
	switch raw {
	case "public":
		return accessPublic, nil
	case "restricted", "members", "private":
		return accessPrivate, nil
	case "":
		// Empty body preserves the current mode; a never-configured app
		// defaults to invite-only, matching the portal's safe default.
		binding := findProjectProductionBinding(p)
		if binding != nil {
			if values, err := projectProviderBindingValues(*binding); err == nil {
				if current, _ := values[accessValueField].(string); current == accessPublic {
					return accessPublic, nil
				}
			}
		}
		return accessPrivate, nil
	default:
		return "", newValidationError(fmt.Sprintf("unknown publishing mode %q: want public or restricted", requested))
	}
}

// --- RBAC grant objects ---

// appAccessRoleName is the deterministic per-app ClusterRole name.
func appAccessRoleName(instance string) string {
	return appAccessRolePrefix + "." + instance
}

// appAccessBindingName is the deterministic per-member grant name. The user
// segment is hashed so arbitrary-length User names stay within the 253-char
// object name bound while remaining stable for lookups.
func appAccessBindingName(instance, user string) string {
	name := appAccessRolePrefix + "." + instance + "." + user
	if len(name) <= 253 {
		return name
	}
	sum := sha256.Sum256([]byte(user))
	return appAccessRolePrefix + "." + instance + "." + hex.EncodeToString(sum[:])[:16]
}

func (s *Server) ensureAppAccessRole(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, target projectPublishingTargetView) error {
	group := strings.SplitN(target.APIVersion, "/", 2)[0]
	rules := []any{
		map[string]any{
			"apiGroups":     []any{group},
			"resources":     []any{target.Resource + "/access"},
			"resourceNames": []any{target.Name},
			"verbs":         []any{"get"},
		},
		// kcp's workspace content authorizer requires the `access` verb on
		// nonResourceURL "/" before ANY RBAC rule in the workspace is even
		// evaluated. Workspace members have it via their admin binding;
		// invited outsiders get it here — it only lets their requests enter
		// the authorizer chain, where everything except the app-access tuple
		// above is still denied. Without this rule every invited grant is
		// dead on arrival ("App access denied" for users who were granted).
		map[string]any{
			"nonResourceURLs": []any{"/"},
			"verbs":           []any{"access"},
		},
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata": map[string]any{
			"name": appAccessRoleName(target.Name),
			"labels": map[string]any{
				appAccessLabel:        target.Name,
				appAccessProjectLabel: p.Name,
			},
		},
		"rules": rules,
	}}
	_, err := c.Resource(clusterRoleResource, "").Create(ctx, role, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Reconcile pre-existing roles so grants created before a rule change
	// (e.g. the workspace-access addition) heal on the next share action.
	existing, getErr := c.Resource(clusterRoleResource, "").Get(ctx, appAccessRoleName(target.Name), metav1.GetOptions{})
	if getErr != nil {
		return getErr
	}
	existing.Object["rules"] = rules
	_, updateErr := c.Resource(clusterRoleResource, "").Update(ctx, existing, metav1.UpdateOptions{})
	return updateErr
}

// ensureAppAccessBinding writes the member's grant. The binding is NAMED and
// LABELED by the stable User CR name (display/dedup key), but its SUBJECT is
// the kcp RBAC identity — the username kcp actually evaluates.
func (s *Server) ensureAppAccessBinding(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, instance, user, subject string) error {
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata": map[string]any{
			"name": appAccessBindingName(instance, user),
			"labels": map[string]any{
				appAccessLabel:        instance,
				appAccessProjectLabel: p.Name,
				appAccessUserLabel:    user,
			},
		},
		"subjects": []any{map[string]any{
			"kind":     "User",
			"apiGroup": "rbac.authorization.k8s.io",
			"name":     subject,
		}},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     appAccessRoleName(instance),
		},
	}}
	_, err := c.Resource(clusterRoleBindingResource, "").Create(ctx, binding, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (s *Server) appAccessGrantViews(ctx context.Context, c *asclient.Client, instance string) ([]projectPublishingGrantView, error) {
	list, err := c.Resource(clusterRoleBindingResource, "").List(ctx, metav1.ListOptions{
		LabelSelector: appAccessLabel + "=" + instance,
	})
	if err != nil {
		return nil, err
	}
	views := make([]projectPublishingGrantView, 0, len(list.Items))
	for i := range list.Items {
		binding := &list.Items[i]
		// Re-check the label client-side: transports that ignore selectors
		// must not leak unrelated bindings into the view.
		if binding.GetLabels()[appAccessLabel] != instance {
			continue
		}
		user := binding.GetLabels()[appAccessUserLabel]
		if user == "" {
			user = subjectUserName(binding)
		}
		if user == "" {
			continue
		}
		views = append(views, projectPublishingGrantView{
			Name:        binding.GetName(),
			UID:         string(binding.GetUID()),
			User:        user,
			Publication: instance,
			Revoked:     false,
			Phase:       "Active",
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].User < views[j].User })
	return views, nil
}

func (s *Server) deleteAllAppAccessGrants(ctx context.Context, c *asclient.Client, instance string) error {
	grants, err := s.appAccessGrantViews(ctx, c, instance)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if err := c.Resource(clusterRoleBindingResource, "").Delete(ctx, grant.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func subjectUserName(binding *unstructured.Unstructured) string {
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	for _, rawSubject := range subjects {
		subject, _ := rawSubject.(map[string]any)
		if subject["kind"] == "User" {
			name, _ := subject["name"].(string)
			return name
		}
	}
	return ""
}

// --- hub membership lookup (unchanged mechanism) ---

// invitePublishingMember invites one email into the caller's organization
// through the hub membership API (invite semantics: the hub pre-provisions a
// pending User adopted at first sign-in). Deliberately org membership, not
// workspace membership — workspace members hold workspace-admin RBAC, which
// "can open this one app" must never imply. Returns the stable User name.
func (s *Server) invitePublishingMember(ctx context.Context, id identity, email string) (publishingMember, error) {
	if s.publishingMemberInviter != nil {
		return s.publishingMemberInviter(ctx, id, email)
	}
	if s.hubBase == "" {
		return publishingMember{}, fmt.Errorf("hub URL is not configured; cannot invite members")
	}
	if id.orgUUID == "" || id.token == "" {
		return publishingMember{}, fmt.Errorf("trusted organization and bearer identity are required to invite members")
	}
	payload, err := json.Marshal(map[string]any{"user": email, "role": "member", "invite": true})
	if err != nil {
		return publishingMember{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.hubBase, "/")+"/api/orgs/"+url.PathEscape(id.orgUUID)+"/memberships",
		strings.NewReader(string(payload)))
	if err != nil {
		return publishingMember{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+id.token)
	req.Header.Set("X-Faros-Org", id.orgUUID)
	req.Header.Set("X-Faros-User", id.user)
	client := s.publishingHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return publishingMember{}, fmt.Errorf("membership invite: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return publishingMember{}, readErr
	}
	if resp.StatusCode/100 != 2 {
		return publishingMember{}, fmt.Errorf("membership invite returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var view publishingMember
	if err := json.Unmarshal(body, &view); err != nil {
		return publishingMember{}, fmt.Errorf("decode membership invite: %w", err)
	}
	if strings.TrimSpace(view.User) == "" {
		return publishingMember{}, fmt.Errorf("membership invite returned no user identity")
	}
	return view, nil
}

func (s *Server) currentPublishingMembers(ctx context.Context, id identity) ([]publishingMember, error) {
	if s.publishingMembershipFetcher != nil {
		return s.publishingMembershipFetcher(ctx, id)
	}
	if s.hubBase == "" {
		return nil, fmt.Errorf("hub URL is not configured; cannot validate organization membership")
	}
	if id.orgUUID == "" || id.workspaceUUID == "" || id.token == "" {
		return nil, fmt.Errorf("trusted organization, workspace, and bearer identity are required for membership validation")
	}
	paths := []string{
		"/api/orgs/" + url.PathEscape(id.orgUUID) + "/memberships",
		"/api/orgs/" + url.PathEscape(id.orgUUID) + "/workspaces/" + url.PathEscape(id.workspaceUUID) + "/memberships",
	}
	seen := map[string]publishingMember{}
	client := s.publishingHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.hubBase, "/")+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+id.token)
		req.Header.Set("X-Faros-Org", id.orgUUID)
		req.Header.Set("X-Faros-Workspace", id.workspaceUUID)
		req.Header.Set("X-Faros-User", id.user)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("membership lookup: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("membership lookup returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var decoded publishingMembersResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("decode membership lookup: %w", err)
		}
		for _, member := range decoded.Items {
			if user := strings.TrimSpace(member.User); user != "" {
				seen[user] = publishingMember{User: user, Role: strings.TrimSpace(member.Role)}
			}
		}
	}
	out := make([]publishingMember, 0, len(seen))
	for _, member := range seen {
		out = append(out, member)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out, nil
}
