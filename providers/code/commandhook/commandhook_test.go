// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package commandhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func TestTokensBindWorkspaceAndRepository(t *testing.T) {
	key := []byte("key")
	path := Path(key, "cluster", "app")
	if !strings.HasPrefix(path, PathPrefix+"cluster/app/") {
		t.Fatalf("path = %q", path)
	}
	token := path[strings.LastIndex(path, "/")+1:]
	if !ValidToken(key, "cluster", "app", token) || ValidToken(key, "cluster", "other", token) || ValidToken(key, "other", "app", token) || ValidToken([]byte("rotated"), "cluster", "app", token) {
		t.Fatal("a token must guard exactly one workspace and Repository under one key")
	}
	if Path(nil, "cluster", "app") != "" || ValidToken(nil, "cluster", "app", "") {
		t.Fatal("no key must mean no URL and no accepted delivery")
	}
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
		"status":   map[string]any{"repoID": repoID},
	}}
	return repo
}

func deliver(t *testing.T, h http.Handler, path, event, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

const commandPayload = `{"action":"created","comment":{"id":77,"body":"/railgrid review"},"issue":{"number":5,"pull_request":{"url":"x"}},"repository":{"id":4242}}`

func TestHandlerRecordsACommandOncePerComment(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrs := map[schema.GroupVersionResource]string{repositoriesGVR: "RepositoryList", commandsGVR: "PullRequestCommandList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs, repository("app", "4242"))
	key := []byte("key")
	h := Handler(key, fakeClients{client})
	route := strings.TrimPrefix(Path(key, "cluster", "app"), "/services/providers/code")

	if code := deliver(t, h, route+"x", "issue_comment", commandPayload); code != http.StatusNotFound {
		t.Fatalf("wrong token = %d", code)
	}
	if code := deliver(t, h, route, "ping", `{}`); code != http.StatusNoContent {
		t.Fatalf("ping = %d", code)
	}
	for _, body := range []string{
		`{"action":"created","comment":{"id":1,"body":"looks good"},"issue":{"number":5,"pull_request":{}},"repository":{"id":4242}}`,
		`{"action":"edited","comment":{"id":1,"body":"/railgrid review"},"issue":{"number":5,"pull_request":{}},"repository":{"id":4242}}`,
		`{"action":"created","comment":{"id":1,"body":"/railgrid review"},"issue":{"number":5},"repository":{"id":4242}}`,
	} {
		if code := deliver(t, h, route, "issue_comment", body); code != http.StatusAccepted {
			t.Fatalf("ignored delivery = %d", code)
		}
	}
	if list, _ := client.Resource(commandsGVR).List(context.Background(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("ignored deliveries recorded %d commands", len(list.Items))
	}

	for range 2 { // a redelivery finds the same object
		if code := deliver(t, h, route, "issue_comment", commandPayload); code != http.StatusAccepted {
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
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs, repository("app", "9999"))
	key := []byte("key")
	h := Handler(key, fakeClients{client})
	if code := deliver(t, h, strings.TrimPrefix(Path(key, "cluster", "app"), "/services/providers/code"), "issue_comment", commandPayload); code != http.StatusNotFound {
		t.Fatalf("mismatched repository = %d", code)
	}
	if code := deliver(t, h, strings.TrimPrefix(Path(key, "cluster", "missing"), "/services/providers/code"), "issue_comment", commandPayload); code != http.StatusNotFound {
		t.Fatalf("missing repository = %d", code)
	}
}
