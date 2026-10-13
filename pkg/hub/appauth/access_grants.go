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
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const (
	appAccessGrantRoute                      = "/{org}/workspaces/{workspace}/app-auth/grants"
	appAccessCapability                      = "app-access.grants.manage"
	appAccessRolePrefix                      = "railgrid-app-access"
	appAccessIdentityRolePrefix              = "railgrid-app-identity"
	appAccessLabel                           = "railgrid.ai/app-access"
	appAccessUserLabel                       = "app-studio.railgrid.ai/user"
	appAccessProject                         = "app-studio.railgrid.ai/project"
	appAccessProjectUID                      = "app-studio.railgrid.ai/project-uid"
	appAccessInstanceUID                     = "app-studio.railgrid.ai/instance-uid"
	appAccessSourceLabel                     = "app-studio.railgrid.ai/grant-source"
	appAccessMigrationAnnotation             = "app-studio.railgrid.ai/grant-migration"
	appAccessGrantSourcePublishing           = "publishing"
	appAccessGrantSourceAuthentication       = "authentication"
	appAccessGrantMigrationSchema            = "v2"
	appAccessGrantBindingPageSize      int64 = 500
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
// handler accepts only the fixed Infrastructure Instance tuple and writes
// source-specific UID-fenced primary grants.
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
	clientMu         sync.Mutex
	workspaceClients map[string]dynamic.Interface
}

func (h *AppAccessGrantHandler) workspaceClient(clusterID string) (dynamic.Interface, error) {
	h.clientMu.Lock()
	defer h.clientMu.Unlock()
	if client := h.workspaceClients[clusterID]; client != nil {
		return client, nil
	}
	client, err := h.clients(clusterID)
	if err != nil {
		return nil, err
	}
	if h.workspaceClients == nil {
		h.workspaceClients = make(map[string]dynamic.Interface)
	}
	h.workspaceClients[clusterID] = client
	return client, nil
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
	source, ok := appAccessGrantSourceForVerb(call.AppAccessSourceVerb)
	if !ok {
		http.Error(w, "app-access grant is not authorized", http.StatusForbidden)
		return
	}
	cluster, err := h.workspaceCluster(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil || !segmentRE.MatchString(cluster) {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	client, err := h.workspaceClient(cluster)
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
		if err := h.ensureAppAccessRole(r.Context(), client, project, instance, req, source); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := h.ensureAppAccessBinding(r.Context(), client, project, instance, req, account, source); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	switch req.Operation {
	case "revoke":
		if err := h.revokeAppAccessBinding(r.Context(), client, project, instance, req, source); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	case "reset":
		if err := h.resetAppAccessBindings(r.Context(), client, project, instance, req, source); err != nil {
			http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	items, err := h.listAppAccessBindings(r.Context(), client, project, instance, req, source)
	if err != nil {
		http.Error(w, "app access is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(appAccessGrantResponse{Items: items})
}

func appAccessGrantSourceForVerb(verb string) (string, bool) {
	switch verb {
	case "preview-grants", "preview", "publishing-grants", "publishing":
		return appAccessGrantSourcePublishing, true
	case "authentication-grants":
		return appAccessGrantSourceAuthentication, true
	default:
		return "", false
	}
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

func (h *AppAccessGrantHandler) ensureAppAccessRole(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) error {
	if err := retireStaleUIDAppAccessRoles(ctx, client, project, instance, req); err != nil {
		return err
	}
	if err := h.retireStaleLegacyAppAccessRole(ctx, client, req); err != nil {
		return err
	}
	return h.ensureCurrentAppAccessRole(ctx, client, project, instance, req, source)
}

func (h *AppAccessGrantHandler) ensureCurrentAppAccessRole(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) error {
	name := appAccessUIDRoleNameForSource(req, source)
	roles := client.Resource(appAccessClusterRoleGVR)
	role := newAppAccessRoleForSource(project, instance, req, source)
	if _, err := roles.Create(ctx, role, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := roles.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !recognizedAppAccessRoleForSource(existing, req, source) {
		return fmt.Errorf("ClusterRole %q is not the expected UID-fenced app-access role", name)
	}
	if appAccessRoleStateCurrentForSource(existing, project, instance, req, source) {
		return nil
	}
	applyAppAccessRoleState(existing, project, instance, req, source)
	_, err = roles.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// retireStaleUIDAppAccessRoles fences exact prior grants before their bindings
// can authorize a same-name replacement through the ordinary access verb. It
// scans only roles labeled for this app and Project, and never scans bindings.
func retireStaleUIDAppAccessRoles(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) error {
	roles := client.Resource(appAccessClusterRoleGVR)
	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	continueToken := ""
	var retirementErrs []error
	for {
		page, err := roles.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: appAccessGrantBindingPageSize, Continue: continueToken})
		if err != nil {
			return errors.Join(append(retirementErrs, fmt.Errorf("listing app-access roles for stale UID fences: %w", err))...)
		}
		for i := range page.Items {
			role := &page.Items[i]
			roleLabels := role.GetLabels()
			oldProjectUID, oldInstanceUID := roleLabels[appAccessProjectUID], roleLabels[appAccessInstanceUID]
			if oldProjectUID == req.ProjectUID && oldInstanceUID == req.UID {
				continue
			}
			if !segmentRE.MatchString(oldProjectUID) || !segmentRE.MatchString(oldInstanceUID) {
				continue
			}
			oldReq := req
			oldReq.ProjectUID, oldReq.UID = oldProjectUID, oldInstanceUID
			source := appAccessRoleSource(role)
			if source == "" {
				continue
			}
			if !appAccessRoleOwnerReferencesMatchUIDs(role, project, instance, oldProjectUID, oldInstanceUID) {
				continue
			}
			if uidAppAccessRoleTombstoned(role, project, instance, oldReq, source) {
				continue
			}
			if !recognizedAppAccessRoleForSource(role, oldReq, source) {
				continue
			}
			if err := tombstoneUIDAppAccessRole(ctx, roles, role, project, instance, oldReq, source); err != nil {
				retirementErrs = append(retirementErrs, fmt.Errorf("fencing stale app-access role %q: %w", role.GetName(), err))
			}
		}
		continueToken = page.GetContinue()
		if continueToken == "" {
			break
		}
	}
	return errors.Join(retirementErrs...)
}

func appAccessRoleOwnerReferencesMatchUIDs(role, project, instance *unstructured.Unstructured, projectUID, instanceUID string) bool {
	if role == nil || project == nil || instance == nil {
		return false
	}
	owners := role.GetOwnerReferences()
	return len(owners) == 2 && owners[0].APIVersion == "ai.railgrid.ai/v1alpha1" && owners[0].Kind == "Project" &&
		owners[0].Name == project.GetName() && owners[0].UID == types.UID(projectUID) &&
		owners[1].APIVersion == instance.GetAPIVersion() && owners[1].Kind == "Instance" &&
		owners[1].Name == instance.GetName() && owners[1].UID == types.UID(instanceUID)
}

func uidAppAccessRoleTombstoned(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) bool {
	if role == nil || role.GetName() != appAccessUIDRoleNameForSource(req, source) || !appAccessRoleOwnerReferencesMatchUIDs(role, project, instance, req.ProjectUID, req.UID) {
		return false
	}
	roleLabels := role.GetLabels()
	if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] != project.GetName() ||
		roleLabels[appAccessProjectUID] != req.ProjectUID || roleLabels[appAccessInstanceUID] != req.UID {
		return false
	}
	if appAccessRoleSource(role) != source {
		return false
	}
	if _, found := role.Object["aggregationRule"]; found {
		return false
	}
	rules, found, err := unstructured.NestedSlice(role.Object, "rules")
	return err == nil && found && len(rules) == 0
}

func tombstoneUIDAppAccessRole(ctx context.Context, roles dynamic.ResourceInterface, role, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) error {
	for attempt := 0; attempt < 2; attempt++ {
		if uidAppAccessRoleTombstoned(role, project, instance, req, source) {
			return nil
		}
		if !recognizedAppAccessRoleForSource(role, req, source) || !appAccessRoleOwnerReferencesMatchUIDs(role, project, instance, req.ProjectUID, req.UID) {
			return fmt.Errorf("ClusterRole %q no longer matches the exact stale app-access role", role.GetName())
		}
		if role.GetUID() == "" || role.GetResourceVersion() == "" {
			return fmt.Errorf("ClusterRole %q lacks UID or resourceVersion fence", role.GetName())
		}
		role.Object["rules"] = []any{}
		if _, err := roles.Update(ctx, role, metav1.UpdateOptions{}); err == nil {
			klog.FromContext(ctx).Info("fenced stale UID-qualified app-access role with empty rules", "role", role.GetName(), "projectUID", req.ProjectUID, "instanceUID", req.UID)
			return nil
		} else if !apierrors.IsConflict(err) {
			return err
		}
		latest, err := roles.Get(ctx, role.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if latest.GetUID() == "" || latest.GetUID() != role.GetUID() {
			return fmt.Errorf("ClusterRole %q was replaced while fencing stale access", role.GetName())
		}
		role = latest
	}
	return fmt.Errorf("ClusterRole %q changed repeatedly while fencing stale access", role.GetName())
}

func (h *AppAccessGrantHandler) ensureAppAccessBinding(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount, source string) error {
	name := appAccessUIDBindingName(req, account.User, source)
	roleName := appAccessUIDRoleNameForSource(req, source)
	binding := newAppAccessBinding(project, instance, req, account, roleName, source)
	_, err := client.Resource(appAccessBindingGVR).Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := client.Resource(appAccessBindingGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !isAppAccessBindingFor(existing, req, project.GetName(), account.User, account.RBACIdentity, roleName, source, source == appAccessGrantSourcePublishing) {
		return fmt.Errorf("ClusterRoleBinding %q is not the requested app-access grant", name)
	}
	if appAccessBindingStateCurrent(existing, project, instance, req, account.User, account.RBACIdentity, roleName, source) {
		return nil
	}
	applyAppAccessBindingState(existing, project, instance, req, account.User, source)
	existing.Object["subjects"] = []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": account.RBACIdentity}}
	_, err = client.Resource(appAccessBindingGVR).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

// migrateCurrentAppAccessGrants upgrades branch-created grants only after the
// migration reconciler has passed Project UID, environment binding, and
// Instance owner checks. New grant objects are source-qualified and refer to
// UID-qualified roles; the static legacy role is terminally fenced with empty
// rules so unlabelled foreign bindings cannot reactivate old access.
func (h *AppAccessGrantHandler) migrateCurrentAppAccessGrants(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest) error {
	if err := retireStaleUIDAppAccessRoles(ctx, client, project, instance, req); err != nil {
		return err
	}
	legacyName := appAccessRoleName(req.Name)
	legacyRoles := client.Resource(appAccessClusterRoleGVR)
	legacyRole, err := legacyRoles.Get(ctx, legacyName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// A valid marker is authoritative only while the current UID role still
	// has its fixed policy and the legacy role is already inert. If an older
	// writer recreated the static role, the active legacy state below is
	// processed again.
	if legacyAppAccessRoleTombstoned(legacyRole, req) {
		currentRole, getErr := client.Resource(appAccessClusterRoleGVR).Get(ctx, appAccessUIDRoleNameForSource(req, appAccessGrantSourcePublishing), metav1.GetOptions{})
		if getErr == nil && appAccessMigrationMarkerCurrent(currentRole, project, instance, req) {
			return nil
		}
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return getErr
		}
	}
	if !recognizedLegacyAppAccessRole(legacyRole, req) && !legacyAppAccessRoleTombstoned(legacyRole, req) {
		return nil
	}

	// A missing or previous Project UID cannot be adopted. Empty the exact
	// fixed-policy legacy role below so existing bindings cannot authorize a
	// same-name replacement, while leaving those binding objects intact.
	if legacyRole.GetLabels()[appAccessProjectUID] != req.ProjectUID {
		if err := tombstoneLegacyAppAccessRole(ctx, legacyRoles, legacyRole, req); err != nil {
			return err
		}
		return nil
	}

	if err := h.ensureCurrentAppAccessRole(ctx, client, project, instance, req, appAccessGrantSourcePublishing); err != nil {
		return err
	}
	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	bindings := client.Resource(appAccessBindingGVR)
	continueToken := ""
	var migrationErrs []error
	for {
		page, err := bindings.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: appAccessGrantBindingPageSize, Continue: continueToken})
		if err != nil {
			return errors.Join(append(migrationErrs, fmt.Errorf("listing app-access grants: %w", err))...)
		}
		for i := range page.Items {
			binding := &page.Items[i]
			bindingSource := appAccessBindingSource(binding)
			if !validRoleReference(binding, legacyName) && !validRoleReference(binding, appAccessUIDRoleNameForSource(req, bindingSource)) {
				continue
			}
			source, managed := appAccessMigrationBindingSource(binding, project, instance, req)
			if !managed {
				if recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, appAccessUIDRoleNameForSource(req, source), source, source == appAccessGrantSourcePublishing) {
					continue
				}
				klog.FromContext(ctx).Error(fmt.Errorf("malformed legacy app-access binding"), "leaving app-access binding untouched; the legacy role will be fenced", "binding", binding.GetName())
				continue
			}
			if binding.GetLabels()[appAccessProjectUID] != req.ProjectUID {
				continue
			}
			user := binding.GetLabels()[appAccessUserLabel]
			account := IdentityAccount{User: user, RBACIdentity: bindingSubjectUser(binding)}
			if err := h.ensureCurrentAppAccessRole(ctx, client, project, instance, req, source); err != nil {
				migrationErrs = append(migrationErrs, fmt.Errorf("ensuring %s app-access role for %q: %w", source, binding.GetName(), err))
				continue
			}
			if err := h.ensureAppAccessBinding(ctx, client, project, instance, req, account, source); err != nil {
				migrationErrs = append(migrationErrs, fmt.Errorf("migrating app-access binding %q: %w", binding.GetName(), err))
				continue
			}
			if binding.GetName() != appAccessUIDBindingName(req, user, source) {
				validate := func(candidate *unstructured.Unstructured) bool {
					candidateSource, candidateManaged := appAccessMigrationBindingSource(candidate, project, instance, req)
					return candidateManaged && candidateSource == source
				}
				if err := deleteGrantObjectWithSingleRetry(ctx, bindings, binding, validate); err != nil && !apierrors.IsNotFound(err) {
					migrationErrs = append(migrationErrs, fmt.Errorf("retiring migrated app-access binding %q: %w", binding.GetName(), err))
				}
			}
		}
		continueToken = page.GetContinue()
		if continueToken == "" {
			break
		}
	}
	if len(migrationErrs) > 0 {
		return errors.Join(migrationErrs...)
	}

	currentRole, err := client.Resource(appAccessClusterRoleGVR).Get(ctx, appAccessUIDRoleNameForSource(req, appAccessGrantSourcePublishing), metav1.GetOptions{})
	if err != nil {
		return err
	}
	setRequiredGrantAnnotations(currentRole, map[string]string{appAccessMigrationAnnotation: appAccessMigrationMarker(project, instance, req)})
	if _, err := client.Resource(appAccessClusterRoleGVR).Update(ctx, currentRole, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("persisting app-access migration marker: %w", err)
	}
	return tombstoneLegacyAppAccessRole(ctx, legacyRoles, legacyRole, req)
}

func (h *AppAccessGrantHandler) retireStaleLegacyAppAccessRole(ctx context.Context, client dynamic.Interface, req appAccessGrantRequest) error {
	roles := client.Resource(appAccessClusterRoleGVR)
	role, err := roles.Get(ctx, appAccessRoleName(req.Name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if (!recognizedLegacyAppAccessRole(role, req) && !legacyAppAccessRoleTombstoned(role, req)) ||
		legacyAppAccessRoleTombstoned(role, req) || role.GetLabels()[appAccessProjectUID] == req.ProjectUID {
		return nil
	}
	return tombstoneLegacyAppAccessRole(ctx, roles, role, req)
}

func recognizedLegacyAppAccessRole(role *unstructured.Unstructured, req appAccessGrantRequest) bool {
	if role == nil || role.GetName() != appAccessRoleName(req.Name) {
		return false
	}
	if _, found := role.Object["aggregationRule"]; found {
		return false
	}
	roleLabels := role.GetLabels()
	if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] != req.Project {
		return false
	}
	if _, present := roleLabels[appAccessSourceLabel]; present {
		return false
	}
	if uid := roleLabels[appAccessProjectUID]; uid != "" && !segmentRE.MatchString(uid) {
		return false
	}
	if instanceUID := roleLabels[appAccessInstanceUID]; instanceUID != "" && !segmentRE.MatchString(instanceUID) {
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

func legacyAppAccessRoleTombstoned(role *unstructured.Unstructured, req appAccessGrantRequest) bool {
	if role == nil || role.GetName() != appAccessRoleName(req.Name) || role.GetLabels()[appAccessLabel] != req.Name || role.GetLabels()[appAccessProject] != req.Project {
		return false
	}
	if _, present := role.GetLabels()[appAccessSourceLabel]; present {
		return false
	}
	if _, found := role.Object["aggregationRule"]; found {
		return false
	}
	rules, found, err := unstructured.NestedSlice(role.Object, "rules")
	return err == nil && found && len(rules) == 0
}

func tombstoneLegacyAppAccessRole(ctx context.Context, roles dynamic.ResourceInterface, role *unstructured.Unstructured, req appAccessGrantRequest) error {
	for attempt := 0; attempt < 2; attempt++ {
		if legacyAppAccessRoleTombstoned(role, req) {
			return nil
		}
		if !recognizedLegacyAppAccessRole(role, req) {
			return fmt.Errorf("ClusterRole %q is not a recognized legacy app-access role; refusing to change it", role.GetName())
		}
		if role.GetUID() == "" || role.GetResourceVersion() == "" {
			return fmt.Errorf("legacy ClusterRole %q lacks UID or resourceVersion fence", role.GetName())
		}
		role.Object["rules"] = []any{}
		if _, err := roles.Update(ctx, role, metav1.UpdateOptions{}); err == nil {
			klog.FromContext(ctx).Info("fenced legacy app-access role with empty rules; existing bindings remain attached but no longer grant access", "role", role.GetName(), "projectUID", role.GetLabels()[appAccessProjectUID])
			return nil
		} else if !apierrors.IsConflict(err) {
			return err
		}
		latest, err := roles.Get(ctx, role.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if latest.GetUID() == "" || latest.GetUID() != role.GetUID() {
			return fmt.Errorf("legacy ClusterRole %q was replaced during fencing", role.GetName())
		}
		role = latest
	}
	return fmt.Errorf("legacy ClusterRole %q changed repeatedly while fencing", role.GetName())
}

func appAccessMigrationMarker(project, instance *unstructured.Unstructured, req appAccessGrantRequest) string {
	return strings.Join([]string{appAccessGrantMigrationSchema, req.ProjectUID, string(instance.GetUID())}, ":")
}

func appAccessMigrationMarkerCurrent(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest) bool {
	return appAccessRoleStateCurrent(role, project, instance, req) && role.GetAnnotations()[appAccessMigrationAnnotation] == appAccessMigrationMarker(project, instance, req)
}

func setRequiredGrantAnnotations(obj *unstructured.Unstructured, required map[string]string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string, len(required))
	}
	for key, value := range required {
		annotations[key] = value
	}
	obj.SetAnnotations(annotations)
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

func deleteGrantObjectWithSingleRetry(ctx context.Context, resource dynamic.ResourceInterface, obj *unstructured.Unstructured, validate func(*unstructured.Unstructured) bool) error {
	err := deleteGrantObjectWithPreconditions(ctx, resource, obj)
	if !apierrors.IsConflict(err) {
		return err
	}
	latest, getErr := resource.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(getErr) {
		return nil
	}
	if getErr != nil {
		return getErr
	}
	if obj.GetUID() == "" || latest.GetUID() == "" || latest.GetUID() != obj.GetUID() {
		return fmt.Errorf("ClusterRoleBinding %q was replaced during deletion; refusing to delete the new object", obj.GetName())
	}
	if validate != nil && !validate(latest) {
		return fmt.Errorf("ClusterRoleBinding %q changed shape during deletion; refusing to delete it", obj.GetName())
	}
	return deleteGrantObjectWithPreconditions(ctx, resource, latest)
}

func newAppAccessRole(project, instance *unstructured.Unstructured, req appAccessGrantRequest) *unstructured.Unstructured {
	return newAppAccessRoleForSource(project, instance, req, appAccessGrantSourcePublishing)
}

func newAppAccessRoleForSource(project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) *unstructured.Unstructured {
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": appAccessUIDRoleNameForSource(req, source)},
	}}
	applyAppAccessRoleState(role, project, instance, req, source)
	return role
}

func appAccessRoleRules(req appAccessGrantRequest) []any {
	return []any{
		map[string]any{"apiGroups": []any{req.Group}, "resources": []any{req.Resource + "/" + AccessSubresource}, "resourceNames": []any{req.Name}, "verbs": []any{AccessVerb}},
		map[string]any{"nonResourceURLs": []any{"/"}, "verbs": []any{"access"}},
	}
}

func appAccessRoleRulesForSource(req appAccessGrantRequest, source string) []any {
	rules := appAccessRoleRules(req)
	if source != appAccessGrantSourceAuthentication {
		return rules
	}
	subresource, ok := authenticationAppAccessSubresource(req.ProjectUID, req.UID)
	if !ok {
		return nil
	}
	return append(rules, map[string]any{
		"apiGroups": []any{req.Group}, "resources": []any{req.Resource + "/" + subresource},
		"resourceNames": []any{req.Name}, "verbs": []any{AccessVerb},
	})
}

// authenticationAppAccessSubresource is granted only by the authentication
// source's primary role. The domain separator keeps it distinct from the
// legacy secondary identity coordinate and fences both authoritative object
// UIDs while retaining the existing instances/access SAR contract.
func authenticationAppAccessSubresource(projectUID, instanceUID string) (string, bool) {
	if !segmentRE.MatchString(projectUID) || !segmentRE.MatchString(instanceUID) {
		return "", false
	}
	sum := sha256.Sum256([]byte("railgrid.app-auth.authentication-grant.v1\x00" + projectUID + "\x00" + instanceUID))
	return AccessSubresource + ".auth." + hex.EncodeToString(sum[:])[:32], true
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

// identityAppAccessSubresource recognizes the legacy secondary identity
// permission for cleanup. Current identity issuance uses the authentication
// primary binding through authenticationAppAccessSubresource instead.
func identityAppAccessSubresource(projectUID, instanceUID string) (string, bool) {
	if !segmentRE.MatchString(projectUID) || !segmentRE.MatchString(instanceUID) {
		return "", false
	}
	sum := sha256.Sum256([]byte(projectUID + "\x00" + instanceUID))
	return AccessSubresource + "." + hex.EncodeToString(sum[:])[:32], true
}

func applyAppAccessRoleState(role *unstructured.Unstructured, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) {
	setRequiredGrantLabels(role, map[string]string{
		appAccessLabel: req.Name, appAccessProject: project.GetName(), appAccessProjectUID: req.ProjectUID,
		appAccessInstanceUID: string(instance.GetUID()), appAccessSourceLabel: source,
	})
	role.Object["rules"] = appAccessRoleRulesForSource(req, source)
	role.SetOwnerReferences(appAccessOwnerReferences(project, instance))
}

func newAppAccessBinding(project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount, roleName, source string) *unstructured.Unstructured {
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": appAccessUIDBindingName(req, account.User, source)},
		"subjects": []any{map[string]any{"kind": "User", "apiGroup": "rbac.authorization.k8s.io", "name": account.RBACIdentity}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": roleName},
	}}
	applyAppAccessBindingState(binding, project, instance, req, account.User, source)
	return binding
}

func appAccessIdentityRoleName(req appAccessGrantRequest) string {
	sum := sha256.Sum256([]byte(req.ProjectUID + "\x00" + req.UID + "\x00" + req.Name))
	return appAccessIdentityRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
}

func appAccessIdentityBindingName(req appAccessGrantRequest, user string) string {
	return appAccessIdentityBindingNameForSource(req, user, appAccessGrantSourcePublishing)
}

func appAccessIdentityBindingNameForSource(req appAccessGrantRequest, user, source string) string {
	identity := appAccessIdentityRoleName(req) + "\x00"
	if source != appAccessGrantSourcePublishing {
		identity += source + "\x00"
	}
	sum := sha256.Sum256([]byte(identity + user))
	return appAccessIdentityRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
}

func appAccessBindingNameForSource(instance, user, source string) string {
	name := appAccessBindingName(instance, user)
	if source == appAccessGrantSourcePublishing {
		return name
	}
	sum := sha256.Sum256([]byte(instance + "\x00" + user + "\x00" + source))
	return appAccessRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
}

func appAccessUIDBindingName(req appAccessGrantRequest, user, source string) string {
	sum := sha256.Sum256([]byte(req.ProjectUID + "\x00" + req.UID + "\x00" + req.Name + "\x00" + user + "\x00" + source))
	return appAccessRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
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

func deleteIdentityAppAccessBindingForSource(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, user, identity, source string) error {
	name := appAccessIdentityBindingNameForSource(req, user, source)
	binding, err := client.Resource(appAccessBindingGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	account := IdentityAccount{User: user, RBACIdentity: identity}
	if !recognizedAppAccessIdentityBinding(binding, project, instance, req, account, appAccessIdentityRoleName(req), source, source == appAccessGrantSourcePublishing) {
		return fmt.Errorf("ClusterRoleBinding %q is not the UID-fenced app-access grant", name)
	}
	return deleteGrantObjectWithPreconditions(ctx, client.Resource(appAccessBindingGVR), binding)
}

func recognizedAppAccessIdentityBinding(binding, project, instance *unstructured.Unstructured, req appAccessGrantRequest, account IdentityAccount, roleName, source string, allowLegacy bool) bool {
	if binding == nil || binding.GetName() != appAccessIdentityBindingNameForSource(req, account.User, source) {
		return false
	}
	labels := binding.GetLabels()
	return labels[appAccessLabel] == req.Name && labels[appAccessProject] == project.GetName() &&
		labels[appAccessProjectUID] == req.ProjectUID && labels[appAccessInstanceUID] == string(instance.GetUID()) &&
		labels[appAccessUserLabel] == account.User && appAccessBindingSourceMatches(binding, source, allowLegacy) &&
		validRoleReference(binding, roleName) && bindingSubjectUser(binding) == account.RBACIdentity
}

func applyAppAccessBindingState(binding *unstructured.Unstructured, project, instance *unstructured.Unstructured, req appAccessGrantRequest, user, source string) {
	setRequiredGrantLabels(binding, map[string]string{
		appAccessLabel: req.Name, appAccessProject: project.GetName(), appAccessProjectUID: req.ProjectUID,
		appAccessInstanceUID: string(instance.GetUID()), appAccessUserLabel: user, appAccessSourceLabel: source,
	})
	binding.SetOwnerReferences(appAccessOwnerReferences(project, instance))
}

func appAccessRoleStateCurrent(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest) bool {
	return appAccessRoleStateCurrentForSource(role, project, instance, req, appAccessGrantSourcePublishing)
}

func appAccessRoleStateCurrentForSource(role, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) bool {
	return recognizedAppAccessRoleForSource(role, req, source) && role.GetLabels()[appAccessSourceLabel] == source && role.GetLabels()[appAccessProject] == project.GetName() &&
		role.GetLabels()[appAccessProjectUID] == req.ProjectUID && role.GetLabels()[appAccessInstanceUID] == string(instance.GetUID()) &&
		appAccessOwnerReferencesCurrent(role, project, instance)
}

func appAccessBindingStateCurrent(binding, project, instance *unstructured.Unstructured, req appAccessGrantRequest, user, identity, roleName, source string) bool {
	return binding != nil && binding.GetName() == appAccessUIDBindingName(req, user, source) &&
		isAppAccessBindingFor(binding, req, project.GetName(), user, identity, roleName, source, source == appAccessGrantSourcePublishing) &&
		binding.GetLabels()[appAccessInstanceUID] == string(instance.GetUID()) && appAccessOwnerReferencesCurrent(binding, project, instance)
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
	return recognizedAppAccessRoleForSource(role, req, appAccessGrantSourcePublishing)
}

func recognizedAppAccessRoleForSource(role *unstructured.Unstructured, req appAccessGrantRequest, source string) bool {
	if role == nil || role.GetName() != appAccessUIDRoleNameForSource(req, source) ||
		(source != appAccessGrantSourcePublishing && source != appAccessGrantSourceAuthentication) {
		return false
	}
	if _, found := role.Object["aggregationRule"]; found {
		return false
	}
	roleLabels := role.GetLabels()
	if roleLabels[appAccessLabel] != req.Name || roleLabels[appAccessProject] != req.Project ||
		roleLabels[appAccessProjectUID] != req.ProjectUID || roleLabels[appAccessInstanceUID] != req.UID {
		return false
	}
	if roleLabels[appAccessSourceLabel] != source {
		return false
	}
	if !segmentRE.MatchString(roleLabels[appAccessProjectUID]) || !segmentRE.MatchString(roleLabels[appAccessInstanceUID]) {
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
	expected, err := json.Marshal(appAccessRoleRulesForSource(req, source))
	return err == nil && string(actual) == string(expected)
}

func recognizedAppAccessBinding(binding *unstructured.Unstructured, req appAccessGrantRequest, project, projectUID, roleName, source string, allowLegacy bool) bool {
	if binding == nil {
		return false
	}
	bindingLabels := binding.GetLabels()
	user := bindingLabels[appAccessUserLabel]
	instanceUID := bindingLabels[appAccessInstanceUID]
	if bindingLabels[appAccessLabel] != req.Name || bindingLabels[appAccessProject] != project ||
		projectUID == "" || bindingLabels[appAccessProjectUID] != projectUID || user == "" ||
		!appAccessBindingSourceMatches(binding, source, allowLegacy) ||
		(instanceUID != "" && !segmentRE.MatchString(instanceUID)) || bindingSubjectUser(binding) == "" {
		return false
	}
	if binding.GetName() == appAccessUIDBindingName(req, user, source) {
		return validRoleReference(binding, roleName)
	}
	if !allowLegacy || binding.GetName() != appAccessBindingNameForSource(req.Name, user, source) {
		return false
	}
	return validRoleReference(binding, roleName) || validRoleReference(binding, appAccessRoleName(req.Name))
}

func appAccessBindingSourceMatches(binding *unstructured.Unstructured, source string, allowLegacy bool) bool {
	value, present := binding.GetLabels()[appAccessSourceLabel]
	if !present {
		return allowLegacy && source == appAccessGrantSourcePublishing
	}
	return value == source
}

func recognizedLegacyAppAccessBinding(binding *unstructured.Unstructured, req appAccessGrantRequest) bool {
	if binding == nil {
		return false
	}
	objectLabels := binding.GetLabels()
	user := objectLabels[appAccessUserLabel]
	projectUID := objectLabels[appAccessProjectUID]
	instanceUID := objectLabels[appAccessInstanceUID]
	if objectLabels[appAccessLabel] != req.Name || objectLabels[appAccessProject] != req.Project ||
		!segmentRE.MatchString(projectUID) || user == "" || !segmentRE.MatchString(user) ||
		(instanceUID != "" && !segmentRE.MatchString(instanceUID)) || bindingSubjectUser(binding) == "" {
		return false
	}
	if _, sourceLabeled := objectLabels[appAccessSourceLabel]; sourceLabeled {
		return false
	}
	return binding.GetName() == appAccessBindingName(req.Name, user) && validRoleReference(binding, appAccessRoleName(req.Name))
}

// appAccessMigrationBindingSource recognizes only exact legacy grants or an
// already-created final UID-qualified binding after an interrupted migration.
// The deployed legacy format had no source label: those approvals are
// publishing-only. Source-labelled static bindings are unsupported and stay
// attached to the tombstoned legacy role.
func appAccessMigrationBindingSource(binding, project, instance *unstructured.Unstructured, req appAccessGrantRequest) (string, bool) {
	if recognizedLegacyAppAccessBinding(binding, req) {
		return appAccessBindingSource(binding), true
	}
	source := appAccessBindingSource(binding)
	roleName := appAccessUIDRoleNameForSource(req, source)
	if recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, roleName, source, source == appAccessGrantSourcePublishing) &&
		binding.GetLabels()[appAccessInstanceUID] == string(instance.GetUID()) && appAccessOwnerReferencesCurrent(binding, project, instance) {
		return source, true
	}
	return "", false
}

func appAccessBindingSource(binding *unstructured.Unstructured) string {
	switch binding.GetLabels()[appAccessSourceLabel] {
	case appAccessGrantSourceAuthentication:
		return appAccessGrantSourceAuthentication
	default:
		return appAccessGrantSourcePublishing
	}
}

func (h *AppAccessGrantHandler) listAppAccessBindings(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) ([]AppAccessGrant, error) {
	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	bindings := client.Resource(appAccessBindingGVR)
	items := make([]AppAccessGrant, 0)
	roleName := appAccessUIDRoleNameForSource(req, source)
	var role *unstructured.Unstructured
	var roleErr error
	continueToken := ""
	for {
		list, err := bindings.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: appAccessGrantBindingPageSize, Continue: continueToken})
		if err != nil {
			return nil, err
		}
		if len(list.Items) > 0 && role == nil && roleErr == nil {
			role, roleErr = client.Resource(appAccessClusterRoleGVR).Get(ctx, roleName, metav1.GetOptions{})
		}
		for i := range list.Items {
			binding := &list.Items[i]
			if binding.GetLabels()[appAccessLabel] != req.Name || binding.GetLabels()[appAccessProject] != project.GetName() {
				continue
			}
			if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, roleName, source, source == appAccessGrantSourcePublishing) {
				continue
			}
			user := binding.GetLabels()[appAccessUserLabel]
			items = append(items, AppAccessGrant{
				Name: binding.GetName(), UID: string(binding.GetUID()), User: user,
				Publication: req.Name, Phase: "Active",
				MigrationPending: appAccessGrantMigrationPending(project, instance, req, binding, role, roleErr, source),
			})
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].User < items[j].User })
	return items, nil
}

// appAccessGrantMigrationPending is an advisory read-only check. RBAC read
// failures keep the hint pending without making an otherwise valid list fail.
func appAccessGrantMigrationPending(project, instance *unstructured.Unstructured, req appAccessGrantRequest, binding, role *unstructured.Unstructured, roleErr error, source string) bool {
	roleName := appAccessUIDRoleNameForSource(req, source)
	if roleErr != nil || !appAccessRoleStateCurrentForSource(role, project, instance, req, source) {
		return true
	}
	user := binding.GetLabels()[appAccessUserLabel]
	identity := bindingSubjectUser(binding)
	return !appAccessBindingStateCurrent(binding, project, instance, req, user, identity, roleName, source)
}

func (h *AppAccessGrantHandler) revokeAppAccessBinding(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) error {
	binding, err := client.Resource(appAccessBindingGVR).Get(ctx, req.Grant, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	roleName := appAccessUIDRoleNameForSource(req, source)
	if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, roleName, source, source == appAccessGrantSourcePublishing) {
		return fmt.Errorf("ClusterRoleBinding %q is not an app-access grant for this target", req.Grant)
	}
	validatePrimary := func(candidate *unstructured.Unstructured) bool {
		return recognizedAppAccessBinding(candidate, req, project.GetName(), req.ProjectUID, roleName, source, source == appAccessGrantSourcePublishing)
	}
	if err := deleteGrantObjectWithSingleRetry(ctx, client.Resource(appAccessBindingGVR), binding, validatePrimary); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := deleteIdentityAppAccessBindingForSource(ctx, client, project, instance, req,
		binding.GetLabels()[appAccessUserLabel], bindingSubjectUser(binding), source); err != nil && !apierrors.IsNotFound(err) {
		klog.FromContext(ctx).Error(err, "primary app-access grant revoked but identity binding cleanup failed", "binding", req.Grant, "source", source)
	}
	return nil
}

func (h *AppAccessGrantHandler) resetAppAccessBindings(ctx context.Context, client dynamic.Interface, project, instance *unstructured.Unstructured, req appAccessGrantRequest, source string) error {
	selector := labels.Set{appAccessLabel: req.Name, appAccessProject: project.GetName()}.AsSelector().String()
	bindings := client.Resource(appAccessBindingGVR)
	continueToken := ""
	for {
		list, err := bindings.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: appAccessGrantBindingPageSize, Continue: continueToken})
		if err != nil {
			return err
		}
		for i := range list.Items {
			binding := &list.Items[i]
			roleName := appAccessUIDRoleNameForSource(req, source)
			if !recognizedAppAccessBinding(binding, req, project.GetName(), req.ProjectUID, roleName, source, source == appAccessGrantSourcePublishing) {
				continue
			}
			validatePrimary := func(candidate *unstructured.Unstructured) bool {
				return recognizedAppAccessBinding(candidate, req, project.GetName(), req.ProjectUID, roleName, source, source == appAccessGrantSourcePublishing)
			}
			if err := deleteGrantObjectWithSingleRetry(ctx, bindings, binding, validatePrimary); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := deleteIdentityAppAccessBindingForSource(ctx, client, project, instance, req,
				binding.GetLabels()[appAccessUserLabel], bindingSubjectUser(binding), source); err != nil && !apierrors.IsNotFound(err) {
				klog.FromContext(ctx).Error(err, "primary app-access grant reset but identity binding cleanup failed", "binding", binding.GetName(), "source", source)
			}
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}
	return nil
}

func appAccessRoleName(instance string) string {
	return appAccessRolePrefix + "." + instance
}

func appAccessUIDRoleName(req appAccessGrantRequest) string {
	sum := sha256.Sum256([]byte(req.ProjectUID + "\x00" + req.UID + "\x00" + req.Name))
	base := appAccessRolePrefix + "."
	suffix := "." + hex.EncodeToString(sum[:])[:24]
	name := req.Name
	maxNameLength := 253 - len(base) - len(suffix)
	if len(name) > maxNameLength {
		nameSum := sha256.Sum256([]byte(req.Name))
		name = hex.EncodeToString(nameSum[:])[:32]
	}
	return base + name + suffix
}

func appAccessUIDRoleNameForSource(req appAccessGrantRequest, source string) string {
	if source == appAccessGrantSourcePublishing {
		return appAccessUIDRoleName(req)
	}
	if source != appAccessGrantSourceAuthentication {
		return ""
	}
	sum := sha256.Sum256([]byte("railgrid.app-auth.authentication-role.v1\x00" + req.ProjectUID + "\x00" + req.UID + "\x00" + req.Name))
	return appAccessRolePrefix + ".authentication." + hex.EncodeToString(sum[:])[:32]
}

func appAccessRoleSource(role *unstructured.Unstructured) string {
	if role == nil {
		return ""
	}
	source := role.GetLabels()[appAccessSourceLabel]
	switch source {
	case appAccessGrantSourcePublishing, appAccessGrantSourceAuthentication:
		return source
	default:
		return ""
	}
}

func appAccessBindingName(instance, user string) string {
	name := appAccessRolePrefix + "." + instance + "." + user
	if len(name) <= 253 {
		return name
	}
	sum := sha256.Sum256([]byte(instance + "\x00" + user))
	return appAccessRolePrefix + "." + hex.EncodeToString(sum[:])[:32]
}

func isAppAccessBindingFor(binding *unstructured.Unstructured, req appAccessGrantRequest, project, user, identity, roleName, source string, allowLegacy bool) bool {
	return recognizedAppAccessBinding(binding, req, project, req.ProjectUID, roleName, source, allowLegacy) &&
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
