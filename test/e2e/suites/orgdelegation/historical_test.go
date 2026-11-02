// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package orgdelegation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"

	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An arbitrary historical account cannot be identified by the retired prefix.
// Check the pinned native authorization boundary without deleting that account
// or pretending the remaining grant is safe within its own workspace.
func (f *fixture) verifyHistoricalChildContainment(t *testing.T, cluster string) {
	t.Helper()
	const name = "historical-custom-account"
	_, err := f.dynamic(t, cluster).Resource(clusterRoleBindings).Create(f.ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "historical-custom-grant"},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": name, "namespace": "default"}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "delegation-cluster-admin"},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	token := f.serviceAccountToken(t, cluster, name)
	own := f.request(t, http.MethodGet, f.server+"/clusters/"+cluster+"/api/v1/namespaces", token, "")
	if own.StatusCode != http.StatusOK {
		t.Fatalf("historical arbitrary account lost its own native grant unexpectedly: %d %s", own.StatusCode, own.Body)
	}
	other := f.workspace(t, "root", "historical-other-team")
	for _, target := range []string{other, "root"} {
		url := f.server + "/clusters/" + target + "/api/v1/namespaces"
		for _, impersonate := range []bool{false, true} {
			var response response
			if impersonate {
				response = f.requestAs(t, http.MethodGet, url, token, "", "system:admin", []string{"system:masters"})
			} else {
				response = f.request(t, http.MethodGet, url, token, "")
			}
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("historical account cross-cluster=%q impersonate=%v returned %d: %s", target, impersonate, response.StatusCode, response.Body)
			}
		}
	}
	// Granting a privileged group through impersonation is refused even in the
	// account's own cluster, before it could broaden that identity's scope.
	ownImpersonation := f.requestAs(t, http.MethodGet, f.server+"/clusters/"+cluster+"/api/v1/namespaces", token, "", "system:admin", []string{"system:masters"})
	if ownImpersonation.StatusCode != http.StatusForbidden {
		t.Fatalf("historical account impersonated privileged group: %d %s", ownImpersonation.StatusCode, ownImpersonation.Body)
	}
}

// Exercise the operator tool against the actual pinned server's Table and
// PartialObjectMetadata responses. The fixture is fresh and isolated.
func (f *fixture) verifyHistoricalInventory(t *testing.T) {
	t.Helper()
	parent := "root"
	for _, name := range []string{"railgrid", "tenants", "audit-org", "audit-team"} {
		f.workspace(t, parent, name)
		parent += ":" + name
	}
	config := rest.CopyConfig(f.base)
	config.Host = f.server + "/clusters/" + parent
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CoreV1().Namespaces().Create(f.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	_, err = client.CoreV1().ServiceAccounts("default").Create(f.ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "historical-audit-account", Namespace: "default"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "credential-audit-must-never-emit-this-secret"
	_, err = client.CoreV1().Secrets("default").Create(f.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "historical-audit-secret", Namespace: "default"}, Data: map[string][]byte{"token": []byte(sentinel)}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../../../hack/audit-historical-credentials.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(f.ctx, "python3", script, "--kubeconfig", f.native.AdminKubeconfigPath(), "--workspace", parent)
	var output, stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	err = command.Run()
	if strings.Contains(output.String(), sentinel) || strings.Contains(output.String(), base64.StdEncoding.EncodeToString([]byte(sentinel))) {
		t.Fatal("audit leaked Secret bytes")
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
			t.Fatalf("operator audit failed to run: %v (stderr omitted)", err)
		}
	}
	var report struct {
		Complete    bool     `json:"complete"`
		Errors      []string `json:"errors"`
		Inventories map[string]struct {
			Available  bool `json:"available"`
			Incomplete bool `json:"incomplete"`
			Items      []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"items"`
		} `json:"inventories"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("operator audit returned invalid JSON: %v", err)
	}
	for key, wantName := range map[string]string{"serviceAccounts": "historical-audit-account", "secrets": "historical-audit-secret"} {
		inventory := report.Inventories[key]
		found := false
		for _, item := range inventory.Items {
			if item.Name == wantName {
				found = true
			}
		}
		if !inventory.Available || inventory.Incomplete || !found {
			t.Fatalf("operator inventory %s unavailable, incomplete, or missing fixture; report errors: %v", key, report.Errors)
		}
	}
}
