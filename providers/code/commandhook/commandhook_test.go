// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package commandhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestParseReadsOnlyTheFirstLine(t *testing.T) {
	cases := []struct {
		body    string
		command string
		args    []string
		ok      bool
	}{
		{"/railgrid review", "review", nil, true},
		{"\n  /railgrid review-fix  factory-line \nplease and thanks", "review-fix", []string{"factory-line"}, true},
		{"/railgrid\treview", "review", nil, true},
		{"/railgrid", "", nil, false},
		{"/railgridreview", "", nil, false},
		{"please /railgrid review", "", nil, false},
		{"/railgrid Review", "", nil, false},
		{"/railgrid review $(curl evil)", "", nil, false},
		{"/railgrid review a b c d e f g h i", "", nil, false},
	}
	for _, tc := range cases {
		command, args, reason, ok := Parse(tc.body)
		if ok != tc.ok || command != tc.command || strings.Join(args, ",") != strings.Join(tc.args, ",") {
			t.Errorf("Parse(%q) = (%q, %v, %q, %t)", tc.body, command, args, reason, ok)
		}
		if !ok && reason == "" {
			t.Errorf("Parse(%q) rejected without a reason", tc.body)
		}
	}
}

func TestGitHubSignature(t *testing.T) {
	if !validSignature("It's a Secret to Everybody", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17", []byte("Hello, World!")) {
		t.Fatal("GitHub's documented signature was rejected")
	}
}

func TestObjectNameBoundsLongRepositoryNames(t *testing.T) {
	if name := ObjectName(strings.Repeat("r", 250), 42); len(name) > 253 || !strings.HasSuffix(name, "-comment-42") {
		t.Fatalf("long object name = %q", name)
	}
}

type fakeClients struct{ client dynamic.Interface }

func (f fakeClients) AsProvider(string) (dynamic.Interface, error) { return f.client, nil }

func repository(name, repoID string) *unstructured.Unstructured {
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "code.railgrid.ai/v1alpha1", "kind": "Repository",
		"metadata": map[string]any{"name": name, "uid": "repo-uid"},
		"spec":     map[string]any{"commandsWebhookSecretRef": map[string]any{"name": "test-signing-secret", "namespace": "hooks"}},
		"status":   map[string]any{"repoID": repoID},
	}}
	return repo
}

const signingSecret = "example webhook signing secret"

var deliveryTime = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func webhookSecret(value string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "test-signing-secret", "namespace": "hooks"},
		"data":     map[string]any{"secret": base64.StdEncoding.EncodeToString([]byte(value))},
	}}
}

func deliverySignature(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func deliver(t *testing.T, h http.Handler, path, event, body, signature string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

const commandPayload = `{"action":"created","comment":{"id":77,"created_at":"2026-11-14T11:59:00Z","body":"/railgrid review"},"issue":{"number":5,"pull_request":{"url":"x"}},"repository":{"id":4242}}`

func TestHandlerRecordsACommandOncePerComment(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrs := map[schema.GroupVersionResource]string{repositoriesGVR: "RepositoryList", commandsGVR: "PullRequestCommandList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs, repository("app", "4242"), webhookSecret(signingSecret))
	h := newHandler(fakeClients{client}, func() time.Time { return deliveryTime })
	route := strings.TrimPrefix(Path("cluster", "app"), "/services/providers/code")

	if code := deliver(t, h, route, "issue_comment", commandPayload, ""); code != http.StatusNotFound {
		t.Fatalf("unsigned delivery = %d", code)
	}
	if code := deliver(t, h, route, "ping", `{}`, deliverySignature(signingSecret, `{}`)); code != http.StatusNoContent {
		t.Fatalf("ping = %d", code)
	}
	for _, body := range []string{
		`{"action":"created","comment":{"id":1,"body":"looks good"},"issue":{"number":5,"pull_request":{}},"repository":{"id":4242}}`,
		`{"action":"edited","comment":{"id":1,"body":"/railgrid review"},"issue":{"number":5,"pull_request":{}},"repository":{"id":4242}}`,
		`{"action":"created","comment":{"id":1,"body":"/railgrid review"},"issue":{"number":5},"repository":{"id":4242}}`,
	} {
		if code := deliver(t, h, route, "issue_comment", body, deliverySignature(signingSecret, body)); code != http.StatusAccepted {
			t.Fatalf("ignored delivery = %d", code)
		}
	}
	if code := deliver(t, h, route, "issue_comment", commandPayload+" ", deliverySignature(signingSecret, commandPayload)); code != http.StatusNotFound {
		t.Fatalf("modified delivery = %d", code)
	}
	if code := deliver(t, h, route, "ping", `{}`, ""); code != http.StatusNotFound {
		t.Fatalf("unsigned ping = %d", code)
	}
	stalePayload := strings.Replace(commandPayload, "11:59:00Z", "10:59:00Z", 1)
	if code := deliver(t, h, route, "issue_comment", stalePayload, deliverySignature(signingSecret, stalePayload)); code != http.StatusAccepted {
		t.Fatalf("expired delivery = %d", code)
	}
	if list, _ := client.Resource(commandsGVR).List(context.Background(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("ignored deliveries recorded %d commands", len(list.Items))
	}

	for range 2 { // a redelivery finds the same object
		if code := deliver(t, h, route, "issue_comment", commandPayload, deliverySignature(signingSecret, commandPayload)); code != http.StatusAccepted {
			t.Fatalf("command delivery = %d", code)
		}
	}
	list, err := client.Resource(commandsGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("commands = %d, err %v", len(list.Items), err)
	}
	got := list.Items[0]
	repoRef, _, _ := unstructured.NestedString(got.Object, "spec", "repositoryRef")
	commentID, _, _ := unstructured.NestedInt64(got.Object, "spec", "commentID")
	if got.GetName() != "app-comment-77" || repoRef != "app" || commentID != 77 || got.GetLabels()[RepositoryLabel] != "app" || len(got.GetOwnerReferences()) != 1 {
		t.Fatalf("recorded command = %+v", got.Object)
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "status"); found {
		t.Fatal("the webhook must not write anything a verifier would trust")
	}
}

func TestHandlerRefusesAnotherRepository(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrs := map[schema.GroupVersionResource]string{repositoriesGVR: "RepositoryList", commandsGVR: "PullRequestCommandList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs, repository("app", "9999"), webhookSecret(signingSecret))
	h := newHandler(fakeClients{client}, func() time.Time { return deliveryTime })
	if code := deliver(t, h, strings.TrimPrefix(Path("cluster", "app"), "/services/providers/code"), "issue_comment", commandPayload, deliverySignature(signingSecret, commandPayload)); code != http.StatusNotFound {
		t.Fatalf("mismatched repository = %d", code)
	}
	if code := deliver(t, h, strings.TrimPrefix(Path("cluster", "missing"), "/services/providers/code"), "issue_comment", commandPayload, deliverySignature(signingSecret, commandPayload)); code != http.StatusNotFound {
		t.Fatalf("missing repository = %d", code)
	}
}

func TestHandlerUsesRotatedRepositorySecret(t *testing.T) {
	const rotatedSecret = "rotated webhook signing secret"
	gvrs := map[schema.GroupVersionResource]string{repositoriesGVR: "RepositoryList", commandsGVR: "PullRequestCommandList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, repository("app", "4242"), webhookSecret(rotatedSecret))
	h := newHandler(fakeClients{client}, func() time.Time { return deliveryTime })
	route := strings.TrimPrefix(Path("cluster", "app"), "/services/providers/code")
	if code := deliver(t, h, route, "issue_comment", commandPayload, deliverySignature(signingSecret, commandPayload)); code != http.StatusNotFound {
		t.Fatalf("old secret = %d", code)
	}
	if code := deliver(t, h, route, "issue_comment", commandPayload, deliverySignature(rotatedSecret, commandPayload)); code != http.StatusAccepted {
		t.Fatalf("rotated secret = %d", code)
	}
}

type failingClients struct{}

func (failingClients) AsProvider(string) (dynamic.Interface, error) {
	return nil, errors.New("workspace unavailable")
}

// Unsigned or wrongly signed deliveries reuse one lookup of the Repository
// and its Secret instead of costing two kcp reads each, and every refusal
// before the signature is checked looks the same.
func TestHandlerCachesItsLookupsAndHidesWhatExists(t *testing.T) {
	gvrs := map[schema.GroupVersionResource]string{repositoriesGVR: "RepositoryList", commandsGVR: "PullRequestCommandList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, repository("app", "4242"), webhookSecret(signingSecret))
	reads := 0
	client.PrependReactor("get", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		reads++
		return false, nil, nil
	})
	clock := deliveryTime
	h := newHandler(fakeClients{client}, func() time.Time { return clock })
	route := strings.TrimPrefix(Path("cluster", "app"), "/services/providers/code")
	forged := deliverySignature("not the secret", commandPayload)
	for range 5 {
		if code := deliver(t, h, route, "issue_comment", commandPayload, forged); code != http.StatusNotFound {
			t.Fatalf("forged delivery = %d", code)
		}
	}
	if reads != 2 {
		t.Fatalf("kcp reads for five forged deliveries = %d, want one Repository and one Secret read", reads)
	}
	clock = clock.Add(targetCacheTTL)
	if code := deliver(t, h, route, "issue_comment", commandPayload, forged); code != http.StatusNotFound || reads != 4 {
		t.Fatalf("after the cache window: code=%d reads=%d, want a fresh lookup", code, reads)
	}
	unavailable := newHandler(failingClients{}, func() time.Time { return deliveryTime })
	if code := deliver(t, unavailable, route, "issue_comment", commandPayload, forged); code != http.StatusNotFound {
		t.Fatalf("an unreachable workspace answered %d, want the same 404 as a missing Repository", code)
	}
}
