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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/faroshq/faros/pkg/util/identity"
)

// cliSession is one logged-in user: an isolated kubeconfig the faros binary
// is driven against, so tests and users never stomp on each other.
type cliSession struct {
	t          *testing.T
	kubeconfig string
	token      string
}

// login logs the token in against the suite hub into a fresh kubeconfig.
func login(t *testing.T, name, token string) *cliSession {
	t.Helper()
	dir := suiteTempDir(t, name)
	s := &cliSession{t: t, kubeconfig: filepath.Join(dir, "faros.kubeconfig"), token: token}
	out := s.run("login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", token)
	if !strings.Contains(out, "Logged in as") {
		t.Fatalf("login output:\n%s", out)
	}
	return s
}

// run executes `faros <args>` and fails the test on a non-zero exit.
func (s *cliSession) run(args ...string) string {
	s.t.Helper()
	out, err := s.try(args...)
	if err != nil {
		s.t.Fatalf("faros %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// try executes `faros <args>` and returns the combined output and error.
func (s *cliSession) try(args ...string) (string, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, farosBin, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+s.kubeconfig, "HOME="+filepath.Dir(s.kubeconfig))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustFail runs the command and requires a failure whose output mentions want.
func (s *cliSession) mustFail(want string, args ...string) string {
	s.t.Helper()
	out, err := s.try(args...)
	if err == nil {
		s.t.Fatalf("faros %s succeeded, expected an error mentioning %q:\n%s", strings.Join(args, " "), want, out)
	}
	if !strings.Contains(out, want) {
		s.t.Fatalf("faros %s: output lacks %q:\n%s", strings.Join(args, " "), want, out)
	}
	return out
}

// runJSON runs the command with -o json and decodes stdout into v.
func (s *cliSession) runJSON(v any, args ...string) {
	s.t.Helper()
	out := s.run(append(args, "-o", "json")...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		s.t.Fatalf("faros %s -o json: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// kubectl runs kubectl against the session kubeconfig.
func (s *cliSession) kubectl(args ...string) (string, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", s.kubeconfig, "--insecure-skip-tls-verify"}, args...)...)
	// PATH must resolve the faros exec plugin for OIDC kubeconfigs; the
	// static-token kubeconfigs here do not need it, but keep parity.
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(farosBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// whoami is the -o json shape of `faros whoami` (mirrors pkg/cli/cmd).
type whoami struct {
	Hub     string `json:"hub"`
	Context string `json:"context"`
	User    string `json:"user"`
	Auth    string `json:"auth"`
	Org     *struct {
		UUID        string `json:"uuid"`
		DisplayName string `json:"displayName"`
		Personal    bool   `json:"personal"`
		Role        string `json:"role"`
	} `json:"org"`
	Workspace *struct {
		UUID        string `json:"uuid"`
		DisplayName string `json:"displayName"`
		ClusterName string `json:"clusterName"`
		Role        string `json:"role"`
	} `json:"workspace"`
	Cluster        string `json:"cluster"`
	KubectlContext string `json:"kubectlContext"`
	KubectlTarget  string `json:"kubectlTarget"`
	Orgs           []struct {
		UUID        string `json:"uuid"`
		DisplayName string `json:"displayName"`
		Personal    bool   `json:"personal"`
		Role        string `json:"role"`
	} `json:"orgs"`
}

type orgRow struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"displayName"`
	Personal    bool   `json:"personal"`
	Role        string `json:"role"`
}

type workspaceRow struct {
	UUID        string `json:"uuid"`
	OrgUUID     string `json:"orgUUID"`
	DisplayName string `json:"displayName"`
	ClusterName string `json:"clusterName"`
	Role        string `json:"role"`
}

type memberRow struct {
	User            string `json:"user"`
	Email           string `json:"email"`
	UserDisplayName string `json:"userDisplayName"`
	Role            string `json:"role"`
}

func (s *cliSession) whoami() whoami {
	s.t.Helper()
	var v whoami
	s.runJSON(&v, "whoami")
	return v
}

// clusterFromKubeconfig extracts the logical cluster name from the server URL
// (https://.../clusters/<cluster>) of the faros login context.
func clusterFromKubeconfig(t *testing.T, kubeconfig string) string {
	t.Helper()
	b, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "/clusters/"); i >= 0 {
			rest := line[i+len("/clusters/"):]
			for j, r := range rest {
				if r == ' ' || r == '\n' || r == '/' || r == '"' {
					return strings.TrimSpace(rest[:j])
				}
			}
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no /clusters/ in kubeconfig:\n%s", string(b))
	return ""
}

func waitFor(t *testing.T, timeout time.Duration, cond func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, msg := cond()
		if ok {
			return true
		}
		last = msg
		time.Sleep(2 * time.Second)
	}
	t.Logf("wait timeout after %s; last: %s", timeout, last)
	return false
}

// --- edges provider enablement (ported from suites/edgesconn) ---

// enableEdges binds the edges APIExport into the tenant workspace with the
// claim set the hub's enable flow accepts, and waits for Bound.
func enableEdges(t *testing.T, tenant dynamic.Interface) {
	t.Helper()
	claimVerbs := func(group, resource string, verbs ...string) map[string]any {
		vs := make([]any, 0, len(verbs))
		for _, v := range verbs {
			vs = append(vs, v)
		}
		return map[string]any{
			"group": group, "resource": resource,
			"verbs":    vs,
			"selector": map[string]any{"matchAll": true},
			"state":    "Accepted",
		}
	}
	claim := func(group, resource string) map[string]any {
		return claimVerbs(group, resource, "get", "list", "watch", "create", "update", "patch", "delete")
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "edges"},
		"spec": map[string]any{
			"reference": map[string]any{"export": map[string]any{"path": edgesWorkspacePath, "name": edgesAPIExportName}},
			"permissionClaims": []any{
				claim("", "namespaces"), claim("", "serviceaccounts"), claim("", "secrets"),
				claim("rbac.authorization.k8s.io", "clusterroles"), claim("rbac.authorization.k8s.io", "clusterrolebindings"),
				claimVerbs("authentication.k8s.io", "tokenreviews", "create"),
				claimVerbs("authorization.k8s.io", "subjectaccessreviews", "create"),
			},
		},
	}}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create APIBinding: %v", err)
	}
	if !waitFor(t, 30*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "edges", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatal("edges APIBinding never reached Bound")
	}
}

// grantEdgeProxy creates the edge-proxy grant the hub REST enable path would
// create, so the provider can read edge CRs and run delegated reviews.
func grantEdgeProxy(t *testing.T, tenant dynamic.Interface) {
	t.Helper()
	providersWS := kcpDynamic(t, "root:faros:providers", adminToken)
	ws, err := providersWS.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), "edges", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get provider workspace: %v", err)
	}
	providerCluster, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
	if providerCluster == "" {
		t.Fatal("provider workspace has no spec.cluster")
	}
	qualified := identity.QualifiedServiceAccount(providerCluster, "default", "provider")

	name := "faros:provider:edges:edgeproxy"
	rules := []any{
		map[string]any{"nonResourceURLs": []any{"/"}, "verbs": []any{"access"}},
		map[string]any{"apiGroups": []any{"edges.faros.sh"}, "resources": []any{"kubernetesclusters", "linuxservers"}, "verbs": []any{"get", "list", "watch", "proxy"}},
		map[string]any{"apiGroups": []any{"edges.faros.sh"}, "resources": []any{"kubernetesclusters/status", "linuxservers/status"}, "verbs": []any{"get", "update", "patch"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get", "list", "watch", "create", "update"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"namespaces"}, "verbs": []any{"get", "create"}},
		map[string]any{"apiGroups": []any{"authentication.k8s.io"}, "resources": []any{"tokenreviews"}, "verbs": []any{"create"}},
		map[string]any{"apiGroups": []any{"authorization.k8s.io"}, "resources": []any{"subjectaccessreviews"}, "verbs": []any{"create"}},
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": name}, "rules": rules,
	}}
	if _, err := tenant.Resource(clusterRoleGVR).Create(ctxWithTimeout(t, 10*time.Second), role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ClusterRole: %v", err)
	}
	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": name},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": name},
		"subjects": []any{
			map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": qualified},
			map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "system:serviceaccount:default:provider"},
		},
	}}
	if _, err := tenant.Resource(clusterRoleBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ClusterRoleBinding: %v", err)
	}
}

// prepareEdgesWorkspace enables the edges provider in the session's
// workspace and returns the tenant cluster and an admin dynamic client.
func prepareEdgesWorkspace(t *testing.T, s *cliSession) (string, dynamic.Interface) {
	t.Helper()
	tenantWS := clusterFromKubeconfig(t, s.kubeconfig)
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)
	return tenantWS, tenantAdmin
}

// joinTokenFromOutput extracts --token from the `faros agent run` block of
// the join guide printed by `edge create` / `edge join-command`.
func joinTokenFromOutput(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\\"))
		if rest, ok := strings.CutPrefix(line, "--token "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no --token line in join output:\n%s", out)
	return ""
}

func startAgent(t *testing.T, edgeName, joinToken, tenantWS string, extra ...string) *exec.Cmd {
	t.Helper()
	logDir := suiteTempDir(t, "agent-"+edgeName)
	logf, _ := os.Create(filepath.Join(logDir, "agent.log"))
	args := append([]string{
		"agent", "run",
		"--hub-url", hubURL,
		"--hub-insecure-skip-tls-verify",
		"--token", joinToken,
		"--tunnel-url", hubURL,
		"--edge-name", edgeName,
		"--cluster", tenantWS,
	}, extra...)
	cmd := exec.Command(farosBin, args...)
	cmd.Env = append(os.Environ(), "HOME="+logDir)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	t.Logf("agent started (pid=%d, log=%s)", cmd.Process.Pid, logf.Name())
	t.Cleanup(func() { killGroup(cmd) })
	return cmd
}

func waitForConnected(t *testing.T, tenant dynamic.Interface, gvr schema.GroupVersionResource, edgeName string) {
	t.Helper()
	if !waitFor(t, 3*time.Minute, func() (bool, string) {
		got, err := tenant.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), edgeName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		conn, _, _ := unstructured.NestedBool(got.Object, "status", "connected")
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return conn, fmt.Sprintf("connected=%v phase=%s", conn, phase)
	}) {
		t.Fatal("edge never became connected")
	}
}

func createKindCluster(t *testing.T, name, kubeconfig string) {
	t.Helper()
	t.Logf("creating kind cluster %q (this takes ~30-60s)", name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", name, "--kubeconfig", kubeconfig, "--wait", "60s")
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "already exist") {
			exp := exec.Command("kind", "export", "kubeconfig", "--name", name, "--kubeconfig", kubeconfig)
			if o2, e2 := exp.CombinedOutput(); e2 != nil {
				t.Fatalf("kind export kubeconfig: %v\n%s", e2, o2)
			}
		} else {
			t.Fatalf("kind create cluster: %v\n%s", err, out)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("kind", "delete", "cluster", "--name", name).Run()
	})
}
