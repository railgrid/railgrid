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

// Package render turns a Workload into the concrete Kubernetes objects an edge
// should run. It renders provider-side (including Helm charts) so the edge
// agent only ever applies a manifest bundle — it needs no chart-registry
// egress and stays a thin, generic applier. See docs/edges-marketplace.md.
package render

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

func intOrString(port int32) intstr.IntOrString { return intstr.FromInt32(port) }

const (
	edgesGroup    = "edges.faros.sh"
	labelWorkload = edgesGroup + "/workload"
)

// TargetNamespace is the edge-cluster namespace a Workload renders into:
// spec.targetNamespace, or "default" when unset. The agent applies bundle
// objects into the namespace they carry (and falls back to "default" for
// objects without one), so every rendered namespaced object names it
// explicitly.
func TargetNamespace(vw *edgesv1alpha1.Workload) string {
	if ns := vw.Spec.TargetNamespace; ns != "" {
		return ns
	}
	return edgesv1alpha1.DefaultTargetNamespace
}

// Render produces the objects for a Workload. Exactly one of the simple,
// template or helm modes drives it. The returned objects carry no
// placement-specific labels; the agent stamps those at apply time.
//
// When the target namespace is not "default", the bundle starts with the
// Namespace object itself so the agent's server-side apply creates it when it
// is missing. The agent never prunes Namespaces, so a pre-existing one (with
// whatever else lives in it) survives the Workload's deletion.
func Render(ctx context.Context, vw *edgesv1alpha1.Workload) ([]*unstructured.Unstructured, error) {
	ns := TargetNamespace(vw)

	var (
		objs []*unstructured.Unstructured
		err  error
	)
	switch {
	case vw.Spec.Helm != nil:
		objs, err = renderHelm(ctx, vw, ns)
	case vw.Spec.Simple != nil || vw.Spec.Template != nil:
		objs, err = renderNative(vw, ns)
	default:
		return nil, fmt.Errorf("workload %q has no simple, template or helm spec", vw.Name)
	}
	if err != nil {
		return nil, err
	}

	if ns != edgesv1alpha1.DefaultTargetNamespace {
		objs = append([]*unstructured.Unstructured{namespaceObject(ns)}, objs...)
	}
	return objs, nil
}

// namespaceObject is the minimal Namespace manifest the agent applies ahead of
// the workload's own objects. Built by hand (not via the typed struct) so the
// bundle carries no empty spec/status stubs.
func namespaceObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}}
}

// renderNative builds a Deployment (and, when the simple spec declares ports, a
// ClusterIP Service so the workload is dialable by an edges Service targetRef).
func renderNative(vw *edgesv1alpha1.Workload, ns string) ([]*unstructured.Unstructured, error) {
	var (
		podSpec corev1.PodSpec
		ports   []corev1.ContainerPort
		tplMeta *edgesv1alpha1.WorkloadPodTemplateMeta
	)
	switch {
	case vw.Spec.Template != nil:
		podSpec = vw.Spec.Template.Spec
		tplMeta = vw.Spec.Template.Metadata
		if len(podSpec.Containers) > 0 {
			ports = podSpec.Containers[0].Ports
		}
	case vw.Spec.Simple != nil:
		podSpec = podSpecFromSimple(vw.Spec.Simple)
		ports = vw.Spec.Simple.Ports
	}

	replicas := int32(1)
	if vw.Spec.Replicas != nil {
		replicas = *vw.Spec.Replicas
	}

	// The selector is the provider's label alone: user labels ride along on the
	// pods but must never widen or shift what the Deployment selects.
	selector := map[string]string{labelWorkload: vw.Name}
	podLabels, podAnnotations := podTemplateMeta(vw.Name, tplMeta)

	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: vw.Name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: podAnnotations},
				Spec:       podSpec,
			},
		},
	}
	objs := []runtime.Object{dep}

	if svc := serviceForPorts(vw.Name, ns, selector, ports); svc != nil {
		objs = append(objs, svc)
	}

	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		u, err := toUnstructured(o)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// podTemplateMeta merges the user's template labels/annotations with the
// provider's workload label, which always wins so the Deployment selector
// keeps matching its pods. Annotations are nil when the user set none, so the
// rendered Deployment is byte-identical to the pre-metadata form.
func podTemplateMeta(workload string, meta *edgesv1alpha1.WorkloadPodTemplateMeta) (labels, annotations map[string]string) {
	labels = map[string]string{}
	if meta != nil {
		for k, v := range meta.Labels {
			labels[k] = v
		}
		if len(meta.Annotations) > 0 {
			annotations = make(map[string]string, len(meta.Annotations))
			for k, v := range meta.Annotations {
				annotations[k] = v
			}
		}
	}
	labels[labelWorkload] = workload
	return labels, annotations
}

// serviceForPorts builds a ClusterIP Service exposing the container ports, or
// nil when there are none. The Service name equals the workload name so an
// edges Service can target it deterministically at "<name>.<ns>.svc".
func serviceForPorts(name, ns string, selector map[string]string, ports []corev1.ContainerPort) *corev1.Service {
	var svcPorts []corev1.ServicePort
	for i, p := range ports {
		if p.ContainerPort == 0 {
			continue
		}
		pn := p.Name
		if pn == "" {
			pn = fmt.Sprintf("port-%d", i)
		}
		proto := p.Protocol
		if proto == "" {
			proto = corev1.ProtocolTCP
		}
		svcPorts = append(svcPorts, corev1.ServicePort{
			Name:       pn,
			Port:       p.ContainerPort,
			TargetPort: intOrString(p.ContainerPort),
			Protocol:   proto,
		})
	}
	if len(svcPorts) == 0 {
		return nil
	}
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    svcPorts,
			Type:     corev1.ServiceTypeClusterIP,
		},
	}
}

func podSpecFromSimple(simple *edgesv1alpha1.SimpleWorkloadSpec) corev1.PodSpec {
	container := corev1.Container{
		Name:  "main",
		Image: simple.Image,
		Ports: simple.Ports,
		Env:   simple.Env,
	}
	if simple.Resources != nil {
		container.Resources = *simple.Resources
	}
	if len(simple.Command) > 0 {
		container.Command = simple.Command
	}
	if len(simple.Args) > 0 {
		container.Args = simple.Args
	}
	spec := corev1.PodSpec{Containers: []corev1.Container{container}}
	if len(simple.ImagePullSecrets) > 0 {
		// Only the reference travels: the docker-registry Secret must already
		// exist in the target namespace on each edge.
		spec.ImagePullSecrets = simple.ImagePullSecrets
	}
	return spec
}

// ToRawExtensions marshals rendered objects into the RawExtension form the
// Placement stores (one JSON document per object).
func ToRawExtensions(objs []*unstructured.Unstructured) ([]runtime.RawExtension, error) {
	out := make([]runtime.RawExtension, 0, len(objs))
	for _, o := range objs {
		raw, err := o.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshaling %s %q: %w", o.GetKind(), o.GetName(), err)
		}
		out = append(out, runtime.RawExtension{Raw: raw})
	}
	return out, nil
}

func toUnstructured(o runtime.Object) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, fmt.Errorf("to unstructured: %w", err)
	}
	return &unstructured.Unstructured{Object: m}, nil
}
