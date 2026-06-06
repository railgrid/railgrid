/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package template reconciles infrastructure.kedge.faros.sh/v1alpha1
// Template CRs. Each Template represents one catalog entry; the
// controller's job is to (a) materialize the per-template CRD
// declared in spec.instanceCRD into the provider workspace's
// apiserver and (b) hand the Template to the named backend for
// backend-specific setup (kro: author an RGD; stub: no-op). Status
// conditions tell operators which step is currently failing.
//
// Out of scope for PR A: pushing the per-template CRD into
// APIExport.spec.schemas + minting an APIResourceSchema. Those land
// in PR B alongside the CachedResource provisioner — they share the
// kcp-specific surface and bench-time together.
package template

import (
	"context"
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/faroshq/faros-kedge/providers/infrastructure/apis/v1alpha1"
	"github.com/faroshq/faros-kedge/providers/infrastructure/backend"
)

// crdGVR is what the Reconciler's dynamic client targets when
// applying per-template CRDs. CRDs are always apiextensions/v1.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// Reconciler reconciles Template objects.
type Reconciler struct {
	// Client reads Templates and writes their status. Comes from the
	// controller-runtime manager.
	Client client.Client

	// Dynamic is the type-erased client for apiextensions CRDs. We
	// don't import the apiextensions clientset here because it pulls
	// a chunk of scheme machinery for two operations.
	Dynamic dynamic.Interface

	// Backends is the registry main.go populated at startup. The
	// reconciler dispatches SetupTemplate / TeardownTemplate through
	// it; an unknown backend name surfaces as a status condition,
	// not a crash.
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into a controller-runtime
// Manager. Watches Template CRs in the workspace the manager is
// configured against (the provider's own workspace at startup).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := infrav1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("template controller: adding scheme: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("template").
		For(&infrav1alpha1.Template{}, builder.WithPredicates()).
		Complete(r)
}

// Reconcile drives the Template through the four phases its status
// tracks: registration (CRDEstablished), backend setup (BackendReady),
// schema publication (SchemaInAPIExport — placeholder True for PR A
// since the APIExport syncer isn't here yet), and the aggregate
// Ready condition.
//
// Returns Result{Requeue:true} for cases where the apiserver is
// still catching up (CRD applied but not yet Established); errors
// bubble to controller-runtime's default exponential backoff.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("template", req.Name)

	var tmpl infrav1alpha1.Template
	if err := r.Client.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get template: %w", err)
	}

	// Snapshot for the status patch base.
	patchBase := tmpl.DeepCopy()

	if !tmpl.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &tmpl, patchBase)
	}

	if controllerutil.AddFinalizer(&tmpl, infrav1alpha1.FinalizerTemplateReconcile) {
		if err := r.Client.Update(ctx, &tmpl); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		// Update returns a fresh ResourceVersion; let the next event
		// loop drive us forward rather than racing the update here.
		return ctrl.Result{Requeue: true}, nil
	}

	// Look up backend FIRST so a typo on spec.backend never causes a
	// CRD to be created without a corresponding handler.
	b, ok := r.Backends.Get(tmpl.Spec.Backend)
	if !ok {
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendNotFound,
			fmt.Sprintf("backend %q is not registered on this process; registered=%v",
				tmpl.Spec.Backend, r.Backends.Names()))
		return r.writeStatus(ctx, &tmpl, patchBase)
	}

	// Step 1: per-template CRD. Build from the Template's instanceCRD
	// + schema and apply with the dynamic client.
	if err := r.ensurePerTemplateCRD(ctx, &tmpl); err != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionCRDEstablished, metav1.ConditionFalse,
			infrav1alpha1.ReasonCRDError, err.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonCRDError, err.Error())
		_, _ = r.writeStatus(ctx, &tmpl, patchBase)
		return ctrl.Result{}, err
	}
	tmpl.Status.Registered.CRDEstablished = true
	setCondition(&tmpl, infrav1alpha1.ConditionCRDEstablished, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")

	// Step 2: APIResourceSchema + APIExport.spec.resources sync.
	// Mints a fresh content-addressed APIResourceSchema (re-used
	// when the per-template CRD's schema hasn't changed) and patches
	// APIExport.spec.resources to point at it. Existing APIBindings
	// keep their frozen schema reference (kcp design); new bindings
	// pick this up immediately.
	crd, _ := buildPerTemplateCRD(&tmpl) // already validated above
	schemaName, err := r.ensureAPIResourceSchema(ctx, crd)
	if err != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionSchemaInAPIExport, metav1.ConditionFalse,
			infrav1alpha1.ReasonAPIExportError, err.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonAPIExportError, err.Error())
		_, _ = r.writeStatus(ctx, &tmpl, patchBase)
		return ctrl.Result{}, err
	}
	if err := r.ensureAPIExportEntry(ctx, schemaName, tmpl.Spec.InstanceCRD.Resource, tmpl.Spec.InstanceCRD.Group); err != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionSchemaInAPIExport, metav1.ConditionFalse,
			infrav1alpha1.ReasonAPIExportError, err.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonAPIExportError, err.Error())
		_, _ = r.writeStatus(ctx, &tmpl, patchBase)
		return ctrl.Result{}, err
	}
	tmpl.Status.Registered.SchemaInAPIExport = true
	setCondition(&tmpl, infrav1alpha1.ConditionSchemaInAPIExport, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")

	// Step 3: backend handoff.
	bs, berr := b.SetupTemplate(ctx, &tmpl)
	tmpl.Status.Backend = infrav1alpha1.TemplateBackendStatus{
		Name:    b.Name(),
		Ready:   bs.Ready,
		Message: bs.Message,
	}
	if berr != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionBackendReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, berr.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, berr.Error())
		_, _ = r.writeStatus(ctx, &tmpl, patchBase)
		return ctrl.Result{}, fmt.Errorf("backend %q SetupTemplate: %w", b.Name(), berr)
	}
	if !bs.Ready {
		setCondition(&tmpl, infrav1alpha1.ConditionBackendReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, bs.Message)
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, bs.Message)
		return r.writeStatus(ctx, &tmpl, patchBase)
	}
	setCondition(&tmpl, infrav1alpha1.ConditionBackendReady, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")

	// All three sub-conditions True → Ready=True.
	setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")
	tmpl.Status.ObservedGeneration = tmpl.Generation
	logger.V(1).Info("template ready", "backend", b.Name())
	return r.writeStatus(ctx, &tmpl, patchBase)
}

// finalize runs the cleanup chain in reverse: backend teardown,
// (future: APIExport schema removal), per-template CRD deletion,
// finalizer drop. Each step is idempotent so a partial-finalize
// crash recovers on the next reconcile.
func (r *Reconciler) finalize(ctx context.Context, tmpl, patchBase *infrav1alpha1.Template) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("template", tmpl.Name, "phase", "finalize")

	if controllerutil.ContainsFinalizer(tmpl, infrav1alpha1.FinalizerTemplateReconcile) {
		if b, ok := r.Backends.Get(tmpl.Spec.Backend); ok {
			if err := b.TeardownTemplate(ctx, tmpl); err != nil {
				setCondition(tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
					infrav1alpha1.ReasonBackendError, "teardown: "+err.Error())
				_, _ = r.writeStatus(ctx, tmpl, patchBase)
				return ctrl.Result{}, err
			}
		}
		// Reverse of the create chain: APIExport.spec.resources first,
		// then the per-template CRD. The frozen APIResourceSchema is
		// left in place because kcp uses it for any APIBinding still
		// referencing it — see the package doc on apiexport.go.
		if err := r.removeAPIExportEntry(ctx, tmpl.Spec.InstanceCRD.Resource, tmpl.Spec.InstanceCRD.Group); err != nil {
			setCondition(tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
				infrav1alpha1.ReasonAPIExportError, "remove apiexport entry: "+err.Error())
			_, _ = r.writeStatus(ctx, tmpl, patchBase)
			return ctrl.Result{}, err
		}
		if err := r.deletePerTemplateCRD(ctx, tmpl); err != nil {
			setCondition(tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
				infrav1alpha1.ReasonCRDError, "delete crd: "+err.Error())
			_, _ = r.writeStatus(ctx, tmpl, patchBase)
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(tmpl, infrav1alpha1.FinalizerTemplateReconcile)
		if err := r.Client.Update(ctx, tmpl); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
		}
	}
	logger.V(1).Info("template finalized")
	return ctrl.Result{}, nil
}

// ensurePerTemplateCRD applies the CRD declared by tmpl.spec.instanceCRD.
// Existing CRDs are patched; new ones created. The CRD's
// openAPIV3Schema is composed from tmpl.spec.schema (the JSON schema
// for spec) plus a fixed status sub-schema the platform always
// provides.
func (r *Reconciler) ensurePerTemplateCRD(ctx context.Context, tmpl *infrav1alpha1.Template) error {
	crd, err := buildPerTemplateCRD(tmpl)
	if err != nil {
		return fmt.Errorf("build CRD: %w", err)
	}

	obj, err := crdToUnstructured(crd)
	if err != nil {
		return fmt.Errorf("convert to unstructured: %w", err)
	}

	existing, err := r.Dynamic.Resource(crdGVR).Get(ctx, crd.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing: %w", err)
	}
	if apierrors.IsNotFound(err) {
		_, err = r.Dynamic.Resource(crdGVR).Create(ctx, obj, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create: %w", err)
		}
		return nil
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = r.Dynamic.Resource(crdGVR).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}

// deletePerTemplateCRD removes the CRD declared by
// tmpl.spec.instanceCRD. 404 is treated as success — the CRD may have
// been garbage-collected already, or the Template might never have
// reached the create step.
func (r *Reconciler) deletePerTemplateCRD(ctx context.Context, tmpl *infrav1alpha1.Template) error {
	name := perTemplateCRDName(tmpl)
	err := r.Dynamic.Resource(crdGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// writeStatus persists status conditions + the registration
// sub-struct. Uses a JSON-merge patch so concurrent Template spec
// updates don't race the status write.
func (r *Reconciler) writeStatus(ctx context.Context, tmpl, patchBase *infrav1alpha1.Template) (ctrl.Result, error) {
	patch := client.MergeFrom(patchBase)
	if err := r.Client.Status().Patch(ctx, tmpl, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}
	return ctrl.Result{}, nil
}

// perTemplateCRDName composes the apiserver CRD name from
// instanceCRD.resource + instanceCRD.group. CRDs are named
// "<resource>.<group>".
func perTemplateCRDName(tmpl *infrav1alpha1.Template) string {
	return tmpl.Spec.InstanceCRD.Resource + "." + tmpl.Spec.InstanceCRD.Group
}

// buildPerTemplateCRD composes the apiextensions/v1 CRD for the
// per-template kind. The CRD is cluster-scoped (instances are
// authored cluster-scoped in the tenant workspace per the design
// doc), single served version pinned to instanceCRD.version, and
// gets a fixed Status sub-schema in addition to the user-provided
// Spec schema.
func buildPerTemplateCRD(tmpl *infrav1alpha1.Template) (*apiextensionsv1.CustomResourceDefinition, error) {
	if tmpl.Spec.Schema == nil || len(tmpl.Spec.Schema.Raw) == 0 {
		return nil, fmt.Errorf("template.spec.schema is required")
	}

	var spec apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &spec); err != nil {
		return nil, fmt.Errorf("decode spec.schema as JSONSchemaProps: %w", err)
	}

	openAPI := apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"apiVersion": {Type: "string"},
			"kind":       {Type: "string"},
			"metadata":   {Type: "object"},
			"spec":       spec,
			"status":     templateInstanceStatusSchema(),
		},
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.k8s.io/v1",
			Kind:       "CustomResourceDefinition",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: perTemplateCRDName(tmpl),
			Labels: map[string]string{
				"infrastructure.kedge.faros.sh/template":         tmpl.Name,
				"infrastructure.kedge.faros.sh/template-version": tmpl.Spec.Version,
				"infrastructure.kedge.faros.sh/backend":          tmpl.Spec.Backend,
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: tmpl.Spec.InstanceCRD.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     tmpl.Spec.InstanceCRD.Kind,
				ListKind: tmpl.Spec.InstanceCRD.Kind + "List",
				Plural:   tmpl.Spec.InstanceCRD.Resource,
				Singular: singularOf(tmpl.Spec.InstanceCRD.Resource),
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    tmpl.Spec.InstanceCRD.Version,
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &openAPI,
					},
					Subresources: &apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
			},
		},
	}
	return crd, nil
}

// templateInstanceStatusSchema is the fixed status shape every
// per-template CRD gets. Lets backends report a consistent
// {phase, message, conditions} set regardless of backend.
func templateInstanceStatusSchema() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"phase":   {Type: "string"},
			"message": {Type: "string"},
			"conditions": {
				Type: "array",
				Items: &apiextensionsv1.JSONSchemaPropsOrArray{
					Schema: &apiextensionsv1.JSONSchemaProps{
						Type:     "object",
						Required: []string{"type", "status"},
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"type":               {Type: "string"},
							"status":             {Type: "string"},
							"observedGeneration": {Type: "integer"},
							"lastTransitionTime": {Type: "string"},
							"reason":             {Type: "string"},
							"message":            {Type: "string"},
						},
					},
				},
			},
		},
	}
}

// singularOf builds the singular form from a plural resource name.
// Strips a trailing "s" / "es" / "ies"; fallback returns the input.
// CRDs work without a singular but defaulting one keeps kubectl
// short-name output stable.
func singularOf(plural string) string {
	switch {
	case len(plural) >= 4 && plural[len(plural)-3:] == "ies":
		return plural[:len(plural)-3] + "y"
	case len(plural) >= 3 && plural[len(plural)-2:] == "es":
		return plural[:len(plural)-2]
	case len(plural) >= 2 && plural[len(plural)-1] == 's':
		return plural[:len(plural)-1]
	}
	return plural
}

// crdToUnstructured round-trips through JSON for the dynamic client.
// Same approach the install/crds.go installer uses; copied here so
// the controller doesn't take a runtime dep on install/.
func crdToUnstructured(crd *apiextensionsv1.CustomResourceDefinition) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(crd)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion("apiextensions.k8s.io/v1")
	out.SetKind("CustomResourceDefinition")
	return out, nil
}

// setCondition is a small wrapper that sets a Condition with
// LastTransitionTime defaulted and ObservedGeneration tracked. Keeps
// the Reconcile body readable without a helper from k8s.io/utils.
func setCondition(tmpl *infrav1alpha1.Template, condType string, status metav1.ConditionStatus, reason, message string) {
	conds := tmpl.Status.Conditions
	now := metav1.Now()
	for i := range conds {
		if conds[i].Type == condType {
			if conds[i].Status != status || conds[i].Reason != reason || conds[i].Message != message {
				conds[i].Status = status
				conds[i].Reason = reason
				conds[i].Message = message
				conds[i].LastTransitionTime = now
				conds[i].ObservedGeneration = tmpl.Generation
			}
			tmpl.Status.Conditions = conds
			return
		}
	}
	tmpl.Status.Conditions = append(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: tmpl.Generation,
	})
}
