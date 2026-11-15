// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package commandhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/tenant"
)

// maxPayloadBytes bounds the body before signature verification.
const maxPayloadBytes = 1 << 20

// RepositoryLabel names the Repository on every PullRequestCommand.
const RepositoryLabel = "code.railgrid.ai/repository"

var (
	repositoriesGVR = codev1alpha1.SchemeGroupVersion.WithResource("repositories")
	commandsGVR     = codev1alpha1.SchemeGroupVersion.WithResource("pullrequestcommands")
)

// ProviderClients acts as this provider in a tenant workspace
// (dataplane.ProviderCallerFactory).
type ProviderClients interface {
	AsProvider(clusterID string) (dynamic.Interface, error)
}

// Handler serves POST Pattern.
func Handler(clients ProviderClients) http.Handler {
	return newHandler(clients, time.Now)
}

func newHandler(clients ProviderClients, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST "+Pattern, &handler{clients: clients, now: now, targets: map[targetKey]target{}})
	return mux
}

// targetCacheTTL bounds how long a Repository and its signing secret, or the
// fact that there is none, are reused across deliveries. Without it every
// unsigned POST would cost a Repository and a Secret read. A rotated secret
// or a newly registered webhook takes effect within this window.
const (
	targetCacheTTL     = 30 * time.Second
	targetCacheEntries = 4096
)

type targetKey struct{ cluster, name string }

// target is what authenticating a delivery to one Repository needs. A zero
// client means the Repository has no usable commands webhook.
type target struct {
	client     dynamic.Interface
	repository *unstructured.Unstructured
	secret     string
	expires    time.Time
}

type handler struct {
	clients ProviderClients
	now     func() time.Time

	mu      sync.Mutex
	targets map[targetKey]target
}

// delivery is the part of GitHub's issue_comment payload the handler reads.
// None of it is trusted beyond choosing which comment to verify.
type delivery struct {
	Action  string `json:"action"`
	Comment struct {
		ID        int64     `json:"id"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"comment"`
	Issue struct {
		Number      int              `json:"number"`
		PullRequest *json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Repository struct {
		ID int64 `json:"id"`
	} `json:"repository"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cluster, name := r.PathValue("cluster"), r.PathValue("name")
	signature := r.Header.Get("X-Hub-Signature-256")
	if len(signature) != signatureLength || h.clients == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPayloadBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	client, repository, status := h.authenticate(ctx, cluster, name, signature, body)
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		w.WriteHeader(http.StatusNoContent)
		return
	case "issue_comment":
	default:
		w.WriteHeader(http.StatusAccepted) // not an event commands use
		return
	}
	var payload delivery
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "created" || payload.Issue.PullRequest == nil || !LooksLikeCommand(payload.Comment.Body) {
		w.WriteHeader(http.StatusAccepted) // not a new command on a pull request
		return
	}
	if payload.Comment.ID <= 0 || payload.Repository.ID <= 0 || payload.Comment.CreatedAt.IsZero() {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	// Once the retained command is reaped, an old signed redelivery must
	// still be too old to recreate it.
	if h.now().After(payload.Comment.CreatedAt.Add(MaxCommandAge)) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	status, err = record(ctx, client, repository, payload)
	if err != nil {
		klog.FromContext(r.Context()).Error(err, "recording pull request command", "cluster", cluster, "repository", name, "comment", payload.Comment.ID)
	}
	w.WriteHeader(status)
}

// authenticate uses only this workspace's Repository and Secret. A URL
// reader cannot enqueue commands or spend the repository's GitHub quota.
// authenticate checks the delivery's signature against the Repository's
// signing secret. Every refusal before the signature is verified is the same
// 404, so a caller cannot learn which workspaces or Repositories exist;
// lookup failures are logged instead.
func (h *handler) authenticate(ctx context.Context, cluster, name, signature string, body []byte) (dynamic.Interface, *unstructured.Unstructured, int) {
	t := h.target(ctx, cluster, name)
	if t.client == nil || !validSignature(t.secret, signature, body) {
		return nil, nil, http.StatusNotFound
	}
	return t.client, t.repository, 0
}

func (h *handler) target(ctx context.Context, cluster, name string) target {
	key := targetKey{cluster: cluster, name: name}
	now := h.now()
	h.mu.Lock()
	cached, ok := h.targets[key]
	h.mu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached
	}
	resolved, cacheable := h.resolveTarget(ctx, cluster, name)
	if !cacheable {
		return resolved
	}
	resolved.expires = now.Add(targetCacheTTL)
	h.mu.Lock()
	if len(h.targets) >= targetCacheEntries {
		for k, entry := range h.targets {
			if !now.Before(entry.expires) {
				delete(h.targets, k)
			}
		}
		if len(h.targets) >= targetCacheEntries {
			h.targets = map[targetKey]target{}
		}
	}
	h.targets[key] = resolved
	h.mu.Unlock()
	return resolved
}

// resolveTarget reads the Repository and its signing secret. A definite
// answer, including "there is none", is cacheable; a transient failure is
// not, so the next delivery tries again.
func (h *handler) resolveTarget(ctx context.Context, cluster, name string) (target, bool) {
	log := klog.FromContext(ctx)
	client, err := h.clients.AsProvider(cluster)
	if err != nil {
		log.Error(err, "building the provider client for a command webhook", "cluster", cluster, "repository", name)
		return target{}, false
	}
	repository, err := client.Resource(repositoriesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		return target{}, true
	}
	if err != nil {
		log.Error(err, "reading the Repository for a command webhook", "cluster", cluster, "repository", name)
		return target{}, false
	}
	secretName, _, _ := unstructured.NestedString(repository.Object, "spec", "commandsWebhookSecretRef", "name")
	if secretName == "" || repository.GetDeletionTimestamp() != nil {
		return target{}, true
	}
	namespace, _, _ := unstructured.NestedString(repository.Object, "spec", "commandsWebhookSecretRef", "namespace")
	key, _, _ := unstructured.NestedString(repository.Object, "spec", "commandsWebhookSecretRef", "key")
	if key == "" {
		key = SecretKey
	}
	secret, err := tenant.ResolveToken(ctx, client, namespace, secretName, key)
	if errors.Is(err, tenant.ErrCredentialsMissing) || errors.Is(err, tenant.ErrAPIBindingMissing) {
		return target{}, true
	}
	if err != nil {
		log.Error(err, "reading command webhook Secret", "cluster", cluster, "repository", name)
		return target{}, false
	}
	if secret == "" {
		return target{}, true
	}
	return target{client: client, repository: repository, secret: secret}, true
}

func record(ctx context.Context, client dynamic.Interface, repository *unstructured.Unstructured, payload delivery) (int, error) {
	name := repository.GetName()
	repoID, _, _ := unstructured.NestedString(repository.Object, "status", "repoID")
	if repoID == "" || repoID != strconv.FormatInt(payload.Repository.ID, 10) {
		return http.StatusNotFound, nil
	}
	command := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "PullRequestCommand",
		"metadata": map[string]any{
			"name": ObjectName(name, payload.Comment.ID),
		},
		"spec": map[string]any{"repositoryRef": name, "commentID": payload.Comment.ID},
	}}
	if len(name) <= 63 {
		command.SetLabels(map[string]string{RepositoryLabel: name})
	}
	command.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: codev1alpha1.SchemeGroupVersion.String(), Kind: "Repository", Name: name, UID: repository.GetUID()}})
	_, err := client.Resource(commandsGVR).Create(ctx, command, metav1.CreateOptions{})
	switch {
	case err == nil, apierrors.IsAlreadyExists(err):
		return http.StatusAccepted, nil
	default:
		return http.StatusServiceUnavailable, err
	}
}
