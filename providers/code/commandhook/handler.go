// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package commandhook

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
)

// maxPayloadBytes bounds a delivery. The handler reads four fields from it.
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
func Handler(key []byte, clients ProviderClients) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST "+Pattern, &handler{key: key, clients: clients})
	return mux
}

type handler struct {
	key     []byte
	clients ProviderClients
}

// delivery is the part of GitHub's issue_comment payload the handler reads.
// None of it is trusted beyond choosing which comment to verify.
type delivery struct {
	Action  string `json:"action"`
	Comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
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
	if !ValidToken(h.key, cluster, name, r.PathValue("token")) {
		http.NotFound(w, r)
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPayloadBytes)).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "created" || payload.Issue.PullRequest == nil || !LooksLikeCommand(payload.Comment.Body) {
		w.WriteHeader(http.StatusAccepted) // not a new command on a pull request
		return
	}
	if payload.Comment.ID <= 0 || payload.Repository.ID <= 0 {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	status, err := h.record(r.Context(), cluster, name, payload)
	if err != nil {
		klog.FromContext(r.Context()).Error(err, "recording pull request command", "cluster", cluster, "repository", name, "comment", payload.Comment.ID)
	}
	w.WriteHeader(status)
}

// record writes the unverified command, once per comment.
func (h *handler) record(ctx context.Context, cluster, name string, payload delivery) (int, error) {
	if h.clients == nil {
		return http.StatusServiceUnavailable, nil
	}
	client, err := h.clients.AsProvider(cluster)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	repository, err := client.Resource(repositoriesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return http.StatusNotFound, nil
	}
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	repoID, _, _ := unstructured.NestedString(repository.Object, "status", "repoID")
	if repoID == "" || repoID != strconv.FormatInt(payload.Repository.ID, 10) || repository.GetDeletionTimestamp() != nil {
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
	_, err = client.Resource(commandsGVR).Create(ctx, command, metav1.CreateOptions{})
	switch {
	case err == nil, apierrors.IsAlreadyExists(err):
		return http.StatusAccepted, nil
	default:
		return http.StatusServiceUnavailable, err
	}
}
