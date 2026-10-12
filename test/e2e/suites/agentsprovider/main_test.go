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

// Package agentsprovider is the end-to-end suite for the agents provider's
// Webhook -> Trigger routing. It starts railgrid-hub with embedded kcp and
// agents-provider as host subprocesses, following the standard provider
// bootstrap the kuery and infrastructure suites use: Provider + CatalogEntry
// applied into root:railgrid:system:providers (the hub's Provider controller
// materializes root:railgrid:providers:agents, its ServiceAccount and the
// provider-token Secret), then `agents-provider init` with the minted SA
// kubeconfig (APIExport + schemas + endpoint slices + bind grant), then serve
// with the in-memory store (no Postgres in CI).
//
// Coverage: the tenant enables agents through the hub's own Enable endpoint,
// creates Webhooks, an Agent and Triggers with a kube client, POSTs signed
// deliveries to the hub-proxied webhook URL anonymously, and drives the
// webhooks/deliveries, webhooks/run, triggers/run and triggers/evaluate verbs
// as kcp custom subresources on the hub's kcp front door. A run only has to be
// RECORDED (a Run object with the right source): the Agent names no model
// credential, so execution fails, which is out of scope here.
//
// # The infrastructure / edges dependency
//
// The agents CatalogEntry's spec.requires names the `infrastructure` and
// `edges` providers, so the hub's Enable endpoint refuses agents in a
// workspace where neither is bound (missingProviderDependencies). Running
// those two providers would drag in kro, a stub backend and the edges tunnel
// for nothing this suite asserts on. Instead the suite registers each as an
// API-ONLY stand-in, the same shape the identity suite's `fixture` provider
// has: a Provider record (so the hub materializes the real
// root:railgrid:providers:<name> workspace), the provider's REAL generated
// APIResourceSchemas and APIExport read from its chart (deploy/chart/files),
// a bind grant, and a CatalogEntry that declares only the export. The schemas,
// export and grant are written as the stand-in workspace's own provider
// ServiceAccount, because the platform PermissionClaimPolicy reserves each
// provider API group for exactly that identity. Two things
// are dropped from the generated export, on purpose:
//
//   - the "<resource>/<verb>" custom-subresource entries, whose virtual storage
//     references a DataPlaneEndpointSlice that only the provider's own `init`
//     creates — there is no process to route to;
//   - the export's own permission claims, which nothing here would consume.
//
// The kinds agents claims (instances, linuxservers, macosservers, services)
// therefore really exist under their real names, and the tenant enables both
// stand-ins through the same hub endpoint before agents. Agents is then
// enabled accepting the two platform-builtin claims its machinery needs
// (authorization.k8s.io/subjectaccessreviews for the verb gate, core secrets
// for the model-credential and connection reconcilers) and DECLINING the
// infrastructure / edges compositions, which become Rejected claims on the
// binding. TestBTenantEnable pins that a declined foreign claim does not keep
// agents' own kinds from binding.
//
// Runs without kind/Helm/Dex, matching the other subprocess provider suites.
// It shares the embedded-kcp etcd port 2380 with them: do not run it
// concurrently with another subprocess suite.
package agentsprovider

import (
	"bytes"
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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

// Suite-shared state populated by TestMain.
var (
	// provCmd is the running agents-provider; provEnv and provLogPath are
	// what startProvider (re)starts it with.
	provCmd     *exec.Cmd
	provEnv     []string
	provLogPath string

	repoRoot     string
	hubURL       string // http://127.0.0.1:<hubPort>
	kcpServer    string // https://127.0.0.1:<kcpPort> (admin kubeconfig)
	adminToken   string // kcp admin token (from <data-dir>/kcp/admin.kubeconfig)
	providerPort string
)

// Ports continue the +10 series the other subprocess suites use (provider
// 19443, infraprovider 19453, edges 19463, edgesconn 19473, cli 19483, kuery
// 19493, identity 19503). Like them, this suite shares the embedded-kcp etcd
// port 2380 and must not run concurrently with the others. The Makefile
// pre-checks all four with lsof.
const (
	hubPort      = "19513"
	kcpPort      = "16513"
	defaultPPort = "18138"

	staticToken = "test:user-default"

	// webhookKey is the signing key both halves of the provider derive webhook
	// URL tokens from. Set explicitly (rather than derived from the provider
	// kubeconfig) so the suite can compute a valid token for a webhook that
	// does not exist and pin the 404 the inbound route answers for it.
	webhookKey = "agents-e2e-webhook-signing-key"

	providerName  = "agents"
	workspacePath = "root:railgrid:providers:agents"
	apiExportName = "agents.railgrid.ai"
	agentsGroup   = "agents.railgrid.ai"
)

// standIns are the dependency providers registered API-only (see the package
// comment). exportName is the generated APIExport's metadata.name.
var standIns = []struct {
	name       string
	exportName string
}{
	{name: "infrastructure", exportName: "infrastructure.providers.railgrid.ai"},
	{name: "edges", exportName: "edges.providers.railgrid.ai"},
}

var (
	secretGVR       = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	apiBindingGVR   = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	apiExportGVR    = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	apiSchemaGVR    = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	clusterRoleGVR  = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	workspaceGVR    = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	providerGVR     = schema.GroupVersionResource{Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "providers"}
	catalogEntryGVR = schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}

	webhookGVR = schema.GroupVersionResource{Group: agentsGroup, Version: "v1alpha1", Resource: "webhooks"}
	triggerGVR = schema.GroupVersionResource{Group: agentsGroup, Version: "v1alpha1", Resource: "triggers"}
	agentGVR   = schema.GroupVersionResource{Group: agentsGroup, Version: "v1alpha1", Resource: "agents"}
	runGVR     = schema.GroupVersionResource{Group: agentsGroup, Version: "v1alpha1", Resource: "runs"}
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	providerPort = defaultPPort
	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort

	// Fail fast if a previous run left ports bound.
	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill railgrid-hub; pkill agents-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	// Build binaries up-front so test-runtime startup is just process exec.
	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "railgrid-e2e-agents-")
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
	)
	hubCmd.Stdout = hubLog
	hubCmd.Stderr = hubLog
	hubCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := hubCmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start hub:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "hub started (pid=%d, log=%s)\n", hubCmd.Process.Pid, hubLog.Name())

	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		if provLogPath != "" {
			fmt.Fprintf(os.Stderr, "--- provider.log (tail) ---\n%s\n", tailFile(provLogPath, 60))
		}
		fmt.Fprintf(os.Stderr, "--- hub.log (tail) ---\n%s\n", tailFile(hubLog.Name(), 40))
		killGroup(hubCmd)
		killGroup(provCmd)
		if !keepData {
			_ = os.RemoveAll(dataDir)
		}
		os.Exit(1)
	}

	// Wait for hub readiness (embedded kcp bootstrap takes ~30-60s).
	if err := waitReady(hubURL+"/readyz", 3*time.Minute); err != nil {
		fail("hub never ready: %v", err)
	}

	tok, err := extractToken(filepath.Join(dataDir, "kcp", "admin.kubeconfig"))
	if err != nil {
		fail("extract admin token: %v", err)
	}
	adminToken = tok

	// The two dependency providers, API-only. Registered before agents so the
	// catalog has them by the time a tenant enables anything.
	for _, s := range standIns {
		if err := registerStandInProvider(s.name, s.exportName); err != nil {
			fail("register stand-in provider %s: %v", s.name, err)
		}
	}

	// Provisioning: Provider + CatalogEntry into root:railgrid:system:providers
	// (mirrors `make install-provider-agents`).
	if err := applyAgentsManifests(); err != nil {
		fail("apply agents manifests: %v", err)
	}

	runtimeKubeconfig := filepath.Join(dataDir, "agents-runtime.kubeconfig")
	if err := mintRuntimeKubeconfig(runtimeKubeconfig, 2*time.Minute); err != nil {
		fail("mint runtime kubeconfig: %v", err)
	}

	initLog, err := os.Create(filepath.Join(dataDir, "init.log"))
	if err != nil {
		fail("create init.log: %v", err)
	}
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "agents-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"AGENTS_WORKSPACE_PATH="+workspacePath,
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "agents", "deploy", "chart", "files"),
		// Where a kcp shard reaches this provider when it forwards a custom
		// subresource. Given explicitly because applyAgentsManifests rewrote
		// the registered entry's backend URL to this same address; the
		// committed manifest targets the dev-loop port.
		"RAILGRID_DATAPLANE_URL=http://localhost:"+providerPort,
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		fail("agents init failed: %v (log: %s)\n%s", err, initLog.Name(), tailFile(initLog.Name(), 60))
	}

	provLogPath = filepath.Join(dataDir, "provider.log")
	provEnv = append(os.Environ(),
		"PORT="+providerPort,
		"RAILGRID_HUB_URL="+hubURL,
		"RAILGRID_PROVIDER_NAME="+providerName,
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		// Readiness's leader lease and the endpoint-slice fallback read the
		// workspace path; the same value init was given.
		"AGENTS_WORKSPACE_PATH="+workspacePath,
		// serve dispatches the "<resource>/<verb>" coordinates it answers on
		// the kcp custom-subresource path from the CatalogEntry, and refuses
		// to start without it.
		"RAILGRID_CATALOGENTRY_FILE="+filepath.Join(repoRoot, "providers", "agents", "manifest.yaml"),
		"AGENTS_IN_MEMORY_STORE=true",
		"AGENTS_DATABASE_URL=",
		"AGENTS_WEBHOOK_KEY="+webhookKey,
	)
	if err := startProvider(); err != nil {
		fail("%v", err)
	}

	code := m.Run()
	if code != 0 {
		fmt.Fprintf(os.Stderr, "--- provider.log (tail) ---\n%s\n", tailFile(provLogPath, 80))
	}
	killGroup(hubCmd)
	killGroup(provCmd)
	if !keepData {
		_ = os.RemoveAll(dataDir)
	} else {
		fmt.Fprintf(os.Stderr, "logs preserved under %s\n", dataDir)
	}
	os.Exit(code)
}

// startProvider starts agents-provider with provEnv, appending to
// provLogPath, and waits for /healthz.
func startProvider() error {
	provLog, err := os.OpenFile(provLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open provider.log: %w", err)
	}
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "agents-provider"))
	provCmd.Env = provEnv
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		return fmt.Errorf("start provider: %w", err)
	}
	fmt.Fprintf(os.Stderr, "agents-provider started (pid=%d, port=:%s, log=%s)\n", provCmd.Process.Pid, providerPort, provLogPath)
	if err := waitReady("http://127.0.0.1:"+providerPort+"/healthz", 90*time.Second); err != nil {
		return fmt.Errorf("agents-provider never healthy: %w", err)
	}
	return nil
}

// restartProvider kills the running provider and starts a fresh one.
func restartProvider() error {
	killGroup(provCmd)
	return startProvider()
}

// applyAgentsManifests applies provider.yaml (kind Provider) + manifest.yaml
// (kind CatalogEntry) into root:railgrid:system:providers, mirroring `make
// install-provider-agents`, with the serving URLs rewritten to the test port.
func applyAgentsManifests() error {
	cl, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     providerGVR,
		"CatalogEntry": catalogEntryGVR,
	}
	for _, file := range []string{"provider.yaml", "manifest.yaml"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "providers", providerName, file))
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		for _, doc := range bytes.Split(raw, []byte("\n---")) {
			if !bytes.Contains(doc, []byte("apiVersion:")) {
				continue
			}
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
				return fmt.Errorf("parse %s: %w", file, err)
			}
			if obj.GetKind() == "" {
				continue
			}
			gvr, ok := gvrByKind[obj.GetKind()]
			if !ok {
				return fmt.Errorf("%s: unexpected kind %q", file, obj.GetKind())
			}
			if obj.GetKind() == "CatalogEntry" {
				// The committed manifest targets :8087 (`make
				// run-provider-agents`); the suite keeps its own port.
				overrideURL := "http://localhost:" + providerPort
				for _, field := range [][]string{{"spec", "serving", "ui", "url"}, {"spec", "serving", "backend", "url"}} {
					if err := unstructured.SetNestedField(obj.Object, overrideURL, field...); err != nil {
						return fmt.Errorf("%s: override %s: %w", file, strings.Join(field, "."), err)
					}
				}
			}
			if err := createWithRetry(cl, gvr, obj, 90*time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}

// registerStandInProvider registers an API-only copy of a dependency provider:
// the Provider record, the provider's real generated schemas and APIExport
// (minus the custom-subresource entries and claims — see the package comment),
// a bind grant, and a CatalogEntry declaring only the export. The kcp objects
// are written as the workspace's provider ServiceAccount, as `init` would.
func registerStandInProvider(name, exportName string) error {
	sys, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	provider := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "Provider",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"displayName": name + " (agents e2e stand-in)"},
	}}
	if err := createWithRetry(sys, providerGVR, provider, 90*time.Second); err != nil {
		return err
	}

	path := "root:railgrid:providers:" + name
	sub, err := kcpDynamicRaw(path, adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client for %s: %w", path, err)
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
		return fmt.Errorf("workspace %s never became usable: %w", path, lastErr)
	}

	// The platform PermissionClaimPolicy reserves every provider API group for
	// the identity the hub mints per provider (ServiceAccount default/provider
	// in the provider's own workspace), so the export is applied as that
	// ServiceAccount — exactly what the provider's own `init` does.
	saToken, err := providerToken(path, 2*time.Minute)
	if err != nil {
		return err
	}
	if sub, err = kcpDynamicRaw(path, saToken); err != nil {
		return fmt.Errorf("dynamic client for %s as its provider ServiceAccount: %w", path, err)
	}

	filesDir := filepath.Join(repoRoot, "providers", name, "deploy", "chart", "files")
	rawExport, err := os.ReadFile(filepath.Join(filesDir, "apiexport.yaml"))
	if err != nil {
		return fmt.Errorf("read %s export: %w", name, err)
	}
	export := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(rawExport, &export.Object); err != nil {
		return fmt.Errorf("parse %s export: %w", name, err)
	}
	if export.GetName() != exportName {
		return fmt.Errorf("%s export is named %q, want %q", name, export.GetName(), exportName)
	}
	resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	var kept []any
	wantSchemas := map[string]bool{}
	for _, r := range resources {
		res, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if _, virtual, _ := unstructured.NestedMap(res, "storage", "virtual"); virtual {
			continue
		}
		kept = append(kept, res)
		if s, _ := res["schema"].(string); s != "" {
			wantSchemas[s] = true
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("%s export has no CRD-stored resources to stand in for", name)
	}
	if err := unstructured.SetNestedSlice(export.Object, kept, "spec", "resources"); err != nil {
		return err
	}
	unstructured.RemoveNestedField(export.Object, "spec", "permissionClaims")

	schemaFiles, err := filepath.Glob(filepath.Join(filesDir, "schemas", "*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range schemaFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, &obj.Object); err != nil {
			return fmt.Errorf("parse %s: %w", f, err)
		}
		if !wantSchemas[obj.GetName()] {
			continue
		}
		delete(wantSchemas, obj.GetName())
		if err := createWithRetry(sub, apiSchemaGVR, obj, 60*time.Second); err != nil {
			return err
		}
	}
	if len(wantSchemas) > 0 {
		return fmt.Errorf("%s export names schemas the chart does not ship: %v", name, wantSchemas)
	}
	if err := createWithRetry(sub, apiExportGVR, export, 60*time.Second); err != nil {
		return err
	}

	bindRole := "railgrid:providers:bind:" + exportName
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": bindRole},
		"rules": []any{map[string]any{
			"apiGroups":     []any{"apis.kcp.io"},
			"resources":     []any{"apiexports"},
			"verbs":         []any{"bind"},
			"resourceNames": []any{exportName},
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
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"displayName": name + " (agents e2e stand-in)",
			"description": "API-only stand-in so the agents provider's declared dependency can be enabled.",
			"vendor":      "railgrid",
			"version":     "0.0.1",
			// No serving section: an APIExport alone is a valid provider
			// shape, and this stand-in runs no process.
			"export": map[string]any{"name": exportName},
		},
	}}
	return createWithRetry(sys, catalogEntryGVR, entry, 90*time.Second)
}

// mintRuntimeKubeconfig waits for the Provider controller to populate the
// provider-token Secret in the sub-workspace and writes a workspace-scoped
// kubeconfig around it — the same credential the provider pod mounts.
func mintRuntimeKubeconfig(path string, timeout time.Duration) error {
	token, err := providerToken(workspacePath, timeout)
	if err != nil {
		return err
	}
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

// providerToken waits for the Provider controller to populate the
// provider-token Secret in a provider workspace and returns the bearer of that
// workspace's default/provider ServiceAccount.
func providerToken(path string, timeout time.Duration) (string, error) {
	cl, err := kcpDynamicRaw(path, adminToken)
	if err != nil {
		return "", err
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
					return "", fmt.Errorf("decode provider-token: %w", err)
				}
				token = string(raw)
				break
			}
			lastErr = "provider-token Secret exists but token not yet populated"
		}
		time.Sleep(2 * time.Second)
	}
	if token == "" {
		return "", fmt.Errorf("provider-token in %s never populated: %s", path, lastErr)
	}
	return token, nil
}

// createWithRetry creates obj, tolerating AlreadyExists and retrying while the
// API is not servable yet. The hub reports /readyz before every bootstrapped
// API answers, and provider workspaces appear asynchronously.
func createWithRetry(cl dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := cl.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		cancel()
		if err == nil || apierrors.IsAlreadyExists(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("create %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		time.Sleep(2 * time.Second)
	}
}

func kcpDynamicRaw(clusterPath, token string) (dynamic.Interface, error) {
	cfg := &rest.Config{
		Host:        kcpServer + "/clusters/" + clusterPath,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // dev cert is self-signed
		},
	}
	return dynamic.NewForConfig(cfg)
}

func kcpDynamic(t *testing.T, clusterPath, token string) dynamic.Interface {
	t.Helper()
	c, err := kcpDynamicRaw(clusterPath, token)
	if err != nil {
		t.Fatalf("dynamic client for %s: %v", clusterPath, err)
	}
	return c
}

// build runs `make build-hub build-agents-provider` so the test runs against
// current source even when the user hasn't built manually.
func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-agents-provider")
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
	// Negative PID signals the process group.
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

// extractToken pulls the first `token:` value out of the kcp admin kubeconfig.
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

// tailFile returns the last n lines of a log, so a failure reports what went
// wrong. It must be read BEFORE the data directory is removed; CI does not
// upload that directory, so stderr is the only place the reason survives.
func tailFile(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// waitForCondition polls fn until it reports true or the timeout expires,
// logging the last reason on failure.
func waitForCondition(t *testing.T, timeout time.Duration, fn func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, reason := fn()
		if ok {
			return true
		}
		last = reason
		time.Sleep(time.Second)
	}
	t.Logf("condition never met: %s", last)
	return false
}
