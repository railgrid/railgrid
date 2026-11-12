// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/snapshotstore"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

var (
	Events                      = api.SchemeGroupVersion.WithResource("githubevents")
	PullRequests                = api.SchemeGroupVersion.WithResource("pullrequests")
	Repositories                = api.SchemeGroupVersion.WithResource("repositories")
	connections                 = api.SchemeGroupVersion.WithResource("connections")
	repositories                = api.SchemeGroupVersion.WithResource("repositories")
	errReceiptIdentityCollision = errors.New("receipt identity collision")
)

// Server owns GitHub ingress, observations and publication. It knows nothing
// about reviewer/fixer scheduling, Factory phases, or Agents execution state.
type Server struct {
	Callers     dataplane.ProviderCallerFactory
	Credentials tenant.CredentialResolver
	Backends    *backend.Registry
	Snapshots   snapshotstore.Store
	HTTP        *http.Client
	Now         func() time.Time
}

type binding struct {
	conn       *api.Connection
	repo       *api.Repository
	data       map[string][]byte
	client     Client
	credential backend.Credential
}

func decode[T any](u *unstructured.Unstructured) (*T, error) {
	out := new(T)
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
	return out, err
}
func asU(v any) (*unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
	return &unstructured.Unstructured{Object: raw}, err
}
func (s *Server) binding(ctx context.Context, dyn dynamic.Interface, name string, mint bool) (binding, error) {
	var b binding
	u, err := dyn.Resource(connections).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return b, err
	}
	b.conn, err = decode[api.Connection](u)
	if err != nil {
		return b, err
	}
	if b.conn.Spec.Provider != "github" || b.conn.Spec.Type != api.CredentialTypeGitHubApp || b.conn.Spec.GitHubWebhook == nil || b.conn.Spec.BaseURL != "" {
		return b, errors.New("connection does not enable GitHub App events")
	}
	ns := b.conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	store := &tenant.DynamicSecretStore{Client: dyn, Namespace: ns, Name: b.conn.Spec.SecretRef.Name}
	b.data, _, err = store.Load(ctx)
	if err != nil {
		return b, err
	}
	if len(b.data["webhookSecret"]) < 32 {
		return b, errors.New("webhook signing key missing")
	}
	u, err = dyn.Resource(repositories).Get(ctx, b.conn.Spec.GitHubWebhook.RepositoryRef, metav1.GetOptions{})
	if err != nil {
		return b, err
	}
	b.repo, err = decode[api.Repository](u)
	if err != nil {
		return b, err
	}
	if b.repo.Spec.ConnectionRef != b.conn.Name || b.repo.Status.RepoID != strconv.FormatInt(b.conn.Spec.GitHubWebhook.RepositoryID, 10) {
		return b, errors.New("repository binding changed")
	}
	if mint {
		b.credential, err = s.Credentials.Resolve(ctx, b.conn, b.data)
		if err != nil {
			return b, err
		}
		b.client = Client{HTTP: s.HTTP, Token: b.credential.Token, BotLogin: b.conn.Spec.GitHubWebhook.BotLogin}
	}
	return b, nil
}
func repositoryName(b binding) string {
	owner := b.repo.Spec.Owner
	if owner == "" {
		owner = b.conn.Spec.Owner
	}
	return owner + "/" + b.repo.Spec.Name
}
func seal(cluster string, spec api.GitHubEventSpec, key []byte) string {
	spec.Seal = ""
	raw, _ := json.Marshal(struct {
		Cluster string
		Spec    api.GitHubEventSpec
	}{cluster, spec})
	return Signature(key, raw)
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func duplicateReceipt(ctx context.Context, dyn dynamic.Interface, cluster string, b binding, ev Event) (bool, error) {
	name := fmt.Sprintf("github-%d-%d", ev.Repository.ID, ev.Comment.ID)
	u, err := dyn.Resource(Events).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	existing, err := decode[api.GitHubEvent](u)
	if err != nil {
		return false, err
	}
	if existing.Spec.ConnectionRef != b.conn.Name || existing.Spec.ConnectionUID != string(b.conn.UID) ||
		existing.Spec.RepositoryRef != b.repo.Name || existing.Spec.RepositoryUID != string(b.repo.UID) ||
		existing.Spec.Repository != repositoryName(b) || existing.Spec.RepositoryID != ev.Repository.ID ||
		existing.Spec.PullRequest != ev.Issue.Number || existing.Spec.CommentID != ev.Comment.ID ||
		existing.Spec.ActorID != ev.Comment.User.ID ||
		existing.Spec.Comment != ev.Comment.Body ||
		!hmac.Equal([]byte(existing.Spec.Seal), []byte(seal(cluster, existing.Spec, b.data["webhookSecret"]))) {
		return false, errReceiptIdentityCollision
	}
	return true, nil
}

// Webhook verifies authenticity, pins repository and actor identity, and stores
// an immutable receipt. Factory alone interprets commands and admits tasks.
func (s *Server) Webhook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	cluster, name := r.PathValue("cluster"), r.PathValue("name")
	// Read and bound the body before any lookup. Every reply before the
	// signature check must be the same whether or not the connection exists,
	// so a size failure cannot come after the binding reads.
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		reply(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	dyn, err := s.Callers.AsProvider(cluster)
	if err != nil {
		log.Printf("github webhook %q/%q: workspace: %v", cluster, name, err)
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	b, err := s.binding(ctx, dyn, name, false)
	if err != nil {
		log.Printf("github webhook %q/%q: binding: %v", cluster, name, err)
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !Verify(b.data["webhookSecret"], raw, r.Header.Get("X-Hub-Signature-256")) {
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		reply(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.Header.Get("X-GitHub-Event") != "issue_comment" {
		reply(w, 200, map[string]string{"status": "ignored"})
		return
	}
	var ev Event
	if json.Unmarshal(raw, &ev) != nil {
		reply(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if ev.Action != "created" || ev.Issue.PullRequest == nil || ev.Sender.Type != "User" || ev.Comment.User.Type != "User" {
		reply(w, 200, map[string]string{"status": "ignored"})
		return
	}
	installation, err := strconv.ParseInt(string(b.data["installationID"]), 10, 64)
	if err != nil || (ev.Installation.ID != installation && (ev.Installation.ID != 0 || b.conn.Spec.GitHubWebhook.Source != "repository")) || ev.Repository.ID != b.conn.Spec.GitHubWebhook.RepositoryID || !strings.EqualFold(ev.Repository.FullName, repositoryName(b)) || ev.Sender.ID != ev.Comment.User.ID || ev.Comment.ID <= 0 || ev.Issue.Number <= 0 {
		reply(w, 403, map[string]string{"error": "event identity mismatch"})
		return
	}
	// Receipt bounds match the API. Ordinary comments do not need PR reads.
	if len(ev.Comment.Body) > 8192 {
		reply(w, 200, map[string]string{"status": "oversize comment ignored"})
		return
	}
	// Factory owns command admission. Keep every addressed /railgrid attempt,
	// including unknown commands, but do not mint credentials or persist prose.
	_, addressed := Parse(ev.Comment.Body)
	if !addressed {
		reply(w, 200, map[string]string{"status": "unaddressed comment ignored"})
		return
	}
	duplicate, err := duplicateReceipt(ctx, dyn, cluster, b, ev)
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, errReceiptIdentityCollision) {
			status = http.StatusServiceUnavailable
		}
		message := "receipt lookup failed"
		if status == http.StatusConflict {
			message = errReceiptIdentityCollision.Error()
		}
		reply(w, status, map[string]string{"error": message})
		return
	}
	if duplicate {
		reply(w, 200, map[string]string{"status": "duplicate"})
		return
	}
	b, err = s.binding(ctx, dyn, name, true)
	if err != nil {
		reply(w, 503, map[string]string{"error": "app authentication unavailable"})
		return
	}
	var pr PullRequest
	if err = b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repositoryName(b), ev.Issue.Number), nil, &pr); err != nil {
		reply(w, 503, map[string]string{"error": "PR unavailable"})
		return
	}
	if pr.State != "open" || pr.Head.Repo.ID != ev.Repository.ID || pr.Base.Repo.ID != ev.Repository.ID {
		reply(w, 200, map[string]string{"status": "unsupported PR"})
		return
	}
	if len(pr.Body) > 16384 || len(pr.Title) > 256 {
		reply(w, 400, map[string]string{"error": "PR context exceeds bounds"})
		return
	}
	e := &api.GitHubEvent{TypeMeta: metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "GitHubEvent"}, ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("github-%d-%d", ev.Repository.ID, ev.Comment.ID)}, Spec: api.GitHubEventSpec{ConnectionRef: name, ConnectionUID: string(b.conn.UID), RepositoryRef: b.repo.Name, RepositoryUID: string(b.repo.UID), Repository: repositoryName(b), RepositoryID: ev.Repository.ID, PullRequest: ev.Issue.Number, CommentID: ev.Comment.ID, Actor: ev.Comment.User.Login, ActorID: ev.Comment.User.ID, Comment: ev.Comment.Body, Head: pr.Head.SHA, Branch: pr.Head.Ref, Base: pr.Base.Ref, Description: pr.Body, Title: pr.Title}}
	e.Spec.Seal = seal(cluster, e.Spec, b.data["webhookSecret"])
	obj, err := asU(e)
	if err == nil {
		_, err = dyn.Resource(Events).Create(ctx, obj, metav1.CreateOptions{})
	}
	if apierrors.IsAlreadyExists(err) {
		duplicate, lookupErr := duplicateReceipt(ctx, dyn, cluster, b, ev)
		if lookupErr != nil {
			reply(w, 503, map[string]string{"error": "receipt lookup failed"})
			return
		}
		if duplicate {
			reply(w, 200, map[string]string{"status": "duplicate"})
			return
		}
		reply(w, 409, map[string]string{"error": "receipt identity collision"})
		return
	}
	if err != nil {
		reply(w, 503, map[string]string{"error": "receipt persistence failed"})
		return
	}
	reply(w, 202, map[string]string{"status": "recorded", "event": e.Name})
}

// Context is re-read before every dispatch and publication. A kube-authored
// forged receipt, replaced binding, revoked maintainer or edited comment fails.
func (s *Server) Context(ctx context.Context, dyn dynamic.Interface, cluster, name string) (*api.GitHubEvent, binding, bool, error) {
	u, err := dyn.Resource(Events).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, binding{}, false, err
	}
	e, err := decode[api.GitHubEvent](u)
	if err != nil {
		return nil, binding{}, false, err
	}
	b, err := s.binding(ctx, dyn, e.Spec.ConnectionRef, false)
	if err != nil {
		return nil, b, false, err
	}
	if e.Spec.ConnectionUID != string(b.conn.UID) || e.Spec.RepositoryUID != string(b.repo.UID) || e.Spec.RepositoryRef != b.repo.Name || e.Spec.RepositoryID != b.conn.Spec.GitHubWebhook.RepositoryID || e.Spec.Repository != repositoryName(b) || !hmac.Equal([]byte(e.Spec.Seal), []byte(seal(cluster, e.Spec, b.data["webhookSecret"]))) {
		return nil, b, false, errors.New("receipt seal or binding mismatch")
	}
	b.credential, err = s.Credentials.Resolve(ctx, b.conn, b.data)
	if err != nil {
		return nil, b, false, err
	}
	b.client = Client{HTTP: s.HTTP, Token: b.credential.Token, BotLogin: b.conn.Spec.GitHubWebhook.BotLogin}
	var c Comment
	if err = b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/issues/comments/%d", e.Spec.Repository, e.Spec.CommentID), nil, &c); err != nil {
		return nil, b, false, err
	}
	if c.ID != e.Spec.CommentID || c.User.ID != e.Spec.ActorID || c.User.Type != "User" || c.Body != e.Spec.Comment {
		return e, b, false, nil
	}
	// The numeric actor is frozen; GitHub's current comment author supplies
	// the current login, so account renames cannot authorize a reused login.
	allowed, err := b.client.Maintainer(ctx, e.Spec.Repository, c.User.Login)
	return e, b, allowed, err
}

// ServedVerbs is the native capability table used by routing and contract checks.
var ServedVerbs = map[string]string{"githubevents": "context", "pullrequests": "operate", "repositories": "authorize"}

// ServeHTTP exposes only the declared receipt/context and PR/operate verbs.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok {
		http.NotFound(w, r)
		return
	}
	req := route.Request
	gvr, kind := Events, "GitHubEvent"
	switch req.Resource {
	case PullRequests.Resource:
		gvr, kind = PullRequests, "PullRequest"
	case Repositories.Resource:
		gvr, kind = Repositories, "Repository"
	}
	if route.Group != gvr.Group || route.APIVersion != gvr.Version || req.Tail != "" || req.Component != "" || ServedVerbs[req.Resource] != req.Verb {
		http.NotFound(w, r)
		return
	}
	env := actionwire.New(r, "code", req.Verb, actionwire.ResourceRef{APIVersion: gvr.GroupVersion().String(), Kind: kind, Resource: gvr.Resource, Name: req.Name})
	obj, dyn, err := dataplane.Gate(r.Context(), s.Callers, gvr, req)
	if err != nil {
		env.Failure(w, dataplane.StatusFor(err), "forbidden", "resource unavailable", false)
		return
	}
	identity, _ := dataplane.ProxiedIdentityFrom(r.Context())
	dataplane.Serve(w, r, env, dataplane.Limits{Timeout: 3 * time.Minute, MaxInputBytes: 36 << 20, MaxOutputBytes: 128 << 10, MaxResultItems: 100}, func(ctx context.Context, raw json.RawMessage) (any, *actionwire.Error) {
		var result any
		switch req.Resource {
		case Repositories.Resource:
			result, err = s.AuthorizeTask(ctx, dyn, req.ClusterID, req.Name, obj, identity, raw)
		case Events.Resource:
			e, b, allowed, eerr := s.Context(ctx, dyn, req.ClusterID, req.Name)
			err = eerr
			if err == nil {
				var current PullRequest
				err = b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", e.Spec.Repository, e.Spec.PullRequest), nil, &current)
				var proof string
				if err == nil {
					proof, err = s.issuePublicationScope(ctx, dyn, req.ClusterID, e, b, allowed, raw)
				}
				result = map[string]any{"event": e, "maintainer": allowed, "pullRequest": current, "scopeProof": proof}
			}
		default:
			result, err = s.Operate(ctx, dyn, req.ClusterID, obj, raw, identity)
		}
		if err != nil {
			return nil, &actionwire.Error{Code: "operation_refused", Message: err.Error(), Retryable: false}
		}
		return result, nil
	})
}

// CheckPublication is called again by the existing PullRequest controller at
// the last publication boundary, including retries after process restart.
func (s *Server) CheckPublication(ctx context.Context, cluster string, pr *api.PullRequest) error {
	if pr == nil || pr.Spec.Command == nil {
		return nil
	}
	if pr.Spec.Command.Task != nil {
		return s.checkTaskPublication(ctx, cluster, pr)
	}
	if time.Now().After(pr.Spec.Command.Deadline.Time) {
		return errors.New("command publication deadline expired")
	}
	dyn, err := s.Callers.AsProvider(cluster)
	if err != nil {
		return err
	}
	// The reconcile object may predate a cancellation or a new command.
	currentObject, err := dyn.Resource(PullRequests).Get(ctx, pr.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	fresh, err := decode[api.PullRequest](currentObject)
	if err != nil {
		return err
	}
	if fresh.UID != pr.UID || !sameCommand(fresh.Spec.Command, pr.Spec.Command) || time.Now().After(fresh.Spec.Command.Deadline.Time) ||
		fresh.Spec.DesiredHead == nil || pr.Spec.DesiredHead == nil || *fresh.Spec.DesiredHead != *pr.Spec.DesiredHead {
		return errors.New("publication authority or desired head changed")
	}
	e, b, allowed, err := s.Context(ctx, dyn, cluster, fresh.Spec.Command.EventRef)
	if err != nil {
		return err
	}
	mode, _ := Parse(e.Spec.Comment)
	if !allowed || string(e.UID) != fresh.Spec.Command.EventUID || mode != "review-fix" ||
		fresh.Spec.RepositoryRef != e.Spec.RepositoryRef || fresh.Spec.Branch != e.Spec.Branch || fresh.Spec.Base != e.Spec.Base ||
		fresh.Spec.RepositoryRef != pr.Spec.RepositoryRef || fresh.Spec.Branch != pr.Spec.Branch || fresh.Spec.Base != pr.Spec.Base ||
		fresh.Spec.Branch == "main" || fresh.Spec.Branch == fresh.Spec.Base ||
		fresh.Spec.DesiredHead.BaseCommit == "" || fresh.Spec.DesiredHead.BaseCommit != fresh.Status.Head ||
		(fresh.Spec.Number != 0 && int64(fresh.Spec.Number) != e.Spec.PullRequest) ||
		(fresh.Status.Number != 0 && int64(fresh.Status.Number) != e.Spec.PullRequest) {
		return errors.New("command does not authorize PR branch publication")
	}
	var current PullRequest
	if err = b.client.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", e.Spec.Repository, e.Spec.PullRequest), nil, &current); err != nil {
		return fmt.Errorf("rechecking PR before publication: %w", err)
	}
	if time.Now().After(fresh.Spec.Command.Deadline.Time) || current.Number != e.Spec.PullRequest || current.State != "open" ||
		current.Head.Repo.ID != e.Spec.RepositoryID || current.Base.Repo.ID != e.Spec.RepositoryID ||
		current.Head.Ref != e.Spec.Branch || current.Base.Ref != e.Spec.Base ||
		(current.Head.SHA != fresh.Spec.DesiredHead.BaseCommit && current.Head.SHA != fresh.Spec.DesiredHead.Commit) {
		return errors.New("live PR no longer matches the admitted publication target")
	}
	return nil
}

func (s *Server) checkTaskPublication(ctx context.Context, cluster string, pr *api.PullRequest) error {
	if s.now().After(pr.Spec.Command.Deadline.Time) {
		return errors.New("task publication deadline expired")
	}
	dyn, err := s.Callers.AsProvider(cluster)
	if err != nil {
		return err
	}
	fresh, b, err := s.activeTaskAuthorization(ctx, dyn, cluster, pr)
	if err != nil {
		return err
	}
	if fresh.Spec.DesiredHead == nil || pr.Spec.DesiredHead == nil || *fresh.Spec.DesiredHead != *pr.Spec.DesiredHead ||
		fresh.Spec.DesiredHead.BaseCommit == "" || fresh.Spec.DesiredHead.BaseCommit != pr.Spec.DesiredHead.BaseCommit {
		return errors.New("task publication authority or desired head changed")
	}
	grant := fresh.Spec.Command.Task
	number := effectivePullRequestNumber(fresh)
	if number > 0 {
		current, err := s.taskPullRequest(ctx, b, fresh, number)
		if err != nil {
			return fmt.Errorf("rechecking task PR before publication: %w", err)
		}
		repositoryID, parseErr := strconv.ParseInt(b.repo.Status.RepoID, 10, 64)
		if parseErr != nil || current.State != "open" || current.Head.Repo.ID != repositoryID || current.Base.Repo.ID != repositoryID ||
			current.Head.Ref != grant.Branch || current.Base.Ref != grant.Base ||
			(current.Head.SHA != fresh.Spec.DesiredHead.BaseCommit && current.Head.SHA != fresh.Spec.DesiredHead.Commit) {
			return errors.New("live PR no longer matches the admitted task publication target")
		}
		return nil
	}
	if fresh.Spec.Branch == "main" || fresh.Spec.Branch == fresh.Spec.Base || grant.Branch != fresh.Spec.Branch || grant.Base != fresh.Spec.Base {
		return errors.New("task publication cannot target main or its base branch")
	}
	implementation, ok := s.Backends.Get(string(b.conn.Spec.Provider))
	if !ok {
		return errors.New("GitHub backend unavailable")
	}
	collaboration, ok := implementation.(backend.Collaboration)
	if !ok {
		return errors.New("GitHub branch reads unavailable")
	}
	base, err := collaboration.BranchHead(ctx, b.conn, b.credential, b.repo, fresh.Spec.Base)
	if err != nil || base == "" || base != fresh.Spec.DesiredHead.BaseCommit {
		return errors.New("task snapshot no longer extends the current PR base")
	}
	branch, err := collaboration.BranchHead(ctx, b.conn, b.credential, b.repo, fresh.Spec.Branch)
	if err != nil || (branch != "" && branch != fresh.Spec.DesiredHead.Commit) {
		return errors.New("task PR branch moved outside the admitted publication")
	}
	return nil
}

func sameCommand(a, b *api.PullRequestCommand) bool {
	return a != nil && b != nil && a.EventRef == b.EventRef && a.EventUID == b.EventUID && a.Deadline.Time.Equal(b.Deadline.Time) && reflect.DeepEqual(a.Task, b.Task)
}

// ReadObservation augments Code's existing PullRequest status for command work.
func (s *Server) ReadObservation(ctx context.Context, cluster string, pr *api.PullRequest) (Observation, error) {
	var out Observation
	dyn, err := s.Callers.AsProvider(cluster)
	if err != nil {
		return out, err
	}
	if pr.Spec.Command.Task != nil {
		fresh, b, taskErr := s.activeTaskAuthorization(ctx, dyn, cluster, pr)
		if taskErr != nil {
			return out, taskErr
		}
		return s.taskObservation(ctx, b, fresh)
	}
	e, b, allowed, err := s.Context(ctx, dyn, cluster, pr.Spec.Command.EventRef)
	if err != nil {
		return out, err
	}
	if !allowed || string(e.UID) != pr.Spec.Command.EventUID {
		return out, errors.New("command no longer authorized")
	}
	var observed PullRequest
	if err = b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", e.Spec.Repository, e.Spec.PullRequest), nil, &observed); err != nil {
		return out, err
	}
	if observed.Head.Ref != pr.Spec.Branch || observed.Base.Ref != pr.Spec.Base || observed.Head.Repo.ID != e.Spec.RepositoryID {
		return out, errors.New("PR identity changed")
	}
	return observe(ctx, b.client, e, observed)
}
