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
	edgesGroup    = "edges.kedge.faros.sh"
	labelWorkload = edgesGroup + "/workload"

	// targetNamespace is where the agent materializes workloads on the edge
	// cluster. Mirrors the agent's constant.
	targetNamespace = "default"
)

// Render produces the objects for a Workload. Exactly one of the simple,
// template or helm modes drives it. The returned objects carry no
// placement-specific labels; the agent stamps those at apply time.
func Render(ctx context.Context, vw *edgesv1alpha1.Workload) ([]*unstructured.Unstructured, error) {
	switch {
	case vw.Spec.Helm != nil:
		return renderHelm(ctx, vw)
	case vw.Spec.Simple != nil || vw.Spec.Template != nil:
		return renderNative(vw)
	default:
		return nil, fmt.Errorf("workload %q has no simple, template or helm spec", vw.Name)
	}
}

// renderNative builds a Deployment (and, when the simple spec declares ports, a
// ClusterIP Service so the workload is dialable by an edges Service targetRef).
func renderNative(vw *edgesv1alpha1.Workload) ([]*unstructured.Unstructured, error) {
	var (
		podSpec corev1.PodSpec
		ports   []corev1.ContainerPort
	)
	switch {
	case vw.Spec.Template != nil:
		podSpec = vw.Spec.Template.Spec
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
	podLabels := map[string]string{labelWorkload: vw.Name}

	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: vw.Name, Namespace: targetNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: podLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec:       podSpec,
			},
		},
	}
	objs := []runtime.Object{dep}

	if svc := serviceForPorts(vw.Name, podLabels, ports); svc != nil {
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

// serviceForPorts builds a ClusterIP Service exposing the container ports, or
// nil when there are none. The Service name equals the workload name so an
// edges Service can target it deterministically at "<name>.<ns>.svc".
func serviceForPorts(name string, selector map[string]string, ports []corev1.ContainerPort) *corev1.Service {
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
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: targetNamespace},
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
	return corev1.PodSpec{Containers: []corev1.Container{container}}
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
