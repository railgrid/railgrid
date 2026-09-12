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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/faroshq/faros/test/e2e/framework"
)

// TestSessionCommands covers the commands every user runs first: login,
// whoami, org/workspace listing, use, token, env, version, help, completion,
// docs and logout — against the live hub, as one static-token user.
func TestSessionCommands(t *testing.T) {
	s := login(t, "session", tokenA)

	// whoami resolves the personal org and default workspace with roles.
	me := s.whoami()
	if me.Hub != hubURL || me.Context != "faros" || me.Auth != "static-token" {
		t.Fatalf("whoami = %+v", me)
	}
	if me.Org == nil || !me.Org.Personal || me.Org.Role != "admin" {
		t.Fatalf("whoami org = %+v", me.Org)
	}
	if me.Workspace == nil || me.Workspace.ClusterName == "" || me.Workspace.UUID == "" {
		t.Fatalf("whoami workspace = %+v", me.Workspace)
	}
	if me.KubectlContext != "faros" || me.KubectlTarget != "hub workspace" {
		t.Fatalf("whoami kubectl = %q → %q", me.KubectlContext, me.KubectlTarget)
	}
	text := s.run("whoami")
	for _, want := range []string{"Hub:        " + hubURL, "Auth:       static-token", "role: admin", `context "faros" → hub workspace`} {
		if !strings.Contains(text, want) {
			t.Fatalf("whoami text lacks %q:\n%s", want, text)
		}
	}

	// org list / workspace list agree with whoami and mark the current one.
	var orgs []orgRow
	s.runJSON(&orgs, "org", "list")
	if len(orgs) != 1 || orgs[0].UUID != me.Org.UUID || orgs[0].Role != "admin" {
		t.Fatalf("org list = %+v", orgs)
	}
	if table := s.run("org", "list"); !strings.Contains(table, "*") || !strings.Contains(table, "personal") {
		t.Fatalf("org list table:\n%s", table)
	}
	var workspaces []workspaceRow
	s.runJSON(&workspaces, "workspace", "list")
	if len(workspaces) != 1 || workspaces[0].UUID != me.Workspace.UUID || workspaces[0].ClusterName != me.Cluster {
		t.Fatalf("workspace list = %+v (whoami cluster %s)", workspaces, me.Cluster)
	}
	if table := s.run("workspace", "list"); !strings.Contains(table, "*") {
		t.Fatalf("workspace list table lacks the current marker:\n%s", table)
	}
	if names := s.run("workspace", "list", "-o", "name"); strings.TrimSpace(names) == "" {
		t.Fatalf("workspace list -o name = %q", names)
	}

	// use: non-interactive by UUID and by display name; interactive needs a TTY.
	out := s.run("use", "--org", me.Org.UUID, "--workspace", me.Workspace.UUID)
	if !strings.Contains(out, "Already using") {
		t.Fatalf("use (same workspace): %s", out)
	}
	wsLabel := workspaces[0].DisplayName
	if wsLabel == "" {
		wsLabel = workspaces[0].UUID
	}
	s.run("use", "--org", me.Org.DisplayName, "--workspace", wsLabel)
	s.mustFail("no interactive terminal", "use")
	s.mustFail(`no organization matches "nope"`, "use", "--org", "nope", "--workspace", "x")

	// token prints the bearer the kubeconfig carries.
	if tok := strings.TrimSpace(s.run("token")); tok != tokenA {
		t.Fatalf("token = %q", tok)
	}

	// env: shell exports for scripts and agents.
	env := s.run("env", "--no-mcp")
	for _, want := range []string{"export HUB='" + hubURL + "'", "export CLUSTER='" + me.Cluster + "'", "export ORG='" + me.Org.UUID + "'", "export WS='" + me.Workspace.UUID + "'", "export TOKEN='" + tokenA + "'"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %q:\n%s", want, env)
		}
	}

	// mcp url: prints the workspace's aggregate endpoint (token readiness is
	// the hub's business; the command must not fail).
	if mcp := s.run("mcp", "url", "--mcpserver-name", "default"); !strings.Contains(mcp, "/services/mcpserver/") {
		t.Fatalf("mcp url:\n%s", mcp)
	}

	// version / help / completion / docs.
	if v := s.run("version"); !strings.Contains(v, "faros version") {
		t.Fatalf("version: %s", v)
	}
	help := s.run("--help")
	for _, want := range []string{"Getting started:", "Edges (clusters and servers):", "Organizations and access:", "Developer workflow:", "  connect ", "  whoami ", "  org "} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "  get ") || strings.Contains(help, "  apply ") || strings.Contains(help, "get-token") {
		t.Fatalf("help shows hidden commands:\n%s", help)
	}
	if comp := s.run("completion", "bash"); !strings.Contains(comp, "faros") {
		t.Fatalf("completion bash: %s", comp)
	}
	docsDir := suiteTempDir(t, "docs")
	s.run("docs", "--dir", docsDir)
	if _, err := os.Stat(filepath.Join(docsDir, "faros_org_members_add.md")); err != nil {
		t.Fatalf("docs not generated: %v", err)
	}

	// Without the edges provider the edge commands say so, including the
	// hidden legacy spellings.
	s.mustFail("edges provider is not enabled", "edge", "list")
	s.mustFail("edges provider is not enabled", "ls")

	// logout forgets the context; commands then ask for a login.
	if out := s.run("logout"); !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %s", out)
	}
	s.mustFail("run 'faros login'", "whoami")
	s.mustFail("faros login", "edge", "list")
	if out := s.run("logout"); !strings.Contains(out, "Not logged in") {
		t.Fatalf("second logout: %s", out)
	}
	// A --hub-url-less login points at the env var and errors clearly.
	s.mustFail("no hub configured", "login", "--token", tokenA)
	// And logging in again restores everything.
	s.run("login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", tokenA)
	if again := s.whoami(); again.Org == nil || again.Org.UUID != me.Org.UUID {
		t.Fatalf("whoami after re-login = %+v", again)
	}
}

// TestMembershipCommands drives the RBAC surface end to end with two real
// users: A creates a team organization and workspace, adds B, changes B's
// role, B sees and switches into the shared workspace, and A removes B again.
func TestMembershipCommands(t *testing.T) {
	a := login(t, "members-a", tokenA)
	b := login(t, "members-b", tokenB)

	// Identify B the way an admin would: by the user id B's own member list
	// shows (static-token users have no email).
	var bSelf []memberRow
	b.runJSON(&bSelf, "org", "members")
	if len(bSelf) != 1 || bSelf[0].Role != "admin" {
		t.Fatalf("B's personal org members = %+v", bSelf)
	}
	bUser := bSelf[0].User
	bLabel := bSelf[0].Email
	if bLabel == "" {
		bLabel = bSelf[0].UserDisplayName
	}
	if bLabel == "" {
		bLabel = bUser
	}

	// A creates a team org and a workspace in it.
	out := a.run("org", "create", "CLI Team", "--workspace-creation", "members")
	if !strings.Contains(out, `Organization "CLI Team" created`) {
		t.Fatalf("org create: %s", out)
	}
	var orgs []orgRow
	a.runJSON(&orgs, "org", "list")
	var team orgRow
	for _, o := range orgs {
		if o.DisplayName == "CLI Team" {
			team = o
		}
	}
	if team.UUID == "" || team.Role != "admin" || team.Personal {
		t.Fatalf("org list after create = %+v", orgs)
	}
	t.Cleanup(func() {
		_, _, _ = framework.DoRESTRequest(context.Background(), "DELETE", hubURL+"/api/orgs/"+team.UUID, tokenA, map[string]string{"X-Faros-Org": team.UUID}, nil)
	})

	out = a.run("workspace", "create", "Platform", "--org", team.UUID)
	if !strings.Contains(out, `Workspace "Platform" created`) {
		t.Fatalf("workspace create: %s", out)
	}
	var platform workspaceRow
	if !waitFor(t, 2*time.Minute, func() (bool, string) {
		var wss []workspaceRow
		a.runJSON(&wss, "workspace", "list", "--org", "CLI Team")
		for _, ws := range wss {
			if ws.DisplayName == "Platform" && ws.ClusterName != "" {
				platform = ws
				return true, ""
			}
		}
		return false, "workspace not ready"
	}) {
		t.Fatal("workspace Platform never got a cluster")
	}
	if platform.Role != "admin" {
		t.Logf("note: creator's workspace role is %q, not admin: %+v", platform.Role, platform)
	}

	// Org members: A alone, then B as member, promoted, then removed.
	var members []memberRow
	a.runJSON(&members, "org", "members", "--org", team.UUID)
	if len(members) != 1 || members[0].Role != "admin" {
		t.Fatalf("initial org members = %+v", members)
	}
	a.mustFail("invalid role", "org", "members", "add", bUser, "--org", team.UUID, "--role", "owner")
	a.mustFail("404", "org", "members", "add", "nobody-here", "--org", team.UUID)
	out = a.run("org", "members", "add", bUser, "--org", team.UUID, "--role", "member")
	if !strings.Contains(out, `to organization "CLI Team" as member`) {
		t.Fatalf("org members add: %s", out)
	}
	a.runJSON(&members, "org", "members", "--org", team.UUID)
	if len(members) != 2 {
		t.Fatalf("org members after add = %+v", members)
	}
	table := a.run("org", "members", "list", "--org", "CLI Team")
	if !strings.Contains(table, "MEMBER") || !strings.Contains(table, bUser) {
		t.Fatalf("org members table:\n%s", table)
	}

	// B now sees the org (as member) and cannot manage its members.
	if !waitFor(t, time.Minute, func() (bool, string) {
		var bOrgs []orgRow
		b.runJSON(&bOrgs, "org", "list")
		for _, o := range bOrgs {
			if o.UUID == team.UUID {
				return o.Role == "member", "role=" + o.Role
			}
		}
		return false, "org not visible to B yet"
	}) {
		t.Fatal("B never saw the team org")
	}
	b.mustFail("403", "org", "members", "add", "someone@example.com", "--org", team.UUID, "--invite")

	// Invite by email pre-provisions a pending user visible in the list.
	out = a.run("org", "members", "add", "invited-cli@example.com", "--org", team.UUID, "--role", "member", "--invite")
	if !strings.Contains(out, "invited-cli@example.com") {
		t.Fatalf("invite: %s", out)
	}
	a.runJSON(&members, "org", "members", "--org", team.UUID)
	var invited memberRow
	for _, m := range members {
		if m.Email == "invited-cli@example.com" {
			invited = m
		}
	}
	if invited.User == "" {
		t.Fatalf("invited user missing from %+v", members)
	}
	a.run("org", "members", "remove", "invited-cli@example.com", "--org", team.UUID, "--yes")

	// set-role (idempotent), then B is admin.
	out = a.run("org", "members", "set-role", bLabel, "admin", "--org", team.UUID)
	if !strings.Contains(out, "is now admin") {
		t.Fatalf("set-role: %s", out)
	}
	if out := a.run("org", "members", "set-role", bLabel, "admin", "--org", team.UUID); !strings.Contains(out, "already admin") {
		t.Fatalf("idempotent set-role: %s", out)
	}
	a.run("org", "members", "set-role", bLabel, "member", "--org", team.UUID)

	// Workspace members: add B to Platform, B switches into it.
	var wsMembers []memberRow
	a.runJSON(&wsMembers, "workspace", "members", "--org", team.UUID, "--workspace", platform.UUID)
	if len(wsMembers) != 1 || wsMembers[0].Role != "admin" {
		t.Fatalf("initial workspace members = %+v", wsMembers)
	}
	out = a.run("workspace", "members", "add", bUser, "--org", team.UUID, "--workspace", "Platform", "--role", "member")
	if !strings.Contains(out, `to workspace "Platform" (organization "CLI Team") as member`) {
		t.Fatalf("workspace members add: %s", out)
	}
	if !waitFor(t, time.Minute, func() (bool, string) {
		_, err := b.try("use", "--org", team.UUID, "--workspace", platform.UUID)
		return err == nil, "use failed"
	}) {
		t.Fatal("B could not switch into the shared workspace")
	}
	bMe := b.whoami()
	if bMe.Org == nil || bMe.Org.UUID != team.UUID || bMe.Workspace == nil || bMe.Workspace.UUID != platform.UUID || bMe.Workspace.Role != "member" || bMe.Cluster != platform.ClusterName {
		t.Fatalf("B whoami after use = %+v", bMe)
	}
	// B reads the shared workspace as a kube API too.
	if !waitFor(t, time.Minute, func() (bool, string) {
		out, err := b.kubectl("get", "clusterroles")
		return err == nil, out
	}) {
		t.Fatal("B cannot read the shared workspace with kubectl")
	}
	b.run("workspace", "members")
	b.mustFail("403", "workspace", "members", "set-role", bUser, "admin")

	a.run("workspace", "members", "set-role", bUser, "admin", "--org", team.UUID, "--workspace", platform.UUID)
	a.run("workspace", "members", "remove", bUser, "--org", team.UUID, "--workspace", platform.UUID, "--yes")
	a.runJSON(&wsMembers, "workspace", "members", "--org", team.UUID, "--workspace", platform.UUID)
	if len(wsMembers) != 1 {
		t.Fatalf("workspace members after remove = %+v", wsMembers)
	}

	// Remove B from the org, cascading any workspace rows; B no longer lists it.
	a.mustFail("stdin is not a terminal", "org", "members", "remove", bUser, "--org", team.UUID)
	a.run("org", "members", "remove", bUser, "--org", team.UUID, "--yes", "--cascade")
	a.runJSON(&members, "org", "members", "--org", team.UUID)
	if len(members) != 1 {
		t.Fatalf("org members after remove = %+v", members)
	}
	if !waitFor(t, time.Minute, func() (bool, string) {
		var bOrgs []orgRow
		b.runJSON(&bOrgs, "org", "list")
		for _, o := range bOrgs {
			if o.UUID == team.UUID {
				return false, "still listed"
			}
		}
		return true, ""
	}) {
		t.Fatal("B still sees the team org after removal")
	}
	// B's kubeconfig still points at the workspace it was removed from;
	// commands say so instead of failing obscurely.
	b.mustFail("not a workspace of any org you belong to", "env", "--no-mcp")
}

// TestServerEdgeSSH registers a Linux server edge with the CLI, connects a
// server-mode agent backed by the in-process test sshd, and runs a command
// over `faros ssh` through the tunnel; then inspects and deletes the edge.
func TestServerEdgeSSH(t *testing.T) {
	const (
		edgeName = "cli-srv"
		sshPort  = 22032
		marker   = "faros_cli_ssh_ok"
	)
	s := login(t, "server-edge", tokenA)
	tenantWS, tenantAdmin := prepareEdgesWorkspace(t, s)

	// Before any edge exists, list says so and get/ssh explain themselves.
	if out := s.run("edge", "list"); !strings.Contains(out, "No edges found") {
		t.Fatalf("edge list (empty): %s", out)
	}
	s.mustFail(`edge "ghost" not found`, "edge", "get", "ghost")
	s.mustFail("not found", "ssh", "ghost", "--", "true")

	sshCtx, cancelSSH := context.WithCancel(context.Background())
	t.Cleanup(cancelSSH)
	sshSrv := framework.NewTestSSHServer(sshPort)
	if err := sshSrv.Start(sshCtx); err != nil {
		t.Fatalf("start embedded SSH server: %v", err)
	}
	t.Cleanup(sshSrv.Stop)

	out := s.run("edge", "create", edgeName, "--type", "server", "--labels", "tier=cli")
	if !strings.Contains(out, "created") || !strings.Contains(out, "--type server") {
		t.Fatalf("edge create: %s", out)
	}
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(linuxServerGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := joinTokenFromOutput(t, out)
	if again := s.run("edge", "join-command", edgeName); joinTokenFromOutput(t, again) != joinToken || !strings.Contains(again, "--type server") {
		t.Fatalf("join-command disagrees with create:\n%s", again)
	}

	// Not connected yet: list/get/ssh say so.
	if out := s.run("edge", "list"); !strings.Contains(out, edgeName) || !strings.Contains(out, "server") {
		t.Fatalf("edge list: %s", out)
	}
	s.mustFail("not connected", "ssh", edgeName, "--", "true")
	s.mustFail("Linux server, not a Kubernetes cluster", "connect", edgeName)

	startAgent(t, edgeName, joinToken, tenantWS, "--type", "server", "--ssh-proxy-port", "22032")
	waitForConnected(t, tenantAdmin, linuxServerGVR, edgeName)

	// Connected: the CLI reflects it in every output form.
	if !waitFor(t, time.Minute, func() (bool, string) {
		out := s.run("edge", "list", "-o", "wide")
		return strings.Contains(out, "true") && strings.Contains(out, "tier=cli"), out
	}) {
		t.Fatal("edge list never showed the edge connected")
	}
	get := s.run("edge", "get", edgeName)
	for _, want := range []string{"Type:           server", "Connected:      true", "Proxy URL:      https://", "Next: faros ssh " + edgeName} {
		if !strings.Contains(get, want) {
			t.Fatalf("edge get lacks %q:\n%s", want, get)
		}
	}
	var obj map[string]any
	s.runJSON(&obj, "edge", "get", edgeName)
	if obj["kind"] != "LinuxServer" {
		t.Fatalf("edge get -o json kind = %v", obj["kind"])
	}
	if names := s.run("edge", "list", "-o", "name"); strings.TrimSpace(names) != edgeName {
		t.Fatalf("edge list -o name = %q", names)
	}

	// The proof: a command over SSH through the tunnel.
	var last string
	if !waitFor(t, 90*time.Second, func() (bool, string) {
		out, _ := s.try("ssh", edgeName, "--", "echo", marker)
		last = out
		return strings.Contains(out, marker), out
	}) {
		t.Fatalf("faros ssh never returned the marker; last output:\n%s", last)
	}

	// delete needs confirmation unless --yes; then the edge is gone.
	s.mustFail("stdin is not a terminal", "edge", "delete", edgeName)
	if out := s.run("edge", "delete", edgeName, "--yes"); !strings.Contains(out, "deleted") {
		t.Fatalf("edge delete: %s", out)
	}
	if !waitFor(t, time.Minute, func() (bool, string) {
		out := s.run("edge", "list")
		return strings.Contains(out, "No edges found"), out
	}) {
		t.Fatal("edge still listed after delete")
	}
}

// TestKubernetesEdgeConnect registers a Kubernetes edge backed by a kind
// cluster and proves both ways of reaching it: a standalone kubeconfig from
// `edge kubeconfig -o`, and `connect` / `disconnect` retargeting kubectl's
// current context. Skips when kind is not installed.
func TestKubernetesEdgeConnect(t *testing.T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind not on PATH; the Kubernetes edge path needs a kind cluster")
	}
	const (
		edgeName = "cli-k8s"
		kindName = "faros-cli-e2e"
	)
	s := login(t, "k8s-edge", tokenA)
	tenantWS, tenantAdmin := prepareEdgesWorkspace(t, s)
	workDir := filepath.Dir(s.kubeconfig)
	kindKubeconfig := filepath.Join(workDir, "kind.kubeconfig")
	edgeKubeconfig := filepath.Join(workDir, "edge.kubeconfig")

	out := s.run("edge", "create", edgeName)
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(kubernetesClusterGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := joinTokenFromOutput(t, out)
	s.mustFail("not connected", "edge", "kubeconfig", edgeName)
	s.mustFail("not connected", "connect", edgeName)

	createKindCluster(t, kindName, kindKubeconfig)
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "kubernetes", "--kubeconfig", kindKubeconfig)
	waitForConnected(t, tenantAdmin, kubernetesClusterGVR, edgeName)

	// 1. Standalone kubeconfig.
	s.run("edge", "kubeconfig", edgeName, "-o", edgeKubeconfig)
	var last string
	if !waitFor(t, 90*time.Second, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", edgeKubeconfig, "--insecure-skip-tls-verify", "get", "nodes").CombinedOutput()
		last = string(b)
		return err == nil && strings.Contains(last, "Ready"), last
	}) {
		t.Fatalf("kubectl through the standalone edge kubeconfig never succeeded:\n%s", last)
	}
	// The legacy spelling still produces the same file.
	s.run("kubeconfig", "edge", edgeName, "-o", edgeKubeconfig+".legacy")

	// 2. connect: kubectl's current context is the edge.
	if out := s.run("connect", edgeName); !strings.Contains(out, `context "faros-`+edgeName+`"`) {
		t.Fatalf("connect: %s", out)
	}
	me := s.whoami()
	if me.KubectlContext != "faros-"+edgeName || me.KubectlTarget != "edge "+edgeName {
		t.Fatalf("whoami after connect: %+v", me)
	}
	if !waitFor(t, 60*time.Second, func() (bool, string) {
		out, err := s.kubectl("get", "nodes")
		return err == nil && strings.Contains(out, "Ready"), out
	}) {
		t.Fatal("kubectl get nodes through 'faros connect' never succeeded")
	}
	// Hub commands keep working while connected (they use the faros context).
	if !strings.Contains(s.run("edge", "list"), edgeName) {
		t.Fatal("edge list while connected")
	}

	// 3. disconnect / use return kubectl to the hub workspace.
	if out := s.run("disconnect"); !strings.Contains(out, "Disconnected") {
		t.Fatalf("disconnect: %s", out)
	}
	if me := s.whoami(); me.KubectlContext != "faros" {
		t.Fatalf("whoami after disconnect: %+v", me)
	}
	s.run("connect", edgeName)
	s.run("use", "--org", me.Org.UUID, "--workspace", me.Workspace.UUID)
	if me := s.whoami(); me.KubectlContext != "faros" {
		t.Fatalf("use must return kubectl to the hub: %+v", me)
	}
	// The edge context is kept for kubectl --context.
	if out, err := s.kubectl("--context", "faros-"+edgeName, "get", "nodes"); err != nil {
		t.Fatalf("kubectl --context faros-%s: %v\n%s", edgeName, err, out)
	}

	// logout drops the edge contexts too.
	s.run("logout")
	if b, _ := os.ReadFile(s.kubeconfig); strings.Contains(string(b), "faros-"+edgeName) {
		t.Fatalf("logout kept the edge context:\n%s", b)
	}
}
