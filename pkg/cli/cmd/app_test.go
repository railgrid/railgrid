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
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const appStudioPrefix = "/services/providers/app-studio/api/projects"

// runRoot executes the faros root command against the given kubeconfig.
// The path must be captured before NewRootCommand: binding --kubeconfig resets
// the package-level variable to its "" default, which would fall back to the
// developer's real kubeconfig.
func runRoot(t *testing.T, kubeconfigPath string, args ...string) (string, error) {
	t.Helper()
	if kubeconfigPath == "" {
		t.Fatal("runRoot needs an explicit kubeconfig path")
	}
	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append(args, "--kubeconfig", kubeconfigPath))
	err := root.Execute()
	return out.String(), err
}

func TestPrintAppStatus(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	st := appStatus{
		Project: json.RawMessage(`{
			"name":"shop","displayName":"Shop","phase":"Ready","template":"application",
			"repository":{"ref":"shop-1","ready":true,"htmlURL":"https://github.com/acme/shop-1","commits":[
				{"name":"c1","phase":"Succeeded","commitSHA":"1111111aaaa","message":"Initial scaffold","createdAt":"2026-09-10T11:00:00Z"},
				{"name":"c2","phase":"Succeeded","commitSHA":"2222222bbbb","message":"Add cart","createdAt":"2026-09-10T11:58:00Z"}]},
			"environments":[{"name":"development","bindings":[{"name":"app","url":"https://shop-dev.example.com"}]}]
		}`),
		Promotion:  json.RawMessage(`{"promotable":true,"build":{"status":"built","commitSHA":"2222222bbbb","note":""},"production":{"phase":"Ready","url":"https://shop.example.com"}}`),
		Publishing: json.RawMessage(`{"published":true,"publication":{"mode":"public","url":"https://shop.example.com","ready":true}}`),
	}
	var buf bytes.Buffer
	if err := printAppStatus(&buf, st, now); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"shop (Shop)  phase=Ready  template=application",
		"shop-1  ready=true  https://github.com/acme/shop-1",
		"2222222 Succeeded Add cart  2m ago",
		"https://shop-dev.example.com",
		"promotable=true  build=built  commit=2222222",
		"Ready  https://shop.example.com",
		"public  https://shop.example.com",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	// Newest commit first.
	if strings.Index(out, "2222222") > strings.Index(out, "1111111") {
		t.Fatalf("commits not newest first:\n%s", out)
	}

	st = appStatus{Project: json.RawMessage(`{"name":"shop"}`), PromotionError: "HTTP 503: busy"}
	buf.Reset()
	if err := printAppStatus(&buf, st, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "unavailable: HTTP 503: busy") {
		t.Fatalf("status:\n%s", buf.String())
	}
}

func TestBuildPromoteRequest(t *testing.T) {
	if got, _ := json.Marshal(buildPromoteRequest("", "")); string(got) != "{}" {
		t.Fatalf("empty = %s", got)
	}
	got, _ := json.Marshal(buildPromoteRequest("shop", "abc"))
	if string(got) != `{"values":{"expose":{"hostnamePrefix":"shop"}},"commitSHA":"abc"}` {
		t.Fatalf("got %s", got)
	}
}

func TestAppCommands(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	var created appCreateRequest
	hub.handle("POST "+appStudioPrefix, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&created)
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, map[string]any{"name": created.Name, "phase": "Pending", "template": created.TemplateName, "repository": map[string]any{"ref": created.Name}})
	})
	hub.handle("GET "+appStudioPrefix, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"items": []map[string]any{{"name": "shop", "displayName": "Shop", "phase": "Ready", "template": "application", "repository": map[string]any{"ref": "shop"}}}})
	})
	var publishMethod, publishBody string
	publish := func(w http.ResponseWriter, r *http.Request) {
		publishMethod = r.Method
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		publishBody = b.String()
		if r.Method == http.MethodDelete {
			writeTestJSON(w, map[string]any{"published": false})
			return
		}
		writeTestJSON(w, map[string]any{"published": true, "publication": map[string]any{"mode": "public", "url": "https://shop.example.com", "ready": true}})
	}
	hub.handle("POST "+appStudioPrefix+"/shop/publishing", publish)
	hub.handle("DELETE "+appStudioPrefix+"/shop/publishing", publish)
	var promote appPromoteRequest
	hub.handle("POST "+appStudioPrefix+"/shop/promote", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&promote)
		writeTestJSON(w, map[string]any{"instance": "shop-prod", "commitSHA": "abc", "rolloutRevision": "r1", "components": []map[string]any{{"name": "api", "built": true}}})
	})

	out, err := runRoot(t, path, "app", "create", "shop", "--template", "application", "--display-name", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created, appCreateRequest{Name: "shop", DisplayName: "Shop", TemplateName: "application"}) || !strings.Contains(out, "project shop created") {
		t.Fatalf("created=%+v out=%q", created, out)
	}
	if _, err := runRoot(t, path, "app", "create", "shop"); err == nil || !strings.Contains(err.Error(), "--template is required") {
		t.Fatalf("create without template: %v", err)
	}

	out, err = runRoot(t, path, "app", "list")
	if err != nil || !strings.Contains(out, "NAME") || !strings.Contains(out, "application") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	out, err = runRoot(t, path, "app", "publish", "shop", "--mode", "public")
	if err != nil || publishMethod != http.MethodPost || publishBody != `{"mode":"public"}` || !strings.Contains(out, "shop: public  https://shop.example.com") {
		t.Fatalf("publish public: %v method=%s body=%s out=%q", err, publishMethod, publishBody, out)
	}
	out, err = runRoot(t, path, "app", "publish", "shop", "--mode", "private")
	if err != nil || publishMethod != http.MethodDelete || !strings.Contains(out, "shop: private") {
		t.Fatalf("publish private: %v method=%s out=%q", err, publishMethod, out)
	}
	if _, err := runRoot(t, path, "app", "publish", "shop", "--mode", "members"); err == nil {
		t.Fatal("expected an invalid --mode to be rejected")
	}

	out, err = runRoot(t, path, "app", "promote", "shop", "--hostname-prefix", "shop")
	if err != nil || promote.Values["expose"] == nil || !strings.Contains(out, "promoted shop to shop-prod (commit abc, rollout r1)") {
		t.Fatalf("promote: %v values=%v out=%q", err, promote.Values, out)
	}
}

func TestAppSyncCommand(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	var calls []string
	hub.handle("POST "+appStudioPrefix+"/shop/hydrate-workspace", func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		calls = append(calls, "hydrate "+b.String())
		writeTestJSON(w, map[string]any{"repositoryRef": "shop", "ref": "main", "commitSHA": "1234567890ab", "written": []string{"api/index.js", "web/index.html"}})
	})
	hub.handle("POST "+appStudioPrefix+"/shop/sync-development", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "sync")
		writeTestJSON(w, map[string]any{
			"target": map[string]any{"ResourceName": "shop-dev"},
			"result": map[string]any{
				"web": map[string]any{"phase": "Synced", "changed": []string{"index.html"}, "restarted": true, "sourceRevision": 7,
					"skipped": []map[string]any{{"path": "public/logo.png", "reason": "binary-unsupported"}}},
				"api": map[string]any{"phase": "Synced", "changed": []string{"index.js"}, "sourceRevision": 7},
			},
		})
	})

	out, err := runRoot(t, path, "app", "sync", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"hydrate {}", "sync"}) {
		t.Fatalf("calls = %q, want hydrate with {} then sync", calls)
	}
	for _, want := range []string{
		"workspace: loaded from shop@main (1234567), 2 written, 0 skipped",
		"shop-dev/api: Synced, 1 changed, 0 deleted, restarted=false, revision 7\n",
		"shop-dev/web: Synced, 1 changed, 0 deleted, restarted=true, revision 7, 1 skipped",
		"  skipped public/logo.png (binary-unsupported)",
		"dev agent does not accept binary files",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "shop-dev/api") > strings.Index(out, "shop-dev/web") {
		t.Fatalf("components not sorted:\n%s", out)
	}

	out, err = runRoot(t, path, "app", "sync", "shop", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded appSyncOutput
	if err := json.Unmarshal([]byte(out), &decoded); err != nil || len(decoded.Hydrate) == 0 || !strings.Contains(string(decoded.Sync), "binary-unsupported") {
		t.Fatalf("json output: %v\n%s", err, out)
	}
}

func TestAppCreateConflictShowsServerMessage(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	const msg = `a code Repository named "shop" already exists (possibly left by a deleted project); adopt it with existingRepositoryRef or choose another name`
	hub.handle("POST "+appStudioPrefix, func(w http.ResponseWriter, r *http.Request) {
		writeTestStatus(w, http.StatusConflict, "Conflict", msg)
	})
	_, err := runRoot(t, path, "app", "create", "shop", "--template", "application")
	if err == nil {
		t.Fatal("expected a 409 to fail create")
	}
	if want := `project "shop" not created (HTTP 409): ` + msg; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

func TestRepositoryStallHint(t *testing.T) {
	now := time.Date(2026, 9, 11, 14, 10, 0, 0, time.UTC)
	fresh := appProjectView{Name: "p", CreatedAt: now.Add(-30 * time.Second), Repository: &appRepositoryView{Ref: "p"}}
	if hint := repositoryStallHint(fresh, now); hint != "" {
		t.Fatalf("a 30 s old project is latency, got %q", hint)
	}
	stalled := appProjectView{Name: "p", CreatedAt: now.Add(-5 * time.Minute), Repository: &appRepositoryView{Ref: "p"}}
	hint := repositoryStallHint(stalled, now)
	for _, want := range []string{"not ready for 5m0s", "not reconciling", "repositories.code.faros.sh p", "don't recreate"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q lacks %q", hint, want)
		}
	}
	// A repository that reports its own message, has a commit, or is ready
	// is not a stall.
	for name, r := range map[string]*appRepositoryView{
		"message": {Ref: "p", Message: "Creating repository \"p\"."},
		"commit":  {Ref: "p", Commits: []appRepositoryCommitView{{Name: "c", Phase: "Succeeded"}}},
		"ready":   {Ref: "p", Ready: true},
		"none":    nil,
	} {
		p := stalled
		p.Repository = r
		if hint := repositoryStallHint(p, now); hint != "" {
			t.Fatalf("%s: unexpected hint %q", name, hint)
		}
	}

	// And printAppStatus surfaces it under the Repository line.
	st := appStatus{Project: json.RawMessage(`{"name":"p","createdAt":"2026-09-11T14:00:00Z","repository":{"ref":"p","status":"Provisioning"}}`)}
	var buf bytes.Buffer
	if err := printAppStatus(&buf, st, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "not reconciling") {
		t.Fatalf("status lacks the stall hint:\n%s", buf.String())
	}
}
