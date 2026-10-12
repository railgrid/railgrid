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

package agentsprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// State the ordered tests hand forward.
var (
	ghPath          string // Webhook gh's status.webhookPath
	firstDeliveryID string // the sink row of the first push to gh
	firstRunID      string // the Run that push fired through on-push
	pushBody        string // exactly what was POSTed as that push
	lonelyPath      string // Webhook lonely's status.webhookPath
)

// The filter on-pr carries. Keys are judged in sorted order, so on a push
// (no "action" in the body) the FIRST failing key — the reason the route and
// the rollup report — is action, not eventType. That is the matcher's
// documented contract (triggerfilter.Evaluate: "the first failing entry's
// reason"), so the assertions below spell it.
const onPRFilteredReason = "action does not match opened"

// TestAProvisioning asserts what provisioning and `agents-provider init` leave
// behind, and that the hub's catalog lists agents Ready with the Webhooks
// sidebar entry.
func TestAProvisioning(t *testing.T) {
	sys := kcpDynamic(t, "root:railgrid:system:providers", adminToken)
	if _, err := sys.Resource(providerGVR).Get(ctxWithTimeout(t, 10*time.Second), providerName, metav1.GetOptions{}); err != nil {
		t.Fatalf("Provider/agents not found in root:railgrid:system:providers: %v", err)
	}

	sub := kcpDynamic(t, workspacePath, adminToken)
	export, err := sub.Resource(apiExportGVR).Get(ctxWithTimeout(t, 10*time.Second), apiExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("APIExport %s not found in %s: %v", apiExportName, workspacePath, err)
	}
	resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	served := map[string]bool{}
	for _, r := range resources {
		if m, ok := r.(map[string]any); ok && m["group"] == agentsGroup {
			name, _ := m["name"].(string)
			served[name] = true
		}
	}
	for _, want := range []string{
		"webhooks", "triggers", "agents", "runs",
		"webhooks/deliveries", "webhooks/run",
		"triggers/run", "triggers/evaluate", "triggers/enable",
	} {
		if !served[want] {
			t.Errorf("APIExport %s does not serve %s; served: %v", apiExportName, want, served)
		}
	}

	login(t)
	type navChild struct {
		DisplayName  string `json:"displayName"`
		BuiltinRoute string `json:"builtinRoute"`
	}
	var agents struct {
		Name    string `json:"name"`
		Ready   bool   `json:"ready"`
		Reason  string `json:"readinessReason"`
		Message string `json:"readinessMessage"`
		Serving struct {
			UI struct {
				Children []navChild `json:"children"`
			} `json:"ui"`
		} `json:"serving"`
	}
	if !waitForCondition(t, 3*time.Minute, func() (bool, string) {
		code, raw := hubDo(t, http.MethodGet, "/api/providers", staticToken, "", "", nil, nil)
		if code != http.StatusOK {
			return false, fmt.Sprintf("GET /api/providers = %d: %s", code, truncate(raw))
		}
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return false, err.Error()
		}
		for _, item := range list.Items {
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(item, &p)
			if p.Name == providerName {
				_ = json.Unmarshal(item, &agents)
				return agents.Ready, fmt.Sprintf("agents ready=%v reason=%q message=%q", agents.Ready, agents.Reason, agents.Message)
			}
		}
		return false, "agents not listed yet"
	}) {
		t.Fatal("/api/providers never listed agents as Ready")
	}
	found := false
	for _, c := range agents.Serving.UI.Children {
		if c.DisplayName == "Webhooks" && c.BuiltinRoute == "webhooks" {
			found = true
		}
	}
	if !found {
		t.Errorf("agents serving.ui.children = %+v, want a Webhooks/webhooks entry", agents.Serving.UI.Children)
	}
}

// TestBTenantEnable creates an org workspace through the hub REST API and
// enables agents in it through the hub's own Enable endpoint — after its two
// declared dependencies, which the endpoint otherwise refuses it without.
func TestBTenantEnable(t *testing.T) {
	orgUUID = personalOrgUUID(t)
	workspaceUUID = createWorkspace(t, orgUUID, "agents-e2e")
	tenantCluster = workspaceCluster(t, orgUUID, workspaceUUID)
	t.Logf("org=%s workspace=%s cluster=%s", orgUUID, workspaceUUID, tenantCluster)

	// The dependency edge is real: agents is refused while neither is bound.
	body, _ := json.Marshal(map[string]any{"acceptedClaims": []any{}})
	code, raw := hubDo(t, http.MethodPost, enablePath(providerName, "enable"), staticToken, orgUUID, workspaceUUID, body, nil)
	if code != http.StatusConflict || !strings.Contains(string(raw), "infrastructure") || !strings.Contains(string(raw), "edges") {
		t.Fatalf("enable agents before its dependencies = %d %s, want 409 naming infrastructure and edges", code, truncate(raw))
	}

	for _, s := range standIns {
		enableProvider(t, s.name, nil)
	}

	// Accept the two platform builtins the provider's own machinery needs;
	// leave the infrastructure / edges compositions declined (Rejected).
	binding := enableProvider(t, providerName, []map[string]string{
		{"group": "authorization.k8s.io", "resource": "subjectaccessreviews"},
		{"group": "", "resource": "secrets"},
	})

	var bound []string
	boundResources, _, _ := unstructured.NestedSlice(binding.Object, "status", "boundResources")
	for _, r := range boundResources {
		if m, ok := r.(map[string]any); ok {
			name, _ := m["resource"].(string)
			bound = append(bound, name)
		}
	}
	if !containsAll(bound, "webhooks", "triggers", "agents", "runs") {
		t.Errorf("agents APIBinding status.boundResources = %v, want webhooks, triggers, agents and runs among them", bound)
	}

	// A declined foreign claim does not keep agents' own kinds from binding:
	// the binding is Bound (above) and the claims say what was decided.
	claims, _, _ := unstructured.NestedSlice(binding.Object, "spec", "permissionClaims")
	states := map[string]string{}
	for _, c := range claims {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		group, _ := m["group"].(string)
		resource, _ := m["resource"].(string)
		state, _ := m["state"].(string)
		states[group+"/"+resource] = state
	}
	t.Logf("agents binding permission claims: %v", states)
	for key, want := range map[string]string{
		"authorization.k8s.io/subjectaccessreviews": "Accepted",
		"/secrets":                             "Accepted",
		"infrastructure.railgrid.ai/instances": "Rejected",
		"edges.railgrid.ai/linuxservers":       "Rejected",
	} {
		if states[key] != want {
			t.Errorf("binding claim %s state = %q, want %q", key, states[key], want)
		}
	}
	conds, _, _ := unstructured.NestedSlice(binding.Object, "status", "conditions")
	t.Logf("agents binding conditions: %v", conds)

	// The kinds must actually answer before the next test writes them.
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		if _, err := tenant(t).Resource(webhookGVR).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{}); err != nil {
			return false, "webhooks: " + err.Error()
		}
		if _, err := tenant(t).Resource(triggerGVR).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{}); err != nil {
			return false, "triggers: " + err.Error()
		}
		return true, ""
	}) {
		t.Fatal("webhooks/triggers never became servable in the tenant workspace")
	}
}

// TestCWebhookMinted creates a Webhook and waits for the reconciler to mint
// its inbound URL and validate it.
func TestCWebhookMinted(t *testing.T) {
	requireTenant(t)
	createObject(t, webhookGVR, "Webhook", "gh", map[string]any{"source": "github", "displayName": "GitHub e2e"})
	ghPath = waitWebhookPath(t, "gh")
	if prefix := webhookPathPrefix + tenantCluster + "/gh/"; !strings.HasPrefix(ghPath, prefix) {
		t.Fatalf("status.webhookPath = %q, want prefix %q", ghPath, prefix)
	}
	if want := expectedWebhookPath(tenantCluster, "gh"); ghPath != want {
		t.Errorf("status.webhookPath = %q, want %q (HMAC of the configured key)", ghPath, want)
	}
	waitValidated(t, webhookGVR, "gh", "True", "Validated")
	u, err := getObject(t, webhookGVR, "gh")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range u.GetFinalizers() {
		found = found || f == "agents.railgrid.ai/webhook-deliveries"
	}
	if !found {
		t.Errorf("Webhook gh finalizers = %v, want agents.railgrid.ai/webhook-deliveries (the sink purge)", u.GetFinalizers())
	}
}

// TestC2InboundReadyAfterFirstEnable pins that the inbound webhook route
// works in a provider that started BEFORE any tenant had bound its export —
// the normal order on a fresh install, and the order this suite runs in.
//
// It probes with a valid token for a webhook that does not exist, which a
// working provider answers 404. KNOWN PROVIDER BUG (reported with this
// suite): at startup the endpoint slice has no endpoints yet, background.run
// logs "it will be rediscovered on demand", and discovery is indeed only
// triggered from background.shardFor — but webhookInbound, runWebhook,
// replayTriggerDelivery (and the channel inbound and OAuth callback) refuse
// with 503 "background executor is not running on this provider" on
// !bg.ready() BEFORE anything calls shardFor, so the provider never
// rediscovers and the routes stay 503 until it restarts or some other path
// happens to call scoped(). When that is the case this test fails, and then
// restarts the provider (whose startup discovery now finds the endpoint) so
// the rest of the suite still exercises routing.
func TestC2InboundReadyAfterFirstEnable(t *testing.T) {
	requireTenant(t)
	probe := expectedWebhookPath(tenantCluster, "ghost")
	var last int
	var lastBody []byte
	ok := waitForCondition(t, 30*time.Second, func() (bool, string) {
		last, _, lastBody = postInbound(t, probe, nil, `{}`)
		return last == http.StatusNotFound, fmt.Sprintf("POST %s = %d: %s", probe, last, truncate(lastBody))
	})
	if ok {
		return
	}
	t.Errorf("inbound webhook route never became available after the first tenant enabled agents (last %d: %s): "+
		"bg.ready() gates the route before on-demand VW discovery can run (api/webhook_deliveries.go webhookInbound; api/background.go run/shardFor)",
		last, truncate(lastBody))
	if err := restartProvider(); err != nil {
		t.Fatalf("restart provider: %v", err)
	}
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		last, _, lastBody = postInbound(t, probe, nil, `{}`)
		return last == http.StatusNotFound, fmt.Sprintf("POST %s = %d: %s", probe, last, truncate(lastBody))
	}) {
		t.Fatalf("inbound webhook route unavailable even after a provider restart (last %d: %s)", last, truncate(lastBody))
	}
}

// TestDRoutingFanOut routes one webhook to two triggers and checks that a push
// fires exactly the one whose filter matches, records a Run for it, and
// stamps status on the webhook and on both triggers.
func TestDRoutingFanOut(t *testing.T) {
	requireTenant(t)
	if ghPath == "" {
		t.Fatal("no webhook path; TestCWebhookMinted must pass first")
	}
	createObject(t, agentGVR, "Agent", "reviewer", map[string]any{"displayName": "Reviewer"})
	createObject(t, triggerGVR, "Trigger", "on-pr", map[string]any{
		"agentRef": "reviewer", "webhookRef": "gh",
		"filter": map[string]any{"eventType": "pull_request", "action": "opened"},
		"task":   "Review the pull request.",
	})
	createObject(t, triggerGVR, "Trigger", "on-push", map[string]any{
		"agentRef": "reviewer", "webhookRef": "gh",
		"filter": map[string]any{"eventType": "push"},
		"task":   "Summarize the push.",
	})

	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		u, err := getObject(t, webhookGVR, "gh")
		if err != nil {
			return false, err.Error()
		}
		got := webhookRoutes(u)
		return reflect.DeepEqual(got, []string{"on-pr", "on-push"}), fmt.Sprintf("status.routes = %v", got)
	}) {
		t.Fatal("Webhook gh status.routes never became [on-pr on-push]")
	}
	waitValidated(t, triggerGVR, "on-pr", "True", "Validated")
	waitValidated(t, triggerGVR, "on-push", "True", "Validated")

	// Long enough that the sink's list preview (512 bytes) cuts it.
	pushBody = fmt.Sprintf(`{"ref":"refs/heads/main","repository":{"full_name":"railgrid/e2e"},"head_commit":{"message":%q}}`,
		strings.Repeat("e2e commit message ", 40))
	status, ans, raw := postInbound(t, ghPath, map[string]string{
		"X-GitHub-Event":      "push",
		"X-GitHub-Delivery":   "e2e-1",
		"X-Hub-Signature-256": "sha256=0123456789abcdef",
		"User-Agent":          "GitHub-Hookshot/e2e",
	}, pushBody)
	if status != http.StatusAccepted {
		t.Fatalf("POST webhook = %d, want 202: %s", status, truncate(raw))
	}
	if ans.Status != "fired" || ans.DeliveryID == "" {
		t.Fatalf("answer = %+v, want status fired with a deliveryID", ans)
	}
	if got := routeNames(ans.Routes); !reflect.DeepEqual(got, []string{"on-pr", "on-push"}) {
		t.Fatalf("answer routes = %v, want [on-pr on-push]", got)
	}
	pr, push := routeByTrigger(t, ans.Routes, "on-pr"), routeByTrigger(t, ans.Routes, "on-push")
	if push.Outcome != "fired" || push.RunID == "" {
		t.Errorf("on-push route = %+v, want fired with a runID", push)
	}
	if pr.Outcome != "filtered" || pr.Reason != onPRFilteredReason || pr.RunID != "" {
		t.Errorf("on-pr route = %+v, want filtered %q without a run", pr, onPRFilteredReason)
	}
	firstDeliveryID, firstRunID = ans.DeliveryID, push.RunID

	// The fired route recorded a Run object named by its id.
	var run *unstructured.Unstructured
	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		var err error
		run, err = getObject(t, runGVR, firstRunID)
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}) {
		t.Fatalf("Run %s never appeared", firstRunID)
	}
	if v, _, _ := unstructured.NestedString(run.Object, "spec", "agentRef"); v != "reviewer" {
		t.Errorf("Run spec.agentRef = %q, want reviewer", v)
	}
	if v, _, _ := unstructured.NestedString(run.Object, "spec", "trigger"); v != "event" {
		t.Errorf("Run spec.trigger = %q, want event", v)
	}
	if v, _, _ := unstructured.NestedString(run.Object, "spec", "delivery", "sourceName"); v != "on-push" {
		t.Errorf("Run spec.delivery.sourceName = %q, want on-push", v)
	}
	if v, _, _ := unstructured.NestedString(run.Object, "spec", "sessionID"); v != "trigger:on-push/e2e-1" {
		t.Errorf("Run spec.sessionID = %q, want trigger:on-push/e2e-1 (one session per sender delivery)", v)
	}

	// Status on the webhook: the rollup and the counts.
	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		u, err := getObject(t, webhookGVR, "gh")
		if err != nil {
			return false, err.Error()
		}
		last, _, _ := unstructured.NestedMap(u.Object, "status", "lastDelivery")
		ok := last["outcome"] == "fired" && last["deliveryID"] == firstDeliveryID &&
			fmt.Sprint(last["routed"]) == "2" && fmt.Sprint(last["fired"]) == "1"
		received, _, _ := unstructured.NestedString(u.Object, "status", "lastReceived")
		return ok && received != "", fmt.Sprintf("lastDelivery=%v lastReceived=%q", last, received)
	}) {
		t.Error("Webhook gh status.lastDelivery never read fired/routed 2/fired 1 for the delivery")
	}
	// ... and on each trigger, its own route's outcome.
	for name, want := range map[string]string{"on-pr": "filtered", "on-push": "fired"} {
		if !waitForCondition(t, 30*time.Second, func() (bool, string) {
			u, err := getObject(t, triggerGVR, name)
			if err != nil {
				return false, err.Error()
			}
			last, _, _ := unstructured.NestedMap(u.Object, "status", "lastDelivery")
			return last["outcome"] == want && last["deliveryID"] == firstDeliveryID, fmt.Sprintf("lastDelivery=%v", last)
		}) {
			t.Errorf("Trigger %s status.lastDelivery never read %s for the delivery", name, want)
		}
	}
	if u, err := getObject(t, triggerGVR, "on-pr"); err == nil {
		if reason, _, _ := unstructured.NestedString(u.Object, "status", "lastDelivery", "reason"); reason != onPRFilteredReason {
			t.Errorf("Trigger on-pr status.lastDelivery.reason = %q, want %q", reason, onPRFilteredReason)
		}
	}
}

// TestEDeliverySink reads the delivery back through the deliveries verb: the
// list (preview, filters, header hygiene) and one row whole.
func TestEDeliverySink(t *testing.T) {
	requireTenant(t)
	if firstDeliveryID == "" {
		t.Fatal("no delivery; TestDRoutingFanOut must pass first")
	}
	list := listDeliveries(t, "gh", nil)
	if len(list.Items) != 1 {
		t.Fatalf("sink has %d rows, want 1: %+v", len(list.Items), list.Items)
	}
	if list.Retention != 200 {
		t.Errorf("retention = %d, want the default 200", list.Retention)
	}
	d := list.Items[0]
	if d.ID != firstDeliveryID || d.WebhookName != "gh" || d.DeliveryID != "e2e-1" || d.Outcome != "fired" {
		t.Errorf("row = id %q webhook %q sender id %q outcome %q, want %q/gh/e2e-1/fired", d.ID, d.WebhookName, d.DeliveryID, d.Outcome, firstDeliveryID)
	}
	if d.EventType != "push" || d.Repository != "railgrid/e2e" || d.Action != "" {
		t.Errorf("row extracted eventType=%q repository=%q action=%q, want push/railgrid/e2e/empty", d.EventType, d.Repository, d.Action)
	}
	if !strings.HasPrefix(d.ContentType, "application/json") {
		t.Errorf("row contentType = %q", d.ContentType)
	}
	if got := routeNames(d.Routes); !reflect.DeepEqual(got, []string{"on-pr", "on-push"}) {
		t.Errorf("row routes = %v, want [on-pr on-push]", got)
	} else {
		if r := routeByTrigger(t, d.Routes, "on-push"); r.Outcome != "fired" || r.RunID != firstRunID {
			t.Errorf("row on-push route = %+v, want fired run %s", r, firstRunID)
		}
		if r := routeByTrigger(t, d.Routes, "on-pr"); r.Outcome != "filtered" || r.Reason != onPRFilteredReason {
			t.Errorf("row on-pr route = %+v, want filtered %q", r, onPRFilteredReason)
		}
	}
	// The list carries a 512-byte preview and says how long the body is.
	if len(d.Body) != 512 || d.BodySize != len(pushBody) || !strings.HasPrefix(pushBody, d.Body) {
		t.Errorf("list body preview = %d bytes (bodySize %d), want the first 512 of %d", len(d.Body), d.BodySize, len(pushBody))
	}
	// Header hygiene: the sender's headers are kept, credentials redacted, hub
	// and transport plumbing dropped.
	if d.Headers["X-Github-Event"] != "push" || d.Headers["X-Github-Delivery"] != "e2e-1" {
		t.Errorf("recorded headers lack the GitHub ones: %v", d.Headers)
	}
	if v := d.Headers["X-Hub-Signature-256"]; v != "[redacted]" {
		t.Errorf("X-Hub-Signature-256 recorded as %q, want [redacted]", v)
	}
	for _, k := range sortedKeys(d.Headers) {
		if k == "Authorization" || k == "Cookie" || hasPrefixAny(k, "X-Remote-", "X-Railgrid-", "X-Forwarded-") {
			t.Errorf("recorded headers keep %s, which the sink must drop: %v", k, sortedKeys(d.Headers))
		}
	}

	if got := listDeliveries(t, "gh", url.Values{"trigger": {"on-pr"}}); len(got.Items) != 1 {
		t.Errorf("?trigger=on-pr = %d rows, want 1", len(got.Items))
	}
	if got := listDeliveries(t, "gh", url.Values{"trigger": {"nope"}}); len(got.Items) != 0 {
		t.Errorf("?trigger=nope = %d rows, want 0", len(got.Items))
	}
	if got := listDeliveries(t, "gh", url.Values{"outcome": {"filtered"}}); len(got.Items) != 0 {
		t.Errorf("?outcome=filtered = %d rows, want 0", len(got.Items))
	}
	if got := listDeliveries(t, "gh", url.Values{"outcome": {"fired"}}); len(got.Items) != 1 {
		t.Errorf("?outcome=fired = %d rows, want 1", len(got.Items))
	}
	if code, raw := callVerb(t, http.MethodGet, "webhooks", "gh", "deliveries", "", url.Values{"limit": {"zero"}}, nil); code != http.StatusBadRequest {
		t.Errorf("?limit=zero = %d, want 400: %s", code, truncate(raw))
	}

	code, raw := callVerb(t, http.MethodGet, "webhooks", "gh", "deliveries", firstDeliveryID, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("GET deliveries/%s = %d: %s", firstDeliveryID, code, truncate(raw))
	}
	one := decode[delivery](t, raw)
	if one.Body != pushBody {
		t.Errorf("one delivery's body is %d bytes, want the whole %d-byte payload as posted", len(one.Body), len(pushBody))
	}
	if code, raw := callVerb(t, http.MethodGet, "webhooks", "gh", "deliveries", "00000000-0000-0000-0000-000000000000", nil, nil); code != http.StatusNotFound {
		t.Errorf("GET an unknown delivery = %d, want 404: %s", code, truncate(raw))
	}
}

// TestFEvaluate dry-runs filters against the recorded delivery and against a
// spelled-out sample, with the matcher the webhook uses.
func TestFEvaluate(t *testing.T) {
	requireTenant(t)
	if firstDeliveryID == "" {
		t.Fatal("no delivery; TestDRoutingFanOut must pass first")
	}
	evaluate := func(body map[string]any) (int, verdict, []byte) {
		t.Helper()
		code, raw := callVerb(t, http.MethodPost, "triggers", "on-pr", "evaluate", "", nil, body)
		var v verdict
		if code == http.StatusOK {
			v = decode[verdict](t, raw)
		}
		return code, v, raw
	}

	// The trigger's own filter against the push.
	code, v, raw := evaluate(map[string]any{"deliveryID": firstDeliveryID})
	if code != http.StatusOK {
		t.Fatalf("evaluate {deliveryID} = %d: %s", code, truncate(raw))
	}
	if v.Matched || v.Reason != onPRFilteredReason || v.DeliveryID != firstDeliveryID {
		t.Errorf("verdict = matched %v reason %q deliveryID %q, want not matched with %q", v.Matched, v.Reason, v.DeliveryID, onPRFilteredReason)
	}
	if v.Extracted.EventType != "push" || v.Extracted.Repository != "railgrid/e2e" {
		t.Errorf("extracted = %+v, want eventType push, repository railgrid/e2e", v.Extracted)
	}
	if !reflect.DeepEqual(v.Filter, map[string]string{"eventType": "pull_request", "action": "opened"}) {
		t.Errorf("evaluated filter = %v, want the trigger's own", v.Filter)
	}
	sawEventType := false
	for _, k := range v.Keys {
		if k.Key == "eventType" {
			sawEventType = true
			if k.Got != "push" || k.Matched || !k.Known || k.Want != "pull_request" {
				t.Errorf("eventType key = %+v, want want=pull_request got=push matched=false known=true", k)
			}
		}
	}
	if !sawEventType {
		t.Errorf("verdict keys %+v carry no eventType entry", v.Keys)
	}

	// A candidate filter that matches.
	if code, v, raw := evaluate(map[string]any{"deliveryID": firstDeliveryID, "filter": map[string]string{"eventType": "push"}}); code != http.StatusOK || !v.Matched {
		t.Errorf("evaluate {eventType: push} = %d matched %v: %s", code, v.Matched, truncate(raw))
	}

	// A typo is ignored by the matcher, and the verdict says so per key.
	code, v, raw = evaluate(map[string]any{"deliveryID": firstDeliveryID, "filter": map[string]string{"evetType": "pull_request"}})
	if code != http.StatusOK {
		t.Fatalf("evaluate {evetType} = %d: %s", code, truncate(raw))
	}
	if !v.Matched || len(v.Keys) != 1 || v.Keys[0].Key != "evetType" || v.Keys[0].Known || !v.Keys[0].Matched {
		t.Errorf("evaluate {evetType} = %+v, want matched overall with one unknown, non-blocking key", v)
	}

	// A spelled-out sample instead of a recorded delivery.
	code, v, raw = evaluate(map[string]any{"request": map[string]any{
		"headers": map[string]string{"X-GitHub-Event": "pull_request"},
		"body":    `{"action":"opened","repository":{"full_name":"railgrid/e2e"}}`,
	}})
	if code != http.StatusOK || !v.Matched || v.DeliveryID != "" || v.Extracted.Action != "opened" {
		t.Errorf("evaluate {request} = %d %+v, want matched against the sample: %s", code, v, truncate(raw))
	}

	// Neither, both, or a delivery the sink does not hold.
	if code, _, raw := evaluate(map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("evaluate {} = %d, want 400: %s", code, truncate(raw))
	}
	if code, _, raw := evaluate(map[string]any{"deliveryID": firstDeliveryID, "request": map[string]any{"body": "{}"}}); code != http.StatusBadRequest {
		t.Errorf("evaluate {deliveryID, request} = %d, want 400: %s", code, truncate(raw))
	}
	if code, _, raw := evaluate(map[string]any{"deliveryID": "nope"}); code != http.StatusNotFound {
		t.Errorf("evaluate {deliveryID: nope} = %d, want 404: %s", code, truncate(raw))
	}
}

// TestGReplay re-feeds the recorded delivery through one trigger, through the
// whole webhook, and injects a sample — each recorded as a new sink row.
func TestGReplay(t *testing.T) {
	requireTenant(t)
	if firstDeliveryID == "" {
		t.Fatal("no delivery; TestDRoutingFanOut must pass first")
	}

	// Through on-pr alone: it still does not match, which is an answer, not
	// an error.
	code, raw := callVerb(t, http.MethodPost, "triggers", "on-pr", "run", "", nil, map[string]string{"deliveryID": firstDeliveryID})
	if code != http.StatusOK {
		t.Fatalf("triggers/on-pr/run {deliveryID} = %d, want 200: %s", code, truncate(raw))
	}
	prReplay := decode[triggerReplayAnswer](t, raw)
	if prReplay.Status != "filtered" || prReplay.Reason != onPRFilteredReason || prReplay.RunID != "" || prReplay.DeliveryID == "" {
		t.Errorf("on-pr replay = %+v, want filtered %q, no run, a new sink row", prReplay, onPRFilteredReason)
	}

	// Through on-push alone: a NEW run.
	code, raw = callVerb(t, http.MethodPost, "triggers", "on-push", "run", "", nil, map[string]string{"deliveryID": firstDeliveryID})
	if code != http.StatusAccepted {
		t.Fatalf("triggers/on-push/run {deliveryID} = %d, want 202: %s", code, truncate(raw))
	}
	pushReplay := decode[triggerReplayAnswer](t, raw)
	if pushReplay.Status != "fired" || pushReplay.RunID == "" || pushReplay.RunID == firstRunID || pushReplay.DeliveryID == "" {
		t.Errorf("on-push replay = %+v, want fired with a run other than %s", pushReplay, firstRunID)
	}
	if code, raw := callVerb(t, http.MethodPost, "triggers", "on-push", "run", "", nil, map[string]string{"deliveryID": "nope"}); code != http.StatusNotFound {
		t.Errorf("triggers/on-push/run {deliveryID: nope} = %d, want 404: %s", code, truncate(raw))
	}

	// Through the whole webhook: both routes, like the original.
	code, raw = callVerb(t, http.MethodPost, "webhooks", "gh", "run", "", nil, map[string]string{"deliveryID": firstDeliveryID})
	if code != http.StatusAccepted {
		t.Fatalf("webhooks/gh/run {deliveryID} = %d, want 202: %s", code, truncate(raw))
	}
	whReplay := decode[deliveryAnswer](t, raw)
	if whReplay.Status != "fired" || !reflect.DeepEqual(routeNames(whReplay.Routes), []string{"on-pr", "on-push"}) {
		t.Errorf("webhook replay = %+v, want fired with routes [on-pr on-push]", whReplay)
	} else {
		if r := routeByTrigger(t, whReplay.Routes, "on-push"); r.Outcome != "fired" || r.RunID == "" || r.RunID == firstRunID || r.RunID == pushReplay.RunID {
			t.Errorf("webhook replay on-push route = %+v, want a third, distinct run", r)
		}
		if r := routeByTrigger(t, whReplay.Routes, "on-pr"); r.Outcome != "filtered" {
			t.Errorf("webhook replay on-pr route = %+v, want filtered", r)
		}
	}
	if code, raw := callVerb(t, http.MethodPost, "webhooks", "gh", "run", "", nil, map[string]string{}); code != http.StatusBadRequest {
		t.Errorf("webhooks/gh/run {} = %d, want 400: %s", code, truncate(raw))
	}

	// Four rows now; the three replays point at the original, and a
	// trigger-only replay records only that trigger's route.
	list := listDeliveries(t, "gh", nil)
	if len(list.Items) != 4 {
		t.Fatalf("sink has %d rows after three replays, want 4", len(list.Items))
	}
	byID := map[string]delivery{}
	for i, d := range list.Items {
		byID[d.ID] = d
		if i < 3 && d.ReplayOf != firstDeliveryID {
			t.Errorf("row %d (%s) replayOf = %q, want %q", i, d.ID, d.ReplayOf, firstDeliveryID)
		}
	}
	if list.Items[3].ID != firstDeliveryID || list.Items[3].ReplayOf != "" {
		t.Errorf("oldest row = %s replayOf %q, want the original with no replayOf", list.Items[3].ID, list.Items[3].ReplayOf)
	}
	if d := byID[prReplay.DeliveryID]; !reflect.DeepEqual(routeNames(d.Routes), []string{"on-pr"}) || d.Outcome != "filtered" {
		t.Errorf("on-pr replay row = routes %v outcome %q, want [on-pr] filtered", routeNames(d.Routes), d.Outcome)
	}
	if d := byID[pushReplay.DeliveryID]; !reflect.DeepEqual(routeNames(d.Routes), []string{"on-push"}) || d.Outcome != "fired" {
		t.Errorf("on-push replay row = routes %v outcome %q, want [on-push] fired", routeNames(d.Routes), d.Outcome)
	}
	if d := byID[whReplay.DeliveryID]; !reflect.DeepEqual(routeNames(d.Routes), []string{"on-pr", "on-push"}) {
		t.Errorf("webhook replay row routes = %v, want [on-pr on-push]", routeNames(d.Routes))
	}

	// A spelled-out sample: a pull request opens, so on-pr fires and on-push
	// does not.
	code, raw = callVerb(t, http.MethodPost, "webhooks", "gh", "run", "", nil, map[string]any{
		"headers": map[string]string{"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "e2e-sample"},
		"body":    map[string]any{"action": "opened", "repository": map[string]any{"full_name": "railgrid/e2e"}},
	})
	if code != http.StatusAccepted {
		t.Fatalf("webhooks/gh/run {sample} = %d, want 202: %s", code, truncate(raw))
	}
	sample := decode[deliveryAnswer](t, raw)
	if sample.Status != "fired" {
		t.Errorf("sample answer status = %q, want fired", sample.Status)
	}
	if r := routeByTrigger(t, sample.Routes, "on-pr"); r.Outcome != "fired" || r.RunID == "" {
		t.Errorf("sample on-pr route = %+v, want fired with a run", r)
	}
	if r := routeByTrigger(t, sample.Routes, "on-push"); r.Outcome != "filtered" || r.Reason != "eventType does not match push" {
		t.Errorf("sample on-push route = %+v, want filtered \"eventType does not match push\"", r)
	}
	code, raw = callVerb(t, http.MethodGet, "webhooks", "gh", "deliveries", sample.DeliveryID, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("GET the sample row = %d: %s", code, truncate(raw))
	}
	if d := decode[delivery](t, raw); d.ReplayOf != "" || d.DeliveryID != "e2e-sample" || d.EventType != "pull_request" || d.Action != "opened" {
		t.Errorf("sample row = replayOf %q sender id %q eventType %q action %q, want a fresh pull_request/opened row", d.ReplayOf, d.DeliveryID, d.EventType, d.Action)
	}
}

// TestHPausedAndUnrouted covers a paused route, a webhook nobody routes from,
// and the two refusals of the inbound URL.
func TestHPausedAndUnrouted(t *testing.T) {
	requireTenant(t)
	if ghPath == "" {
		t.Fatal("no webhook path; TestCWebhookMinted must pass first")
	}

	mergePatch(t, triggerGVR, "on-push", map[string]any{"spec": map[string]any{"suspend": true}})
	status, ans, raw := postInbound(t, ghPath, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "e2e-2"},
		`{"ref":"refs/heads/main","repository":{"full_name":"railgrid/e2e"}}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST while on-push is paused = %d, want 202: %s", status, truncate(raw))
	}
	if ans.Status != "filtered" || ans.Reason != "none of 2 triggers fired" {
		t.Errorf("rollup = %q %q, want filtered \"none of 2 triggers fired\"", ans.Status, ans.Reason)
	}
	if r := routeByTrigger(t, ans.Routes, "on-push"); r.Outcome != "suspended" || r.Reason != "trigger is paused" || r.RunID != "" {
		t.Errorf("on-push route = %+v, want suspended \"trigger is paused\"", r)
	}
	if r := routeByTrigger(t, ans.Routes, "on-pr"); r.Outcome != "filtered" {
		t.Errorf("on-pr route = %+v, want filtered", r)
	}
	mergePatch(t, triggerGVR, "on-push", map[string]any{"spec": map[string]any{"suspend": false}})

	// A webhook no trigger routes from still records what arrived.
	createObject(t, webhookGVR, "Webhook", "lonely", map[string]any{"source": "webhook"})
	lonelyPath = waitWebhookPath(t, "lonely")
	status, ans, raw = postInbound(t, lonelyPath, map[string]string{"X-Event-Type": "ping"}, `{"type":"ping"}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST to lonely = %d, want 202: %s", status, truncate(raw))
	}
	if ans.Status != "unrouted" || ans.Reason != "no trigger routes from this webhook" || len(ans.Routes) != 0 || ans.DeliveryID == "" {
		t.Errorf("lonely answer = %+v, want unrouted, no routes, a sink row", ans)
	}
	if !strings.Contains(string(raw), `"routes":[]`) {
		t.Errorf("an unrouted answer must carry an empty routes list, not omit it: %s", truncate(raw))
	}
	list := listDeliveries(t, "lonely", nil)
	if len(list.Items) != 1 || list.Items[0].Outcome != "unrouted" || list.Items[0].ID != ans.DeliveryID || list.Items[0].EventType != "ping" {
		t.Fatalf("lonely sink = %+v, want the one unrouted ping", list.Items)
	}

	// A bad token is unauthenticated traffic: refused and not recorded.
	cut := strings.LastIndex(lonelyPath, "/")
	badPath := lonelyPath[:cut+1] + strings.Repeat("0", len(lonelyPath)-cut-1)
	if status, _, raw := postInbound(t, badPath, nil, `{"type":"ping"}`); status != http.StatusForbidden {
		t.Errorf("POST with a bad token = %d, want 403: %s", status, truncate(raw))
	}
	if got := listDeliveries(t, "lonely", nil); len(got.Items) != 1 {
		t.Errorf("lonely sink has %d rows after a bad-token POST, want still 1", len(got.Items))
	}

	// A valid token for a webhook that does not exist.
	if status, _, raw := postInbound(t, expectedWebhookPath(tenantCluster, "ghost"), nil, `{}`); status != http.StatusNotFound {
		t.Errorf("POST to a nonexistent webhook = %d, want 404: %s", status, truncate(raw))
	}
}

// TestIValidation pins the Trigger reconciler's Validated reasons and that a
// fix (or the missing object appearing) flips it back.
func TestIValidation(t *testing.T) {
	requireTenant(t)
	for _, name := range []string{"bad-ref", "bad-agent", "typo"} {
		t.Cleanup(func() {
			_ = tenant(t).Resource(triggerGVR).Delete(ctxWithTimeout(t, 10*time.Second), name, metav1.DeleteOptions{})
		})
	}
	t.Cleanup(func() {
		_ = tenant(t).Resource(webhookGVR).Delete(ctxWithTimeout(t, 10*time.Second), "missing", metav1.DeleteOptions{})
	})

	createObject(t, triggerGVR, "Trigger", "bad-ref", map[string]any{"agentRef": "reviewer", "webhookRef": "missing", "task": "x"})
	msg := waitValidated(t, triggerGVR, "bad-ref", "False", "UnknownWebhookRef")
	if !strings.Contains(msg, `"missing"`) {
		t.Errorf("UnknownWebhookRef message = %q, want it to name the webhook", msg)
	}

	createObject(t, triggerGVR, "Trigger", "bad-agent", map[string]any{"agentRef": "ghost", "webhookRef": "gh", "task": "x"})
	waitValidated(t, triggerGVR, "bad-agent", "False", "UnknownAgentRef")

	createObject(t, triggerGVR, "Trigger", "typo", map[string]any{
		"agentRef": "reviewer", "webhookRef": "gh", "task": "x",
		"filter": map[string]any{"evetType": "push"},
	})
	msg = waitValidated(t, triggerGVR, "typo", "False", "UnknownFilterKey")
	if !strings.Contains(msg, "evetType") || !strings.Contains(msg, "ignored") {
		t.Errorf("UnknownFilterKey message = %q, want it to name evetType and say it is ignored", msg)
	}
	mergePatch(t, triggerGVR, "typo", map[string]any{"spec": map[string]any{"filter": map[string]any{"evetType": nil, "eventType": "push"}}})
	waitValidated(t, triggerGVR, "typo", "True", "Validated")

	// The webhook bad-ref names appears: the Webhook watch re-validates it.
	createObject(t, webhookGVR, "Webhook", "missing", map[string]any{"source": "webhook"})
	waitValidated(t, triggerGVR, "bad-ref", "True", "Validated")
}

// TestJRetention pins spec.deliveryRetention: a cap of 2 keeps the newest two,
// and 0 keeps nothing while still acking.
func TestJRetention(t *testing.T) {
	requireTenant(t)
	createObject(t, webhookGVR, "Webhook", "tiny", map[string]any{"source": "webhook", "deliveryRetention": int64(2)})
	tinyPath := waitWebhookPath(t, "tiny")
	for i := 1; i <= 3; i++ {
		if status, _, raw := postInbound(t, tinyPath, map[string]string{"X-GitHub-Delivery": fmt.Sprintf("t-%d", i)}, fmt.Sprintf(`{"n":%d}`, i)); status != http.StatusAccepted {
			t.Fatalf("POST %d to tiny = %d: %s", i, status, truncate(raw))
		}
	}
	list := listDeliveries(t, "tiny", nil)
	if list.Retention != 2 {
		t.Errorf("tiny retention = %d, want 2", list.Retention)
	}
	var senders []string
	for _, d := range list.Items {
		senders = append(senders, d.DeliveryID)
	}
	if !reflect.DeepEqual(senders, []string{"t-3", "t-2"}) {
		t.Errorf("tiny sink = %v, want the newest two [t-3 t-2]", senders)
	}

	createObject(t, webhookGVR, "Webhook", "none", map[string]any{"source": "webhook", "deliveryRetention": int64(0)})
	nonePath := waitWebhookPath(t, "none")
	status, ans, raw := postInbound(t, nonePath, nil, `{"n":1}`)
	if status != http.StatusAccepted || ans.Status != "unrouted" {
		t.Fatalf("POST to none = %d %+v: %s", status, ans, truncate(raw))
	}
	if ans.DeliveryID != "" || strings.Contains(string(raw), "deliveryID") {
		t.Errorf("a webhook that keeps no sink answered with a deliveryID: %s", truncate(raw))
	}
	if got := listDeliveries(t, "none", nil); len(got.Items) != 0 || got.Retention != 0 {
		t.Errorf("none sink = %d rows retention %d, want 0 and 0", len(got.Items), got.Retention)
	}
}

// TestKDeletion deletes the webhook: the finalizer purges the sink and lets go,
// the verbs and the URL stop answering, the triggers say why, and a webhook
// recreated under the same name starts with an empty sink.
func TestKDeletion(t *testing.T) {
	requireTenant(t)
	if ghPath == "" {
		t.Fatal("no webhook path; TestCWebhookMinted must pass first")
	}
	if err := tenant(t).Resource(webhookGVR).Delete(ctxWithTimeout(t, 10*time.Second), "gh", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete Webhook gh: %v", err)
	}
	waitGone(t, webhookGVR, "gh", 60*time.Second)

	if code, raw := callVerb(t, http.MethodGet, "webhooks", "gh", "deliveries", "", nil, nil); code != http.StatusNotFound {
		t.Errorf("GET webhooks/gh/deliveries after delete = %d, want 404: %s", code, truncate(raw))
	}
	if status, _, raw := postInbound(t, ghPath, map[string]string{"X-GitHub-Event": "push"}, `{}`); status != http.StatusNotFound {
		t.Errorf("POST to the deleted webhook's URL = %d, want 404: %s", status, truncate(raw))
	}
	waitValidated(t, triggerGVR, "on-pr", "False", "UnknownWebhookRef")
	waitValidated(t, triggerGVR, "on-push", "False", "UnknownWebhookRef")

	// Same name, new object: the sink is keyed by UID, so nothing is inherited,
	// while the URL (derived from cluster and name) is the same.
	createObject(t, webhookGVR, "Webhook", "gh", map[string]any{"source": "github"})
	if path := waitWebhookPath(t, "gh"); path != ghPath {
		t.Errorf("recreated gh path = %q, want the same %q", path, ghPath)
	}
	if got := listDeliveries(t, "gh", nil); len(got.Items) != 0 {
		t.Errorf("recreated gh inherited %d sink rows, want none", len(got.Items))
	}
	waitValidated(t, triggerGVR, "on-pr", "True", "Validated")
}

// TestLTenantDisable disables agents through the hub, mirroring the kuery
// suite's last test: the binding goes, and the agents kinds with it.
func TestLTenantDisable(t *testing.T) {
	requireTenant(t)
	code, raw := hubDo(t, http.MethodPost, enablePath(providerName, "disable"), staticToken, orgUUID, workspaceUUID, nil, nil)
	if code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("disable agents = %d: %s", code, truncate(raw))
	}
	if !waitForCondition(t, 3*time.Minute, func() (bool, string) {
		got, err := tenant(t).Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), providerName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		return false, fmt.Sprintf("binding still present (deletionTimestamp=%v conditions=%v)", got.GetDeletionTimestamp(), conds)
	}) {
		t.Fatal("agents APIBinding was never removed after disable")
	}
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := tenant(t).Resource(webhookGVR).List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{})
		if err == nil {
			return false, "webhooks still served"
		}
		return apierrors.IsNotFound(err) || meta.IsNoMatchError(err), err.Error()
	}) {
		t.Error("webhooks API still served after agents was disabled")
	}
}
