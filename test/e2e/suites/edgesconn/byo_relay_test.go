// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package edgesconn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/railgrid/railgrid/test/e2e/framework"
)

// TestBYOCustomSubresource uses a fresh kind cluster and the real agent pod,
// restricted provider init, quickstart controller and greet Gate. Quickstart
// itself runs on the host: the Service Endpoint points at Docker's host address,
// keeping this suite's localhost kcp virtual workspace reachable by its controller.
// The tunnel still resolves and dials the hub-created .svc target inside kind.
// This does not claim to test a provider Helm pod's kcp networking configuration.
func TestBYOCustomSubresource(t *testing.T) {
	if os.Getenv("RAILGRID_E2E_BYO_RELAY") != "true" {
		t.Skip("set RAILGRID_E2E_BYO_RELAY=true; requires local agent image e2e-byo and quickstart binary")
	}
	dir := suiteTempDir(t, "byo-relay")
	kubeconfig := filepath.Join(dir, "tenant.kubeconfig")
	runCLI(t, kubeconfig, railgridBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	cluster := clusterFromKubeconfig(t, kubeconfig)
	org, ws := orgAndWorkspaceForCluster(t, cluster)
	tenant := kcpDynamic(t, cluster, adminToken)
	enableEdges(t, tenant)
	grantEdgeProxy(t, tenant)
	if !waitFor(t, 45*time.Second, func() (bool, string) {
		user, err := dynamic.NewForConfig(&rest.Config{Host: hubURL + "/clusters/" + cluster, BearerToken: staticToken, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
		if err != nil {
			return false, err.Error()
		}
		review, err := user.Resource(schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"group": "edges.railgrid.ai", "resource": "kubernetesclusters", "verb": "create"}}}}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		allowed, _, _ := unstructured.NestedBool(review.Object, "status", "allowed")
		return allowed, "waiting for bound edges group in tenant admin role"
	}) {
		t.Fatal("tenant admin role never granted bound edges access")
	}
	const edge = "byo-relay"
	runCLI(t, kubeconfig, railgridBin, "edge", "create", edge, "--type", "kubernetes")
	t.Run("team admin cannot publish control plane endpoints", func(t *testing.T) {
		user, err := dynamic.NewForConfig(&rest.Config{Host: hubURL + "/clusters/" + cluster, BearerToken: staticToken, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []struct {
			gvr  schema.GroupVersionResource
			kind string
		}{
			{schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}, "APIExport"},
			{schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}, "APIResourceSchema"},
			{schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, "CustomResourceDefinition"},
		} {
			_, err := user.Resource(target.gvr).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{"apiVersion": target.gvr.GroupVersion().String(), "kind": target.kind, "metadata": map[string]any{"name": "escape-probe"}}}, metav1.CreateOptions{})
			if !apierrors.IsForbidden(err) {
				t.Errorf("team admin %s: expected403, got %v", target.kind, err)
			}
		}
		role := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]any{"name": "escape-probe"}, "rules": []any{map[string]any{"apiGroups": []any{"apis.kcp.io"}, "resources": []any{"*"}, "verbs": []any{"*"}}}}}
		_, err = user.Resource(schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}).Create(ctxWithTimeout(t, 10*time.Second), role, metav1.CreateOptions{})
		if !apierrors.IsForbidden(err) {
			t.Errorf("team admin RBAC escalation: expected403, got %v", err)
		}
		// Seed an otherwise unbound privileged role as the suite administrator:
		// kcp does not necessarily materialize the stock cluster-admin role.
		privileged := role.DeepCopy()
		privileged.SetName("test-only-superuser")
		roles := tenant.Resource(schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"})
		if _, err := roles.Create(ctxWithTimeout(t, 10*time.Second), privileged, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = roles.Delete(context.Background(), "test-only-superuser", metav1.DeleteOptions{}) })
		binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "escape-probe"}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "test-only-superuser"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "namespace": "default", "name": "default"}}}}
		_, err = user.Resource(schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}).Create(ctxWithTimeout(t, 10*time.Second), binding, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if !apierrors.IsForbidden(err) {
			t.Errorf("team admin cluster-admin binding: expected403, got %v", err)
		}
		quota := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]any{"name": "escape-probe", "namespace": "default"}, "spec": map[string]any{"hard": map[string]any{"count/configmaps": "0"}}}}
		_, err = user.Resource(schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}).Namespace("default").Create(ctxWithTimeout(t, 10*time.Second), quota, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if !apierrors.IsForbidden(err) {
			t.Errorf("team admin quota mutation: expected403, got %v", err)
		}
	})
	t.Cleanup(func() {
		_ = tenant.Resource(kubernetesClusterGVR).Delete(context.Background(), edge, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenant, kubernetesClusterGVR, edge)
	kindName := fmt.Sprintf("railgrid-byo-relay-%d", time.Now().UnixNano())
	kindConfig := filepath.Join(dir, "kind.kubeconfig")
	// Never reuse a named cluster, and never fall back to the caller's kube context.
	byoCommand(t, "kind", "create", "cluster", "--name", kindName, "--kubeconfig", kindConfig, "--wait", "90s")
	t.Cleanup(func() { _ = exec.Command("kind", "delete", "cluster", "--name", kindName).Run() })
	byoCommand(t, "kind", "load", "docker-image", "ghcr.io/railgrid/railgrid-agent:e2e-byo", "--name", kindName)
	hostLine := byoCommand(t, "docker", "exec", kindName+"-control-plane", "getent", "ahostsv4", "host.docker.internal")
	hostIP := strings.Fields(hostLine)[0]
	if net.ParseIP(hostIP) == nil {
		t.Fatalf("invalid Docker host IP")
	}
	values, _ := yaml.Marshal(map[string]any{"image": map[string]any{"tag": "e2e-byo", "pullPolicy": "Never"}, "agent": map[string]any{"edgeName": edge, "cluster": cluster, "hub": map[string]any{"url": "https://host.docker.internal:" + hubPort, "token": joinToken, "insecureSkipTLSVerify": true}}, "hostAliases": []any{map[string]any{"ip": hostIP, "hostnames": []string{"host.docker.internal"}}}})
	valuesPath := filepath.Join(dir, "agent-values.yaml")
	if err := os.WriteFile(valuesPath, values, 0600); err != nil {
		t.Fatal(err)
	}
	byoCommand(t, "helm", "upgrade", "--install", "byo-agent", filepath.Join(repoRoot, "deploy/charts/railgrid-agent"), "--kubeconfig", kindConfig, "--namespace", "byo-relay", "--create-namespace", "--values", valuesPath, "--wait", "--timeout", "3m")
	waitForConnected(t, tenant, kubernetesClusterGVR, edge)
	t.Log("pod agent connected through local hub")
	code, raw := postOrgProvider(t, org, map[string]any{"name": "quickstart", "edge": map[string]string{"workspace": ws, "name": edge}})
	if code != http.StatusCreated {
		t.Fatalf("register authored provider: %d %s", code, raw)
	}
	t.Log("authored provider registered with restricted credential")
	t.Cleanup(func() { deleteOrgProvider(t, org, "quickstart") })
	var registration struct {
		Kubeconfig string `json:"kubeconfig"`
		Provider   struct {
			WorkspacePath string `json:"workspacePath"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &registration); err != nil {
		t.Fatal(err)
	}
	t.Run("org admin cannot mutate provider infrastructure", func(t *testing.T) {
		for _, target := range []struct {
			workspace string
			gvr       schema.GroupVersionResource
			kind      string
		}{
			{registration.Provider.WorkspacePath, schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}, "APIExport"},
			{"root:railgrid:tenants:" + org + ":providers", orgWorkspaceGVR, "Workspace"},
		} {
			user, err := dynamic.NewForConfig(&rest.Config{Host: hubURL + "/clusters/" + target.workspace, BearerToken: staticToken, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = user.Resource(target.gvr).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{"apiVersion": target.gvr.GroupVersion().String(), "kind": target.kind, "metadata": map[string]any{"name": "escape-probe"}}}, metav1.CreateOptions{})
			if !apierrors.IsForbidden(err) {
				t.Errorf("human org admin %s in %s: expected403, got %v", target.kind, target.workspace, err)
			}
		}
	})
	providerConfig := filepath.Join(dir, "provider.kubeconfig")
	if err := os.WriteFile(providerConfig, []byte(registration.Kubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := clientcmd.BuildConfigFromFlags("", providerConfig)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("credential cannot publish endpoints or privileged objects", func(t *testing.T) {
		for _, coordinate := range []schema.GroupVersionResource{{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}, {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, {Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}} {
			_, err := restricted.Resource(coordinate).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{"apiVersion": coordinate.Group + "/" + coordinate.Version, "kind": map[string]string{"apiexports": "APIExport", "clusterroles": "ClusterRole", "customresourcedefinitions": "CustomResourceDefinition"}[coordinate.Resource], "metadata": map[string]any{"name": "rogue"}}}, metav1.CreateOptions{})
			if !apierrors.IsForbidden(err) {
				t.Errorf("%s create: expected forbidden, got %v", coordinate.Resource, err)
			}
		}
	})
	const providerPort = "18099"
	manifestRaw, err := os.ReadFile(filepath.Join(repoRoot, "providers/quickstart/manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := yaml.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	endpoint := "http://quickstart.byo-relay.svc.cluster.local:" + providerPort
	if err := unstructured.SetNestedField(manifest, endpoint, "spec", "serving", "backend", "url"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(manifest, endpoint, "spec", "serving", "ui", "url"); err != nil {
		t.Fatal(err)
	}
	rendered, _ := yaml.Marshal(manifest)
	manifestPath := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(manifestPath, rendered, 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "RAILGRID_PROVIDER_KUBECONFIG="+providerConfig, "RAILGRID_CATALOGENTRY_FILE="+manifestPath, "RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers/quickstart/deploy/chart/files"), "RAILGRID_HUB_URL="+hubURL, "RAILGRID_HUB_INSECURE=true", "RAILGRID_PROVIDER_NAME=quickstart", "PORT="+providerPort)
	binary := filepath.Join(repoRoot, "bin/quickstart-provider")
	bootstrap := func() {
		init := exec.CommandContext(ctxWithTimeout(t, 90*time.Second), binary, "init")
		init.Env = env
		if output, err := init.CombinedOutput(); err != nil {
			t.Fatalf("restricted init: %v: %s", err, output)
		}
	}
	bootstrap()
	t.Log("restricted provider bootstrap completed")
	log, err := os.Create(filepath.Join(dir, "quickstart.log"))
	if err != nil {
		t.Fatal(err)
	}
	serve := exec.Command(binary, "serve")
	serve.Env = env
	serve.Stdout = log
	serve.Stderr = log
	serve.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killGroup(serve); _ = log.Close() })
	service := fmt.Sprintf("apiVersion: v1\nkind: Service\nmetadata:\n  name: quickstart\n  namespace: byo-relay\nspec:\n  ports:\n  - port: %s\n    targetPort: %s\n---\napiVersion: v1\nkind: Endpoints\nmetadata:\n  name: quickstart\n  namespace: byo-relay\nsubsets:\n- addresses:\n  - ip: %s\n  ports:\n  - port: %s\n", providerPort, providerPort, hostIP, providerPort)
	servicePath := filepath.Join(dir, "service.yaml")
	if err := os.WriteFile(servicePath, []byte(service), 0600); err != nil {
		t.Fatal(err)
	}
	byoCommand(t, "kubectl", "--kubeconfig", kindConfig, "apply", "-f", servicePath)
	if !waitFor(t, 2*time.Minute, func() (bool, string) {
		digest, err := byoConsentDigest(t, org, ws, "quickstart")
		if err != nil {
			return false, err.Error()
		}
		request := map[string]any{"acceptedClaims": []any{map[string]any{"group": "authorization.k8s.io", "resource": "subjectaccessreviews"}}}
		if digest != "" {
			request["consentDigest"] = digest
		}
		payload, _ := json.Marshal(request)
		code, body := hubJSON(t, http.MethodPost, fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/quickstart/enable", org, ws), org, payload, ws)
		return code == 200 || code == 201, fmt.Sprintf("enable %d: %s", code, body)
	}) {
		t.Fatal("provider never enabled")
	}
	t.Log("provider enabled with current consent digest")
	greetings := schema.GroupVersionResource{Group: "quickstart.providers.railgrid.ai", Version: "v1alpha1", Resource: "greetings"}
	if !waitFor(t, 90*time.Second, func() (bool, string) {
		_, err := tenant.Resource(greetings).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{"apiVersion": greetings.GroupVersion().String(), "kind": "Greeting", "metadata": map[string]any{"name": "relay"}, "spec": map[string]any{"message": "hello relay"}}}, metav1.CreateOptions{})
		return err == nil || apierrors.IsAlreadyExists(err), fmt.Sprint(err)
	}) {
		t.Fatal("Greeting unavailable")
	}
	t.Log("Greeting created in consumer workspace")
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	review, err := caller.Resource(schema.GroupVersionResource{Group: "authentication.k8s.io", Version: "v1", Resource: "selfsubjectreviews"}).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	username, _, _ := unstructured.NestedString(review.Object, "status", "userInfo", "username")
	if username == "" {
		t.Fatal("SelfSubjectReview omitted caller username")
	}
	path := "/clusters/" + cluster + "/apis/" + greetings.Group + "/v1alpha1/greetings/relay/greet"
	if !waitFor(t, 2*time.Minute, func() (bool, string) {
		status, body := byoVerb(t, path, staticToken)
		var response struct {
			Result struct {
				Greeting string `json:"greeting"`
			} `json:"result"`
			Error any `json:"error"`
		}
		err := json.Unmarshal(body, &response)
		return status == 200 && err == nil && response.Error == nil && response.Result.Greeting == "hello relay, "+username, fmt.Sprintf("greet %d: %s", status, body)
	}) {
		t.Fatal("custom subresource did not return original caller identity")
	}
	t.Log("custom subresource returned the authenticated original caller")
	// Check idempotency only after proving the first bootstrap routes a verb.
	bootstrap()
	t.Log("second restricted init completed after first successful verb")
	t.Run("foreign organization cannot bind or invoke", func(t *testing.T) {
		foreign, err := framework.CreateOrgViaREST(ctxWithTimeout(t, 90*time.Second), hubURL, staticToken, "BYO relay foreign")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = framework.DeleteOrgViaREST(context.Background(), hubURL, staticToken, foreign.UUID) })
		fw, err := framework.CreateWorkspaceViaREST(ctxWithTimeout(t, 90*time.Second), hubURL, staticToken, foreign.UUID, "foreign")
		if err != nil {
			t.Fatal(err)
		}
		foreignPath := "root:railgrid:tenants:" + foreign.UUID + ":" + fw.UUID
		foreignClient := kcpDynamic(t, foreignPath, adminToken)
		binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "foreign-byo"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": registration.Provider.WorkspacePath, "name": "quickstart.providers.railgrid.ai"}}}}}
		// Even the foreign workspace's ordinary org-admin caller has no bind grant.
		userConfig := *cfg
		userConfig.Host = hubURL + "/clusters/" + foreignPath
		foreignUser, err := dynamic.NewForConfig(&userConfig)
		if err != nil {
			t.Fatal(err)
		}
		_, err = foreignUser.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 20*time.Second), binding, metav1.CreateOptions{})
		if !apierrors.IsForbidden(err) {
			t.Fatalf("foreign org admin bind: expected Forbidden, got %v", err)
		}
		lc, err := foreignClient.Resource(logicalClusterGVR).Get(ctxWithTimeout(t, 10*time.Second), "cluster", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		foreignCluster := lc.GetAnnotations()["kcp.io/cluster"]
		if foreignCluster == "" {
			foreignCluster = lc.GetLabels()["kcp.io/cluster"]
		}
		if foreignCluster == "" {
			t.Fatal("foreign LogicalCluster has no cluster annotation")
		}
		status, body := byoVerb(t, strings.Replace(path, "/clusters/"+cluster+"/", "/clusters/"+foreignCluster+"/", 1), staticToken)
		if status != http.StatusForbidden && status != http.StatusNotFound {
			t.Fatalf("foreign verb: %d %s", status, body)
		}
	})
}

func byoCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctxWithTimeout(t, 6*time.Minute), name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v: %s", name, err, output)
	}
	return string(output)
}
func byoVerb(t *testing.T, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 20*time.Second), http.MethodPost, hubURL+path, strings.NewReader(`{"input":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Remote-User", "forged-admin")
	req.Header.Set("X-Remote-Group", "system:masters")
	resp, err := insecureClient(25 * time.Second).Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body
}

func byoConsentDigest(t *testing.T, org, workspace, name string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 20*time.Second), http.MethodGet, hubURL+"/api/providers", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+staticToken)
	req.Header.Set("X-Railgrid-Org", org)
	req.Header.Set("X-Railgrid-Workspace", workspace)
	response, err := insecureClient(25 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("catalog status %d", response.StatusCode)
	}
	var catalog struct {
		Items []struct {
			Name          string `json:"name"`
			ConsentDigest string `json:"consentDigest"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&catalog); err != nil {
		return "", err
	}
	for _, entry := range catalog.Items {
		if entry.Name == name {
			return entry.ConsentDigest, nil
		}
	}
	return "", fmt.Errorf("provider not yet registered in catalog")
}
