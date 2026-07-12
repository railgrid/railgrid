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

// Promote to production (Phase D of the build→launch loop). "Production" is
// not a mode flip on the Project — it is a SECOND environment alongside the
// development sandbox: an artifact-mode ProjectEnvironment bound to a
// "<project>-prod" instance of the SAME template, provisioned with
// kedgeMode: production and each template imageInput set to the digest the
// per-component build recorded in git. The user promotes explicitly ("Promote
// to Prod") once the sandbox looks good and the build is green; promotion is
// repeatable (re-promote redeploys the latest digests). The dev sandbox keeps
// running untouched — see docs/app-studio-template-sandboxes.md.

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

// projectRegistryPullSecretName is the tenant Secret (holding a ghcr
// dockerconfigjson) the infrastructure provider bridges into the runtime
// namespace for a production instance. Convention shared with infra:
// "<instance>-registry" in the tenant default namespace.
func projectRegistryPullSecretName(instanceName string) string {
	return instanceName + "-registry"
}

// ensureProjectRegistryPullSecret derives a ghcr image-pull credential from the
// project's Code connection token and writes it as a dockerconfigjson Secret in
// the tenant workspace, named for the production instance. The infrastructure
// provider's secret-bridge controller carries it into the runtime namespace and
// attaches it to the default ServiceAccount so every production pod can pull the
// private image. A no-op (nil) when there is no connection/token.
func (s *Server) ensureProjectRegistryPullSecret(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) error {
	if c == nil || p == nil || p.Spec.Repository == nil {
		return nil
	}
	connectionRef := strings.TrimSpace(p.Spec.Repository.ConnectionRef)
	if connectionRef == "" {
		return nil
	}
	conn, err := c.Resource(codeConnectionResource, "").Get(ctx, connectionRef, metav1.GetOptions{})
	if err != nil {
		return err
	}
	secretName, _, _ := unstructured.NestedString(conn.Object, "spec", "secretRef", "name")
	secretKey, _, _ := unstructured.NestedString(conn.Object, "spec", "secretRef", "key")
	if strings.TrimSpace(secretKey) == "" {
		secretKey = "token"
	}
	login, _, _ := unstructured.NestedString(conn.Object, "status", "login")
	if strings.TrimSpace(secretName) == "" {
		return nil
	}

	tokenSecret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	token := secretDataValue(tokenSecret, secretKey)
	if strings.TrimSpace(token) == "" {
		return nil
	}

	username := strings.TrimSpace(login)
	if username == "" {
		username = "kedge-app-studio" // ghcr validates the token, not the username
	}
	dockerConfig, err := json.Marshal(map[string]any{
		"auths": map[string]any{
			"ghcr.io": map[string]any{
				"username": username,
				"password": token,
				"auth":     base64.StdEncoding.EncodeToString([]byte(username + ":" + token)),
			},
		},
	})
	if err != nil {
		return err
	}

	name := projectRegistryPullSecretName(projectTemplateProdInstanceName(p))
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": projectLLMSecretNamespace,
		},
		"type": "kubernetes.io/dockerconfigjson",
		"stringData": map[string]any{
			".dockerconfigjson": string(dockerConfig),
		},
	}}
	res := c.Resource(secretResource, projectLLMSecretNamespace)
	existing, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = res.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	_, err = res.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

const (
	projectProductionEnvironmentName = "production"
	projectProductionBindingName     = "prod"

	projectToolPromoteProject = "promote_project"
)

// projectPromoteRequest is the "Promote to Prod" form submission: the
// template's production inputs (ports, replicas, oidc, …). The instance name,
// kedgeMode, and per-component image fields are platform-owned and ignored if
// supplied — name/kedgeMode are deterministic and images come from the build.
type projectPromoteRequest struct {
	Values map[string]any `json:"values,omitempty"`
}

type projectPromoteResponse struct {
	Environment string                       `json:"environment"`
	Instance    string                       `json:"instance"`
	Components  []projectBuildCheckComponent `json:"components,omitempty"`
	Project     json.RawMessage              `json:"project,omitempty"`
}

// projectTemplateProdBinding builds the production binding: an instance of the
// template kind named "<project>-prod", provisioned with kedgeMode: production,
// the user's production input values, and each imageInput set to the built
// digest. Platform-owned fields (name, kedgeMode, image inputs) always win over
// anything in values.
func projectTemplateProdBinding(p *aiv1alpha1.Project, info projectTemplateInfo, images map[string]string, values map[string]any) (aiv1alpha1.ProjectProviderBindingSpec, error) {
	name := projectTemplateProdInstanceName(p)
	if name == "" {
		return aiv1alpha1.ProjectProviderBindingSpec{}, fmt.Errorf("project has no name")
	}
	merged := map[string]any{}
	for k, v := range values {
		merged[k] = v
	}
	for imageInput, image := range images {
		merged[imageInput] = image
	}
	merged["name"] = name
	merged["kedgeMode"] = "production"
	raw, err := json.Marshal(merged)
	if err != nil {
		return aiv1alpha1.ProjectProviderBindingSpec{}, err
	}
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     projectProductionBindingName,
		Provider: projectDevelopmentProviderAppStudio,
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name:       name,
			APIVersion: info.APIVersion,
			Kind:       info.Kind,
			Resource:   info.Resource,
		},
		Values: runtime.RawExtension{Raw: raw},
	}, nil
}

// promoteProject stands up (or re-deploys) the project's production
// environment from the current build evidence. It refuses unless every
// launchable component has a built image (check_project_build == "built"), so
// production never references an image that was not built.
func (s *Server) promoteProject(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, httpReq *http.Request, values map[string]any) (*aiv1alpha1.Project, projectPromoteResponse, error) {
	if p.Spec.Template == nil || strings.TrimSpace(p.Spec.Template.Name) == "" {
		return nil, projectPromoteResponse{}, newValidationError("project has no template to promote; select a template and build first")
	}

	check, err := s.checkProjectBuild(ctx, c, id, p)
	if err != nil {
		return nil, projectPromoteResponse{}, err
	}
	if check.Status != "built" {
		return nil, projectPromoteResponse{}, newValidationError("project is not ready to promote: " + check.Note)
	}

	info, err := fetchProjectTemplate(ctx, c, p.Spec.Template.Name)
	if err != nil {
		return nil, projectPromoteResponse{}, err
	}

	images := make(map[string]string, len(check.Components))
	for _, comp := range check.Components {
		if comp.ImageInput != "" && comp.Image != "" {
			images[comp.ImageInput] = comp.Image
		}
	}
	if len(images) == 0 {
		return nil, projectPromoteResponse{}, newValidationError("no built component images recorded for this project")
	}

	binding, err := projectTemplateProdBinding(p, info, images, values)
	if err != nil {
		return nil, projectPromoteResponse{}, err
	}

	next := p.DeepCopy()
	upsertProjectProductionBinding(next, binding)

	// Mint a ghcr image-pull credential (from the Code connection's token) as a
	// tenant Secret so the infrastructure provider can bridge it into the
	// runtime namespace — production images are private packages the runtime
	// cluster cannot otherwise pull. Best-effort: a public image needs none, so
	// a failure here must not block promotion.
	_ = s.ensureProjectRegistryPullSecret(ctx, c, p)

	updated, err := c.Projects().Update(ctx, next, metav1.UpdateOptions{})
	if err != nil {
		return nil, projectPromoteResponse{}, err
	}
	// Provision the production instance explicitly: reconcileProjectLiveBindings
	// only ensures live (development) bindings, so the production artifact
	// binding would otherwise never create its instance.
	if _, err := ensureProjectProviderResource(ctx, c, updated, binding, id); err != nil {
		return nil, projectPromoteResponse{}, err
	}
	reconciled, err := s.reconcileProjectLiveBindings(ctx, c, updated, id)
	if err != nil {
		return nil, projectPromoteResponse{}, err
	}

	raw, _ := json.Marshal(reconciled)
	return reconciled, projectPromoteResponse{
		Environment: projectProductionEnvironmentName,
		Instance:    projectTemplateProdInstanceName(p),
		Components:  check.Components,
		Project:     raw,
	}, nil
}

// upsertProjectProductionBinding sets the production environment's binding,
// replacing any existing one (re-promote redeploys), and leaves every other
// environment — notably the live development sandbox — untouched.
func upsertProjectProductionBinding(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec) {
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		if strings.TrimSpace(env.Name) != projectProductionEnvironmentName {
			continue
		}
		kept := env.Bindings[:0]
		for _, b := range env.Bindings {
			if strings.TrimSpace(b.Name) == projectProductionBindingName {
				continue
			}
			kept = append(kept, b)
		}
		env.Bindings = append(kept, binding)
		return
	}
	p.Spec.Environments = append(p.Spec.Environments, aiv1alpha1.ProjectEnvironmentSpec{
		Name:      projectProductionEnvironmentName,
		Mode:      aiv1alpha1.ProjectEnvironmentModeArtifact,
		Promotion: aiv1alpha1.ProjectPromotionManual,
		Bindings:  []aiv1alpha1.ProjectProviderBindingSpec{binding},
	})
}

// promoteProjectHandler is POST /api/projects/{project}/promote — the portal's
// "Promote to Prod" action.
func (s *Server) promoteProjectHandler(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req projectPromoteRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
			return
		}
	}
	_, resp, err := s.promoteProject(r.Context(), c, id, p, r, req.Values)
	if err != nil {
		writeProjectPromoteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// projectPromotionReadinessResponse gates the portal's "Promote to Prod"
// button and seeds its form: whether the build is green, the component image
// plan, and the production instance name.
type projectPromotionReadinessResponse struct {
	Template   string                  `json:"template,omitempty"`
	Instance   string                  `json:"instance,omitempty"`
	Promotable bool                    `json:"promotable"`
	Build      projectBuildCheckResult `json:"build"`
	// Production reports the live production environment when the project has
	// been promoted at least once: its phase and, once serving, its URL. Nil
	// when the project has never been promoted.
	Production *aiv1alpha1.ProjectProviderBindingStatus `json:"production,omitempty"`
}

// findProjectProductionBinding returns the project's production binding spec,
// or nil when it has never been promoted.
func findProjectProductionBinding(p *aiv1alpha1.Project) *aiv1alpha1.ProjectProviderBindingSpec {
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		if strings.TrimSpace(env.Name) != projectProductionEnvironmentName {
			continue
		}
		for j := range env.Bindings {
			if strings.TrimSpace(env.Bindings[j].Name) == projectProductionBindingName {
				return &env.Bindings[j]
			}
		}
	}
	return nil
}

// getProjectPromotion is GET /api/projects/{project}/promotion — the portal
// polls it to enable the "Promote to Prod" button (promotable) and to show the
// image plan; the template's production input schema for the form comes from
// the infrastructure describe-template surface.
func (s *Server) getProjectPromotion(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	check, err := s.checkProjectBuild(r.Context(), c, id, p)
	if err != nil {
		writeProjectPromoteError(w, err)
		return
	}
	template := ""
	if p.Spec.Template != nil {
		template = strings.TrimSpace(p.Spec.Template.Name)
	}
	resp := projectPromotionReadinessResponse{
		Template:   template,
		Instance:   projectTemplateProdInstanceName(p),
		Promotable: check.Status == "built",
		Build:      check,
	}
	// Artifact-mode (production) environments are not reported by the live
	// (development) environment status surface, so read the production
	// binding's status directly for its phase and serving URL.
	if prod := findProjectProductionBinding(p); prod != nil {
		st := projectProviderBindingStatus(r.Context(), c, p, *prod, id)
		resp.Production = &st
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeProjectPromoteError(w http.ResponseWriter, err error) {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
}
