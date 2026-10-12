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
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// webhookPathPrefix is where the hub proxies a Webhook's inbound URL
// (providers/agents/internal/webhookpath.PathPrefix).
const webhookPathPrefix = "/services/providers/agents/webhooks/triggers/"

// Tenant state shared by the ordered tests, set by TestBTenantEnable.
var (
	orgUUID       string
	workspaceUUID string
	tenantCluster string
)

// route is one trigger's verdict on one delivery, as the inbound answer, the
// webhooks/run answer and the sink all spell it.
type route struct {
	Trigger string `json:"trigger"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	RunID   string `json:"runID,omitempty"`
}

// deliveryAnswer is what a sender (or webhooks/run) is told.
type deliveryAnswer struct {
	DeliveryID string  `json:"deliveryID,omitempty"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason,omitempty"`
	Routes     []route `json:"routes"`
}

// triggerReplayAnswer is the answer of triggers/{name}/run with a deliveryID.
type triggerReplayAnswer struct {
	DeliveryID string `json:"deliveryID,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	RunID      string `json:"runID,omitempty"`
}

// delivery is one sink row.
type delivery struct {
	ID            string            `json:"id"`
	WebhookName   string            `json:"webhookName"`
	WebhookUID    string            `json:"webhookUID"`
	DeliveryID    string            `json:"deliveryID,omitempty"`
	ReceivedAt    time.Time         `json:"receivedAt"`
	Outcome       string            `json:"outcome"`
	Reason        string            `json:"reason,omitempty"`
	EventType     string            `json:"eventType,omitempty"`
	Action        string            `json:"action,omitempty"`
	Repository    string            `json:"repository,omitempty"`
	ContentType   string            `json:"contentType,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Body          string            `json:"body,omitempty"`
	BodySize      int               `json:"bodySize"`
	BodyTruncated bool              `json:"bodyTruncated,omitempty"`
	ReplayOf      string            `json:"replayOf,omitempty"`
	Routes        []route           `json:"routes"`
}

type deliveryList struct {
	Items     []delivery `json:"items"`
	Retention int        `json:"retention"`
}

// verdict is the answer of triggers/{name}/evaluate.
type verdict struct {
	Matched bool   `json:"matched"`
	Reason  string `json:"reason,omitempty"`
	Keys    []struct {
		Key     string `json:"key"`
		Want    string `json:"want"`
		Got     string `json:"got,omitempty"`
		Matched bool   `json:"matched"`
		Known   bool   `json:"known"`
		Reason  string `json:"reason,omitempty"`
	} `json:"keys"`
	Extracted struct {
		EventType  string `json:"eventType,omitempty"`
		Action     string `json:"action,omitempty"`
		Repository string `json:"repository,omitempty"`
	} `json:"extracted"`
	DeliveryID string            `json:"deliveryID,omitempty"`
	Filter     map[string]string `json:"filter"`
}

func requireTenant(t *testing.T) {
	t.Helper()
	if tenantCluster == "" {
		t.Fatal("no tenant workspace; TestBTenantEnable must run first (do not use -run to select a later test alone)")
	}
}

func tenant(t *testing.T) dynamic.Interface {
	t.Helper()
	requireTenant(t)
	return kcpDynamic(t, tenantCluster, staticToken)
}

// hubDo issues a request against the hub. org/workspace, when set, are sent as
// the tenant-scope headers the hub's REST middleware reads; an empty token
// sends no Authorization at all.
func hubDo(t *testing.T, method, path, token, org, workspace string, body []byte, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), method, hubURL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Railgrid-Org", org)
	}
	if workspace != "" {
		req.Header.Set("X-Railgrid-Workspace", workspace)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// verbPath is a kcp custom subresource on the agents export, addressed on the
// hub's kcp front door by the tenant's logical cluster.
func verbPath(resource, name, verb, tail string, query url.Values) string {
	p := "/clusters/" + tenantCluster + "/apis/" + agentsGroup + "/v1alpha1/" + resource + "/" + name + "/" + verb
	if tail != "" {
		p += "/" + tail
	}
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	return p
}

// callVerb invokes a verb as the tenant (the static token). body, when not
// nil, is JSON-encoded.
func callVerb(t *testing.T, method, resource, name, verb, tail string, query url.Values, body any) (int, []byte) {
	t.Helper()
	requireTenant(t)
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatalf("marshal verb body: %v", err)
		}
	}
	return hubDo(t, method, verbPath(resource, name, verb, tail, query), staticToken, "", "", raw, nil)
}

// postInbound POSTs a delivery to a webhook URL through the hub, anonymously:
// the token in the path is the whole credential.
func postInbound(t *testing.T, path string, headers map[string]string, body string) (int, deliveryAnswer, []byte) {
	t.Helper()
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		h[k] = v
	}
	status, raw := hubDo(t, http.MethodPost, path, "", "", "", []byte(body), h)
	var ans deliveryAnswer
	if status == http.StatusAccepted || status == http.StatusServiceUnavailable {
		if err := json.Unmarshal(raw, &ans); err != nil {
			t.Fatalf("decode inbound answer (%d): %v body=%s", status, err, truncate(raw))
		}
	}
	return status, ans, raw
}

// listDeliveries reads a webhook's sink through the deliveries verb.
func listDeliveries(t *testing.T, webhook string, query url.Values) deliveryList {
	t.Helper()
	status, raw := callVerb(t, http.MethodGet, "webhooks", webhook, "deliveries", "", query, nil)
	if status != http.StatusOK {
		t.Fatalf("GET webhooks/%s/deliveries?%s = %d: %s", webhook, query.Encode(), status, truncate(raw))
	}
	var out deliveryList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode deliveries: %v body=%s", err, truncate(raw))
	}
	return out
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %T: %v body=%s", out, err, truncate(raw))
	}
	return out
}

// expectedWebhookPath recomputes the inbound path the provider mints, from the
// key the suite gave it (internal/webhookpath: first 32 hex chars of
// HMAC-SHA256(key, "<cluster>/<name>")).
func expectedWebhookPath(cluster, name string) string {
	mac := hmac.New(sha256.New, []byte(webhookKey))
	mac.Write([]byte(cluster + "/" + name))
	return webhookPathPrefix + cluster + "/" + name + "/" + hex.EncodeToString(mac.Sum(nil))[:32]
}

func createObject(t *testing.T, gvr schema.GroupVersionResource, kind, name string, spec map[string]any) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.Group + "/" + gvr.Version,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if _, err := tenant(t).Resource(gvr).Create(ctxWithTimeout(t, 15*time.Second), obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s %s: %v", kind, name, err)
	}
}

func mergePatch(t *testing.T, gvr schema.GroupVersionResource, name string, patch map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(patch)
	if _, err := tenant(t).Resource(gvr).Patch(ctxWithTimeout(t, 15*time.Second), name, types.MergePatchType, raw, metav1.PatchOptions{}); err != nil {
		t.Fatalf("patch %s %s: %v", gvr.Resource, name, err)
	}
}

func getObject(t *testing.T, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
	t.Helper()
	return tenant(t).Resource(gvr).Get(ctxWithTimeout(t, 10*time.Second), name, metav1.GetOptions{})
}

// condition returns one status condition's status, reason and message.
func condition(u *unstructured.Unstructured, condType string) (status, reason, message string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != condType {
			continue
		}
		status, _ = m["status"].(string)
		reason, _ = m["reason"].(string)
		message, _ = m["message"].(string)
		return
	}
	return "", "", ""
}

// waitValidated waits for an object's Validated condition to read want with
// the given reason ("" accepts any) and returns its message.
func waitValidated(t *testing.T, gvr schema.GroupVersionResource, name, want, wantReason string) string {
	t.Helper()
	var msg string
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		u, err := getObject(t, gvr, name)
		if err != nil {
			return false, err.Error()
		}
		status, reason, message := condition(u, "Validated")
		msg = message
		return status == want && (wantReason == "" || reason == wantReason),
			fmt.Sprintf("Validated=%q reason=%q message=%q", status, reason, message)
	}) {
		t.Fatalf("%s %s never reached Validated=%s (reason %q)", gvr.Resource, name, want, wantReason)
	}
	return msg
}

// waitWebhookPath waits for the reconciler to mint status.webhookPath.
func waitWebhookPath(t *testing.T, name string) string {
	t.Helper()
	var path string
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		u, err := getObject(t, webhookGVR, name)
		if err != nil {
			return false, err.Error()
		}
		path, _, _ = unstructured.NestedString(u.Object, "status", "webhookPath")
		return path != "", "status.webhookPath still empty"
	}) {
		t.Fatalf("Webhook %s never got status.webhookPath", name)
	}
	return path
}

// webhookRoutes reads status.routes names in order.
func webhookRoutes(u *unstructured.Unstructured) []string {
	routes, _, _ := unstructured.NestedSlice(u.Object, "status", "routes")
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		if m, ok := r.(map[string]any); ok {
			name, _ := m["name"].(string)
			out = append(out, name)
		}
	}
	return out
}

func waitGone(t *testing.T, gvr schema.GroupVersionResource, name string, timeout time.Duration) {
	t.Helper()
	if !waitForCondition(t, timeout, func() (bool, string) {
		u, err := getObject(t, gvr, name)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("still present (deletionTimestamp=%v finalizers=%v)", u.GetDeletionTimestamp(), u.GetFinalizers())
	}) {
		t.Fatalf("%s %s was never deleted", gvr.Resource, name)
	}
}

func routeNames(routes []route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Trigger)
	}
	return out
}

func routeByTrigger(t *testing.T, routes []route, trigger string) route {
	t.Helper()
	for _, r := range routes {
		if r.Trigger == trigger {
			return r
		}
	}
	t.Fatalf("no route for trigger %s in %+v", trigger, routes)
	return route{}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// truncate keeps failure output readable when a handler returns a large body.
func truncate(b []byte) string {
	const max = 768
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// login performs the static-token login, which materializes the user, their
// personal org and their default workspace. The hub reports /readyz before the
// users binding settles, so the first logins can 500; retry.
func login(t *testing.T) {
	t.Helper()
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		code, raw := hubDo(t, http.MethodPost, "/auth/token-login", staticToken, "", "", nil, nil)
		return code == http.StatusOK, fmt.Sprintf("status %d: %s", code, truncate(raw))
	}) {
		t.Fatal("token-login never succeeded")
	}
}

// personalOrgUUID returns the static-token user's personal Organization.
func personalOrgUUID(t *testing.T) string {
	t.Helper()
	login(t)
	var orgs struct {
		Items []struct {
			UUID     string `json:"uuid"`
			Personal bool   `json:"personal"`
		} `json:"items"`
	}
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		code, raw := hubDo(t, http.MethodGet, "/api/orgs", staticToken, "", "", nil, nil)
		if code != http.StatusOK {
			return false, fmt.Sprintf("status %d: %s", code, truncate(raw))
		}
		if err := json.Unmarshal(raw, &orgs); err != nil {
			return false, err.Error()
		}
		return len(orgs.Items) > 0, "no orgs yet"
	}) {
		t.Fatal("the static-token user never got an organization")
	}
	for _, org := range orgs.Items {
		if org.Personal {
			return org.UUID
		}
	}
	return orgs.Items[0].UUID
}

func createWorkspace(t *testing.T, org, displayName string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"displayName": displayName})
	code, raw := hubDo(t, http.MethodPost, "/api/orgs/"+org+"/workspaces", staticToken, org, "", body, nil)
	if code != http.StatusCreated {
		t.Fatalf("create workspace = %d: %s", code, truncate(raw))
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.UUID == "" {
		t.Fatalf("decode workspace create: %v (body %s)", err, truncate(raw))
	}
	return out.UUID
}

// workspaceCluster resolves the workspace's kcp logical cluster off the child
// Workspace's spec.cluster, the field the hub's bootstrapper reads.
func workspaceCluster(t *testing.T, org, workspace string) string {
	t.Helper()
	orgClient := kcpDynamic(t, "root:railgrid:tenants:"+org, adminToken)
	var cluster string
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		ws, err := orgClient.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), workspace, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		cluster, _, _ = unstructured.NestedString(ws.Object, "spec", "cluster")
		return cluster != "", "spec.cluster still empty (workspace not Ready)"
	}) {
		t.Fatalf("workspace %s never reported spec.cluster", workspace)
	}
	return cluster
}

// enablePath is the hub's Enable/Disable endpoint for a provider.
func enablePath(name, action string) string {
	return fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/%s/%s", orgUUID, workspaceUUID, name, action)
}

// enableProvider drives POST .../providers/{name}/enable as the tenant and
// waits for the binding to reach Bound. The catalog controller registers
// providers on its own cadence, so a refusal right after startup is retried.
func enableProvider(t *testing.T, name string, acceptedClaims []map[string]string) *unstructured.Unstructured {
	t.Helper()
	if acceptedClaims == nil {
		acceptedClaims = []map[string]string{}
	}
	body, _ := json.Marshal(map[string]any{"acceptedClaims": acceptedClaims})
	var code int
	var raw []byte
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		code, raw = hubDo(t, http.MethodPost, enablePath(name, "enable"), staticToken, orgUUID, workspaceUUID, body, nil)
		return code == http.StatusOK || code == http.StatusCreated, fmt.Sprintf("status %d: %s", code, truncate(raw))
	}) {
		t.Fatalf("enable %s never succeeded: last status %d: %s", name, code, truncate(raw))
	}
	var bound *unstructured.Unstructured
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		got, err := tenant(t).Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		bound = got
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatalf("%s APIBinding never reached Bound", name)
	}
	return bound
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func hasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
