// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	kubeinformers "github.com/kcp-dev/client-go/informers"
	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"
)

const PluginName = "railgrid.ai/OrganizationLimits"

func (g *Guard) Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(io.Reader) (admission.Interface, error) {
		return &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}, nil
	})
}

type sizeAdmission struct {
	*admission.Handler
	guard *Guard
}

var _ admission.ValidationInterface = &sizeAdmission{}
var _ kcpinitializers.WantsKcpInformers = &sizeAdmission{}
var _ kcpinitializers.WantsKubeInformers = &sizeAdmission{}

func (a *sizeAdmission) SetKcpInformers(local, _ kcpinformers.SharedInformerFactory) {
	a.guard.SetInformers(local)
}

func (a *sizeAdmission) SetKubeInformers(local, global kubeinformers.SharedInformerFactory) {
	a.guard.SetKubeInformers(local, global)
}

func (a *sizeAdmission) ValidateInitialization() error {
	if a.guard.lister == nil || a.guard.synced == nil || a.guard.quotaLister == nil || a.guard.quotaSynced == nil {
		return fmt.Errorf("organization limits require logical cluster and resource quota informers")
	}
	a.guard.admissionInstalled.Store(true)
	return nil
}

func (a *sizeAdmission) Validate(ctx context.Context, attributes admission.Attributes, _ admission.ObjectInterfaces) error {
	if attributes.GetOperation() == admission.Delete {
		if err := a.guard.protectConsumerControlDelete(ctx, attributes); err != nil {
			return admission.NewForbidden(attributes, err)
		}
		return nil
	}
	if err := a.guard.requireConsumerQuotaCoverage(ctx, attributes); err != nil {
		return admission.NewForbidden(attributes, err)
	}
	orgs, err := a.guard.owners(ctx)
	if err != nil {
		return admission.NewForbidden(attributes, err)
	}
	// Native scheduling creates the protected LC singleton before ownership
	// can be read from an informer. Its canonical path is already supplied by
	// the privileged scheduler; do not grant this fallback to tenant identities.
	cluster := request.ClusterFrom(ctx)
	var ownerErr error
	if cluster != nil && !cluster.Name.Empty() {
		_, ownerErr = a.guard.clusterOwner(cluster.Name.String())
	}
	if len(orgs) == 0 && ownerErr != nil && attributes.GetResource().Group == "core.kcp.io" && attributes.GetResource().Resource == "logicalclusters" && attributes.GetOperation() == admission.Create && bootstrapIdentity(attributes.GetUserInfo()) {
		metadata, err := meta.Accessor(attributes.GetObject())
		if err != nil {
			return admission.NewForbidden(attributes, err)
		}
		org, err := organization(metadata.GetAnnotations()[pathAnnotation])
		if err != nil {
			return admission.NewForbidden(attributes, err)
		}
		if org != "" {
			orgs = append(orgs, org)
		}
	}
	if len(orgs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(attributes.GetObject())
	if err != nil {
		return admission.NewForbidden(attributes, fmt.Errorf("cannot measure admitted object: %w", err))
	}
	if int64(len(encoded)) <= a.guard.config.MaxObjectBytes {
		return nil
	}
	// Permit maintenance of an existing oversized object only if it does not
	// grow. This keeps finalizer removal and staged shrink/migration possible.
	if attributes.GetOperation() == admission.Update && attributes.GetOldObject() != nil {
		old, err := json.Marshal(attributes.GetOldObject())
		if err == nil && len(encoded) <= len(old) {
			return nil
		}
	}
	return admission.NewForbidden(attributes, fmt.Errorf("organization object exceeds %d serialized bytes", a.guard.config.MaxObjectBytes))
}

func (g *Guard) protectConsumerControlDelete(ctx context.Context, attributes admission.Attributes) error {
	if !targetsProtectedConsumerControl(attributes, ctx) {
		return nil
	}
	cluster := request.ClusterFrom(ctx)
	if cluster == nil || cluster.Wildcard || cluster.Name.Empty() {
		return fmt.Errorf("protected consumer control deletion has no target workspace")
	}
	org, err := g.clusterOwner(cluster.Name.String())
	if err != nil {
		return fmt.Errorf("consumer workspace protection state is unavailable: %w", err)
	}
	if org == "" {
		return nil
	}
	lc, err := g.lister.Cluster(cluster.Name).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return fmt.Errorf("consumer workspace protection state unavailable: %w", err)
	}
	if lc.Annotations[ConsumerQuotaRequiredAnnotation] != ConsumerQuotaRequiredValue {
		return nil
	}
	if bootstrapIdentity(attributes.GetUserInfo()) {
		return nil
	}
	return fmt.Errorf("protected consumer quota namespace and quota cannot be deleted by workspace users")
}

func targetsProtectedConsumerControl(attributes admission.Attributes, ctx context.Context) bool {
	r := attributes.GetResource()
	switch {
	case r.Group == "" && r.Resource == "namespaces":
		if requestIsDeleteCollection(ctx) {
			return true
		}
		names := []string{attributes.GetName(), objectName(attributes.GetObject()), objectName(attributes.GetOldObject())}
		for _, name := range names {
			if name == ConsumerQuotaNamespace {
				return true
			}
		}
		return noKnownValue(names)
	case r.Group == "" && r.Resource == "resourcequotas":
		namespaces := []string{attributes.GetNamespace(), objectNamespace(attributes.GetObject()), objectNamespace(attributes.GetOldObject())}
		inAdminNamespace := false
		for _, namespace := range namespaces {
			inAdminNamespace = inAdminNamespace || namespace == ConsumerQuotaNamespace
		}
		if requestIsDeleteCollection(ctx) {
			// An all-namespace or unidentified collection can include the
			// protected quota; a specifically scoped other namespace cannot.
			return inAdminNamespace || noKnownValue(namespaces)
		}
		if !inAdminNamespace {
			return noKnownValue(namespaces)
		}
		names := []string{attributes.GetName(), objectName(attributes.GetObject()), objectName(attributes.GetOldObject())}
		for _, name := range names {
			if name == ConsumerQuotaName {
				return true
			}
		}
		return noKnownValue(names)
	default:
		return false
	}
}

func noKnownValue(values []string) bool {
	for _, value := range values {
		if value != "" {
			return false
		}
	}
	return true
}

func requestIsDeleteCollection(ctx context.Context) bool {
	info, ok := request.RequestInfoFrom(ctx)
	return ok && info.Verb == "deletecollection"
}

func objectName(object runtime.Object) string {
	if isNilRuntimeObject(object) {
		return ""
	}
	metadata, err := meta.Accessor(object)
	if err != nil {
		return ""
	}
	return metadata.GetName()
}

func objectNamespace(object runtime.Object) string {
	if isNilRuntimeObject(object) {
		return ""
	}
	metadata, err := meta.Accessor(object)
	if err != nil {
		return ""
	}
	return metadata.GetNamespace()
}

func isNilRuntimeObject(object runtime.Object) bool {
	if object == nil {
		return true
	}
	value := reflect.ValueOf(object)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

// requireConsumerQuotaCoverage applies to persisted writes in a consumer
// workspace whose LogicalCluster carries the trusted lifecycle marker.
func (g *Guard) requireConsumerQuotaCoverage(ctx context.Context, attributes admission.Attributes) error {
	if isConsumerControlWrite(attributes) {
		return nil
	}
	cluster := request.ClusterFrom(ctx)
	if cluster == nil || cluster.Wildcard || cluster.Name.Empty() {
		return nil
	}
	org, err := g.clusterOwner(cluster.Name.String())
	if err != nil {
		return fmt.Errorf("consumer workspace protection state is unavailable: %w", err)
	}
	// Structural root/system workspaces are not consumers. clusterOwner
	// recognizes these before consulting informers so kcp's bootstrap RBAC
	// reconciliation can proceed before informer startup completes.
	if org == "" {
		return nil
	}
	lc, err := g.lister.Cluster(cluster.Name).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return fmt.Errorf("consumer workspace protection state unavailable: %w", err)
	}
	// Lifecycle code writes this trusted marker only on consumer workspaces.
	// Provider-private source quotas have their own lifecycle and are outside
	// this check. Once set, a missing quota never disables the marker.
	if lc.Annotations[ConsumerQuotaRequiredAnnotation] != ConsumerQuotaRequiredValue {
		return nil
	}
	if strictlyShrinksExistingObject(attributes) {
		// Permit bounded cleanup/adoption while quota coverage is missing or
		// over limit. Equal-size writes and changes that grow serialized data
		// still require a complete quota proof.
		return nil
	}
	gr := attributes.GetResource().GroupResource()
	if g.quotaLister == nil || g.quotaSynced == nil || !g.quotaSynced() {
		return fmt.Errorf("protected consumer quota informer is not synchronized")
	}
	quota, err := g.quotaLister.Cluster(cluster.Name).ResourceQuotas(ConsumerQuotaNamespace).Get(ConsumerQuotaName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("protected consumer quota is missing")
		}
		return fmt.Errorf("protected consumer quota is unavailable: %w", err)
	}
	return safeStatusProof(quota, gr)
}

func strictlyShrinksExistingObject(attributes admission.Attributes) bool {
	if attributes.GetOperation() != admission.Update || attributes.GetOldObject() == nil || attributes.GetObject() == nil {
		return false
	}
	current, err := json.Marshal(attributes.GetObject())
	if err != nil {
		return false
	}
	previous, err := json.Marshal(attributes.GetOldObject())
	return err == nil && len(current) < len(previous)
}

// isConsumerControlWrite lists the small set of fixed native control APIs
// needed to bootstrap and maintain the quota contract. It never exempts an
// authenticated admin by identity or group.
func isConsumerControlWrite(attributes admission.Attributes) bool {
	r := attributes.GetResource()
	switch {
	case r.Group == "core.kcp.io" && r.Resource == "logicalclusters":
		return attributes.GetName() == corev1alpha1.LogicalClusterName
	case r.Group == "" && r.Resource == "resourcequotas":
		return attributes.GetNamespace() == ConsumerQuotaNamespace && attributes.GetName() == ConsumerQuotaName
	case r.Group == "" && r.Resource == "namespaces":
		return attributes.GetOperation() == admission.Create && attributes.GetName() == ConsumerQuotaNamespace
	case r.Group == "apis.kcp.io" && r.Resource == "apibindings":
		return true
	case r.Group == "authorization.k8s.io" && (r.Resource == "subjectaccessreviews" || r.Resource == "localsubjectaccessreviews" || r.Resource == "selfsubjectaccessreviews"):
		return true
	case r.Group == "authorization.k8s.io" && r.Resource == "selfsubjectrulesreviews":
		return true
	case r.Group == "authentication.k8s.io" && (r.Resource == "tokenreviews" || r.Resource == "selfsubjectreviews"):
		return true
	case r.Group == "" && r.Resource == "serviceaccounts" && attributes.GetSubresource() == "token":
		return true
	default:
		return false
	}
}
