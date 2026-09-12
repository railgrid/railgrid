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

package render

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

func simpleWorkload(name string) *edgesv1alpha1.Workload {
	return &edgesv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "hub-ns"},
		Spec: edgesv1alpha1.WorkloadSpec{
			Simple: &edgesv1alpha1.SimpleWorkloadSpec{
				Image: "traefik/whoami:v1.10",
				Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}},
			},
		},
	}
}

func kindNames(objs []*unstructured.Unstructured) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetKind()+"/"+o.GetNamespace()+"/"+o.GetName())
	}
	return out
}

func assertBundle(t *testing.T, objs []*unstructured.Unstructured, want ...string) {
	t.Helper()
	got := kindNames(objs)
	if len(got) != len(want) {
		t.Fatalf("bundle = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bundle[%d] = %q, want %q (bundle %v)", i, got[i], want[i], got)
		}
	}
}

// TestRenderSimpleDefaultsToDefaultNamespace pins the compatibility contract:
// no targetNamespace → Deployment + Service in "default", no Namespace object,
// and the hub namespace is NOT carried over.
func TestRenderSimpleDefaultsToDefaultNamespace(t *testing.T) {
	objs, err := Render(context.Background(), simpleWorkload("whoami"))
	if err != nil {
		t.Fatal(err)
	}
	assertBundle(t, objs, "Deployment/default/whoami", "Service/default/whoami")

	secrets, found, _ := unstructured.NestedSlice(objs[0].Object, "spec", "template", "spec", "imagePullSecrets")
	if found || len(secrets) != 0 {
		t.Fatalf("simple mode without imagePullSecrets rendered %v", secrets)
	}
	// No annotations key at all when the user set none.
	if _, found, _ := unstructured.NestedMap(objs[0].Object, "spec", "template", "metadata", "annotations"); found {
		t.Fatal("pod template gained an annotations map without any being set")
	}
}

// TestRenderSimpleTargetNamespace: targetNamespace drives every object, the
// Namespace itself leads the bundle so the agent creates it first, and
// imagePullSecrets reach the pod spec by reference only.
func TestRenderSimpleTargetNamespace(t *testing.T) {
	vw := simpleWorkload("whoami")
	vw.Spec.TargetNamespace = "kiosk"
	vw.Spec.Simple.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "ghcr-pull"}}

	objs, err := Render(context.Background(), vw)
	if err != nil {
		t.Fatal(err)
	}
	assertBundle(t, objs, "Namespace//kiosk", "Deployment/kiosk/whoami", "Service/kiosk/whoami")

	// The Namespace manifest is minimal: apiVersion/kind/metadata.name only.
	if len(objs[0].Object) != 3 {
		t.Fatalf("namespace object carries extra fields: %v", objs[0].Object)
	}

	secrets, _, _ := unstructured.NestedSlice(objs[1].Object, "spec", "template", "spec", "imagePullSecrets")
	if len(secrets) != 1 {
		t.Fatalf("imagePullSecrets = %v, want one entry", secrets)
	}
	if name := secrets[0].(map[string]any)["name"]; name != "ghcr-pull" {
		t.Fatalf("imagePullSecrets[0].name = %v, want ghcr-pull", name)
	}

	// The Service selector still pins the provider label, in the new namespace.
	sel, _, _ := unstructured.NestedStringMap(objs[2].Object, "spec", "selector")
	if sel[labelWorkload] != "whoami" || len(sel) != 1 {
		t.Fatalf("service selector = %v", sel)
	}
}

// TestRenderTemplateMetadata: template.metadata labels/annotations land on the
// pod template; the workload label always wins and the selector never widens.
func TestRenderTemplateMetadata(t *testing.T) {
	vw := &edgesv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "notes", Namespace: "hub-ns"},
		Spec: edgesv1alpha1.WorkloadSpec{
			TargetNamespace: "kiosk",
			Template: &edgesv1alpha1.WorkloadPodTemplate{
				Metadata: &edgesv1alpha1.WorkloadPodTemplateMeta{
					Labels:      map[string]string{"app": "notes", labelWorkload: "spoofed"},
					Annotations: map[string]string{"prometheus.io/scrape": "true"},
				},
				Spec: corev1.PodSpec{
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "ghcr-pull"}},
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "ghcr.io/example/notes:latest",
						Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
					}},
				},
			},
		},
	}

	objs, err := Render(context.Background(), vw)
	if err != nil {
		t.Fatal(err)
	}
	assertBundle(t, objs, "Namespace//kiosk", "Deployment/kiosk/notes", "Service/kiosk/notes")

	dep := objs[1].Object
	labels, _, _ := unstructured.NestedStringMap(dep, "spec", "template", "metadata", "labels")
	if labels["app"] != "notes" {
		t.Fatalf("user label missing from pod template: %v", labels)
	}
	if labels[labelWorkload] != "notes" {
		t.Fatalf("workload label was overridden by the user: %v", labels)
	}
	ann, _, _ := unstructured.NestedStringMap(dep, "spec", "template", "metadata", "annotations")
	if ann["prometheus.io/scrape"] != "true" {
		t.Fatalf("annotations = %v", ann)
	}
	sel, _, _ := unstructured.NestedStringMap(dep, "spec", "selector", "matchLabels")
	if len(sel) != 1 || sel[labelWorkload] != "notes" {
		t.Fatalf("selector widened to %v", sel)
	}
	secrets, _, _ := unstructured.NestedSlice(dep, "spec", "template", "spec", "imagePullSecrets")
	if len(secrets) != 1 {
		t.Fatalf("template imagePullSecrets = %v", secrets)
	}
	// Unnamed port → generated Service port name.
	ports, _, _ := unstructured.NestedSlice(objs[2].Object, "spec", "ports")
	if ports[0].(map[string]any)["name"] != "port-0" {
		t.Fatalf("service ports = %v", ports)
	}
}

// TestRenderRejectsEmptyWorkload: no mode selected is an error, not an empty
// (and therefore silently pruning) bundle.
func TestRenderRejectsEmptyWorkload(t *testing.T) {
	_, err := Render(context.Background(), &edgesv1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: "x"}})
	if err == nil {
		t.Fatal("expected an error for a workload with no mode")
	}
}

func TestStampNamespace(t *testing.T) {
	u := func(kind, ns string) *unstructured.Unstructured {
		o := &unstructured.Unstructured{}
		o.SetAPIVersion("v1")
		o.SetKind(kind)
		o.SetName("x")
		if ns != "" {
			o.SetNamespace(ns)
		}
		return o
	}
	objs := []*unstructured.Unstructured{u("Deployment", ""), u("ClusterRole", ""), u("ConfigMap", "other"), u("Namespace", "")}
	stampNamespace(objs, "kiosk")
	want := []string{"kiosk", "", "other", ""}
	for i, o := range objs {
		if o.GetNamespace() != want[i] {
			t.Errorf("%s namespace = %q, want %q", o.GetKind(), o.GetNamespace(), want[i])
		}
	}
}

// TestRenderHelmTargetNamespace renders a tiny chart whose templates omit the
// namespace on one object and use {{ .Release.Namespace }} on another — the
// two shapes real charts use — and checks both land in the target namespace,
// with the Namespace object first and a cluster-scoped object untouched.
func TestRenderHelmTargetNamespace(t *testing.T) {
	templates := map[string]string{
		"templates/deploy.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Values.fullnameOverride }}
spec:
  replicas: 1
  selector:
    matchLabels: {app: demo}
  template:
    metadata:
      labels: {app: demo}
    spec:
      containers:
        - name: demo
          image: demo:1
`,
		"templates/cm.yaml": `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.fullnameOverride }}-cm
  namespace: {{ .Release.Namespace }}
data: {a: b}
`,
		"templates/crb.yaml": `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ .Values.fullnameOverride }}-crb
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: view}
subjects: []
`,
	}
	archive := chartArchiveWithFiles(t, "demo", "1.2.3", templates)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "entries:\n  demo:\n    - version: 1.2.3\n      urls:\n        - %s/demo-1.2.3.tgz\n", srv.URL)
	})
	mux.HandleFunc("/demo-1.2.3.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})

	vw := &edgesv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "hub-ns"},
		Spec: edgesv1alpha1.WorkloadSpec{
			TargetNamespace: "monitoring",
			Helm:            &edgesv1alpha1.HelmWorkloadSpec{RepoURL: srv.URL, Chart: "demo", Version: "1.2.3"},
		},
	}
	objs, err := Render(context.Background(), vw)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(objs) != 4 || objs[0].GetKind() != "Namespace" || objs[0].GetName() != "monitoring" {
		t.Fatalf("bundle = %v, want Namespace/monitoring first + 3 chart objects", kindNames(objs))
	}
	byKind := map[string]*unstructured.Unstructured{}
	for _, o := range objs[1:] {
		byKind[o.GetKind()] = o
	}
	if ns := byKind["Deployment"].GetNamespace(); ns != "monitoring" {
		t.Errorf("namespace-less Deployment stamped %q, want monitoring", ns)
	}
	if ns := byKind["ConfigMap"].GetNamespace(); ns != "monitoring" {
		t.Errorf(".Release.Namespace rendered %q, want monitoring", ns)
	}
	if ns := byKind["ClusterRoleBinding"].GetNamespace(); ns != "" {
		t.Errorf("cluster-scoped ClusterRoleBinding got namespace %q", ns)
	}
	if byKind["Deployment"].GetName() != "grafana" {
		t.Errorf("fullnameOverride not forced: %q", byKind["Deployment"].GetName())
	}
}

// TestRenderHelmDefaultNamespaceLeavesObjectsUnstamped keeps the pre-existing
// contract for the default namespace: chart objects without a namespace stay
// namespace-less (the agent defaults them), so existing bundles do not churn.
func TestRenderHelmDefaultNamespaceLeavesObjectsUnstamped(t *testing.T) {
	templates := map[string]string{
		"templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\ndata: {a: b}\n",
	}
	archive := chartArchiveWithFiles(t, "demo", "1.2.3", templates)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/demo-1.2.3.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})

	vw := &edgesv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       edgesv1alpha1.WorkloadSpec{Helm: &edgesv1alpha1.HelmWorkloadSpec{RepoURL: srv.URL, Chart: "demo", Version: "1.2.3"}},
	}
	objs, err := Render(context.Background(), vw)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(objs) != 1 || objs[0].GetKind() != "ConfigMap" || objs[0].GetNamespace() != "" {
		t.Fatalf("bundle = %v, want a single namespace-less ConfigMap", kindNames(objs))
	}
}
