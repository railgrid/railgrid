// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const (
	maxTaskGrantLifetime = 24 * time.Hour
	maxTaskCallerGroups  = 64
)

var sourceDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// TaskCaller is the original authenticated user Factory admitted. Code checks
// that identity's current Code operation permission before it accepts a grant
// and again before each operation or publication.
type TaskCaller struct {
	User   string   `json:"user"`
	UID    string   `json:"uid,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// TaskAuthorizationInput is the strict wire request for repositories/authorize.
// Code does not read or import the coordinator's task API.
type TaskAuthorizationInput struct {
	FinalStatusBody    string     `json:"finalStatusBody,omitempty"`
	Op                 string     `json:"op"`
	SourceUID          string     `json:"sourceUID"`
	SourceDigest       string     `json:"sourceDigest"`
	Repository         string     `json:"repository"`
	RepositoryUID      string     `json:"repositoryUID"`
	ConnectionUID      string     `json:"connectionUID"`
	PullRequest        string     `json:"pullRequest"`
	PullRequestUID     string     `json:"pullRequestUID"`
	Caller             TaskCaller `json:"caller,omitempty"`
	Deadline           time.Time  `json:"deadline,omitempty"`
	Mode               string     `json:"mode,omitempty"`
	GrantRef           string     `json:"grantRef,omitempty"`
	Proof              string     `json:"proof,omitempty"`
	InvocationKey      string     `json:"invocationKey,omitempty"`
	InvocationAgent    string     `json:"invocationAgent,omitempty"`
	InvocationAgentUID string     `json:"invocationAgentUID,omitempty"`
}

type TaskAuthorizationResult struct {
	GrantRef       string    `json:"grantRef"`
	SourceUID      string    `json:"sourceUID"`
	SourceDigest   string    `json:"sourceDigest"`
	Repository     string    `json:"repository"`
	RepositoryRef  string    `json:"repositoryRef"`
	RepositoryID   string    `json:"repositoryID"`
	RepositoryUID  string    `json:"repositoryUID"`
	ConnectionUID  string    `json:"connectionUID"`
	PullRequest    string    `json:"pullRequest"`
	PullRequestUID string    `json:"pullRequestUID"`
	Deadline       time.Time `json:"deadline"`
	Mode           string    `json:"mode"`
	Proof          string    `json:"proof,omitempty"`
	ScopeProof     string    `json:"scopeProof,omitempty"`
	Revoked        bool      `json:"revoked,omitempty"`
}

type taskBinding struct {
	binding
	key []byte
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func decodeAuthorization(raw json.RawMessage) (TaskAuthorizationInput, error) {
	var in TaskAuthorizationInput
	if len(raw) == 0 {
		return in, errors.New("authorization input is required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return in, errors.New("invalid authorization input")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return in, errors.New("trailing authorization input")
	}
	return in, nil
}

func (s *Server) taskBinding(ctx context.Context, dyn dynamic.Interface, repositoryName, repositoryUID, connectionUID string, mint, signing bool) (taskBinding, error) {
	var out taskBinding
	u, err := dyn.Resource(Repositories).Get(ctx, repositoryName, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	out.repo, err = decode[api.Repository](u)
	if err != nil {
		return out, err
	}
	if string(out.repo.UID) != repositoryUID || out.repo.DeletionTimestamp != nil || out.repo.Spec.ConnectionRef == "" || out.repo.Status.RepoID == "" {
		return out, errors.New("repository identity changed or is not ready")
	}
	u, err = dyn.Resource(connections).Get(ctx, out.repo.Spec.ConnectionRef, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	out.conn, err = decode[api.Connection](u)
	if err != nil {
		return out, err
	}
	if string(out.conn.UID) != connectionUID || out.conn.DeletionTimestamp != nil || out.conn.Spec.Provider != "github" || out.conn.Spec.Type != api.CredentialTypeGitHubApp ||
		out.conn.Spec.GitHubWebhook == nil || out.conn.Spec.BaseURL != "" || out.conn.Spec.GitHubWebhook.RepositoryRef != out.repo.Name ||
		out.conn.Spec.GitHubWebhook.RepositoryID <= 0 || strconv.FormatInt(out.conn.Spec.GitHubWebhook.RepositoryID, 10) != out.repo.Status.RepoID {
		return out, errors.New("connection identity changed or is unsupported")
	}
	ns := out.conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	if mint || signing {
		store := &tenant.DynamicSecretStore{Client: dyn, Namespace: ns, Name: out.conn.Spec.SecretRef.Name}
		out.data, _, err = store.Load(ctx)
		if err != nil {
			return out, err
		}
	}
	if signing {
		out.key, err = taskSigningKey(out.conn, out.data)
		if err != nil {
			return out, err
		}
	}
	if mint {
		out.credential, err = s.Credentials.Resolve(ctx, out.conn, out.data)
		if err != nil {
			return out, err
		}
		login := out.conn.Status.Login
		if out.conn.Spec.GitHubWebhook != nil && out.conn.Spec.GitHubWebhook.BotLogin != "" {
			login = out.conn.Spec.GitHubWebhook.BotLogin
		}
		out.client = Client{HTTP: s.HTTP, Token: out.credential.Token, BotLogin: login}
	}
	return out, nil
}

// taskSigningKey derives a Code-side key from the App webhook signing secret.
// The secret is present in the App binding already used to verify signed forge
// deliveries; domain separation keeps task authority proofs distinct.
func taskSigningKey(conn *api.Connection, data map[string][]byte) ([]byte, error) {
	source := data["webhookSecret"]
	if len(source) < 32 {
		return nil, errors.New("GitHub App webhook signing key is unavailable")
	}
	h := sha256.New()
	_, _ = h.Write([]byte("railgrid-code-task-authority-v1\x00"))
	_, _ = h.Write([]byte(string(conn.UID)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(source)
	return h.Sum(nil), nil
}

func canonicalCaller(caller TaskCaller) (TaskCaller, error) {
	caller.User = strings.TrimSpace(caller.User)
	caller.UID = strings.TrimSpace(caller.UID)
	if caller.User == "" || len(caller.User) > 253 || len(caller.UID) > 128 || len(caller.Groups) > maxTaskCallerGroups {
		return caller, errors.New("original caller identity is incomplete or oversized")
	}
	groups := make([]string, 0, len(caller.Groups))
	seen := make(map[string]struct{}, len(caller.Groups))
	for _, group := range caller.Groups {
		group = strings.TrimSpace(group)
		if group == "" || len(group) > 253 {
			return caller, errors.New("original caller group is invalid")
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	sort.Strings(groups)
	caller.Groups = groups
	return caller, nil
}

func taskGrantPayload(grant *api.PullRequestTaskAuthorization) ([]byte, error) {
	copy := *grant
	copy.Proof = ""
	return json.Marshal(copy)
}

func taskGrantRef(grant *api.PullRequestTaskAuthorization) (string, error) {
	copy := *grant
	copy.GrantRef = ""
	copy.Proof = ""
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func taskGrantProof(key []byte, grant *api.PullRequestTaskAuthorization) (string, error) {
	raw, err := taskGrantPayload(grant)
	if err != nil {
		return "", err
	}
	return Signature(key, raw), nil
}

func taskScopeProof(key []byte, grant *api.PullRequestTaskAuthorization, invocationKey, agent, agentUID string) string {
	raw, _ := json.Marshal([]string{"code-task-invocation-v1", grant.GrantRef, grant.Proof, invocationKey, agent, agentUID})
	return Signature(key, raw)
}

func (s *Server) authorizeTaskCaller(ctx context.Context, dyn dynamic.Interface, grant *api.PullRequestTaskAuthorization) error {
	allowed, err := dataplane.Authorize(ctx, dyn, dataplane.ProxiedIdentity{User: grant.Caller, Groups: grant.CallerGroups}, dataplane.ResourceAttributes{
		Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "pullrequests", Subresource: "operate", Name: grant.PullRequestRef, Verb: "create",
	})
	if err != nil {
		return fmt.Errorf("checking original caller Code publication permission: %w", err)
	}
	if !allowed {
		return errors.New("original caller cannot create pullrequests/operate")
	}
	return nil
}

// AuthorizeTask handles generic scoped grants from trusted coordinator providers.
// A successful revoke confirms that Code recorded the tombstone and removed
// authority for subsequent Code operations. It does not drain a GitHub request
// already accepted by a reconciler; an in-flight branch update or PR create may
// still finish.
func (s *Server) AuthorizeTask(ctx context.Context, dyn dynamic.Interface, cluster, repositoryRef string, visible *unstructured.Unstructured, issuer dataplane.ProxiedIdentity, raw json.RawMessage) (any, error) {
	if !issuer.IsForeignProvider(cluster) || issuer.User == "" || issuer.ClusterName() == "" {
		return nil, errors.New("task authorization requires a trusted consumer provider identity")
	}
	in, err := decodeAuthorization(raw)
	if err != nil {
		return nil, err
	}
	if len(in.Repository) > 256 || in.PullRequest == "" || len(in.PullRequest) > 253 || in.PullRequestUID == "" || len(in.PullRequestUID) > 128 ||
		in.RepositoryUID == "" || len(in.RepositoryUID) > 128 || in.ConnectionUID == "" || len(in.ConnectionUID) > 128 ||
		len(in.SourceUID) == 0 || len(in.SourceUID) > 128 || !sourceDigestPattern.MatchString(in.SourceDigest) {
		return nil, errors.New("task authorization identity is incomplete")
	}
	if in.Op != "grant" && in.Op != "scope" && in.Op != "revoke" {
		return nil, errors.New("unknown task authorization operation")
	}
	visibleRepo, err := decode[api.Repository](visible)
	if err != nil || visibleRepo.Name != repositoryRef || string(visibleRepo.UID) != in.RepositoryUID || visibleRepo.DeletionTimestamp != nil {
		return nil, errors.New("repository identity changed")
	}
	if len(in.FinalStatusBody) > 16384 || in.FinalStatusBody != "" && in.Op != "revoke" {
		return nil, errors.New("final status is bounded and permitted only with revocation")
	}
	b, err := s.taskBinding(ctx, dyn, repositoryRef, in.RepositoryUID, in.ConnectionUID, false, in.Op != "revoke")
	if err != nil {
		return nil, err
	}
	canonicalRepository := repositoryName(b.binding)
	if in.Repository != "" && in.Repository != canonicalRepository {
		return nil, errors.New("canonical repository identity changed")
	}
	in.Repository = canonicalRepository
	repositoryID, parseErr := strconv.ParseInt(b.repo.Status.RepoID, 10, 64)
	if parseErr != nil || repositoryID <= 0 {
		return nil, errors.New("registered repository identity is unavailable")
	}
	prObject, err := dyn.Resource(PullRequests).Get(ctx, in.PullRequest, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	pr, err := decode[api.PullRequest](prObject)
	if err != nil {
		return nil, err
	}
	if string(pr.UID) != in.PullRequestUID || pr.Spec.RepositoryRef != repositoryRef || pr.Spec.Branch == "" || pr.Spec.Base == "" ||
		pr.Spec.Branch == "main" || pr.Spec.Branch == pr.Spec.Base || pr.DeletionTimestamp != nil {
		return nil, errors.New("pull request identity or branch scope is invalid")
	}
	if in.Op != "grant" {
		return s.taskAuthorizationControl(ctx, dyn, cluster, repositoryRef, b, pr, issuer, in)
	}
	if revokedTaskSource(pr.Spec.TaskAuthorizationRevocations, in.SourceUID) {
		return nil, errors.New("task authorization source was revoked")
	}
	now := s.now()
	deadline := in.Deadline.UTC()
	if deadline.IsZero() || !deadline.After(now) || deadline.After(now.Add(maxTaskGrantLifetime)) {
		return nil, errors.New("task authorization deadline is expired or exceeds 24 hours")
	}
	if in.Mode != "review-fix" {
		return nil, errors.New("unsupported task authorization mode")
	}
	grant, err := taskGrantForRequest(repositoryRef, repositoryID, b.key, pr, issuer, in)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTaskCaller(ctx, dyn, grant); err != nil {
		return nil, err
	}
	if pr.Spec.Command != nil {
		if pr.Spec.Command.Task == nil || pr.Spec.Command.Task.GrantRef != grant.GrantRef || !sameTaskAuthorization(pr.Spec.Command.Task, grant) {
			return nil, errors.New("pull request already has a different command authority")
		}
		if s.now().After(pr.Spec.Command.Deadline.Time) {
			return nil, errors.New("task authorization is expired")
		}
	} else {
		pr.Spec.Command = &api.PullRequestCommand{Deadline: grant.Deadline, Task: grant}
		u, err := asU(pr)
		if err != nil {
			return nil, err
		}
		if _, err = dyn.Resource(PullRequests).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			return nil, fmt.Errorf("persisting task authorization: %w", err)
		}
	}
	return taskAuthorizationResult(repositoryRef, pr, grant, ""), nil
}

func taskGrantForRequest(repositoryRef string, repositoryID int64, key []byte, pr *api.PullRequest, issuer dataplane.ProxiedIdentity, in TaskAuthorizationInput) (*api.PullRequestTaskAuthorization, error) {
	caller, err := canonicalCaller(in.Caller)
	if err != nil {
		return nil, err
	}
	grant := &api.PullRequestTaskAuthorization{
		SourceUID: in.SourceUID, SourceDigest: in.SourceDigest,
		RepositoryRef: repositoryRef, Repository: in.Repository, RepositoryID: repositoryID, RepositoryUID: in.RepositoryUID, ConnectionUID: in.ConnectionUID,
		PullRequestUID: in.PullRequestUID, PullRequestRef: pr.Name, Branch: pr.Spec.Branch, Base: pr.Spec.Base,
		Caller: caller.User, CallerUID: caller.UID, CallerGroups: caller.Groups,
		Deadline: metav1.NewTime(in.Deadline.UTC()), Mode: in.Mode, IssuerUser: issuer.User, IssuerCluster: issuer.ClusterName(), IssuerIdentityDigest: issuerIdentityDigest(issuer),
	}
	grant.GrantRef, err = taskGrantRef(grant)
	if err != nil {
		return nil, err
	}
	grant.Proof, err = taskGrantProof(key, grant)
	if err != nil {
		return nil, err
	}
	return grant, nil
}

func (s *Server) taskAuthorizationControl(ctx context.Context, dyn dynamic.Interface, cluster, repositoryName string, b taskBinding, pr *api.PullRequest, issuer dataplane.ProxiedIdentity, in TaskAuthorizationInput) (any, error) {
	if in.Op != "scope" && in.Op != "revoke" {
		return nil, errors.New("unknown task authorization operation")
	}
	command := pr.Spec.Command
	// An acknowledged tombstone retires only that source. A later command on
	// the same PR must neither prevent old cleanup nor have its status replaced.
	if in.Op == "revoke" && command != nil && (command.Task == nil || command.Task.SourceUID != in.SourceUID) {
		revocation := taskRevocation(in, issuer, s.now())
		if existing := findTaskRevocation(pr.Spec.TaskAuthorizationRevocations, in.SourceUID); existing != nil && sameTaskRevocation(existing, &revocation) {
			return revokedResult(repositoryName, pr.Name, in), nil
		}
	}
	if command == nil || command.Task == nil {
		if in.Op == "revoke" {
			if command != nil {
				return nil, errors.New("cannot revoke a different PR command")
			}
			revocation := taskRevocation(in, issuer, s.now())
			if existing := findTaskRevocation(pr.Spec.TaskAuthorizationRevocations, in.SourceUID); existing != nil {
				if !sameTaskRevocation(existing, &revocation) {
					return nil, errors.New("task source UID was already revoked with different scope")
				}
				return s.revokedWithFinalStatus(ctx, dyn, b, pr, repositoryName, issuer, in)
			}
			if len(pr.Spec.TaskAuthorizationRevocations) >= 64 {
				return nil, errors.New("pull request task revocation limit reached")
			}
			pr.Spec.TaskAuthorizationRevocations = append(pr.Spec.TaskAuthorizationRevocations, revocation)
			if err := updateAuthorization(ctx, dyn, pr); err != nil {
				return nil, fmt.Errorf("recording task revocation: %w", err)
			}
			return s.revokedWithFinalStatus(ctx, dyn, b, pr, repositoryName, issuer, in)
		}
		return nil, errors.New("active task authorization is unavailable")
	}
	grant := command.Task
	if !issuerMatches(grant, cluster, issuer) ||
		grant.SourceUID != in.SourceUID || grant.SourceDigest != in.SourceDigest || grant.RepositoryRef != repositoryName ||
		(in.Repository != "" && grant.Repository != in.Repository) || grant.RepositoryUID != in.RepositoryUID || grant.ConnectionUID != in.ConnectionUID || grant.PullRequestUID != in.PullRequestUID ||
		grant.PullRequestRef != in.PullRequest || (in.GrantRef != "" && grant.GrantRef != in.GrantRef) ||
		(in.Proof != "" && !hmac.Equal([]byte(grant.Proof), []byte(in.Proof))) {
		return nil, errors.New("task authorization source, issuer, or proof changed")
	}
	if in.Op == "revoke" {
		if command.EventUID != "" || command.EventRef != "" {
			return nil, errors.New("cannot revoke a mixed forge-event command")
		}
		revocation := taskRevocation(in, issuer, s.now())
		if existing := findTaskRevocation(pr.Spec.TaskAuthorizationRevocations, in.SourceUID); existing != nil && !sameTaskRevocation(existing, &revocation) {
			return nil, errors.New("task source UID was already revoked with different scope")
		}
		if findTaskRevocation(pr.Spec.TaskAuthorizationRevocations, in.SourceUID) == nil {
			if len(pr.Spec.TaskAuthorizationRevocations) >= 64 {
				return nil, errors.New("pull request task revocation limit reached")
			}
			pr.Spec.TaskAuthorizationRevocations = append(pr.Spec.TaskAuthorizationRevocations, revocation)
		}
		pr.Spec.Command = nil
		pr.Spec.DesiredHead = nil
		if err := updateAuthorization(ctx, dyn, pr); err != nil {
			return nil, fmt.Errorf("revoking task authorization: %w", err)
		}
		return s.revokedWithFinalStatus(ctx, dyn, b, pr, repositoryName, issuer, in)
	}
	if !verifyTaskGrant(b.key, grant) {
		return nil, errors.New("task authorization proof is invalid")
	}
	if !matchesTaskRequest(grant, repositoryName, in) {
		return nil, errors.New("task authorization source or proof changed")
	}
	if s.now().After(grant.Deadline.Time) {
		return nil, errors.New("task authorization is expired")
	}
	if err := s.authorizeTaskCaller(ctx, dyn, grant); err != nil {
		return nil, err
	}
	if in.InvocationKey == "" || !operationKeyPattern.MatchString(in.InvocationKey) || in.InvocationAgent == "" || in.InvocationAgentUID == "" || len(in.InvocationAgent) > 253 || len(in.InvocationAgentUID) > 128 {
		return nil, errors.New("invocation identity is incomplete")
	}
	result := taskAuthorizationResult(repositoryName, pr, grant, "")
	result.ScopeProof = taskScopeProof(b.key, grant, in.InvocationKey, in.InvocationAgent, in.InvocationAgentUID)
	return result, nil
}

func taskAuthorizationResult(repositoryName string, pr *api.PullRequest, grant *api.PullRequestTaskAuthorization, result string) TaskAuthorizationResult {
	out := TaskAuthorizationResult{GrantRef: grant.GrantRef, SourceUID: grant.SourceUID, SourceDigest: grant.SourceDigest, Repository: grant.Repository, RepositoryRef: repositoryName, RepositoryID: strconv.FormatInt(grant.RepositoryID, 10), RepositoryUID: grant.RepositoryUID, ConnectionUID: grant.ConnectionUID, PullRequest: pr.Name, PullRequestUID: grant.PullRequestUID, Deadline: grant.Deadline.UTC(), Mode: grant.Mode, Proof: grant.Proof}
	if result == "revoked" {
		out.Proof = ""
		out.Revoked = true
	}
	return out
}

func revokedResult(repositoryName, pullRequest string, in TaskAuthorizationInput) TaskAuthorizationResult {
	return TaskAuthorizationResult{GrantRef: in.GrantRef, SourceUID: in.SourceUID, SourceDigest: in.SourceDigest, Repository: in.Repository, RepositoryRef: repositoryName, RepositoryUID: in.RepositoryUID, ConnectionUID: in.ConnectionUID, PullRequest: pullRequest, PullRequestUID: in.PullRequestUID, Revoked: true}
}

func taskRevocation(in TaskAuthorizationInput, issuer dataplane.ProxiedIdentity, now time.Time) api.PullRequestTaskRevocation {
	return api.PullRequestTaskRevocation{SourceUID: in.SourceUID, SourceDigest: in.SourceDigest, RepositoryUID: in.RepositoryUID, ConnectionUID: in.ConnectionUID, PullRequestUID: in.PullRequestUID, IssuerUser: issuer.User, IssuerCluster: issuer.ClusterName(), IssuerIdentityDigest: issuerIdentityDigest(issuer), RevokedAt: metav1.NewTime(now)}
}

func issuerIdentityDigest(identity dataplane.ProxiedIdentity) string {
	groups := append([]string(nil), identity.Groups...)
	sort.Strings(groups)
	extra := make(map[string][]string, len(identity.Extra))
	for key, values := range identity.Extra {
		values = append([]string(nil), values...)
		sort.Strings(values)
		extra[key] = values
	}
	raw, _ := json.Marshal(struct {
		User   string
		Groups []string
		Extra  map[string][]string
	}{identity.User, groups, extra})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func findTaskRevocation(revocations []api.PullRequestTaskRevocation, sourceUID string) *api.PullRequestTaskRevocation {
	for i := range revocations {
		if revocations[i].SourceUID == sourceUID {
			return &revocations[i]
		}
	}
	return nil
}

func revokedTaskSource(revocations []api.PullRequestTaskRevocation, sourceUID string) bool {
	return findTaskRevocation(revocations, sourceUID) != nil
}

func sameTaskRevocation(a, b *api.PullRequestTaskRevocation) bool {
	return a != nil && b != nil && a.SourceUID == b.SourceUID && a.SourceDigest == b.SourceDigest &&
		a.RepositoryUID == b.RepositoryUID && a.ConnectionUID == b.ConnectionUID && a.PullRequestUID == b.PullRequestUID &&
		a.IssuerUser == b.IssuerUser && a.IssuerCluster == b.IssuerCluster && a.IssuerIdentityDigest == b.IssuerIdentityDigest
}

func updateAuthorization(ctx context.Context, dyn dynamic.Interface, pr *api.PullRequest) error {
	u, err := asU(pr)
	if err != nil {
		return err
	}
	_, err = dyn.Resource(PullRequests).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func issuerMatches(grant *api.PullRequestTaskAuthorization, cluster string, issuer dataplane.ProxiedIdentity) bool {
	return grant != nil && issuer.IsForeignProvider(cluster) && issuer.User == grant.IssuerUser && issuer.ClusterName() == grant.IssuerCluster &&
		hmac.Equal([]byte(issuerIdentityDigest(issuer)), []byte(grant.IssuerIdentityDigest))
}

func matchesTaskRequest(grant *api.PullRequestTaskAuthorization, repositoryName string, in TaskAuthorizationInput) bool {
	return grant != nil && grant.SourceUID == in.SourceUID && grant.SourceDigest == in.SourceDigest &&
		grant.RepositoryRef == repositoryName && (in.Repository == "" || grant.Repository == in.Repository) && grant.RepositoryUID == in.RepositoryUID && grant.ConnectionUID == in.ConnectionUID &&
		grant.PullRequestUID == in.PullRequestUID && grant.PullRequestRef == in.PullRequest && grant.GrantRef == in.GrantRef && hmac.Equal([]byte(grant.Proof), []byte(in.Proof))
}

func sameTaskAuthorization(a, b *api.PullRequestTaskAuthorization) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, _ := taskGrantPayload(a)
	right, _ := taskGrantPayload(b)
	return a.GrantRef == b.GrantRef && hmac.Equal(left, right) && hmac.Equal([]byte(a.Proof), []byte(b.Proof))
}

func verifyTaskGrant(key []byte, grant *api.PullRequestTaskAuthorization) bool {
	if grant == nil || !sourceDigestPattern.MatchString(grant.SourceDigest) || grant.Mode != "review-fix" {
		return false
	}
	ref, err := taskGrantRef(grant)
	if err != nil || !hmac.Equal([]byte(ref), []byte(grant.GrantRef)) {
		return false
	}
	proof, err := taskGrantProof(key, grant)
	return err == nil && hmac.Equal([]byte(proof), []byte(grant.Proof))
}

func (s *Server) activeTaskAuthorization(ctx context.Context, dyn dynamic.Interface, cluster string, pr *api.PullRequest) (*api.PullRequest, taskBinding, error) {
	var empty taskBinding
	if pr == nil || pr.Spec.Command == nil || pr.Spec.Command.Task == nil {
		return nil, empty, errors.New("PR has no active Factory task authorization")
	}
	grant := pr.Spec.Command.Task
	if !grant.Deadline.After(s.now()) || pr.Spec.Command.Deadline.Time.IsZero() || !pr.Spec.Command.Deadline.Time.Equal(grant.Deadline.Time) {
		return nil, empty, errors.New("task authorization deadline expired or changed")
	}
	currentObject, err := dyn.Resource(PullRequests).Get(ctx, pr.Name, metav1.GetOptions{})
	if err != nil {
		return nil, empty, err
	}
	fresh, err := decode[api.PullRequest](currentObject)
	if err != nil {
		return nil, empty, err
	}
	if fresh.UID != pr.UID || fresh.Spec.Command == nil || fresh.Spec.Command.Task == nil || !sameCommand(fresh.Spec.Command, pr.Spec.Command) ||
		fresh.Spec.RepositoryRef != grant.RepositoryRef || string(fresh.UID) != grant.PullRequestUID || fresh.Name != grant.PullRequestRef ||
		revokedTaskSource(fresh.Spec.TaskAuthorizationRevocations, grant.SourceUID) ||
		fresh.Spec.Branch != grant.Branch || fresh.Spec.Base != grant.Base {
		return nil, empty, errors.New("task authorization or PR branch scope changed")
	}
	b, err := s.taskBinding(ctx, dyn, grant.RepositoryRef, grant.RepositoryUID, grant.ConnectionUID, true, true)
	if err != nil {
		return nil, empty, err
	}
	if !verifyTaskGrant(b.key, grant) {
		return nil, empty, errors.New("task authorization proof is invalid")
	}
	if err := s.authorizeTaskCaller(ctx, dyn, grant); err != nil {
		return nil, empty, err
	}
	return fresh, b, nil
}

func (s *Server) taskObservation(ctx context.Context, b taskBinding, pr *api.PullRequest) (Observation, error) {
	number := pr.Status.Number
	if number == 0 && pr.Spec.Number > 0 {
		number = pr.Spec.Number
	}
	if number > 0 {
		var observed PullRequest
		if err := b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repositoryName(binding{repo: b.repo, conn: b.conn}), number), nil, &observed); err != nil {
			return Observation{}, err
		}
		repositoryID, parseErr := strconv.ParseInt(b.repo.Status.RepoID, 10, 64)
		if parseErr != nil || observed.Head.Ref != pr.Spec.Branch || observed.Base.Ref != pr.Spec.Base || observed.Head.Repo.ID != repositoryID || observed.Base.Repo.ID != repositoryID {
			return Observation{}, errors.New("PR identity changed")
		}
		e := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: repositoryName(binding{repo: b.repo, conn: b.conn})}}
		return observe(ctx, b.client, e, observed)
	}
	if s.Backends == nil {
		return Observation{}, errors.New("GitHub backend unavailable")
	}
	implementation, ok := s.Backends.Get(string(b.conn.Spec.Provider))
	if !ok {
		return Observation{}, errors.New("GitHub backend unavailable")
	}
	collaboration, ok := implementation.(backend.Collaboration)
	if !ok {
		return Observation{}, errors.New("GitHub branch reads unavailable")
	}
	base, err := collaboration.BranchHead(ctx, b.conn, b.credential, b.repo, pr.Spec.Base)
	if err != nil || base == "" {
		return Observation{}, errors.New("PR base branch is unavailable")
	}
	branch, err := collaboration.BranchHead(ctx, b.conn, b.credential, b.repo, pr.Spec.Branch)
	if err != nil || branch != "" {
		return Observation{}, errors.New("PR branch already exists outside this task")
	}
	return Observation{Head: base, BaseHead: base, CIState: "unknown", MergeableState: "unknown"}, nil
}

func (s *Server) taskPullRequest(ctx context.Context, b taskBinding, pr *api.PullRequest, number int64) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, errors.New("PR has not been opened yet")
	}
	var observed PullRequest
	err := b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repositoryName(binding{repo: b.repo, conn: b.conn}), number), nil, &observed)
	if err != nil {
		return PullRequest{}, err
	}
	repositoryID, parseErr := strconv.ParseInt(b.repo.Status.RepoID, 10, 64)
	if parseErr != nil || observed.Number != number || observed.Head.Ref != pr.Spec.Branch || observed.Base.Ref != pr.Spec.Base ||
		observed.Head.Repo.ID != repositoryID || observed.Base.Repo.ID != repositoryID {
		return PullRequest{}, errors.New("live PR identity changed")
	}
	return observed, nil
}

func taskPublishAllowed(grant *api.PullRequestTaskAuthorization, op string) bool {
	if grant == nil || grant.Mode != "review-fix" {
		return false
	}
	switch op {
	case "observe", "review", "publish":
		return true
	default:
		return false
	}
}

func (s *Server) operateTask(ctx context.Context, dyn dynamic.Interface, cluster string, prRecord *api.PullRequest, in Operation, identity dataplane.ProxiedIdentity) (any, error) {
	if !identity.IsForeignProvider(cluster) || in.EventUID != "" || in.PullRequestUID != string(prRecord.UID) {
		return nil, errors.New("task PR operations require a foreign consumer and the admitted PR UID")
	}
	fresh, b, err := s.activeTaskAuthorization(ctx, dyn, cluster, prRecord)
	if err != nil {
		return nil, err
	}
	grant := fresh.Spec.Command.Task
	if grant.SourceUID != in.SourceUID || grant.SourceDigest != in.SourceDigest || grant.GrantRef != in.GrantRef || !hmac.Equal([]byte(grant.Proof), []byte(in.SourceProof)) {
		return nil, errors.New("task source proof does not match the active Code grant")
	}
	if !operationKeyPattern.MatchString(in.InvocationKey) || in.InvocationAgent == "" || in.InvocationAgentUID == "" ||
		!hmac.Equal([]byte(roleProof(taskScopeProof(b.key, grant, in.InvocationKey, in.InvocationAgent, in.InvocationAgentUID), in.ReviewOnly, b.key)), []byte(in.ScopeProof)) {
		return nil, errors.New("invocation scope proof is invalid")
	}
	if in.ReviewOnly && in.Op != "observe" && in.Op != "review" {
		return nil, errors.New("delegated review authority permits only observe and review")
	}
	if in.Op == "delegate-review" {
		if err := validReviewDelegate(in); err != nil {
			return nil, err
		}
		child := in.Delegate
		proof := taskScopeProof(b.key, grant, child.InvocationKey, child.Agent, child.AgentUID)
		return map[string]any{"scopeProof": roleProof(proof, true, b.key)}, nil
	}
	if in.Op == "comment" {
		if !issuerMatches(grant, cluster, identity) {
			return nil, errors.New("only the grant issuer may update the PR status comment")
		}
		if len(in.Body) > 16384 {
			return nil, errors.New("PR status comment exceeds the Code limit")
		}
		number := effectivePullRequestNumber(fresh)
		if number <= 0 {
			return nil, errors.New("the pull request has not been opened yet; there is no status comment target")
		}
		e := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: repositoryName(binding{repo: b.repo, conn: b.conn}), PullRequest: number}}
		marker := fmt.Sprintf("<!-- railgrid-pr-status:%s -->", fresh.UID)
		return publishTextUpdating(ctx, b.client, e, 0, in.Body, marker, true)
	}
	if !taskPublishAllowed(grant, in.Op) {
		return nil, errors.New("task authorization does not permit this operation")
	}
	if in.Op == "observe" {
		observation, err := s.taskObservation(ctx, b, fresh)
		if err != nil {
			return nil, err
		}
		repositoryID, parseErr := strconv.ParseInt(b.repo.Status.RepoID, 10, 64)
		if parseErr != nil || repositoryID <= 0 {
			return nil, errors.New("registered repository identity is unavailable")
		}
		return map[string]any{"observation": observation, "repository": repositoryName(binding{repo: b.repo, conn: b.conn}), "repositoryID": repositoryID, "branch": fresh.Spec.Branch, "base": fresh.Spec.Base}, nil
	}
	if !operationKeyPattern.MatchString(in.Key) || len(in.Body) > 16384 {
		return nil, errors.New("a bounded idempotency key and body are required")
	}
	if in.Op == "review" {
		observed, err := s.taskPullRequest(ctx, b, fresh, effectivePullRequestNumber(fresh))
		if err != nil {
			return nil, err
		}
		if observed.State != "open" || in.ExpectedHead == "" || observed.Head.SHA != in.ExpectedHead {
			return nil, errors.New("review requires the exact current PR head")
		}
		if in.FindingsCount == nil || *in.FindingsCount < 0 {
			return nil, errors.New("review requires a nonnegative findingsCount; use zero for a clean review")
		}
		if err := validateReviewComments(in.Comments, *in.FindingsCount); err != nil {
			return nil, err
		}
		if *in.FindingsCount == 0 {
			return map[string]any{"suppressed": true, "reason": "clean review belongs in the PR status comment"}, nil
		}
		return publishReview(ctx, b.client, &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: repositoryName(binding{repo: b.repo, conn: b.conn}), PullRequest: effectivePullRequestNumber(fresh)}}, in.ExpectedHead, in.Body,
			fmt.Sprintf("<!-- railgrid-pr-task:%s:%s:review:%s:%s -->", grant.GrantRef, in.InvocationKey, in.ExpectedHead, in.Key), in.Comments)
	}
	if in.Op != "publish" {
		return nil, errors.New("unknown task operation")
	}
	if in.Snapshot == nil || in.ExpectedHead == "" || in.Snapshot.BaseCommit != in.ExpectedHead {
		return nil, errors.New("snapshot must extend the exact expected head")
	}
	if len(in.Snapshot.Bundle) > 34<<20 {
		return nil, errors.New("snapshot bundle exceeds the Code operation limit")
	}
	if fresh.Spec.Branch == "main" || fresh.Spec.Branch == fresh.Spec.Base {
		return nil, errors.New("task publication cannot target main or its base branch")
	}
	if effectivePullRequestNumber(fresh) > 0 {
		observed, err := s.taskPullRequest(ctx, b, fresh, effectivePullRequestNumber(fresh))
		if err != nil {
			return nil, err
		}
		if err = validTaskPublication(fresh, grant, observed, in); err != nil {
			return nil, err
		}
	} else {
		if fresh.Spec.DesiredHead != nil && fresh.Spec.DesiredHead.Commit != in.Snapshot.Commit {
			if fresh.Status.Head != fresh.Spec.DesiredHead.Commit {
				return nil, errors.New("previous task publication has not been observed yet")
			}
		}
		implementation, ok := s.Backends.Get(string(b.conn.Spec.Provider))
		if !ok {
			return nil, errors.New("GitHub backend unavailable")
		}
		collaboration, ok := implementation.(backend.Collaboration)
		if !ok {
			return nil, errors.New("GitHub branch reads unavailable")
		}
		base, err := collaboration.BranchHead(ctx, b.conn, b.credential, b.repo, fresh.Spec.Base)
		if err != nil || base == "" || base != in.ExpectedHead {
			return nil, errors.New("task snapshot is not based on the current PR base")
		}
		branch, err := collaboration.BranchHead(ctx, b.conn, b.credential, b.repo, fresh.Spec.Branch)
		if err != nil || (branch != "" && branch != in.Snapshot.Commit) {
			return nil, errors.New("task PR branch already exists outside this publication")
		}
	}
	if s.Snapshots == nil {
		return nil, errors.New("snapshot store unavailable")
	}
	implementation, ok := s.Backends.Get(string(b.conn.Spec.Provider))
	if !ok {
		return nil, errors.New("GitHub backend unavailable")
	}
	publisher, ok := implementation.(backend.SnapshotPublisher)
	if !ok {
		return nil, errors.New("snapshot validation unavailable")
	}
	if err = publisher.VerifySnapshot(ctx, b.conn, b.credential, b.repo, *in.Snapshot); err != nil {
		return nil, err
	}
	if fresh.Spec.DesiredHead != nil && fresh.Spec.DesiredHead.Commit == in.Snapshot.Commit && fresh.Spec.DesiredHead.BaseCommit == in.ExpectedHead {
		return map[string]any{"commit": in.Snapshot.Commit, "desiredHeadRecorded": true, "reused": true}, nil
	}
	raw, err := json.Marshal(in.Snapshot)
	if err != nil {
		return nil, err
	}
	ref, err := s.Snapshots.Stage(ctx, cluster, "pr-task:"+grant.GrantRef, raw)
	if err != nil {
		return nil, err
	}
	fresh.Spec.DesiredHead = &api.PullRequestHead{Commit: in.Snapshot.Commit, BaseCommit: in.ExpectedHead, Tree: in.Snapshot.Tree, BundleRef: ref, Message: in.Snapshot.Message}
	u, err := asU(fresh)
	if err != nil {
		return nil, err
	}
	if _, err = dyn.Resource(PullRequests).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("recording desired task snapshot: %w", err)
	}
	return map[string]any{"commit": in.Snapshot.Commit, "desiredHeadRecorded": true}, nil
}

func effectivePullRequestNumber(pr *api.PullRequest) int64 {
	if pr.Status.Number > 0 {
		return int64(pr.Status.Number)
	}
	return int64(pr.Spec.Number)
}

func validTaskPublication(prRecord *api.PullRequest, grant *api.PullRequestTaskAuthorization, pr PullRequest, in Operation) error {
	if grant == nil || grant.Mode != "review-fix" || prRecord.Spec.RepositoryRef != grant.RepositoryRef ||
		prRecord.Spec.Branch != grant.Branch || prRecord.Spec.Base != grant.Base || prRecord.Spec.Branch == "main" || prRecord.Spec.Branch == prRecord.Spec.Base {
		return errors.New("task has no code-write authority for this PR target")
	}
	if pr.State != "open" || pr.Head.Ref != grant.Branch || pr.Base.Ref != grant.Base ||
		pr.Head.Repo.ID <= 0 || pr.Head.Repo.ID != pr.Base.Repo.ID {
		return errors.New("task publication target is not the admitted open PR")
	}
	if in.Snapshot == nil || in.Snapshot.BaseCommit != in.ExpectedHead || in.ExpectedHead == "" {
		return errors.New("snapshot must extend the exact expected head")
	}
	if pr.Head.SHA != in.ExpectedHead && pr.Head.SHA != in.Snapshot.Commit {
		return errors.New("PR head changed; publication refused")
	}
	if prRecord.Spec.DesiredHead != nil && prRecord.Spec.DesiredHead.Commit != in.Snapshot.Commit && prRecord.Status.Head != prRecord.Spec.DesiredHead.Commit {
		return errors.New("previous task publication has not been observed yet")
	}
	return nil
}

// Revocation is already persisted before this optional publication. A lost
// response or forge failure can replay it through the issuer-bound tombstone;
// no worker scope, live grant, deadline extension or new lifecycle record exists.
func (s *Server) revokedWithFinalStatus(ctx context.Context, dyn dynamic.Interface, b taskBinding, pr *api.PullRequest, repository string, issuer dataplane.ProxiedIdentity, in TaskAuthorizationInput) (any, error) {
	// Revocations are appended durably. Once a newer task has retired, an
	// older issuer's delayed cleanup must not replace its completed status.
	revocations := pr.Spec.TaskAuthorizationRevocations
	if len(revocations) > 0 && revocations[len(revocations)-1].SourceUID != in.SourceUID {
		return revokedResult(repository, pr.Name, in), nil
	}
	if in.FinalStatusBody != "" && effectivePullRequestNumber(pr) > 0 {
		// Cleanup remains possible after expiry or permission revocation. A
		// tombstone alone is not proof that this issuer ever held a grant:
		// pre-admission cancellation can also create one. Require the original
		// signed coordinates before borrowing the App to publish final status.
		verification, err := s.taskBinding(ctx, dyn, b.repo.Name, in.RepositoryUID, in.ConnectionUID, false, true)
		if err != nil {
			return nil, fmt.Errorf("publication authority revoked; original grant verification unavailable: %w", err)
		}
		repositoryID, parseErr := strconv.ParseInt(b.repo.Status.RepoID, 10, 64)
		grant, proofErr := taskGrantForRequest(repository, repositoryID, verification.key, pr, issuer, in)
		if parseErr != nil || proofErr != nil || grant.GrantRef != in.GrantRef || !hmac.Equal([]byte(grant.Proof), []byte(in.Proof)) {
			return nil, errors.New("publication authority revoked; final status requires the original signed task grant")
		}
		// Do not require forge credentials before revocation is durably saved.
		publication, err := s.taskBinding(ctx, dyn, b.repo.Name, in.RepositoryUID, in.ConnectionUID, true, false)
		if err != nil {
			return nil, fmt.Errorf("publication authority revoked; final status credentials unavailable: %w", err)
		}
		e := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: repositoryName(b.binding), PullRequest: effectivePullRequestNumber(pr)}}
		if _, err := publishTextUpdating(ctx, publication.client, e, 0, in.FinalStatusBody, fmt.Sprintf("<!-- railgrid-pr-status:%s -->", pr.UID), true); err != nil {
			return nil, fmt.Errorf("publication authority revoked; final status publication pending: %w", err)
		}
	}
	return revokedResult(repository, pr.Name, in), nil
}
