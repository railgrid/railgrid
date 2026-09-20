/*
Copyright 2026 The Railgrid Authors.

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

// Package identity is the end-to-end suite for the hub's scoped-identity
// service (pkg/hub/identity, pkg/hub/restapi/identities.go) — the one place a
// runtime credential for a tenant object is minted since the provider-contract
// remediation took minting away from providers.
//
// The shape follows the provider suite: embedded kcp plus the quickstart
// provider as host subprocesses, no kind and no Helm. Quickstart is the
// REQUESTING provider — it holds a real provider ServiceAccount token, which is
// what /api/identities attests — and its one kind, Greeting, is the OWNER an
// identity hangs off.
//
// Two things this suite adds to that bootstrap:
//
//  1. A second, synthetic provider called `fixture`, so clause E (composition)
//     has a foreign API group to compose. It is registered the way a real
//     provider is — a Provider record, a workspace-scoped APIExport over group
//     `fixture.railgrid.ai`, a bind grant, a CatalogEntry — but it runs no
//     process: the identity policy reads the catalog and kcp, never the
//     provider's backend, so a served backend would add nothing. See
//     applyFixtureProvider.
//
//  2. quickstart's CatalogEntry is applied with a `dependencies[].composes`
//     declaration naming that fixture. The committed manifest is NOT edited:
//     the suite patches the object in memory before creating it, exactly as the
//     provider suite already patches spec.ui.url to the test port. Editing the
//     shipped manifest would hand every deployment a dependency that exists
//     only in this test.
//
// The hub runs with --provider-hub-access-platform-default=false. Quickstart is
// a platform provider, and under the default (true) an undecided composition is
// allowed, which would make "refused until accepted" vacuous. False is also the
// setting the flag's own help text says every provider should eventually be
// held to.
package identity

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Suite-shared state populated by TestMain.
var (
	repoRoot   string
	hubURL     string // http://127.0.0.1:<hubPort>
	kcpServer  string // https://127.0.0.1:<kcpPort>
	adminToken string // kcp admin token (from .kcp/admin.kubeconfig)
	// providerToken is the quickstart provider ServiceAccount's bearer — the
	// credential /api/identities attests. It is the same token the provider
	// pod mounts, read out of the provider-token Secret the Provider
	// controller writes.
	providerToken string
	providerPort  string
	providerURL   string
)

const (
	// Ports are this suite's own: every subprocess suite shares the embedded
	// kcp etcd port 2380, so they must not run concurrently, but nothing else
	// may collide. 19503/16503/18128 continue the series after kueryprovider
	// (19493/16493/18118). The Makefile pre-checks them with lsof.
	hubPort      = "19503"
	kcpPort      = "16503"
	defaultPPort = "18128"

	staticToken = "test:user-default"

	// The requesting provider and its workspace.
	providerName  = "quickstart"
	workspacePath = "root:railgrid:providers:quickstart"

	// The synthetic dependency whose group quickstart composes.
	fixtureName          = "fixture"
	fixtureWorkspacePath = "root:railgrid:providers:fixture"
	fixtureExportName    = "fixture.providers.railgrid.ai"
	fixtureGroup         = "fixture.railgrid.ai"
	// fixtureResource is the kind quickstart DECLARES it composes — clause E.
	fixtureResource   = "widgets"
	fixtureSchemaName = "v1.widgets.fixture.railgrid.ai"
	// fixturePlainResource is a second kind in the same group that quickstart
	// does NOT compose. It is what makes the clause B and C refusals testable:
	// clause E claims a rule only when every resource in it is composed, so a
	// rule over `widgets` can never reach the foreign-read path, and a suite
	// with only one fixture kind would be asserting the wrong clause.
	fixturePlainResource   = "gadgets"
	fixturePlainSchemaName = "v1.gadgets.fixture.railgrid.ai"
)

var (
	secretGVR       = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	apiBindingGVR   = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	apiExportGVR    = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	apiSchemaGVR    = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	clusterRoleGVR  = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	serviceAcctGVR  = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	workspaceGVR    = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	providerGVR     = schema.GroupVersionResource{Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "providers"}
	catalogEntryGVR = schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
	scopedIDGVR     = schema.GroupVersionResource{Group: "tenants.railgrid.ai", Version: "v1alpha1", Resource: "scopedidentities"}

	// greetingGVR is quickstart's one kind: the owner object identities hang
	// off in this suite. Cluster-scoped, like every data-plane-addressed
	// provider kind.
	greetingGVR = schema.GroupVersionResource{
		Group: "quickstart.providers.railgrid.ai", Version: "v1alpha1", Resource: "greetings",
	}
	widgetGVR = schema.GroupVersionResource{
		Group: fixtureGroup, Version: "v1alpha1", Resource: fixtureResource,
	}
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	providerPort = defaultPPort
	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort
	providerURL = "http://127.0.0.1:" + providerPort

	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill railgrid-hub; pkill quickstart-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "railgrid-e2e-identity-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	keepData := os.Getenv("RAILGRID_E2E_KEEP_DATA") == "true"

	hubLog, _ := os.Create(filepath.Join(dataDir, "hub.log"))
	hubCmd := exec.Command(filepath.Join(repoRoot, "bin", "railgrid-hub"),
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-secure-port", kcpPort,
		"--listen-addr", ":"+hubPort,
		"--data-dir", dataDir,
		"--static-auth-token", staticToken,
		// See the package comment: without this a platform provider composes
		// what it declares in a workspace nobody has decided on, and the
		// "refused until accepted" assertion would pass for the wrong reason.
		"--provider-hub-access-platform-default=false",
	)
	hubCmd.Stdout = hubLog
	hubCmd.Stderr = hubLog
	hubCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := hubCmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start hub:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "hub started (pid=%d, log=%s)\n", hubCmd.Process.Pid, hubLog.Name())

	var provCmd *exec.Cmd
	cleanup := func() {
		killGroup(hubCmd)
		killGroup(provCmd)
		if !keepData {
			_ = os.RemoveAll(dataDir)
		} else {
			fmt.Fprintf(os.Stderr, "logs preserved under %s\n", dataDir)
		}
	}
	die := func(msg string, err error) {
		cleanup()
		fmt.Fprintln(os.Stderr, msg+":", err)
		os.Exit(1)
	}

	if err := waitReady(hubURL+"/readyz", 3*time.Minute); err != nil {
		die("hub never ready", err)
	}

	tok, err := extractToken(filepath.Join(dataDir, "kcp", "admin.kubeconfig"))
	if err != nil {
		die("extract admin token", err)
	}
	adminToken = tok

	// The synthetic dependency goes in FIRST: quickstart's patched CatalogEntry
	// names it, and the Enable flow refuses a provider whose dependencies are
	// not enabled, so the fixture has to be a real catalog entry by then.
	if err := applyFixtureProvider(); err != nil {
		die("register the fixture provider", err)
	}
	if err := applyQuickstartManifests(); err != nil {
		die("apply quickstart manifests", err)
	}

	runtimeKubeconfig := filepath.Join(dataDir, "quickstart-runtime.kubeconfig")
	if err := mintRuntimeKubeconfig(runtimeKubeconfig, 2*time.Minute); err != nil {
		die("mint runtime kubeconfig", err)
	}

	initLog, err := os.Create(filepath.Join(dataDir, "init.log"))
	if err != nil {
		die("create init.log", err)
	}
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "quickstart-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"QUICKSTART_WORKSPACE_PATH="+workspacePath,
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "quickstart", "deploy", "chart", "files"),
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		die("quickstart init failed (log: "+initLog.Name()+")", err)
	}

	provLog, err := os.Create(filepath.Join(dataDir, "provider.log"))
	if err != nil {
		die("create provider.log", err)
	}
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "quickstart-provider"))
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"RAILGRID_HUB_URL="+hubURL,
		"RAILGRID_HUB_TOKEN="+staticToken,
		"RAILGRID_PROVIDER_NAME="+providerName,
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		die("start provider", err)
	}
	fmt.Fprintf(os.Stderr, "quickstart-provider started (pid=%d, port=:%s)\n", provCmd.Process.Pid, providerPort)

	if err := waitReady(providerURL+"/healthz", 60*time.Second); err != nil {
		die("quickstart never ready", err)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// applyQuickstartManifests applies quickstart's Provider + CatalogEntry into
// root:railgrid:system:providers, patched for this suite: the UI/backend URLs
// point at the test port, and spec.dependencies declares the composition clause
// E is tested through. The committed manifest is read, not written.
func applyQuickstartManifests() error {
	cl, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	provider := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "Provider",
		"metadata":   map[string]any{"name": providerName},
		"spec":       map[string]any{"displayName": "Quickstart"},
	}}
	if err := createWithRetry(cl, providerGVR, provider, 90*time.Second); err != nil {
		return err
	}

	entry := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "providers.railgrid.ai/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata":   map[string]any{"name": providerName},
		"spec": map[string]any{
			"displayName": "Quickstart",
			"description": "Reference provider driving the scoped-identity e2e.",
			"vendor":      "railgrid",
			"version":     "0.1.0",
			"category":    "Demo",
			"ui":          map[string]any{"url": providerURL, "indexPath": "/"},
			"backend":     map[string]any{"url": providerURL, "healthPath": "/readyz"},
			"apiExport":   map[string]any{"name": "quickstart.providers.railgrid.ai"},
			// The one declaration the committed manifest does not carry. It
			// names the fixture provider — the owner of fixtureGroup — because
			// the policy refuses a composition whose dependency does not
			// actually export the group (policy.go, authorizeComposition).
			"dependencies": []any{map[string]any{
				"name": fixtureName,
				"composes": []any{map[string]any{
					"group":    fixtureGroup,
					"resource": fixtureResource,
					// Deliberately not the full vocabulary: the suite asks for
					// `delete` too, and gets composition_verb_not_declared.
					"verbs": []any{"get", "list", "watch", "create"},
				}},
			}},
			"dataPlane": map[string]any{"verbs": []any{map[string]any{
				"resource": "greetings", "verb": "greet",
				"description": "Return the greeting this Greeting describes.",
				"readOnly":    true,
			}}},
		},
	}}
	return createWithRetry(cl, catalogEntryGVR, entry, 90*time.Second)
}

// applyFixtureProvider registers the synthetic dependency: a Provider record
// (which makes the hub materialize root:railgrid:providers:fixture), then —
// standing in for what a real provider's `init` does — an APIResourceSchema, an
// APIExport over fixtureGroup, and the bind grant a tenant needs to APIBind it.
// The CatalogEntry comes last, so the catalog controller resolves
// status.apiGroups from an export that already exists.
func applyFixtureProvider() error {
	sys, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	provider := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "Provider",
		"metadata":   map[string]any{"name": fixtureName},
		"spec":       map[string]any{"displayName": "Identity E2E Fixture"},
	}}
	if err := createWithRetry(sys, providerGVR, provider, 90*time.Second); err != nil {
		return err
	}

	// The Provider controller creates the sub-workspace; wait for it to answer.
	sub, err := kcpDynamicRaw(fixtureWorkspacePath, adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client for %s: %w", fixtureWorkspacePath, err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, lastErr = sub.Resource(apiSchemaGVR).List(ctx, metav1.ListOptions{})
		cancel()
		if lastErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("workspace %s never became usable: %w", fixtureWorkspacePath, lastErr)
	}

	if err := createWithRetry(sub, apiSchemaGVR, fixtureSchema(fixtureSchemaName, "Widget", fixtureResource, "widget"), 60*time.Second); err != nil {
		return err
	}
	if err := createWithRetry(sub, apiSchemaGVR, fixtureSchema(fixturePlainSchemaName, "Gadget", fixturePlainResource, "gadget"), 60*time.Second); err != nil {
		return err
	}
	if err := createWithRetry(sub, apiExportGVR, fixtureExport(), 60*time.Second); err != nil {
		return err
	}
	// Without the bind grant the Enable flow's APIBinding is refused, and an
	// unbound dependency makes every clause E rule fail as provider_not_bound
	// rather than on consent.
	bindRole := "railgrid:providers:bind:" + fixtureExportName
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": bindRole},
		"rules": []any{map[string]any{
			"apiGroups":     []any{"apis.kcp.io"},
			"resources":     []any{"apiexports"},
			"verbs":         []any{"bind"},
			"resourceNames": []any{fixtureExportName},
		}},
	}}
	if err := createWithRetry(sub, clusterRoleGVR, role, 60*time.Second); err != nil {
		return err
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": bindRole},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": bindRole,
		},
		"subjects": []any{map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "Group", "name": "system:authenticated",
		}},
	}}
	if err := createWithRetry(sub, clusterRoleBGVR, binding, 60*time.Second); err != nil {
		return err
	}

	entry := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "providers.railgrid.ai/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata":   map[string]any{"name": fixtureName},
		"spec": map[string]any{
			"displayName": "Identity E2E Fixture",
			"description": "API-only provider whose group the quickstart provider composes.",
			"vendor":      "railgrid",
			"version":     "0.0.1",
			"category":    "Demo",
			// No ui and no backend on purpose: an APIExport alone is a valid
			// provider shape (controller.go: EndpointsValid counts it), and it
			// is the shape an org-owned, API-only provider has.
			"apiExport": map[string]any{"name": fixtureExportName},
		},
	}}
	return createWithRetry(sys, catalogEntryGVR, entry, 90*time.Second)
}

// fixtureSchema is the smallest structural schema kcp will serve. The kinds
// exist only so the (group, resource) coordinates are real: the policy resolves
// the group's owner from the APIExport's spec.resources[].group, and the
// acceptance test writes one Widget with the minted token.
func fixtureSchema(name, kind, plural, singular string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIResourceSchema",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"group": fixtureGroup,
			"names": map[string]any{
				"kind": kind, "listKind": kind + "List",
				"plural": plural, "singular": singular,
			},
			"scope": "Cluster",
			"versions": []any{map[string]any{
				"name": "v1alpha1", "served": true, "storage": true,
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"apiVersion": map[string]any{"type": "string"},
						"kind":       map[string]any{"type": "string"},
						"metadata":   map[string]any{"type": "object"},
						"spec": map[string]any{
							"type":       "object",
							"properties": map[string]any{"size": map[string]any{"type": "string"}},
						},
					},
				},
			}},
		},
	}}
}

func fixtureExport() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": fixtureExportName},
		"spec": map[string]any{
			"resources": []any{
				map[string]any{
					"group": fixtureGroup, "name": fixtureResource,
					"schema":  fixtureSchemaName,
					"storage": map[string]any{"crd": map[string]any{}},
				},
				map[string]any{
					"group": fixtureGroup, "name": fixturePlainResource,
					"schema":  fixturePlainSchemaName,
					"storage": map[string]any{"crd": map[string]any{}},
				},
			},
		},
	}}
}

// createWithRetry creates obj, tolerating AlreadyExists and retrying while the
// API is not servable yet. The hub reports /readyz before every bootstrapped
// API answers, and the fixture's workspace appears asynchronously.
func createWithRetry(cl dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := cl.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		cancel()
		if err == nil || strings.Contains(err.Error(), "already exists") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("create %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		time.Sleep(2 * time.Second)
	}
}

// mintRuntimeKubeconfig waits for the Provider controller to populate the
// provider-token Secret in quickstart's sub-workspace, writes the kubeconfig
// the provider process mounts, and keeps the raw bearer: it is the credential
// /api/identities attests, so the suite calls the endpoint as the provider
// really does.
func mintRuntimeKubeconfig(path string, timeout time.Duration) error {
	cl, err := kcpDynamicRaw(workspacePath, adminToken)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var token, lastErr string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sec, err := cl.Resource(secretGVR).Namespace("default").Get(ctx, "provider-token", metav1.GetOptions{})
		cancel()
		if err != nil {
			lastErr = err.Error()
		} else {
			enc, _, _ := unstructured.NestedString(sec.Object, "data", "token")
			if enc != "" {
				raw, err := base64.StdEncoding.DecodeString(enc)
				if err != nil {
					return fmt.Errorf("decode provider-token: %w", err)
				}
				token = string(raw)
				break
			}
			lastErr = "provider-token Secret exists but token not yet populated"
		}
		time.Sleep(2 * time.Second)
	}
	if token == "" {
		return fmt.Errorf("provider-token never populated: %s", lastErr)
	}
	providerToken = token
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: railgrid
  cluster:
    server: %s/clusters/%s
    insecure-skip-tls-verify: true
contexts:
- name: railgrid
  context:
    cluster: railgrid
    user: railgrid
current-context: railgrid
users:
- name: railgrid
  user:
    token: %s
`, kcpServer, workspacePath, token)
	return os.WriteFile(path, []byte(kc), 0o600)
}

func kcpDynamicRaw(clusterPath, token string) (dynamic.Interface, error) {
	return dynamic.NewForConfig(&rest.Config{
		Host:            kcpServer + "/clusters/" + clusterPath,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true}, // dev cert is self-signed
	})
}

func kcpDynamic(t *testing.T, clusterPath, token string) dynamic.Interface {
	t.Helper()
	c, err := kcpDynamicRaw(clusterPath, token)
	if err != nil {
		t.Fatalf("dynamic client for %s: %v", clusterPath, err)
	}
	return c
}

// build runs the same targets the Makefile does, so `go test ./...` on this
// package works without a prior make.
func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-quickstart-provider")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func portInUse(p string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func killGroup(c *exec.Cmd) {
	if c == nil || c.Process == nil {
		return
	}
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	_, _ = c.Process.Wait()
}

func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "ok") {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout after %s waiting for %s", timeout, url)
}

func extractToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "token:") {
			return strings.TrimSpace(strings.TrimPrefix(s, "token:")), nil
		}
	}
	return "", fmt.Errorf("no token: line in %s", path)
}

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// waitForCondition polls every second until cond() returns true or the deadline
// expires; the last message is logged so a timeout says why.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastMsg string
	for time.Now().Before(deadline) {
		ok, msg := cond()
		if ok {
			return true
		}
		lastMsg = msg
		time.Sleep(time.Second)
	}
	t.Logf("wait timeout after %s; last status: %s", timeout, lastMsg)
	return false
}
