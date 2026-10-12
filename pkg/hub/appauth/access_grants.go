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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const (
	appAccessGrantRoute         = "/{org}/workspaces/{workspace}/app-auth/grants"
	appAccessCapability         = "app-access.grants.manage"
	appAccessRolePrefix         = "railgrid-app-access"
	appAccessIdentityRolePrefix = "railgrid-app-identity"
	appAccessLabel              = "railgrid.ai/app-access"
	appAccessUserLabel          = "app-studio.railgrid.ai/user"
	appAccessProject            = "app-studio.railgrid.ai/project"
	appAccessProjectUID         = "app-studio.railgrid.ai/project-uid"
	appAccessInstanceUID        = "app-studio.railgrid.ai/instance-uid"
)

var (
	appAccessClusterRoleGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	appAccessBindingGVR     = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	appAccessProjectGVR     = schema.GroupVersionResource{Group: "ai.railgrid.ai", Version: "v1alpha1", Resource: "projects"}
)

// AppAccessGrant is the small, portal-facing view returned by the hub broker.
// It represents one named ClusterRoleBinding and carries no RBAC policy input.
type AppAccessGrant struct {
	Name        string `json:"name"`
	UID         string `json:"uid,omitempty"`
	User        string `json:"user"`
	Publication string `json:"publication"`
	Revoked     bool   `json:"revoked"`
	Phase       string `json:"phase"`
	// MigrationPending is a read-only hint that the hub migration controller
	// has not yet upgraded this grant's RBAC metadata.
	MigrationPending bool `json:"migrationPending,omitempty"`
}

type appAccessGrantRequest struct {
	Operation  string `json:"operation"`
	Group      string `json:"group"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Project    string `json:"projectName"`
	ProjectUID string `json:"projectUID"`
	User       string `json:"user,omitempty"`
	Grant      string `json:"grant,omitempty"`
}

type appAccessGrantResponse struct {
	Items []AppAccessGrant `json:"items"`
}

// AppAccessWorkspaceClientFactory resolves the tenant logical cluster named
// by the authenticated hub context.
type AppAccessWorkspaceClientFactory func(clusterID string) (dynamic.Interface, error)

// AppAccessGrantHandlerConfig assembles the hub-owned app-access broker. The
// handler accepts only the fixed Infrastructure Instance tuple and writes the
// legacy reachability grant plus its separate UID-fenced identity grant.
type AppAccessGrantHandlerConfig struct {
	Clients          AppAccessWorkspaceClientFactory
	SARClient        SARFactory
	IdentityApp      IdentityAppResolver
	WorkspaceCluster IdentityWorkspaceCluster
	AccountLookup    IdentityAccountLookup
	Memberships      tenant.MembershipLookup
}

// AppAccessGrantHandler owns the narrow RBAC broker used by App Studio.
type AppAccessGrantHandler struct {
	clients          AppAccessWorkspaceClientFactory
	sarClient        SARFactory
	identityApp      IdentityAppResolver
	workspaceCluster IdentityWorkspaceCluster
	accountLookup    IdentityAccountLookup
	memberships      tenant.MembershipLookup
}

// NewAppAccessGrantHandler validates the broker dependencies.
func NewAppAccessGrantHandler(cfg AppAccessGrantHandlerConfig) (*AppAccessGrantHandler, error) {
	if cfg.Clients == nil || cfg.SARClient == nil || cfg.IdentityApp == nil ||
		cfg.WorkspaceCluster == nil || cfg.AccountLookup == nil || cfg.Memberships == nil {
		return nil, fmt.Errorf("appauth: app-access broker requires workspace clients, SAR, identity app, membership, and account lookups")
	}
	return &AppAccessGrantHandler{
		clients: cfg.Clients, sarClient: cfg.SARClient, identityApp: cfg.IdentityApp,
		workspaceCluster: cfg.WorkspaceCluster, accountLookup: cfg.AccountLookup, memberships: cfg.Memberships,
	}, nil
}

// NewKCPAppAccessGrantHandler creates the per-tenant dynamic client factory
// from the hub's kcp admin configuration.
func NewKCPAppAccessGrantHandler(kcpConfig *rest.Config, cfg AppAccessGrantHandlerConfig) (*AppAccessGrantHandler, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("appauth: kcp config is required for app-access grants")
	}
	if cfg.Clients == nil {
		cfg.Clients = func(clusterID string) (dynamic.Interface, error) {
			if !segmentRE.MatchString(clusterID) {
				return nil, fmt.Errorf("invalid workspace cluster")
			}
			workspaceConfig := rest.CopyConfig(kcpConfig)
			workspaceConfig.Host = apiurl.KCPClusterURL(workspaceConfig.Host, clusterID)
			workspaceConfig.Timeout = 15 * time.Second
			return dynamic.NewForConfig(workspaceConfig)
		}
	}
	return NewAppAccessGrantHandler(cfg)
}

// Register mounts the sole POST endpoint on the hub-access and tenant-scoped
// router.
func (h *AppAccessGrantHandler) Register(router *mux.Router) {
	if h == nil || router == nil {
		return
	}
	router.HandleFunc(appAccessGrantRoute, h.Handle).Methods(http.MethodPost)
}

// Handle performs one fixed list/create/revoke/reset operation after checking
// the exact action proof, Project ownership and human SARs.
func (h *AppAccessGrantHandler) Handle(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		http.Error(w, "invalid app-access grant request", http.StatusBadRequest)
		return
	}
	tc, scoped := tenant.FromContext(r.Context())
	call, proof := tenant.DelegatedCallFrom(r.Context())
	if !scoped || !proof || !call.ActionProof || call.Provider != "app-studio" ||
		call.Capability != appAccessCapability || call.Scope != "workspace" || call.User == "" ||
		call.User != tc.User || call.RBACIdentity == "" || tc.OrgUUID == "" || tc.WorkspaceUUID == "" {
		http.Error(w, "app-access grant is not authorized", http.StatusForbidden)
		return
	}
	vars := mux.Vars(r)
	if vars["org"] != tc.OrgUUID || vars["workspace"] != tc.WorkspaceUUID {
		http.Error(w, "app-access grant is not authorized", http.StatusForbidden)
		return
	}
	var req appAccessGrantRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "malformed app-access grant request", http.StatusBadRequest)
		return
	}
	if !h.requestMatchesProof(req, call) {
		http.Error(w, "app-access grant is not authorized", http.StatusForbidden)
		return
	}
	cluster, err := h.workspaceCluster(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil || !segmentRE.MatchString(cluster) {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	client, err := h.clients(cluster)
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	project, instance, err := h.loadOwnedTarget(r.Context(), client, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "app-access target is no longer available", http.StatusConflict)
			return
		}
		http.Error(w, "app-access target is not authorized", http.StatusForbidden)
		return
	}
	verb := "get"
	if req.Operation != "list" {
		verb = "update"
	}
	if !h.authorized(r.Context(), call, cluster, project, verb) || !h.authorized(r.Context(), call, cluster, instance, verb) {
		http.Error(w, "app-access grant is not authorized", http.StatusForbidden)
		return
	}
	var account IdentityAccount
	if req.Operation == "create" {
		account, err = h.accountLookup(r.Context(), req.User)
		if apierrors.IsNotFound(err) || (err == nil && (account.User != req.User || account.Deleted || account.RBACIdentity == "")) {
			http.Error(w, "existing app account is unavailable", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if !h.proofNamesAccount(call, account) {
			http.Error(w, "app-access grant is not authorized", http.StatusForbidden)
			return
		}
		if call.AppAccessSourceVerb != "authentication-grants" && !h.workspaceMember(r.Context(), req.User, tc.OrgUUID, tc.WorkspaceUUID) {
			http.Error(w, "existing app account is not a member of this workspace", http.StatusForbidden)
			return
		}
		if call.AppAccessSourceVerb == "authentication-grants" {
			app, err := h.identityApp(r.Context(), InstanceRef{Cluster: cluster, Group: req.Group, Resource: req.Resource, Name: req.Name})
			if err != nil || !validIdentityApp(app) || app.Environment != "production" ||
				app.AppID != req.ProjectUID+"/production" || app.InstanceUID != req.UID {
				http.Error(w, "Railgrid Auth is not enabled for this production app", http.StatusForbidden)
				return
			}
		}
	}
	if req.Operation == "create" {
		if err := h.ensureAppAccessRole(r.Context(), client, project, instance, req); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := h.ensureAppAccessBinding(r.Context(), client, project, instance, req, account); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	switch req.Operation {
	case "revoke":
		if err := h.revokeAppAccessBinding(r.Context(), client, project, instance, req); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	case "reset":
		if err := h.resetAppAccessBindings(r.Context(), client, project, instance, req); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	items, err := h.listAppAccessBindings(r.Context(), client, project, instance, req)
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(appAccessGrantResponse{Items: items})
}

func (h *AppAccessGrantHandler) requestMatchesProof(req appAccessGrantRequest, call tenant.DelegatedCall) bool {
	if req.Group != "infrastructure.railgrid.ai" || req.Resource != "instances" ||
		!segmentRE.MatchString(req.Name) || !segmentRE.MatchString(req.UID) ||
		!segmentRE.MatchString(req.Project) || !segmentRE.MatchString(req.ProjectUID) ||
		req.Operation != call.AppAccessOperation || req.Group != call.AppAccessGroup ||
		req.Resource != call.AppAccessResource || req.Name != call.AppAccessName || req.UID != call.AppAccessUID ||
		req.Project != call.AppAccessProject || req.ProjectUID != call.AppAccessProjectUID || req.User != "" && !segmentRE.MatchString(req.User) {
		return false
	}
	switch req.Operation {
	case "list":
		return req.User == "" && req.Grant == ""
	case "create":
		if req.User == "" || req.Grant != "" || call.AppAccessUser == "" {
			return false
		}
		if strings.Contains(call.AppAccessUser, "@") {
			return call.AppAccessUser == call.AppAccessInviteUser
		}
		return req.User == call.AppAccessUser
	case "revoke":
		return req.User == "" && req.Grant != "" && req.Grant == call.AppAccessGrant && segmentRE.MatchString(req.Grant)
	case "reset":
		return req.User == "" && req.Grant == ""
	default:
		return false
	}
}

func (h *AppAccessGrantHandler) loadOwnedTarget(ctx context.Context, client dynamic.Interface, req appAccessGrantRequest) (*unstructured.Unstructured, *unstructured.Unstructured, error) {
	project, err := client.Resource(appAccessProjectGVR).Get(ctx, req.Project, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	if string(project.GetUID()) != req.ProjectUID || project.GetUID() == "" {
		return nil, nil, fmt.Errorf("project UID changed")
	}
	environment, bindingName := "production", "prod"
	if req.Operation != "" {
		call, ok := tenant.DelegatedCallFrom(ctx)
		if !ok {
			return nil, nil, fmt.Errorf("action proof context missing")
		}
		switch call.AppAccessSourceVerb {
		case "preview-grants", "preview":
			environment, bindingName = "development", "dev"
		case "publishing-grants", "authentication-grants", "publishing":
		default:
			return nil, nil, fmt.Errorf("unsupported app-access source action")
		}
	}
	name, version, err := brokerProjectTarget(project, environment, bindingName)
	if err != nil || name != req.Name {
		return nil, nil, fmt.Errorf("app-access target no longer matches Project binding")
	}
	gvr := schema.GroupVersionResource{Group: req.Group, Version: version, Resource: req.Resource}
	instance, err := client.Resource(gvr).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	if string(instance.GetUID()) != req.UID || instance.GetUID() == "" ||
		!brokerProjectOwnsInstance(project, instance) {
		return nil, nil, fmt.Errorf("app-access target UID or owner changed")
	}
	return project, instance, nil
}

func brokerProjectTarget(project *unstructured.Unstructured, environment, bindingName string) (string, string, error) {
	environments, found, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil || !found {
		return "", "", fmt.Errorf("project environments unavailable")
	}
	for _, rawEnv := range environments {
		env, ok := rawEnv.(map[string]any)
		if !ok || env["name"] != environment {
			continue
		}
		bindings, _, _ := unstructured.NestedSlice(env, "bindings")
		for _, rawBinding := range bindings {
			binding, ok := rawBinding.(map[string]any)
			if !ok || binding["name"] != bindingName || binding["provider"] != "app-studio" || binding["kind"] != "providerResource" {
				continue
			}
			ref, ok := binding["resourceRef"].(map[string]any)
			if !ok || ref["kind"] != "Instance" || ref["resource"] != "instances" {
				return "", "", fmt.Errorf("project binding does not name an Infrastructure Instance")
			}
			gv, err := schema.ParseGroupVersion(nestedStringValue(ref["apiVersion"]))
			if err != nil || gv.Group != "infrastructure.railgrid.ai" {
				return "", "", fmt.Errorf("project binding does not name the Infrastructure API")
			}
			name := strings.TrimSpace(nestedStringValue(ref["name"]))
			if name == "" {
				values, _, _ := unstructured.NestedMap(binding, "values")
				name = strings.TrimSpace(nestedStringValue(values["name"]))
			}
			if name == "" {
				name = project.GetName() + "-" + bindingName
			}
			if !segmentRE.MatchString(name) {
				return "", "", fmt.Errorf("project binding has an invalid Instance name")
			}
			return name, gv.Version, nil
		}
	}
	return "", "", fmt.Errorf("project has no supported %s Instance binding", environment)
}

func brokerProjectOwnsInstance(project, instance *unstructured.Unstructured) bool {
	if project == nil || instance == nil || project.GetName() == "" || project.GetUID() == "" ||
		instance.GetLabels()[appAccessProject] != project.GetName() {
		return false
	}
	for _, owner := range instance.GetOwnerReferences() {
		if owner.APIVersion == "ai.railgrid.ai/v1alpha1" && owner.Kind == "Project" &&
			owner.Name == project.GetName() && owner.UID == project.GetUID() {
			return true
		}
	}
	return false
}

func (h *AppAccessGrantHandler) authorized(ctx context.Context, call tenant.DelegatedCall, cluster string, obj *unstructured.Unstructured, verb string) bool {
	client, err := h.sarClient(cluster)
	if err != nil {
		return false
	}
	gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
	if err != nil {
		return false
	}
	group, version := gv.Group, gv.Version
	resource := ""
	switch group {
	case "ai.railgrid.ai":
		resource = "projects"
	case "infrastructure.railgrid.ai":
		resource = "instances"
	default:
		return false
	}
	review, err := client.Create(ctx, &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: call.RBACIdentity, Groups: append([]string(nil), call.Groups...), Extra: extraToSAR(call.Extra),
		ResourceAttributes: &authorizationv1.ResourceAttributes{Group: group, Version: version, Resource: resource, Name: obj.GetName(), Verb: verb},
	}}, metav1.CreateOptions{})
	return err == nil && review.Status.Allowed
}

func (h *AppAccessGrantHandler) proofNamesAccount(call tenant.DelegatedCall, account IdentityAccount) bool {
	if call.AppAccessUser == account.User {
		return true
	}
	return strings.Contains(call.AppAccessUser, "@") && call.AppAccessUser == call.AppAccessInviteUser &&
		account.Email != "" && strings.EqualFold(strings.TrimSpace(account.Email), call.AppAccessUser)
}

func (h *AppAccessGrantHandler) workspaceMember(ctx context.Context, userName, orgUUID, workspaceUUID string) bool {
	index, err := h.memberships.GetUserMembershipIndex(ctx, userName)
	if err != nil {
		return false
	}
	for _, entry := range index.Spec.Entries {
		if entry.OrgUUID != orgUUID || entry.SoftDeletedAt != nil {
			continue
		}
		if entry.WorkspaceUUID == workspaceUUID || entry.WorkspaceUUID == "" {
			return true
		}
	}
	return false
}

func (h *AppAccessGrantHandler) ensureAppAccessRole(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) error {
	name := appAccessRoleName(req.Name)
	roles := client.Resource(appAccessClusterRoleGVR)
	for attempt := 0; attempt < 2; attempt++ {
		role := newAppAccessRole(project, instance, req)
		_, err := roles.Create(ctx, role, metav1.CreateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, err := roles.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		roleLabels := existing.GetLabels()
		if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] != project.GetName() {
			return fmt.Errorf("ClusterRole %q is not an app-access role for this Project", name)
		}
		recordedProjectUID := roleLabels[appAccessProjectUID]
		if recordedProjectUID == "" {
			return fmt.Errorf("ClusterRole %q has no Project UID; refusing to adopt it", name)
		}
		if recordedProjectUID != req.ProjectUID {
			if err := h.removeStaleAppAccessRole(ctx, client, existing, req, recordedProjectUID); err != nil {
				return err
			}
			continue
		}
		if !recognizedAppAccessRole(existing, req) {
			return fmt.Errorf("ClusterRole %q has an unexpected app-access policy", name)
		}
		if appAccessRoleStateCurrent(existing, project, instance, req) {
			return nil
		}
		applyAppAccessRoleState(existing, project, instance, req)
		_, err = roles.Update(ctx, existing, metav1.UpdateOptions{})
		return err
	}
	return fmt.Errorf("ClusterRole %q changed while stale app-access grants were removed", name)
}

func (h *AppAccessGrantHandler) ensureAppAccessBinding(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount) error {
	name := appAccessBindingName(req.Name, account.User)
	roleName := appAccessRoleName(req.Name)
	binding := newAppAccessBinding(project, instance, req, account, roleName)
	_, err := client.Resource(appAccessBindingGVR).Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		return h.ensureIdentityAppAccessGrant(ctx, client, project, instance, req, account)
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := client.Resource(appAccessBindingGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !isAppAccessBindingFor(existing, req, project.GetName(), account.User, account.RBACIdentity, roleName) {
		return fmt.Errorf("ClusterRoleBinding %q is not the requested app-access grant", name)
	}
	if appAccessBindingStateCurrent(existing, project, instance, req, account.User, account.RBACIdentity, roleName) {
		return h.ensureIdentityAppAccessGrant(ctx, client, project, instance, req, account)
	}
	applyAppAccessBindingState(existing, project, instance, req, account.User)
	_, err = client.Resource(appAccessBindingGVR).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return h.ensureIdentityAppAccessGrant(ctx, client, project, instance, req, account)
}

// migrateCurrentAppAccessGrants upgrades branch-created grants only after the
// migration reconciler has passed Project UID, environment binding, and
// Instance owner checks. Recognized grants from a prior Project UID are
// removed with delete preconditions; they are never relabeled to the
// replacement Project.
func (h *AppAccessGrantHandler) migrateCurrentAppAccessGrants(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) error {
	roleName := appAccessRoleName(req.Name)
	role, err := client.Resource(appAccessClusterRoleGVR).Get(ctx, roleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	roleLabels := role.GetLabels()
	if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] != project.GetName() {
		return nil
	}
	recordedProjectUID := roleLabels[appAccessProjectUID]
	if recordedProjectUID == "" {
		return nil
	}
	if recordedProjectUID != req.ProjectUID {
		if !recognizedAppAccessRole(role, req) {
			return nil
		}
		return h.removeStaleAppAccessRole(ctx, client, role, req, recordedProjectUID)
	}
	if !recognizedAppAccessRole(role, req) {
		return fmt.Errorf("ClusterRole %q has an unexpected app-access policy", roleName)
	}
	if !appAccessRoleStateCurrent(role, project, instance, req) {
		applyAppAccessRoleState(role, project, instance, req)
		if _, err := client.Resource(appAccessClusterRoleGVR).Update(ctx, role, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}

	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	bindings, err := client.Resource(appAccessBindingGVR).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if !validRoleReference(binding, roleName) || binding.GetLabels()[appAccessProjectUID] != req.ProjectUID {
			continue
		}
		user := binding.GetLabels()[appAccessUserLabel]
		if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, roleName) || user == "" {
			return fmt.Errorf("ClusterRoleBinding %q has an unexpected app-access grant", binding.GetName())
		}
		if !appAccessBindingStateCurrent(binding, project, instance, req, user, bindingSubjectUser(binding), roleName) {
			applyAppAccessBindingState(binding, project, instance, req, user)
			if _, err := client.Resource(appAccessBindingGVR).Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		account := IdentityAccount{User: user, RBACIdentity: bindingSubjectUser(binding)}
		current, err := appAccessIdentityGrantCurrent(ctx, client, project, instance, req, account)
		if err != nil {
			return err
		}
		if !current {
			if err := h.ensureIdentityAppAccessGrant(ctx, client, project, instance, req, account); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *AppAccessGrantHandler) removeStaleAppAccessRole(ctx context.Context, client dynamic.Interface, role *unstructured.Unstructured, req appAccessGrantRequest, oldProjectUID string) error {
	name := appAccessRoleName(req.Name)
	if oldProjectUID == "" || oldProjectUID == req.ProjectUID || !recognizedAppAccessRole(role, req) {
		return fmt.Errorf("ClusterRole %q cannot be safely replaced", name)
	}
	bindings, err := client.Resource(appAccessBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var staleBindings []*unstructured.Unstructured
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if !validRoleReference(binding, name) {
			continue
		}
		if !recognizedAppAccessBinding(binding, req, role.GetLabels()[appAccessProject], oldProjectUID, name) {
			return fmt.Errorf("ClusterRole %q has a binding that cannot be safely removed", name)
		}
		staleBindings = append(staleBindings, binding.DeepCopy())
	}
	for _, binding := range staleBindings {
		if err := deleteGrantObjectWithPreconditions(ctx, client.Resource(appAccessBindingGVR), binding); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if err := deleteGrantObjectWithPreconditions(ctx, client.Resource(appAccessClusterRoleGVR), role); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func deleteGrantObjectWithPreconditions(ctx context.Context, resource dynamic.ResourceInterface, obj *unstructured.Unstructured) error {
	preconditions := &metav1.Preconditions{}
	if uid := obj.GetUID(); uid != "" {
		preconditions.UID = &uid
	}
	if resourceVersion := obj.GetResourceVersion(); resourceVersion != "" {
		preconditions.ResourceVersion = &resourceVersion
	}
	options := metav1.DeleteOptions{}
	if preconditions.UID != nil || preconditions.ResourceVersion != nil {
		options.Preconditions = preconditions
	}
	return resource.Delete(ctx, obj.GetName(), options)
}

func newAppAccessRole(project, instance *unstructured.Unstructured, req appAccessGrantRequest) *unstructured.Unstructured {
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": appAccessRoleName(req.Name)},
	}}
	applyAppAccessRoleState(role, project, instance, req)
	return role
}

func appAccessRoleRules(req appAccessGrantRequest) []any {
	return []any{
		map[string]any{"apiGroups": []any{req.Group}, "resources": []any{req.Resource + "/" + AccessSubresource}, "resourceNames": []any{req.Name}, "verbs": []any{AccessVerb}},
		map[string]any{"nonResourceURLs": []any{"/"}, "verbs": []any{"access"}},
	}
}

func appAccessIdentityRoleRules(req appAccessGrantRequest) []any {
	subresource, ok := identityAppAccessSubresource(req.ProjectUID, req.UID)
	if !ok {
		return nil
	}
	return []any{map[string]any{
		"apiGroups": []any{req.Group}, "resources": []any{req.Resource + "/" + subresource},
		"resourceNames": []any{req.Name}, "verbs": []any{AccessVerb},
	}}
}

// identityAppAccessSubresource binds app identity issuance to both the
// authoritative Project and Instance UIDs without adding a public API verb.
func identityAppAccessSubresource(projectUID, instanceUID string) (string, bool) {
	if !segmentRE.MatchString(projectUID) || !segmentRE.MatchString(instanceUID) {
		return "", false
	}
	sum := sha256.Sum256([]byte(projectUID + "\x00" + instanceUID))
	return AccessSubresource + "." + hex.EncodeToString(sum[:])[:32], true
}

func applyAppAccessRoleState(role *unstructured.Unstructured, project, instance *unstructured.Unstructured, req appAccessGrantRequest) {
	setRequiredGrantLabels(role, map[string]string{
		appAccessLabel: req.Name, appAccessProject: project.GetName(), appAccessProjectUID: req.ProjectUID,
		appAccessInstanceUID: string(instance.GetUID()),
	})
	role.Object["rules"] = appAccessRoleRules(req)
	role.SetOwnerReferences(appAccessOwnerReferences(project, instance))
}

func newAppAccessBinding(project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount, roleName string) *unstructured.Unstructured {
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": appAccessBindingName(req.Name, account.User)},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": account.RBACIdentity}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": roleName},
	}}
	applyAppAccessBindingState(binding, project, instance, req, account.User)
	return binding
}

func appAccessIdentityRoleName(req appAccessGrantRequest) string {
	sum := sha256.Sum256([]byte(req.ProjectUID + "\x00" + req.UID + "\x00" + req.Name))
	return appAccessIdentityRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
}

func appAccessIdentityBindingName(req appAccessGrantRequest, user string) string {
	sum := sha256.Sum256([]byte(appAccessIdentityRoleName(req) + "\x00" + user))
	return appAccessIdentityRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
}

func newAppAccessIdentityRole(project, instance *unstructured.Unstructured, req appAccessGrantRequest) *unstructured.Unstructured {
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": appAccessIdentityRoleName(req)},
	}}
	applyAppAccessIdentityRoleState(role, project, instance, req)
	return role
}

func applyAppAccessIdentityRoleState(role *unstructured.Unstructured, project, instance *unstructured.Unstructured, req appAccessGrantRequest) {
	setRequiredGrantLabels(role, map[string]string{
		appAccessLabel: req.Name, appAccessProject: project.GetName(), appAccessProjectUID: req.ProjectUID,
		appAccessInstanceUID: string(instance.GetUID()),
	})
	role.Object["rules"] = appAccessIdentityRoleRules(req)
	role.SetOwnerReferences(appAccessOwnerReferences(project, instance))
}

func (h *AppAccessGrantHandler) ensureIdentityAppAccessGrant(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount) error {
	if _, ok := identityAppAccessSubresource(req.ProjectUID, req.UID); !ok {
		return fmt.Errorf("invalid Project or Instance UID for identity grant")
	}
	roleName := appAccessIdentityRoleName(req)
	roles := client.Resource(appAccessClusterRoleGVR)
	role := newAppAccessIdentityRole(project, instance, req)
	if _, err := roles.Create(ctx, role, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, getErr := roles.Get(ctx, roleName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if !recognizedAppAccessIdentityRole(existing, project, instance, req) {
			return fmt.Errorf("ClusterRole %q is not the UID-fenced app-access role", roleName)
		}
		if !appAccessIdentityRoleStateCurrent(existing, project, instance, req) {
			applyAppAccessIdentityRoleState(existing, project, instance, req)
			if _, err := roles.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	bindingName := appAccessIdentityBindingName(req, account.User)
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": bindingName},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": account.RBACIdentity}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": roleName},
	}}
	applyAppAccessBindingState(binding, project, instance, req, account.User)
	bindings := client.Resource(appAccessBindingGVR)
	if _, err := bindings.Create(ctx, binding, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := bindings.Get(ctx, bindingName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !recognizedAppAccessIdentityBinding(existing, project, instance, req, account, roleName) {
		return fmt.Errorf("ClusterRoleBinding %q is not the requested UID-fenced grant", bindingName)
	}
	if appAccessIdentityBindingStateCurrent(existing, project, instance, req, account, roleName) {
		return nil
	}
	applyAppAccessBindingState(existing, project, instance, req, account.User)
	_, err = bindings.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func deleteIdentityAppAccessBinding(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, user, identity string) error {
	name := appAccessIdentityBindingName(req, user)
	binding, err := client.Resource(appAccessBindingGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	account := IdentityAccount{User: user, RBACIdentity: identity}
	if !recognizedAppAccessIdentityBinding(binding, project, instance, req, account, appAccessIdentityRoleName(req)) {
		return fmt.Errorf("ClusterRoleBinding %q is not the UID-fenced app-access grant", name)
	}
	return deleteGrantObjectWithPreconditions(ctx, client.Resource(appAccessBindingGVR), binding)
}

func recognizedAppAccessIdentityRole(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest) bool {
	if role == nil || role.GetName() != appAccessIdentityRoleName(req) {
		return false
	}
	if _, found := role.Object["aggregationRule"]; found {
		return false
	}
	roleLabels := role.GetLabels()
	if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] != project.GetName() ||
		roleLabels[appAccessProjectUID] != req.ProjectUID || roleLabels[appAccessInstanceUID] != string(instance.GetUID()) {
		return false
	}
	rules, found, err := unstructured.NestedSlice(role.Object, "rules")
	if err != nil || !found {
		return false
	}
	actual, err := json.Marshal(rules)
	if err != nil {
		return false
	}
	expected, err := json.Marshal(appAccessIdentityRoleRules(req))
	return err == nil && string(actual) == string(expected)
}

func recognizedAppAccessIdentityBinding(binding, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount, roleName string) bool {
	if binding == nil || binding.GetName() != appAccessIdentityBindingName(req, account.User) {
		return false
	}
	labels := binding.GetLabels()
	return labels[appAccessLabel] == req.Name && labels[appAccessProject] == project.GetName() &&
		labels[appAccessProjectUID] == req.ProjectUID && labels[appAccessInstanceUID] == string(instance.GetUID()) &&
		labels[appAccessUserLabel] == account.User && validRoleReference(binding, roleName) && bindingSubjectUser(binding) == account.RBACIdentity
}

func applyAppAccessBindingState(binding *unstructured.Unstructured, project, instance *unstructured.Unstructured, req appAccessGrantRequest, user string) {
	setRequiredGrantLabels(binding, map[string]string{
		appAccessLabel: req.Name, appAccessProject: project.GetName(), appAccessProjectUID: req.ProjectUID,
		appAccessInstanceUID: string(instance.GetUID()), appAccessUserLabel: user,
	})
	binding.SetOwnerReferences(appAccessOwnerReferences(project, instance))
}

func appAccessRoleStateCurrent(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest) bool {
	return recognizedAppAccessRole(role, req) && role.GetLabels()[appAccessProject] == project.GetName() &&
		role.GetLabels()[appAccessProjectUID] == req.ProjectUID && role.GetLabels()[appAccessInstanceUID] == string(instance.GetUID()) &&
		appAccessOwnerReferencesCurrent(role, project, instance)
}

func appAccessBindingStateCurrent(binding, project, instance *unstructured.Unstructured, req appAccessGrantRequest, user, identity, roleName string) bool {
	return isAppAccessBindingFor(binding, req, project.GetName(), user, identity, roleName) &&
		binding.GetLabels()[appAccessInstanceUID] == string(instance.GetUID()) && appAccessOwnerReferencesCurrent(binding, project, instance)
}

func appAccessIdentityRoleStateCurrent(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest) bool {
	return recognizedAppAccessIdentityRole(role, project, instance, req) && appAccessOwnerReferencesCurrent(role, project, instance)
}

func appAccessIdentityBindingStateCurrent(binding, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount, roleName string) bool {
	return recognizedAppAccessIdentityBinding(binding, project, instance, req, account, roleName) && appAccessOwnerReferencesCurrent(binding, project, instance)
}

func appAccessIdentityGrantCurrent(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount) (bool, error) {
	roleName := appAccessIdentityRoleName(req)
	role, err := client.Resource(appAccessClusterRoleGVR).Get(ctx, roleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !appAccessIdentityRoleStateCurrent(role, project, instance, req) {
		return false, nil
	}
	binding, err := client.Resource(appAccessBindingGVR).Get(ctx, appAccessIdentityBindingName(req, account.User), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return appAccessIdentityBindingStateCurrent(binding, project, instance, req, account, roleName), nil
}

func appAccessOwnerReferencesCurrent(obj, project, instance *unstructured.Unstructured) bool {
	owners := obj.GetOwnerReferences()
	return len(owners) == 2 && owners[0].APIVersion == "ai.railgrid.ai/v1alpha1" && owners[0].Kind == "Project" &&
		owners[0].Name == project.GetName() && owners[0].UID == project.GetUID() &&
		owners[1].APIVersion == instance.GetAPIVersion() && owners[1].Kind == "Instance" &&
		owners[1].Name == instance.GetName() && owners[1].UID == instance.GetUID()
}

func appAccessOwnerReferences(project, instance *unstructured.Unstructured) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		{APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Name: project.GetName(), UID: project.GetUID()},
		{APIVersion: instance.GetAPIVersion(), Kind: "Instance", Name: instance.GetName(), UID: instance.GetUID()},
	}
}

func setRequiredGrantLabels(obj *unstructured.Unstructured, required map[string]string) {
	objectLabels := obj.GetLabels()
	if objectLabels == nil {
		objectLabels = make(map[string]string, len(required))
	}
	for key, value := range required {
		objectLabels[key] = value
	}
	obj.SetLabels(objectLabels)
}

func recognizedAppAccessRole(role *unstructured.Unstructured, req appAccessGrantRequest) bool {
	if role == nil || role.GetName() != appAccessRoleName(req.Name) {
		return false
	}
	if _, found := role.Object["aggregationRule"]; found {
		return false
	}
	roleLabels := role.GetLabels()
	if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] == "" || roleLabels[appAccessProjectUID] == "" {
		return false
	}
	instanceUID := roleLabels[appAccessInstanceUID]
	if instanceUID != "" && !segmentRE.MatchString(instanceUID) {
		return false
	}
	rules, found, err := unstructured.NestedSlice(role.Object, "rules")
	if err != nil || !found {
		return false
	}
	actual, err := json.Marshal(rules)
	if err != nil {
		return false
	}
	expected, err := json.Marshal(appAccessRoleRules(req))
	return err == nil && string(actual) == string(expected)
}

func recognizedAppAccessBinding(binding *unstructured.Unstructured, req appAccessGrantRequest, project, projectUID, roleName string) bool {
	if binding == nil {
		return false
	}
	bindingLabels := binding.GetLabels()
	user := bindingLabels[appAccessUserLabel]
	instanceUID := bindingLabels[appAccessInstanceUID]
	return bindingLabels[appAccessLabel] == req.Name && bindingLabels[appAccessProject] == project &&
		projectUID != "" && bindingLabels[appAccessProjectUID] == projectUID && user != "" &&
		(binding.GetName() == appAccessBindingName(req.Name, user)) &&
		(instanceUID == "" || segmentRE.MatchString(instanceUID)) &&
		validRoleReference(binding, roleName) && bindingSubjectUser(binding) != ""
}

func (h *AppAccessGrantHandler) listAppAccessBindings(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) ([]AppAccessGrant, error) {
	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	list, err := client.Resource(appAccessBindingGVR).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	items := make([]AppAccessGrant, 0, len(list.Items))
	roleName := appAccessRoleName(req.Name)
	var role *unstructured.Unstructured
	var roleErr error
	if len(list.Items) > 0 {
		role, roleErr = client.Resource(appAccessClusterRoleGVR).Get(ctx, roleName, metav1.GetOptions{})
	}
	for i := range list.Items {
		binding := &list.Items[i]
		if binding.GetLabels()[appAccessLabel] != req.Name || binding.GetLabels()[appAccessProject] != project.GetName() {
			continue
		}
		if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, roleName) {
			continue
		}
		user := binding.GetLabels()[appAccessUserLabel]
		items = append(items, AppAccessGrant{
			Name: binding.GetName(), UID: string(binding.GetUID()), User: user,
			Publication: req.Name, Phase: "Active",
			MigrationPending: appAccessGrantMigrationPending(ctx, client, project, instance, req, binding, role, roleErr),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].User < items[j].User })
	return items, nil
}

// appAccessGrantMigrationPending is an advisory read-only check. RBAC read
// failures keep the hint pending without making an otherwise valid list fail.
func appAccessGrantMigrationPending(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, binding, role *unstructured.Unstructured, roleErr error) bool {
	roleName := appAccessRoleName(req.Name)
	if roleErr != nil || !appAccessRoleStateCurrent(role, project, instance, req) {
		return true
	}
	user := binding.GetLabels()[appAccessUserLabel]
	identity := bindingSubjectUser(binding)
	if !appAccessBindingStateCurrent(binding, project, instance, req, user, identity, roleName) {
		return true
	}
	account := IdentityAccount{User: user, RBACIdentity: identity}
	identityRoleName := appAccessIdentityRoleName(req)
	identityRole, err := client.Resource(appAccessClusterRoleGVR).Get(ctx, identityRoleName, metav1.GetOptions{})
	if err != nil || !appAccessIdentityRoleStateCurrent(identityRole, project, instance, req) {
		return true
	}
	identityBinding, err := client.Resource(appAccessBindingGVR).Get(ctx, appAccessIdentityBindingName(req, user), metav1.GetOptions{})
	return err != nil || !appAccessIdentityBindingStateCurrent(identityBinding, project, instance, req, account, identityRoleName)
}

func (h *AppAccessGrantHandler) revokeAppAccessBinding(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) error {
	binding, err := client.Resource(appAccessBindingGVR).Get(ctx, req.Grant, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, appAccessRoleName(req.Name)) {
		return fmt.Errorf("ClusterRoleBinding %q is not an app-access grant for this target", req.Grant)
	}
	if err := deleteIdentityAppAccessBinding(ctx, client, project, instance, req,
		binding.GetLabels()[appAccessUserLabel], bindingSubjectUser(binding)); err != nil {
		return err
	}
	return deleteGrantObjectWithPreconditions(ctx, client.Resource(appAccessBindingGVR), binding)
}

func (h *AppAccessGrantHandler) resetAppAccessBindings(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) error {
	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	list, err := client.Resource(appAccessBindingGVR).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for i := range list.Items {
		binding := &list.Items[i]
		if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, appAccessRoleName(req.Name)) {
			continue
		}
		if err := deleteIdentityAppAccessBinding(ctx, client, project, instance, req,
			binding.GetLabels()[appAccessUserLabel], bindingSubjectUser(binding)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err := deleteGrantObjectWithPreconditions(ctx, client.Resource(appAccessBindingGVR), binding); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func appAccessRoleName(instance string) string {
	return appAccessRolePrefix + "." + instance
}

func appAccessBindingName(instance, user string) string {
	name := appAccessRolePrefix + "." + instance + "." + user
	if len(name) <= 253 {
		return name
	}
	sum := sha256.Sum256([]byte(user))
	return appAccessRolePrefix + "." + instance + "." + hex.EncodeToString(sum[:])[:16]
}

func isAppAccessBindingFor(binding *unstructured.Unstructured, req appAccessGrantRequest, project, user, identity, roleName string) bool {
	return recognizedAppAccessBinding(binding, req, project, req.ProjectUID, roleName) &&
		binding.GetLabels()[appAccessUserLabel] == user &&
		bindingSubjectUser(binding) == identity
}

func validRoleReference(binding *unstructured.Unstructured, roleName string) bool {
	group, _, _ := unstructured.NestedString(binding.Object, "roleRef", "apiGroup")
	kind, _, _ := unstructured.NestedString(binding.Object, "roleRef", "kind")
	name, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
	return group == "rbac.authorization.k8s.io" && kind == "ClusterRole" && name == roleName
}

func bindingSubjectUser(binding *unstructured.Unstructured) string {
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	if len(subjects) != 1 {
		return ""
	}
	subject, _ := subjects[0].(map[string]any)
	if subject["kind"] != "User" || subject["apiGroup"] != "rbac.authorization.k8s.io" {
		return ""
	}
	name, _ := subject["name"].(string)
	return strings.TrimSpace(name)
}

func nestedStringValue(value any) string {
	text, _ := value.(string)
	return text
}
