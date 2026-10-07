/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/internal/projectidentity"
	"github.com/railgrid/provider-sdk/dataplane"
)

type providerRESTConfigFactory interface {
	ProviderRESTConfig(string) (*rest.Config, error)
}

// projectIdentityToken derives the Project-owner identity from the current
// persisted Project every time. scopedidentity.Cache reuses a live token only
// when this complete rule set has not changed.
func (s *Server) projectIdentityToken(ctx context.Context, id identity, p *aiv1alpha1.Project) (string, error) {
	if s != nil && s.projectIdentityTokenFor != nil {
		return s.projectIdentityTokenFor(ctx, id, p)
	}
	if s == nil || s.projectIdentities == nil {
		return "", errors.New("the hub-minted Project identity service is unavailable")
	}
	if p == nil || strings.TrimSpace(p.Name) == "" || strings.TrimSpace(string(p.UID)) == "" {
		return "", errors.New("project identity requires a persisted Project name and UID")
	}
	if !dataplane.IsClusterID(id.clusterID) {
		return "", fmt.Errorf("project identity requires a kcp cluster ID, got %q", id.clusterID)
	}
	current, err := s.currentProjectForIdentity(ctx, id, p)
	if err != nil {
		return "", err
	}
	return s.projectIdentities.TokenVersioned(ctx, projectidentity.Owner(current, id.clusterID), current.Generation, projectidentity.Rules(current))
}

func (s *Server) currentProjectForIdentity(ctx context.Context, id identity, project *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	if project == nil {
		return nil, errors.New("project identity requires a persisted Project")
	}
	provider := id.provider
	if provider == nil && s.callers != nil {
		var err error
		provider, err = s.callers.AsProvider(id.clusterID)
		if err != nil {
			return nil, fmt.Errorf("resolve App Studio client for current Project read: %w", err)
		}
	}
	if provider == nil {
		return nil, errors.New("current Project read requires App Studio's provider client")
	}
	object, err := provider.Resource(asclient.ProjectGVR).Get(ctx, project.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read current Project before identity mint: %w", err)
	}
	current := &aiv1alpha1.Project{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, current); err != nil {
		return nil, fmt.Errorf("decode current Project before identity mint: %w", err)
	}
	if current.UID == "" || current.UID != project.UID {
		return nil, errors.New("project UID changed before identity mint")
	}
	if !current.DeletionTimestamp.IsZero() {
		return nil, errors.New("project is deleting; refusing to mint its identity")
	}
	return current, nil
}

// projectIdentityClient creates a tenant-cluster client authenticated only by
// the Project-owned scoped token. The provider kubeconfig contributes TLS
// trust and transport settings, while every provider/App Studio credential or
// impersonation setting is removed before installing the Project token.
func (s *Server) projectIdentityClient(ctx context.Context, id identity, p *aiv1alpha1.Project) (dynamic.Interface, error) {
	token, err := s.projectIdentityToken(ctx, id, p)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(s.hubBase), "/")
	if base == "" {
		return nil, errors.New("hub endpoint is not configured for a Project identity request")
	}
	config, err := s.anonymousProjectRESTConfig(base + "/clusters/" + id.clusterID)
	if err != nil {
		return nil, fmt.Errorf("configure Project identity transport: %w", err)
	}
	config.BearerToken = token
	transport, err := projectProviderActionTransport(config, s.mcpInsecureSkipTLSVerify)
	if err != nil {
		return nil, fmt.Errorf("configure Project identity TLS: %w", err)
	}
	transport, err = rest.HTTPWrappersForConfig(config, transport)
	if err != nil {
		return nil, fmt.Errorf("configure Project identity authentication: %w", err)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("project identity redirect rejected")
		},
	}
	return dynamic.NewForConfigAndClient(config, client)
}

// anonymousProjectRESTConfig uses the provider kubeconfig only for the hub
// endpoint and its TLS trust settings. The minted kubeconfig used by some
// deployments opts out of verification, so this path clears that implicit
// bypass. The transport applies only the explicit hub development TLS option.
func (s *Server) anonymousProjectRESTConfig(target string) (*rest.Config, error) {
	if s == nil || s.callers == nil {
		return nil, errors.New("app studio provider transport is unavailable for a Project identity request")
	}
	factory, ok := s.callers.(providerRESTConfigFactory)
	if !ok {
		return nil, errors.New("app studio provider transport cannot supply trusted REST settings for a Project identity")
	}
	config, err := factory.ProviderRESTConfig(target)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errors.New("app studio provider transport returned an empty REST config")
	}
	config = rest.AnonymousClientConfig(config)
	config.Insecure = false
	return config, nil
}

// readProjectProviderReference performs a named GET through the hub's tenant
// cluster path as the Project workload identity. It never lists a foreign
// provider's resource type.
func (s *Server) readProjectProviderReference(ctx context.Context, id identity, p *aiv1alpha1.Project, ref *aiv1alpha1.ProjectProviderResourceReference) (*unstructured.Unstructured, error) {
	if s != nil && s.projectProviderReferenceReader != nil {
		return s.projectProviderReferenceReader(ctx, id, p, ref)
	}
	if ref == nil || strings.TrimSpace(ref.Name) == "" {
		return nil, errors.New("provider resource reference needs a name")
	}
	gvr, err := schema.ParseGroupVersion(strings.TrimSpace(ref.APIVersion))
	if err != nil || gvr.Group == "" || gvr.Version == "" || strings.TrimSpace(ref.Resource) == "" {
		if err == nil {
			err = errors.New("provider resource reference needs a group, version, and resource")
		}
		return nil, err
	}
	client, err := s.projectIdentityClient(ctx, id, p)
	if err != nil {
		return nil, err
	}
	return client.Resource(gvr.WithResource(strings.TrimSpace(ref.Resource))).Get(ctx, strings.TrimSpace(ref.Name), metav1.GetOptions{})
}
