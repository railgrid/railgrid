// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package provideractions runs the local host-process provider-actions E2E.
// It deliberately keeps source/configuration, process readiness, invocation,
// and result evidence as separate records so a 200 or a Ready condition is
// never mistaken for a verified interaction.
package provideractions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

	fixture "github.com/faroshq/faros-kedge/test/e2e/provideractions"
)

var (
	repoRoot       string
	hubURL         string
	kcpServer      string
	adminToken     string
	staticToken    = "test:user-default"
	bootstrapToken = "provider-actions-e2e-bootstrap"
	dataDir        string
	fakeDB         *fixture.FakeDatabricks
	fakeAttestor   *fixture.FakeInfrastructureAttestor
	liveOnly       bool

	appStudioPort   = "18085"
	databricksPort  = "18086"
	graphqlGRPCPort = "25063"
)

const (
	hubPort = "19463"
	kcpPort = "16463"

	appStudioWorkspace  = "root:kedge:providers:app-studio"
	databricksWorkspace = "root:kedge:providers:databricks"

	appStudioExport  = "ai.kedge.faros.sh"
	databricksExport = "databricks.providers.kedge.faros.sh"
)

var (
	secretGVR       = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	apiBindingGVR   = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	catalogEntryGVR = schema.GroupVersionResource{Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries"}
	projectGVR      = schema.GroupVersionResource{Group: "ai.kedge.faros.sh", Version: "v1alpha1", Resource: "projects"}
	connectionGVR   = schema.GroupVersionResource{Group: "databricks.kedge.faros.sh", Version: "v1alpha1", Resource: "connections"}
	warehouseGVR    = schema.GroupVersionResource{Group: "databricks.kedge.faros.sh", Version: "v1alpha1", Resource: "warehouses"}
	tableGVR        = schema.GroupVersionResource{Group: "databricks.kedge.faros.sh", Version: "v1alpha1", Resource: "tables"}
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
	if strings.EqualFold(strings.TrimSpace(os.Getenv("KEDGE_E2E_PROVIDER_ACTIONS_LIVE_ONLY")), "true") {
		liveOnly = true
		os.Exit(m.Run())
	}

	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort
	for _, port := range []string{hubPort, kcpPort, appStudioPort, databricksPort, graphqlGRPCPort, "2380"} {
		if portInUse(port) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; stop the provider-actions E2E processes and retry\n", port)
			os.Exit(2)
		}
	}
	// The default lane builds from source. Local iteration may opt out when
	// the Make target has already produced the three process binaries.
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("KEDGE_E2E_PROVIDER_ACTIONS_SKIP_BUILD")), "true") {
		if err := build(repoRoot); err != nil {
			fmt.Fprintln(os.Stderr, "build failed:", err)
			os.Exit(1)
		}
	}

	var err error
	dataDir, err = os.MkdirTemp("", "kedge-e2e-provider-actions-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	keepData := strings.EqualFold(strings.TrimSpace(os.Getenv("KEDGE_E2E_KEEP_DATA")), "true")
	fakeDB = fixture.NewFakeDatabricks()
	fakeAttestor = fixture.NewFakeInfrastructureAttestor(bootstrapToken)

	hubLog, err := os.Create(filepath.Join(dataDir, "hub.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "hub log:", err)
		os.Exit(1)
	}
	hubBinary := strings.TrimSpace(os.Getenv("KEDGE_E2E_HUB_BINARY"))
	if hubBinary == "" {
		hubBinary = filepath.Join(repoRoot, "bin", "kedge-hub")
	}
	hubCmd := exec.Command(hubBinary,
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-secure-port", kcpPort,
		"--embedded-graphql",
		"--graphql-apiexport-slice-name", "core.faros.sh",
		"--graphql-apiexport-logical-cluster", "root:kedge:system:controllers",
		"--graphql-grpc-addr", "127.0.0.1:"+graphqlGRPCPort,
		"--listen-addr", ":"+hubPort,
		"--data-dir", dataDir,
		"--static-auth-token", staticToken,
	)
	hubCmd.Stdout = hubLog
	hubCmd.Stderr = hubLog
	hubCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := hubCmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start hub:", err)
		fakeDB.Close()
		fakeAttestor.Close()
		os.Exit(1)
	}

	var providerCmds []*exec.Cmd
	cleanup := func() {
		for _, cmd := range providerCmds {
			killGroup(cmd)
		}
		killGroup(hubCmd)
		fakeDB.Close()
		fakeAttestor.Close()
		if keepData {
			fmt.Fprintf(os.Stderr, "provider-actions evidence/logs preserved under %s\n", dataDir)
		} else {
			_ = os.RemoveAll(dataDir)
		}
	}

	if err := waitReady(hubURL+"/readyz", 3*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "hub never ready:", err)
		os.Exit(1)
	}
	adminToken, err = extractToken(filepath.Join(dataDir, "kcp", "admin.kubeconfig"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "extract admin token:", err)
		os.Exit(1)
	}

	writeEvidence("source-config.json", map[string]any{
		"providers":        []string{"app-studio", "databricks", "infrastructure"},
		"storage":          map[string]any{"appStudioMessageStore": "in-memory"},
		"fakeUpstream":     map[string]any{"scheme": "https", "url": fakeDB.URL(), "certificate": "self-signed"},
		"workloadAttestor": map[string]any{"provider": "infrastructure", "url": fakeAttestor.URL(), "bootstrapToken": "deterministic-fixture-only"},
		"appInputContract": map[string]any{"base": "hub/services/providers/app-studio", "credentials": "short-lived workload token from exchange file", "tokenFile": "KEDGE_ACTIONS_TOKEN_FILE", "providerURL": false, "pat": false, "mcp": false},
	})

	if err := applyProviderManifests(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "apply provider manifests:", err)
		os.Exit(1)
	}
	for _, workspace := range []string{appStudioWorkspace, databricksWorkspace} {
		if err := waitWorkspace(workspace, 2*time.Minute); err != nil {
			cleanup()
			fmt.Fprintln(os.Stderr, "provider workspace never ready:", err)
			os.Exit(1)
		}
	}

	appKubeconfig := filepath.Join(dataDir, "app-studio-runtime.kubeconfig")
	dbKubeconfig := filepath.Join(dataDir, "databricks-runtime.kubeconfig")
	if err := mintRuntimeKubeconfig(appStudioWorkspace, appKubeconfig, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "mint app-studio kubeconfig:", err)
		os.Exit(1)
	}
	if err := mintRuntimeKubeconfig(databricksWorkspace, dbKubeconfig, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "mint databricks kubeconfig:", err)
		os.Exit(1)
	}
	if err := initProvider("app-studio", appKubeconfig, appStudioWorkspace, filepath.Join(repoRoot, "providers", "app-studio", "deploy", "chart", "files", "schemas")); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "app-studio init:", err)
		os.Exit(1)
	}
	if err := initProvider("databricks", dbKubeconfig, databricksWorkspace, filepath.Join(repoRoot, "providers", "databricks", "deploy", "chart", "files", "schemas")); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "databricks init:", err)
		os.Exit(1)
	}

	appCmd, err := startProvider("app-studio", appStudioPort, appKubeconfig, map[string]string{
		"APP_STUDIO_IN_MEMORY_MESSAGE_STORE":          "true",
		"APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY": "true",
		"APP_STUDIO_PREVIEW_CONSOLE_ENABLED":          "false",
		// The production action-enabled runtime contract requires an HTTPS
		// external origin. This fixture is only persisted in Project values;
		// the generated app still enters through the local hub URL below.
		"KEDGE_ACTIONS_EXTERNAL_URL": "https://actions.invalid",
	})
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "start app-studio:", err)
		os.Exit(1)
	}
	providerCmds = append(providerCmds, appCmd)
	dbCmd, err := startProvider("databricks", databricksPort, dbKubeconfig, map[string]string{
		"DATABRICKS_E2E_LOOPBACK": "true",
		"DATABRICKS_MCP_ENABLED":  "false",
	})
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "start databricks:", err)
		os.Exit(1)
	}
	providerCmds = append(providerCmds, dbCmd)

	if err := waitReady("http://127.0.0.1:"+appStudioPort+"/healthz", 45*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "app-studio never ready:", err)
		os.Exit(1)
	}
	if err := waitReady("http://127.0.0.1:"+databricksPort+"/healthz", 45*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "databricks never ready:", err)
		os.Exit(1)
	}
	if err := waitCatalogReady([]string{"app-studio", "databricks", "infrastructure"}, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "catalog entries never ready:", err)
		os.Exit(1)
	}
	writeEvidence("runtime-readiness.json", map[string]any{
		"hubReady":            true,
		"providers":           map[string]any{"app-studio": map[string]any{"healthz": true, "catalogReady": true}, "databricks": map[string]any{"healthz": true, "catalogReady": true, "mcpEnabled": false}, "infrastructure": map[string]any{"catalogReady": true, "fakeAttestor": true}},
		"interactionVerified": false,
	})

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-app-studio-provider", "build-databricks-provider")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func applyProviderManifests() error {
	client, err := kcpDynamicRaw("root:kedge:system:providers", adminToken)
	if err != nil {
		return err
	}
	for _, provider := range []struct {
		name string
		port string
	}{
		{name: "app-studio", port: appStudioPort},
		{name: "databricks", port: databricksPort},
		{name: "infrastructure"},
	} {
		for _, filename := range []string{"provider.yaml", "manifest.yaml"} {
			raw, err := os.ReadFile(filepath.Join(repoRoot, "providers", provider.name, filename))
			if err != nil {
				return fmt.Errorf("read %s/%s: %w", provider.name, filename, err)
			}
			for _, doc := range bytes.Split(raw, []byte("\n---")) {
				if !bytes.Contains(doc, []byte("apiVersion:")) {
					continue
				}
				obj := &unstructured.Unstructured{}
				if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
					return fmt.Errorf("parse %s: %w", filename, err)
				}
				if obj.GetKind() == "" {
					continue
				}
				gvr, ok := map[string]schema.GroupVersionResource{
					"Provider":     {Group: "admin.kedge.faros.sh", Version: "v1alpha1", Resource: "providers"},
					"CatalogEntry": catalogEntryGVR,
				}[obj.GetKind()]
				if !ok {
					return fmt.Errorf("%s: unexpected kind %q", filename, obj.GetKind())
				}
				if obj.GetKind() == "CatalogEntry" {
					endpoint := "http://127.0.0.1:" + provider.port
					if provider.name == "infrastructure" {
						// The E2E only needs Infrastructure's attestation
						// endpoint. It lives on the backend origin under the
						// hub-only /workload-identities prefix; the backend
						// proxy refuses that prefix so only the hub's attestor
						// can reach it. Keep the catalog record deterministic
						// without starting the full provider process.
						endpoint = fakeAttestor.URL()
						_ = unstructured.SetNestedField(obj.Object, endpoint, "spec", "backend", "url")
					} else {
						_ = unstructured.SetNestedField(obj.Object, endpoint, "spec", "ui", "url")
						// Provider actions are data-plane verbs on the backend
						// origin, reached through the hub backend proxy at
						// /services/providers/{name}/actions/clusters/....
						_ = unstructured.SetNestedField(obj.Object, endpoint, "spec", "backend", "url")
					}
					// The local action lane does not need the optional Code or
					// Infrastructure providers; removing this catalog-only gate
					// keeps the suite bounded to the two processes under test.
					if provider.name == "app-studio" {
						unstructured.RemoveNestedField(obj.Object, "spec", "dependencies")
					}
				}
				deadline := time.Now().Add(90 * time.Second)
				for {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					_, createErr := client.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
					cancel()
					if createErr == nil || apierrors.IsAlreadyExists(createErr) {
						break
					}
					if time.Now().After(deadline) {
						return fmt.Errorf("create %s %s: %w", obj.GetKind(), obj.GetName(), createErr)
					}
					time.Sleep(2 * time.Second)
				}
			}
		}
	}
	return nil
}

func initProvider(name, kubeconfig, workspace, schemas string) error {
	binary := filepath.Join(repoRoot, "bin", name+"-provider")
	cmd := exec.Command(binary, "init")
	cmd.Env = append(os.Environ(),
		"KEDGE_PROVIDER_KUBECONFIG="+kubeconfig,
		"KEDGE_SCHEMAS_DIR="+schemas,
	)
	if name == "app-studio" {
		cmd.Env = append(cmd.Env, "APP_STUDIO_WORKSPACE_PATH="+workspace)
	} else {
		cmd.Env = append(cmd.Env, "DATABRICKS_WORKSPACE_PATH="+workspace)
	}
	logFile, err := os.Create(filepath.Join(dataDir, name+"-init.log"))
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s init: %w (log %s)", name, err, logFile.Name())
	}
	return nil
}

func startProvider(name, port, kubeconfig string, extra map[string]string) (*exec.Cmd, error) {
	cmd := exec.Command(filepath.Join(repoRoot, "bin", name+"-provider"))
	env := append(os.Environ(),
		"PORT="+port,
		"KEDGE_HUB_URL="+hubURL,
		"KEDGE_HUB_TOKEN="+staticToken,
		"KEDGE_HUB_INSECURE=true",
		"KEDGE_PROVIDER_NAME="+name,
		"KEDGE_PROVIDER_KUBECONFIG="+kubeconfig,
	)
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	cmd.Env = env
	logFile, err := os.Create(filepath.Join(dataDir, name+".log"))
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	return cmd, nil
}

func waitCatalogReady(names []string, timeout time.Duration) error {
	client, err := kcpDynamicRaw("root:kedge:system:providers", adminToken)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allReady := true
		for _, name := range names {
			obj, getErr := client.Resource(catalogEntryGVR).Get(context.Background(), name, metav1.GetOptions{})
			if getErr != nil {
				allReady = false
				continue
			}
			ready := false
			conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			for _, raw := range conditions {
				condition, _ := raw.(map[string]any)
				if condition["type"] == "Ready" && condition["status"] == "True" {
					ready = true
				}
			}
			if !ready {
				allReady = false
			}
		}
		if allReady {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("catalog entries %v did not reach Ready", names)
}

func waitWorkspace(workspace string, timeout time.Duration) error {
	client, err := kcpDynamicRaw(workspace, adminToken)
	if err != nil {
		return err
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, lastErr = client.Resource(nsGVR).List(ctx, metav1.ListOptions{Limit: 1})
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("workspace %s unavailable: %v", workspace, lastErr)
}

func mintRuntimeKubeconfig(workspace, path string, timeout time.Duration) error {
	client, err := kcpDynamicRaw(workspace, adminToken)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var token string
	var lastErr string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		secret, getErr := client.Resource(secretGVR).Namespace("default").Get(ctx, "provider-token", metav1.GetOptions{})
		cancel()
		if getErr != nil {
			lastErr = getErr.Error()
		} else if encoded, _, _ := unstructured.NestedString(secret.Object, "data", "token"); encoded != "" {
			raw, decodeErr := base64.StdEncoding.DecodeString(encoded)
			if decodeErr != nil {
				return decodeErr
			}
			token = string(raw)
			break
		} else {
			lastErr = "provider-token Secret exists but token is empty"
		}
		time.Sleep(2 * time.Second)
	}
	if token == "" {
		return fmt.Errorf("provider-token never populated: %s", lastErr)
	}
	contents := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: kedge
  cluster:
    server: %s/clusters/%s
    insecure-skip-tls-verify: true
contexts:
- name: kedge
  context:
    cluster: kedge
    user: kedge
current-context: kedge
users:
- name: kedge
  user:
    token: %s
`, kcpServer, workspace, token)
	return os.WriteFile(path, []byte(contents), 0o600)
}

func kcpDynamicRaw(clusterPath, token string) (dynamic.Interface, error) {
	return dynamic.NewForConfig(&rest.Config{
		Host:            kcpServer + "/clusters/" + clusterPath,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	})
}

func kcpDynamic(t *testing.T, clusterPath, token string) dynamic.Interface {
	t.Helper()
	client, err := kcpDynamicRaw(clusterPath, token)
	if err != nil {
		t.Fatalf("dynamic client for %s: %v", clusterPath, err)
	}
	return client
}

func portInUse(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
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
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "token:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "token:")), nil
		}
	}
	return "", fmt.Errorf("no token in %s", path)
}

func ctxWithTimeout(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}

func writeEvidence(name string, value any) {
	if dataDir == "" {
		return
	}
	dir := filepath.Join(dataDir, "evidence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o600)
}

func requireLocalSuite(t *testing.T) {
	t.Helper()
	if liveOnly {
		t.Skip("local provider-actions harness disabled in live-only mode")
	}
}
