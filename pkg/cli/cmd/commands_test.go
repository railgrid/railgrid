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

package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	cliauth "github.com/faroshq/faros/pkg/cli/auth"
)

// edgeObject builds a KubernetesCluster or LinuxServer as the hub's kcp proxy
// would serve it.
func edgeObject(kind, name string, connected bool, labels map[string]string) map[string]any {
	resource := "kubernetesclusters"
	sub := "k8s"
	if kind == "LinuxServer" {
		resource, sub = "linuxservers", "ssh"
	}
	status := map[string]any{"phase": "Pending", "connected": connected}
	if connected {
		status["phase"] = "Ready"
		status["agentVersion"] = "v0.9.0"
		status["hostname"] = name + ".local"
		status["lastHeartbeatTime"] = "2026-09-11T10:00:00Z"
		// An internal host: the CLI must externalize it against the hub.
		status["URL"] = "https://hub.internal:8443/services/providers/edges/edgeproxy/clusters/cl-b/apis/edges.faros.sh/v1alpha1/" + resource + "/" + name + "/" + sub
	}
	meta := map[string]any{"name": name, "creationTimestamp": "2026-09-01T00:00:00Z"}
	if labels != nil {
		meta["labels"] = labels
	}
	return map[string]any{
		"apiVersion": "edges.faros.sh/v1alpha1",
		"kind":       kind,
		"metadata":   meta,
		"spec":       map[string]any{},
		"status":     status,
	}
}

// serveEdges registers the edges API of cluster cl-b on the fake hub with
// one connected cluster, one pending cluster and one connected server.
func serveEdges(h *fakeHub) {
	clusters := map[string]map[string]any{
		"prod":    edgeObject("KubernetesCluster", "prod", true, map[string]string{"env": "prod", "region": "eu"}),
		"staging": edgeObject("KubernetesCluster", "staging", false, nil),
	}
	servers := map[string]map[string]any{
		"vps": edgeObject("LinuxServer", "vps", true, nil),
	}
	list := func(kind string, objs map[string]map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			items := make([]map[string]any, 0, len(objs))
			for _, o := range objs {
				items = append(items, o)
			}
			writeTestJSON(w, map[string]any{"apiVersion": "edges.faros.sh/v1alpha1", "kind": kind + "List", "items": items})
		}
	}
	get := func(objs map[string]map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			o, ok := objs[r.PathValue("name")]
			if !ok {
				writeTestStatus(w, http.StatusNotFound, "NotFound", r.PathValue("name")+" not found")
				return
			}
			writeTestJSON(w, o)
		}
	}
	h.handle("GET /clusters/cl-b/apis/edges.faros.sh/v1alpha1/kubernetesclusters", list("KubernetesCluster", clusters))
	h.handle("GET /clusters/cl-b/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{name}", get(clusters))
	h.handle("GET /clusters/cl-b/apis/edges.faros.sh/v1alpha1/linuxservers", list("LinuxServer", servers))
	h.handle("GET /clusters/cl-b/apis/edges.faros.sh/v1alpha1/linuxservers/{name}", get(servers))
}

func mustRun(t *testing.T, path string, args ...string) string {
	t.Helper()
	out, err := runRoot(t, path, args...)
	if err != nil {
		t.Fatalf("faros %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func mustFail(t *testing.T, path string, want string, args ...string) {
	t.Helper()
	out, err := runRoot(t, path, args...)
	if err == nil {
		t.Fatalf("faros %s succeeded, want error containing %q\n%s", strings.Join(args, " "), want, out)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("faros %s: err = %v, want %q", strings.Join(args, " "), err, want)
	}
}

func TestEdgeListOutputs(t *testing.T) {
	hub := newFakeHub(t)
	serveEdges(hub)
	path := hub.useKubeconfig("cl-b")

	out := mustRun(t, path, "edge", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("table:\n%s", out)
	}
	// Sorted by name, type derived from the kind (the old spec.type field no
	// longer exists), so a LinuxServer must say "server".
	if !strings.HasPrefix(lines[1], "prod") || !strings.HasPrefix(lines[2], "staging") || !strings.HasPrefix(lines[3], "vps") {
		t.Fatalf("order:\n%s", out)
	}
	if !strings.Contains(lines[3], "server") || !strings.Contains(lines[1], "kubernetes") {
		t.Fatalf("types:\n%s", out)
	}
	if strings.Contains(out, "HOSTNAME") {
		t.Fatalf("plain table must not carry wide columns:\n%s", out)
	}

	wide := mustRun(t, path, "edge", "list", "-o", "wide")
	if !strings.Contains(wide, "HOSTNAME") || !strings.Contains(wide, "prod.local") || !strings.Contains(wide, "env=prod,region=eu") {
		t.Fatalf("wide:\n%s", wide)
	}

	names := mustRun(t, path, "edge", "ls", "-o", "name")
	if names != "prod\nstaging\nvps\n" {
		t.Fatalf("names = %q", names)
	}

	asJSON := mustRun(t, path, "edge", "list", "-o", "json")
	var list struct {
		Kind  string           `json:"kind"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(asJSON), &list); err != nil || list.Kind != "List" || len(list.Items) != 3 {
		t.Fatalf("json list: %v\n%s", err, asJSON)
	}

	asYAML := mustRun(t, path, "edge", "list", "-o", "yaml")
	if !strings.Contains(asYAML, "kind: List") || !strings.Contains(asYAML, "name: vps") {
		t.Fatalf("yaml:\n%s", asYAML)
	}

	mustFail(t, path, "unsupported output format", "edge", "list", "-o", "xml")

	// The pre-1.0 shorthand keeps working.
	if legacy := mustRun(t, path, "ls", "-o", "name"); legacy != names {
		t.Fatalf("faros ls = %q, want %q", legacy, names)
	}
}

func TestEdgeGet(t *testing.T) {
	hub := newFakeHub(t)
	serveEdges(hub)
	path := hub.useKubeconfig("cl-b")

	out := mustRun(t, path, "edge", "get", "vps")
	for _, want := range []string{"Type:           server", "Connected:      true", "Hostname:       vps.local", "Next: faros ssh vps"} {
		if !strings.Contains(out, want) {
			t.Fatalf("edge get vps missing %q:\n%s", want, out)
		}
	}
	out = mustRun(t, path, "edge", "describe", "prod")
	if !strings.Contains(out, "Labels:         env=prod,region=eu") || !strings.Contains(out, "Next: faros connect prod") {
		t.Fatalf("edge get prod:\n%s", out)
	}
	out = mustRun(t, path, "edge", "get", "staging", "-o", "json")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil || obj["kind"] != "KubernetesCluster" {
		t.Fatalf("json: %v\n%s", err, out)
	}
	mustFail(t, path, `edge "nope" not found`, "edge", "get", "nope")
}

func TestEdgeKubeconfigConnectDisconnect(t *testing.T) {
	hub := newFakeHub(t)
	serveEdges(hub)
	path := hub.useKubeconfig("cl-b")
	wantServer := hub.URL + "/services/providers/edges/edgeproxy/clusters/cl-b/apis/edges.faros.sh/v1alpha1/kubernetesclusters/prod/k8s"

	// Standalone kubeconfig on stdout: one context, the hub's credentials,
	// the proxy URL externalized onto the hub host.
	out := mustRun(t, path, "edge", "kubeconfig", "prod")
	cfg, err := clientcmd.Load([]byte(out))
	if err != nil {
		t.Fatalf("parsing kubeconfig: %v\n%s", err, out)
	}
	if cfg.CurrentContext != "faros-prod" || cfg.Clusters["faros-prod"].Server != wantServer {
		t.Fatalf("standalone kubeconfig: %+v", cfg)
	}
	if cfg.AuthInfos["faros-prod"].Token != fakeUserToken {
		t.Fatalf("credentials not copied: %+v", cfg.AuthInfos)
	}
	if !cfg.Clusters["faros-prod"].InsecureSkipTLSVerify {
		t.Fatalf("hub TLS settings not inherited: %+v", cfg.Clusters["faros-prod"])
	}

	// -o writes a file; the legacy spelling still works.
	file := filepath.Join(t.TempDir(), "prod.kubeconfig")
	mustRun(t, path, "kubeconfig", "edge", "prod", "-o", file)
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}

	// Not connected / wrong kind / unknown are actionable errors.
	mustFail(t, path, `edge "staging" is not connected`, "edge", "kubeconfig", "staging")
	mustFail(t, path, "use: faros ssh vps", "connect", "vps")
	mustFail(t, path, `edge "nope" not found`, "connect", "nope")

	// connect merges a context and makes it current; use returns to the hub
	// (as does disconnect).
	out = mustRun(t, path, "connect", "prod")
	if !strings.Contains(out, `context "faros-prod"`) {
		t.Fatalf("connect: %s", out)
	}
	raw := loadTestKubeconfig(t, path)
	if raw.CurrentContext != "faros-prod" || raw.Contexts["faros-prod"].AuthInfo != "faros" || raw.Clusters["faros-prod"].Server != wantServer {
		t.Fatalf("after connect: current=%s contexts=%v", raw.CurrentContext, raw.Contexts)
	}

	who := mustRun(t, path, "whoami")
	if !strings.Contains(who, `context "faros-prod" → edge prod`) {
		t.Fatalf("whoami after connect:\n%s", who)
	}

	out = mustRun(t, path, "disconnect")
	if !strings.Contains(out, `Disconnected from "faros-prod"`) {
		t.Fatalf("disconnect: %s", out)
	}
	raw = loadTestKubeconfig(t, path)
	if raw.CurrentContext != "faros" {
		t.Fatalf("after disconnect current = %s", raw.CurrentContext)
	}
	if _, ok := raw.Contexts["faros-prod"]; !ok {
		t.Fatal("disconnect must keep the edge context for kubectl --context")
	}
	out = mustRun(t, path, "disconnect")
	if !strings.Contains(out, "already uses the hub context") {
		t.Fatalf("second disconnect: %s", out)
	}

	// --merge adds without switching.
	mustRun(t, path, "connect", "prod")
	mustRun(t, path, "disconnect")
	delete(raw.Contexts, "faros-prod")
	mustRun(t, path, "edge", "kubeconfig", "prod", "--merge")
	raw = loadTestKubeconfig(t, path)
	if raw.CurrentContext != "faros" {
		t.Fatalf("--merge switched the context to %s", raw.CurrentContext)
	}
	if _, ok := raw.Contexts["faros-prod"]; !ok {
		t.Fatal("--merge did not add the context")
	}
}

func loadTestKubeconfig(t *testing.T, path string) *clientcmdapi.Config {
	t.Helper()
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// membershipStore is the fake hub's membership API for one scope, recording
// the writes the CLI makes.
type membershipStore struct {
	mu      sync.Mutex
	members []map[string]any
	writes  []string // "METHOD path org=… ws=… body"
}

func (m *membershipStore) install(h *fakeHub, base string) {
	h.handle("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		m.record(r, "")
		m.mu.Lock()
		defer m.mu.Unlock()
		writeTestJSON(w, map[string]any{"items": m.members})
	})
	h.handle("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.record(r, string(body))
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		user, _ := req["user"].(string)
		role, _ := req["role"].(string)
		if !strings.Contains(user, "@") && req["invite"] != true {
			writeTestStatus(w, http.StatusNotFound, "NotFound", "user "+user+" not found")
			return
		}
		added := map[string]any{"user": "user-" + strings.SplitN(user, "@", 2)[0], "email": user, "role": role}
		m.mu.Lock()
		m.members = append(m.members, added)
		m.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, added)
	})
	h.handle("PATCH "+base+"/{user}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.record(r, string(body))
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, mem := range m.members {
			if mem["user"] == r.PathValue("user") {
				mem["role"] = req["role"]
				writeTestJSON(w, mem)
				return
			}
		}
		writeTestStatus(w, http.StatusNotFound, "NotFound", "no such member")
	})
	h.handle("DELETE "+base+"/{user}", func(w http.ResponseWriter, r *http.Request) {
		m.record(r, "")
		m.mu.Lock()
		defer m.mu.Unlock()
		kept := m.members[:0]
		for _, mem := range m.members {
			if mem["user"] != r.PathValue("user") {
				kept = append(kept, mem)
			}
		}
		m.members = kept
		w.WriteHeader(http.StatusNoContent)
	})
}

func (m *membershipStore) record(r *http.Request, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" org="+r.Header.Get("X-Faros-Org")+" ws="+r.Header.Get("X-Faros-Workspace")+" "+body))
}

func (m *membershipStore) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) == 0 {
		return ""
	}
	return m.writes[len(m.writes)-1]
}

func TestOrgAndWorkspaceCommands(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	// Roles ride along on the org/workspace views.
	hub.orgs = []map[string]any{
		{"uuid": "org-a", "displayName": "Personal", "personal": true, "role": "admin"},
		{"uuid": "org-b", "displayName": "Acme", "role": "admin", "workspaceCreation": "members", "catalogEntryCreation": "admin", "createdAt": "2026-09-01T00:00:00Z"},
	}
	orgMembers := &membershipStore{members: []map[string]any{
		{"user": "user-alice", "email": "alice@example.com", "userDisplayName": "Alice", "role": "admin", "rbacIdentity": "faros:alice@example.com"},
		{"user": "user-bob", "email": "bob@example.com", "role": "member"},
	}}
	orgMembers.install(hub, "/api/orgs/{org}/memberships")
	wsMembers := &membershipStore{members: []map[string]any{
		{"user": "user-alice", "email": "alice@example.com", "role": "admin"},
	}}
	wsMembers.install(hub, "/api/orgs/{org}/workspaces/{ws}/memberships")
	var created []string
	hub.handle("POST /api/orgs", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		created = append(created, "org "+string(body))
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, map[string]any{"uuid": "org-new", "displayName": "New Org", "role": "admin"})
	})
	hub.handle("POST /api/orgs/{org}/workspaces", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		created = append(created, "ws "+r.Header.Get("X-Faros-Org")+" ws="+r.Header.Get("X-Faros-Workspace")+" "+string(body))
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, map[string]any{"uuid": "ws-new", "orgUUID": r.PathValue("org"), "displayName": "Platform 2"})
	})

	// org list: the org owning the kubeconfig's workspace is marked.
	out := mustRun(t, path, "org", "list")
	if !strings.Contains(out, "*         Acme") || !strings.Contains(out, "admin") || !strings.Contains(out, "personal") {
		t.Fatalf("org list:\n%s", out)
	}
	if names := mustRun(t, path, "org", "list", "-o", "name"); names != "Personal\nAcme\n" {
		t.Fatalf("org names = %q", names)
	}
	wide := mustRun(t, path, "org", "list", "-o", "wide")
	if !strings.Contains(wide, "WORKSPACE CREATION") || !strings.Contains(wide, "members") {
		t.Fatalf("org list wide:\n%s", wide)
	}

	// workspace list defaults to the current org and marks the current
	// workspace; --org selects another.
	out = mustRun(t, path, "workspace", "list")
	if !strings.Contains(out, "*         platform") || !strings.Contains(out, "default") || strings.Contains(out, "ws-a1") {
		t.Fatalf("workspace list:\n%s", out)
	}
	out = mustRun(t, path, "ws", "list", "--org", "Personal", "-o", "name")
	if out != "default\n" {
		t.Fatalf("workspace list --org Personal = %q", out)
	}

	// org members: list, add, set-role, remove. Org routes must not carry
	// the workspace header.
	out = mustRun(t, path, "org", "members")
	if !strings.Contains(out, "alice@example.com") || !strings.Contains(out, "MEMBER") {
		t.Fatalf("org members:\n%s", out)
	}
	if last := orgMembers.last(); !strings.HasPrefix(last, "GET /api/orgs/org-b/memberships org=org-b ws= ") && last != "GET /api/orgs/org-b/memberships org=org-b ws=" {
		t.Fatalf("org members request = %q", last)
	}
	mustFail(t, path, "invalid role", "org", "members", "add", "carol@example.com", "--role", "owner")
	out = mustRun(t, path, "org", "members", "add", "carol@example.com", "--role", "admin", "--invite")
	if !strings.Contains(out, `Added carol@example.com to organization "Acme" as admin`) {
		t.Fatalf("add: %s", out)
	}
	if last := orgMembers.last(); !strings.Contains(last, `"invite":true`) || !strings.Contains(last, `"role":"admin"`) || !strings.Contains(last, "ws= ") {
		t.Fatalf("add request = %q", last)
	}
	out = mustRun(t, path, "org", "members", "set-role", "bob@example.com", "admin")
	if !strings.Contains(out, "bob@example.com is now admin") {
		t.Fatalf("set-role: %s", out)
	}
	if last := orgMembers.last(); !strings.HasPrefix(last, "PATCH /api/orgs/org-b/memberships/user-bob ") {
		t.Fatalf("set-role must address the User name: %q", last)
	}
	out = mustRun(t, path, "org", "members", "set-role", "Alice", "admin")
	if !strings.Contains(out, "already admin") {
		t.Fatalf("idempotent set-role: %s", out)
	}
	mustFail(t, path, "stdin is not a terminal; pass --yes", "org", "members", "remove", "bob@example.com")
	out = mustRun(t, path, "org", "members", "remove", "bob@example.com", "--yes", "--cascade")
	if !strings.Contains(out, "Removed bob@example.com") {
		t.Fatalf("remove: %s", out)
	}
	if last := orgMembers.last(); !strings.HasPrefix(last, "DELETE /api/orgs/org-b/memberships/user-bob?cascade=true ") {
		t.Fatalf("remove request = %q", last)
	}
	mustFail(t, path, `no member matches "nobody"`, "org", "members", "remove", "nobody", "-y")
	// --org retargets; the other org is org-a.
	mustRun(t, path, "org", "members", "--org", "org-a")
	if last := orgMembers.last(); !strings.HasPrefix(last, "GET /api/orgs/org-a/memberships org=org-a ws=") {
		t.Fatalf("--org request = %q", last)
	}

	// workspace members carry both tenant headers.
	out = mustRun(t, path, "workspace", "members", "list", "-o", "json")
	var members []memberView
	if err := json.Unmarshal([]byte(out), &members); err != nil || len(members) != 1 || members[0].Email != "alice@example.com" {
		t.Fatalf("workspace members json: %v\n%s", err, out)
	}
	mustRun(t, path, "workspace", "members", "add", "dave@example.com", "--workspace", "default", "--org", "Acme")
	if last := wsMembers.last(); !strings.HasPrefix(last, "POST /api/orgs/org-b/workspaces/ws-b2/memberships org=org-b ws=ws-b2 ") {
		t.Fatalf("workspace add request = %q", last)
	}

	// create commands.
	out = mustRun(t, path, "org", "create", "New Org", "--workspace-creation", "admin")
	if !strings.Contains(out, "faros use --org org-new") || len(created) != 1 || !strings.Contains(created[0], `"workspaceCreation":"admin"`) {
		t.Fatalf("org create: %s / %v", out, created)
	}
	out = mustRun(t, path, "workspace", "create", "Platform 2")
	if !strings.Contains(out, "ws-new") || len(created) != 2 || !strings.HasPrefix(created[1], "ws org-b ws= ") {
		t.Fatalf("workspace create: %s / %v", out, created)
	}
}

func TestWhoamiTokenLogout(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	hub.orgs = []map[string]any{
		{"uuid": "org-a", "displayName": "me@example.com", "personal": true, "role": "admin"},
		{"uuid": "org-b", "displayName": "Acme", "role": "member"},
	}

	out := mustRun(t, path, "whoami")
	for _, want := range []string{
		"Hub:        " + hub.URL,
		"User:       me@example.com",
		"Auth:       static-token",
		`Org:        Acme (org-b) — role: member`,
		`Workspace:  platform (ws-b1)`,
		`kubectl:    context "faros" → hub workspace`,
		"* Acme (member)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("whoami missing %q:\n%s", want, out)
		}
	}
	out = mustRun(t, path, "status", "-o", "json")
	var v whoamiView
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Org.UUID != "org-b" || v.Workspace.UUID != "ws-b1" || v.Auth != "static-token" || len(v.Orgs) != 2 {
		t.Fatalf("whoami json: %v\n%s", err, out)
	}

	if tok := mustRun(t, path, "token"); tok != fakeUserToken+"\n" {
		t.Fatalf("token = %q", tok)
	}
	if tok := mustRun(t, path, "token", "--refresh"); tok != fakeUserToken+"\n" {
		t.Fatalf("token --refresh with a static token = %q", tok)
	}

	out = mustRun(t, path, "logout")
	if !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %s", out)
	}
	raw := loadTestKubeconfig(t, path)
	if _, ok := raw.Contexts["faros"]; ok || raw.CurrentContext != "" || len(raw.AuthInfos) != 0 {
		t.Fatalf("kubeconfig after logout: %+v", raw)
	}
	out = mustRun(t, path, "logout")
	if !strings.Contains(out, "Not logged in") {
		t.Fatalf("second logout: %s", out)
	}
	mustFail(t, path, "run 'faros login'", "whoami")
}

func TestLogoutRemovesOIDCCacheAndEdgeContexts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	issuer, clientID := "https://issuer.test", "faros"
	if err := cliauth.SaveTokenCache(&cliauth.TokenCache{IDToken: "x", RefreshToken: "y", IssuerURL: issuer, ClientID: clientID}); err != nil {
		t.Fatal(err)
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["faros"] = &clientcmdapi.Cluster{Server: "https://hub.test/clusters/abc"}
	cfg.Clusters["faros-prod"] = &clientcmdapi.Cluster{Server: "https://hub.test/services/x"}
	cfg.Clusters["other"] = &clientcmdapi.Cluster{Server: "https://other.test"}
	cfg.AuthInfos["user-1"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion: "client.authentication.k8s.io/v1beta1", Command: "faros",
		Args: []string{"get-token", "--oidc-issuer-url=" + issuer, "--oidc-client-id=" + clientID},
	}}
	cfg.AuthInfos["other"] = &clientcmdapi.AuthInfo{Token: "t"}
	cfg.Contexts["faros"] = &clientcmdapi.Context{Cluster: "faros", AuthInfo: "user-1"}
	cfg.Contexts["faros-prod"] = &clientcmdapi.Context{Cluster: "faros-prod", AuthInfo: "user-1"}
	cfg.Contexts["other"] = &clientcmdapi.Context{Cluster: "other", AuthInfo: "other"}
	cfg.CurrentContext = "faros-prod"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, path, "logout")
	if !strings.Contains(out, "Removed cached OIDC tokens") {
		t.Fatalf("logout: %s", out)
	}
	if _, err := cliauth.LoadTokenCache(issuer, clientID); err == nil {
		t.Fatal("token cache still present")
	}
	raw := loadTestKubeconfig(t, path)
	if _, ok := raw.Contexts["faros-prod"]; ok {
		t.Fatal("edge context kept")
	}
	if _, ok := raw.Contexts["other"]; !ok || raw.AuthInfos["other"] == nil || raw.Clusters["other"] == nil {
		t.Fatalf("unrelated entries touched: %+v", raw)
	}
	if raw.CurrentContext != "" {
		t.Fatalf("current context = %q", raw.CurrentContext)
	}
}

func TestResolveMember(t *testing.T) {
	members := []memberView{
		{User: "user-1", Email: "Alice@Example.com", UserDisplayName: "Alice", RBACIdentity: "faros:alice@example.com"},
		{User: "user-2", Email: "bob@example.com"},
		{User: "user-3", UserDisplayName: "Alice"},
	}
	for q, want := range map[string]string{"user-2": "user-2", "alice@example.com": "user-1", "faros:alice@example.com": "user-1", "BOB@example.com": "user-2"} {
		m, err := resolveMember(members, q)
		if err != nil || m.User != want {
			t.Errorf("resolveMember(%q) = %v, %v; want %s", q, m.User, err, want)
		}
	}
	if _, err := resolveMember(members, "Alice"); err == nil || !strings.Contains(err.Error(), "matches 2 members") {
		t.Errorf("ambiguous display name: %v", err)
	}
	if _, err := resolveMember(members, "zed"); err == nil {
		t.Error("unknown member resolved")
	}
}

func TestExecOIDCArgsAndJWT(t *testing.T) {
	exec := &clientcmdapi.ExecConfig{Args: []string{"get-token", "--oidc-issuer-url=https://i", "--oidc-client-id", "cid", "--insecure-skip-tls-verify"}}
	if iss, cid := execOIDCArgs(exec); iss != "https://i" || cid != "cid" {
		t.Fatalf("execOIDCArgs = %q %q", iss, cid)
	}
	payload := `{"sub":"s","email":"e@x","exp":1700000000}`
	tok := "h." + strings.TrimRight(base64URL(payload), "=") + ".sig"
	c := jwtClaims(tok)
	if c == nil || c.Email != "e@x" || c.Expiry != 1700000000 {
		t.Fatalf("jwtClaims = %+v", c)
	}
	if jwtClaims("opaque-token") != nil {
		t.Fatal("opaque token decoded as JWT")
	}
}

func base64URL(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var b strings.Builder
	data := []byte(s)
	for i := 0; i < len(data); i += 3 {
		var n uint32
		rem := len(data) - i
		for j := 0; j < 3; j++ {
			n <<= 8
			if j < rem {
				n |= uint32(data[i+j])
			}
		}
		for j := 0; j < 4; j++ {
			if j <= rem {
				b.WriteByte(alphabet[(n>>(18-6*j))&63])
			}
		}
	}
	return b.String()
}

func TestRootCommandTreeIsGrouped(t *testing.T) {
	root := NewRootCommand()
	for _, c := range root.Commands() {
		if !c.IsAvailableCommand() || c.IsAdditionalHelpTopicCommand() || c.Name() == "completion" || c.Name() == "version" {
			continue
		}
		if c.GroupID == "" {
			t.Errorf("visible command %q has no help group", c.Name())
		}
	}
	// Legacy spellings stay reachable but hidden.
	for _, name := range []string{"list", "get", "apply", "kubeconfig", "kcp-workspace", "get-token", "docs"} {
		c, _, err := root.Find([]string{name})
		if err != nil || c == nil || c.Name() != name {
			t.Errorf("command %q not found: %v", name, err)
			continue
		}
		if !c.Hidden {
			t.Errorf("command %q should be hidden", name)
		}
	}
}

func TestGenerateDocs(t *testing.T) {
	dir := t.TempDir()
	if err := generateDocs(NewRootCommand(), dir); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[faros login](faros_login.md)", "[faros org members add](faros_org_members_add.md)", "## Edges (clusters and servers)"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("index missing %q", want)
		}
	}
	if strings.Contains(string(index), "faros_get-token.md") || strings.Contains(string(index), "faros apply") {
		t.Error("hidden commands leaked into the index")
	}
	if _, err := os.Stat(filepath.Join(dir, "faros_connect.md")); err != nil {
		t.Error("faros_connect.md not generated")
	}
	if _, err := os.Stat(filepath.Join(dir, "faros_get-token.md")); err == nil {
		t.Error("hidden command page generated")
	}
}

// TestEdgeListMacOSAndMissingKinds: a MacOSServer lists as "macos" and is
// refused by connect and ssh with a service-only hint; a workspace whose
// edges API does not serve every kind still lists the kinds it does serve,
// and one that serves none says the provider is not enabled.
func TestEdgeListMacOSAndMissingKinds(t *testing.T) {
	hub := newFakeHub(t)
	serveEdges(hub)
	mac := edgeObject("KubernetesCluster", "mac", true, nil)
	mac["kind"] = "MacOSServer"
	hub.handle("GET /clusters/cl-b/apis/edges.faros.sh/v1alpha1/macosservers", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"apiVersion": "edges.faros.sh/v1alpha1", "kind": "MacOSServerList", "items": []any{mac}})
	})
	hub.handle("GET /clusters/cl-b/apis/edges.faros.sh/v1alpha1/macosservers/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "mac" {
			writeTestStatus(w, http.StatusNotFound, "NotFound", "not found")
			return
		}
		writeTestJSON(w, mac)
	})
	path := hub.useKubeconfig("cl-b")

	out := mustRun(t, path, "edge", "list")
	if !strings.Contains(out, "mac ") || !strings.Contains(out, "macos") {
		t.Fatalf("edge list lacks the macOS edge:\n%s", out)
	}
	if get := mustRun(t, path, "edge", "get", "mac"); !strings.Contains(get, "Type:           macos") || !strings.Contains(get, "service-only") {
		t.Fatalf("edge get mac:\n%s", get)
	}
	mustFail(t, path, "service-only", "connect", "mac")
	mustFail(t, path, "SSH is only available for Linux server edges", "ssh", "mac", "--", "true")
	mustFail(t, path, "use: faros connect prod", "ssh", "prod", "--", "true")

	// A hub whose edges provider predates macosservers: the other kinds list.
	old := newFakeHub(t)
	serveEdges(old)
	oldPath := old.useKubeconfig("cl-b")
	if names := mustRun(t, oldPath, "edge", "list", "-o", "name"); names != "prod\nstaging\nvps\n" {
		t.Fatalf("edge list without macosservers = %q", names)
	}

	// No edges API at all.
	none := newFakeHub(t)
	nonePath := none.useKubeconfig("cl-b")
	mustFail(t, nonePath, "edges provider is not enabled", "edge", "list")
}
