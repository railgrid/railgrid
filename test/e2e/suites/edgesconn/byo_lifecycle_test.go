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

package edgesconn

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// The BYO-provider REGISTRATION lifecycle, against a live hub.
//
// byo_provider_test.go proves the transport — what the far end receives once a
// request is routed over a tunnel. This proves the half that decides whether
// there is anything to route to: the preflight that refuses an install which
// could not work, the edge binding the hub records so the proxy can find the
// provider later, and the hub-owned Service the catalog controller derives from
// what the provider publishes about itself.
//
// Those are worth an e2e rather than unit tests because each one spans a
// boundary a fake cannot: a REST handler writing kcp objects, a controller
// watching an APIExport virtual workspace, and a path parsed out of a
// CatalogEntry the provider authored.

var (
	orgWorkspaceGVR = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	catalogEntryGVR = schema.GroupVersionResource{
		Group: "providers.faros.sh", Version: "v1alpha1", Resource: "catalogentries",
	}
)

func TestBYOProviderRegistrationLifecycle(t *testing.T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind not on PATH; an install target must be a KubernetesCluster edge")
	}

	edgeName := "byo-lifecycle"
	workDir := suiteTempDir(t, "byo-lifecycle")
	kubeconfig := filepath.Join(workDir, "faros.kubeconfig")

	runCLI(t, kubeconfig, farosBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	orgUUID, wsUUID := orgAndWorkspaceForCluster(t, tenantWS)
	t.Logf("org = %s, workspace = %s (cluster %s)", orgUUID, wsUUID, tenantWS)

	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	// 1. Preflight. Registration mints a long-lived cluster-admin credential and
	// builds a workspace tree; doing that for an install that cannot work leaves
	// the Org holding a live token and a half-built tree to clean up by hand.
	t.Run("refuses to register without a connected edge", func(t *testing.T) {
		code, body := postOrgProvider(t, orgUUID, map[string]any{
			"name": "byo-noedge", "sourceProvider": "quickstart",
			"edge": map[string]string{"workspace": wsUUID, "name": "does-not-exist"},
		})
		if code == http.StatusCreated {
			t.Fatal("registered a provider against an edge that does not exist")
		}
		if code != http.StatusConflict {
			t.Errorf("status = %d, want 409 Conflict (body %s)", code, body)
		}
		// Nothing may be left behind: the point of a preflight is that a failed
		// attempt is a clean no-op the client can retry.
		if orgProviderWorkspaceExists(t, orgUUID, "byo-noedge") {
			t.Error("a refused registration still created the provider workspace")
		}
	})

	// A KubernetesCluster edge specifically: a self-hosted provider needs a
	// cluster to run in, so that is the only kind the hub will accept as an
	// install target (edgetargets.go looks up kubernetesclusters, not
	// linuxservers). Hence a kind cluster here, where the transport test could
	// get away with a host-run LinuxServer agent.
	kindKubeconfig := filepath.Join(workDir, "kind.kubeconfig")
	runCLI(t, kubeconfig, farosBin, "edge", "create", edgeName, "--type", "kubernetes")
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(kubernetesClusterGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenantAdmin, kubernetesClusterGVR, edgeName)
	createKindCluster(t, "faros-byo-lifecycle", kindKubeconfig)
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "kubernetes", "--kubeconfig", kindKubeconfig)
	waitForConnected(t, tenantAdmin, kubernetesClusterGVR, edgeName)

	const providerName = "byo-quickstart"

	// 2. Registration proper.
	code, body := postOrgProvider(t, orgUUID, map[string]any{
		"name": providerName, "sourceProvider": "quickstart",
		"edge": map[string]string{"workspace": wsUUID, "name": edgeName},
	})
	if code != http.StatusCreated {
		t.Fatalf("register = %d, want 201: %s", code, body)
	}
	t.Cleanup(func() { deleteOrgProvider(t, orgUUID, providerName) })

	var reg struct {
		Provider struct {
			Name          string `json:"name"`
			WorkspacePath string `json:"workspacePath"`
		} `json:"provider"`
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.Unmarshal(body, &reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if reg.Kubeconfig == "" {
		t.Error("registration returned no kubeconfig; the Org has nothing to install with")
	}
	t.Logf("provider workspace = %s", reg.Provider.WorkspacePath)

	// 3. The edge binding, recorded by the hub on an object only the hub writes.
	// The backend proxy reads it back to route this provider's data plane; if it
	// were taken from the CatalogEntry instead, the tenant's own chart would
	// decide where platform traffic goes.
	t.Run("records the chosen edge on the provider workspace", func(t *testing.T) {
		ws := getOrgProviderWorkspace(t, orgUUID, providerName)
		if ws == nil {
			t.Fatal("provider workspace not found after registration")
		}
		ann := ws.GetAnnotations()
		if got := ann["edges.faros.sh/route-edge"]; got != edgeName {
			t.Errorf("route-edge annotation = %q, want %q", got, edgeName)
		}
		if got := ann["edges.faros.sh/route-workspace"]; got != wsUUID {
			t.Errorf("route-workspace annotation = %q, want %q", got, wsUUID)
		}
	})

	// 4. The hub-owned Service. It cannot be created at registration — the
	// address only exists once the provider's chart has run and published a
	// CatalogEntry — so the catalog controller derives it from what the provider
	// says about itself, validated as cluster DNS.
	t.Run("derives the hub-owned Service from the provider's published backend", func(t *testing.T) {
		providerWS := kcpDynamic(t, reg.Provider.WorkspacePath, adminToken)
		writeCatalogEntry(t, providerWS, providerName,
			"http://byo-quickstart.faros-provider-byo-quickstart.svc.cluster.local:8081")

		var svc *unstructured.Unstructured
		if !waitFor(t, 90*time.Second, func() (bool, string) {
			got, err := tenantAdmin.Resource(edgeServiceGVR).Get(
				ctxWithTimeout(t, 10*time.Second), "provider-"+providerName, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			svc = got
			return true, ""
		}) {
			t.Fatal("the hub never created the edges Service fronting this provider")
		}

		spec, _, _ := unstructured.NestedMap(svc.Object, "spec")
		// passthrough is the field that matters: the default, "secret", would
		// substitute a token and break a provider backend whose entire
		// authorization model is the caller's own bearer.
		if got, _ := spec["auth"].(string); got != "passthrough" {
			t.Errorf("spec.auth = %q, want passthrough", got)
		}
		if got, _, _ := unstructured.NestedString(svc.Object, "spec", "edgeRef", "name"); got != edgeName {
			t.Errorf("spec.edgeRef.name = %q, want %q", got, edgeName)
		}
		// Derived from the published address, not guessed from the provider
		// name — a chart chooses its own Service name and namespace.
		if got, _, _ := unstructured.NestedString(svc.Object, "spec", "targetRef", "name"); got != "byo-quickstart" {
			t.Errorf("spec.targetRef.name = %q, want the published Service name", got)
		}
		if got, _, _ := unstructured.NestedString(svc.Object, "spec", "targetRef", "namespace"); got != "faros-provider-byo-quickstart" {
			t.Errorf("spec.targetRef.namespace = %q, want the published namespace", got)
		}
		if got, _, _ := unstructured.NestedInt64(svc.Object, "spec", "port"); got != 8081 {
			t.Errorf("spec.port = %d, want 8081", got)
		}
	})

	// 5. The address is read from the provider but not trusted. A backend URL
	// that is not cluster DNS would point a hub-initiated request somewhere
	// outside the cluster the tunnel terminates in.
	t.Run("refuses a backend address that is not cluster DNS", func(t *testing.T) {
		const rogue = "rogue-target"
		code, body := postOrgProvider(t, orgUUID, map[string]any{
			"name": rogue, "sourceProvider": "quickstart",
			"edge": map[string]string{"workspace": wsUUID, "name": edgeName},
		})
		if code != http.StatusCreated {
			t.Fatalf("register %s = %d: %s", rogue, code, body)
		}
		t.Cleanup(func() { deleteOrgProvider(t, orgUUID, rogue) })

		providerWS := kcpDynamic(t, "root:faros:tenants:"+orgUUID+":providers:"+rogue, adminToken)
		writeCatalogEntry(t, providerWS, rogue, "http://169.254.169.254/latest/meta-data")

		// Give the controller the same window the positive case needed, then
		// assert nothing appeared. A Service here would mean the hub pointing a
		// tunnel at an address the tenant chose.
		time.Sleep(20 * time.Second)
		if _, err := tenantAdmin.Resource(edgeServiceGVR).Get(
			ctxWithTimeout(t, 10*time.Second), "provider-"+rogue, metav1.GetOptions{}); err == nil {
			t.Error("a Service was created for a backend address outside the cluster")
		}
	})
}

// orgAndWorkspaceForCluster finds which of the caller's organizations owns the
// workspace backing a logical cluster, and what that workspace is called.
//
// Both halves have to be derived rather than assumed. A caller can belong to
// several organizations, so the first one listed is not necessarily the one
// holding this workspace; and the REST surface names a workspace by its UUID
// while the CLI hands back a logical-cluster ID, which are different strings.
func orgAndWorkspaceForCluster(t *testing.T, cluster string) (string, string) {
	t.Helper()
	var out struct {
		Items []struct {
			UUID string `json:"uuid"`
		} `json:"items"`
	}
	code, body := hubJSON(t, http.MethodGet, "/api/orgs", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/orgs = %d: %s", code, body)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode orgs: %v", err)
	}

	for _, org := range out.Items {
		orgClient := kcpDynamic(t, "root:faros:tenants:"+org.UUID, adminToken)
		list, err := orgClient.Resource(orgWorkspaceGVR).List(ctxWithTimeout(t, 20*time.Second), metav1.ListOptions{})
		if err != nil {
			continue // an org whose workspace list we cannot read is not this one
		}
		for _, ws := range list.Items {
			if got, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster"); got == cluster {
				return org.UUID, ws.GetName()
			}
		}
	}
	t.Fatalf("no organization of this caller owns a workspace with cluster %s", cluster)
	return "", ""
}

func getOrgProviderWorkspace(t *testing.T, orgUUID, name string) *unstructured.Unstructured {
	t.Helper()
	parent := kcpDynamic(t, "root:faros:tenants:"+orgUUID+":providers", adminToken)
	got, err := parent.Resource(orgWorkspaceGVR).Get(ctxWithTimeout(t, 20*time.Second), name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return got
}

func orgProviderWorkspaceExists(t *testing.T, orgUUID, name string) bool {
	t.Helper()
	return getOrgProviderWorkspace(t, orgUUID, name) != nil
}

// writeCatalogEntry stands in for the provider's chart `init`, publishing the
// address the provider listens on inside the tenant's cluster.
func writeCatalogEntry(t *testing.T, cl dynamic.Interface, name, backendURL string) {
	t.Helper()
	entry := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "providers.faros.sh/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"displayName": name,
			"backend":     map[string]any{"url": backendURL, "healthPath": "/healthz"},
		},
	}}
	if _, err := cl.Resource(catalogEntryGVR).Create(ctxWithTimeout(t, 30*time.Second), entry, metav1.CreateOptions{}); err != nil {
		t.Fatalf("writing CatalogEntry %s: %v", name, err)
	}
}

func postOrgProvider(t *testing.T, orgUUID string, body map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return hubJSON(t, http.MethodPost, "/api/orgs/"+orgUUID+"/providers", orgUUID, raw)
}

func deleteOrgProvider(t *testing.T, orgUUID, name string) {
	t.Helper()
	_, _ = hubJSON(t, http.MethodDelete, "/api/orgs/"+orgUUID+"/providers/"+name, orgUUID, nil)
}

// hubJSON issues one authenticated REST call against the hub and returns the
// status and body. orgUUID, when set, supplies the tenant-scope header the
// middleware requires.
func hubJSON(t *testing.T, method, path, orgUUID string, body []byte) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body == nil {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 90*time.Second), method, hubURL+path, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+staticToken)
	req.Header.Set("Content-Type", "application/json")
	if orgUUID != "" {
		req.Header.Set("X-Faros-Org", orgUUID)
	}
	resp, err := insecureClient(120 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading %s %s: %v", method, path, err)
	}
	return resp.StatusCode, buf.Bytes()
}
