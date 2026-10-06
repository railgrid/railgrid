// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The `repository` block of the `run` verb: what turns an unattended run into
// a REPOSITORY attempt on the agent's harness.
//
// A coding coordinator (the Factory provider) names an Agent for its edge,
// harness and credential, and sends the repository, the approved commit and a
// short-lived clone token with each run. The token is dispatch data exactly
// like the harness credential — held in memory for the turn, never on the
// run record, never in a transcript row, never in a log line — and the rest
// of the block minus the credential is recorded on the run (store.Run.Repository,
// projected to the Run object's spec.repository) so a reader knows what ran.
//
// Validation here mirrors the runner's own (pkg/runner validateStartRequest,
// validateCloneSource, validateCommitMessage), which are not exported: a
// request the runner would refuse is refused at the verb, with a field named,
// before a run record exists for it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/railgrid/railgrid/pkg/runner"

	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
)

// invokeRepository is the `repository` member of invokeRunRequest.
type invokeRepository struct {
	RepositoryID string `json:"repositoryID"`
	BaseCommit   string `json:"baseCommit"`
	// CloneSource is where the runner fetches the repository from, with the
	// credential to do it. Required: a repository attempt runs in a fresh
	// clone, and the runner keeps no checkout of its own.
	CloneSource *invokeCloneSource `json:"cloneSource"`
	// CommitMessage is the subject the exported snapshot commit carries: one
	// line, at most 200 bytes. Empty keeps the runner's canonical message.
	CommitMessage        string              `json:"commitMessage,omitempty"`
	RequiredCapabilities []string            `json:"requiredCapabilities,omitempty"`
	RequiredToolchains   []string            `json:"requiredToolchains,omitempty"`
	RequiredEnvironment  []string            `json:"requiredEnvironment,omitempty"`
	Verification         *invokeVerification `json:"verification,omitempty"`
	// ApprovedInput is forwarded verbatim as the dispatch's approvedInput. It
	// must be a JSON object when present.
	ApprovedInput json.RawMessage `json:"approvedInput,omitempty"`
	// MaxDurationSeconds bounds the attempt on the runner and the run's own
	// clock. 0 is the agent's spec.limits.timeoutSeconds; the cap is a day.
	MaxDurationSeconds int `json:"maxDurationSeconds,omitempty"`
}

type invokeCloneSource struct {
	RemoteURL string `json:"remoteURL"`
	Username  string `json:"username,omitempty"`
	Token     string `json:"token,omitempty"`
}

type invokeVerification struct {
	Names    []string `json:"names,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

// repositoryAttempt is the validated block as the run lifecycle carries it:
// the backend's coordinates (token included, for the length of the turn) and
// the duration the run is bounded to.
type repositoryAttempt struct {
	backendharness.Repository
	MaxDurationSeconds int
}

// Bounds on the block. The identifier and commit grammars are the runner's
// (pkg/runner/config.go, pkg/runner/workspace.go).
const (
	maxRepositoryListEntries  = 32
	maxRepositoryListEntry    = 128
	maxVerificationCommand    = 4096
	maxCloneUsernameBytes     = 256
	maxCloneTokenBytes        = 4096
	maxRepositoryCommitBytes  = 200
	maxApprovedInputBytes     = 64 << 10
	maxRepositoryDurationSecs = 86400
)

var (
	runnerIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	fullCommit       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// validate checks the block and renders it for the lifecycle. Every refusal
// names the field, in the input's own spelling.
func (r *invokeRepository) validate() (*repositoryAttempt, error) {
	if r == nil {
		return nil, nil
	}
	out := &repositoryAttempt{}
	out.RepositoryID = strings.TrimSpace(r.RepositoryID)
	if !runnerIdentifier.MatchString(out.RepositoryID) {
		return nil, errors.New("repository.repositoryID must be a runner identifier (letters, digits, '.', '_' or '-'; at most 128 characters)")
	}
	out.BaseCommit = strings.ToLower(strings.TrimSpace(r.BaseCommit))
	if !fullCommit.MatchString(out.BaseCommit) {
		return nil, errors.New("repository.baseCommit must be a full 40-hex commit id")
	}
	if r.CloneSource == nil {
		return nil, errors.New("repository.cloneSource is required")
	}
	source, err := r.CloneSource.validate()
	if err != nil {
		return nil, err
	}
	out.Source = source
	if out.CommitMessage, err = validateCommitMessage(r.CommitMessage); err != nil {
		return nil, err
	}
	if out.RequiredCapabilities, err = validateRepositoryList("repository.requiredCapabilities", r.RequiredCapabilities, maxRepositoryListEntry); err != nil {
		return nil, err
	}
	if out.RequiredToolchains, err = validateRepositoryList("repository.requiredToolchains", r.RequiredToolchains, maxRepositoryListEntry); err != nil {
		return nil, err
	}
	if out.RequiredEnvironment, err = validateRepositoryList("repository.requiredEnvironment", r.RequiredEnvironment, maxRepositoryListEntry); err != nil {
		return nil, err
	}
	if v := r.Verification; v != nil {
		if out.Verification.Names, err = validateRepositoryList("repository.verification.names", v.Names, maxRepositoryListEntry); err != nil {
			return nil, err
		}
		if out.Verification.Commands, err = validateRepositoryList("repository.verification.commands", v.Commands, maxVerificationCommand); err != nil {
			return nil, err
		}
	}
	if len(r.ApprovedInput) > 0 {
		if len(r.ApprovedInput) > maxApprovedInputBytes {
			return nil, fmt.Errorf("repository.approvedInput must be at most %d bytes", maxApprovedInputBytes)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(r.ApprovedInput, &object); err != nil || object == nil {
			return nil, errors.New("repository.approvedInput must be a JSON object")
		}
		out.ApprovedInput = append(json.RawMessage(nil), r.ApprovedInput...)
	}
	switch {
	case r.MaxDurationSeconds < 0:
		return nil, errors.New("repository.maxDurationSeconds must not be negative")
	case r.MaxDurationSeconds > maxRepositoryDurationSecs:
		return nil, fmt.Errorf("repository.maxDurationSeconds must be at most %d", maxRepositoryDurationSecs)
	}
	out.MaxDurationSeconds = r.MaxDurationSeconds
	return out, nil
}

// validate checks the clone source the way the runner will: an https or ssh
// remote with no embedded credential, and a token only where https can carry
// one.
func (c *invokeCloneSource) validate() (*runner.RepositorySource, error) {
	remote := c.RemoteURL
	if err := validateRemoteURL(remote); err != nil {
		return nil, fmt.Errorf("repository.cloneSource.remoteURL %s", err)
	}
	username := strings.TrimSpace(c.Username)
	if len(username) > maxCloneUsernameBytes || strings.ContainsAny(username, ":\r\n") || hasControlCharacter(username) {
		return nil, errors.New("repository.cloneSource.username contains an invalid character or is too long")
	}
	token := c.Token
	if len(token) > maxCloneTokenBytes || hasControlCharacter(token) || strings.TrimSpace(token) != token {
		return nil, errors.New("repository.cloneSource.token contains an invalid character or is too long")
	}
	if token != "" && !strings.HasPrefix(strings.ToLower(remote), "https://") {
		return nil, errors.New("repository.cloneSource.token is only used with an https remote")
	}
	return &runner.RepositorySource{RemoteURL: remote, Username: username, Token: token}, nil
}

// validateRemoteURL admits an https:// or ssh:// URL, or an scp-style
// user@host:path remote, none of them carrying a password or an option. The
// runner accepts local paths too; a provider dispatching across a network
// has no use for them and does not.
func validateRemoteURL(raw string) error {
	switch {
	case raw == "":
		return errors.New("is required")
	case strings.TrimSpace(raw) != raw:
		return errors.New("must not have surrounding whitespace")
	case strings.HasPrefix(raw, "-"):
		return errors.New("must not begin with an option")
	case hasControlCharacter(raw):
		return errors.New("contains a control character")
	}
	if isSCPRemote(raw) {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("is malformed")
	}
	var ssh bool
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "ssh":
		ssh = true
	default:
		return errors.New("must be an https:// or ssh:// URL, or an scp-style ssh remote")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("has unsupported options")
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if !ssh || hasPassword {
			return errors.New("must not contain embedded credentials")
		}
		if hasControlCharacter(parsed.User.Username()) {
			return errors.New("has an invalid SSH user")
		}
	}
	host := parsed.Hostname()
	if host == "" || strings.HasPrefix(host, "-") {
		return errors.New("has an invalid host")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return errors.New("has no repository path")
	}
	return nil
}

// isSCPRemote mirrors the runner's reading of "user@host:path".
func isSCPRemote(value string) bool {
	if strings.HasPrefix(strings.ToLower(value), "ext::") {
		return false
	}
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 || strings.Contains(value[:separator], "/") {
		return false
	}
	if strings.HasPrefix(value[separator+1:], "//") {
		return false
	}
	if strings.ContainsAny(value, " \t\r\n?#") || hasControlCharacter(value) {
		return false
	}
	userHost := value[:separator]
	host := userHost
	if at := strings.LastIndexByte(userHost, '@'); at >= 0 {
		if at == 0 || at == len(userHost)-1 || strings.Contains(userHost[:at], ":") {
			return false
		}
		host = userHost[at+1:]
	}
	return host != "" && !strings.HasPrefix(host, "-") && !strings.ContainsAny(host, " \t\r\n/?#")
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// validateCommitMessage admits one line of printable text within the
// runner's bound, trimmed. Empty is allowed and means the canonical message.
func validateCommitMessage(message string) (string, error) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > maxRepositoryCommitBytes || strings.ContainsAny(message, "\n\r") {
		return "", fmt.Errorf("repository.commitMessage must be one line of at most %d bytes", maxRepositoryCommitBytes)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("repository.commitMessage must not contain control characters")
		}
	}
	return trimmed, nil
}

// validateRepositoryList bounds a list of names: how many, how long, and
// printable. Entries are trimmed; an empty one is refused rather than dropped.
func validateRepositoryList(field string, values []string, maxEntry int) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxRepositoryListEntries {
		return nil, fmt.Errorf("%s must have at most %d entries", field, maxRepositoryListEntries)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > maxEntry || hasControlCharacter(v) {
			return nil, fmt.Errorf("%s entries must be non-empty printable text of at most %d bytes", field, maxEntry)
		}
		out = append(out, v)
	}
	return out, nil
}

// persisted is the block as the run record keeps it: what ran, and not how
// to clone it.
func (r *repositoryAttempt) persisted() *store.RunRepository {
	if r == nil {
		return nil
	}
	return &store.RunRepository{RepositoryID: r.RepositoryID, BaseCommit: r.BaseCommit, CommitMessage: r.CommitMessage}
}

// repositoryAttemptFromStored rebuilds the attempt for a run picked up after
// the process that started it is gone: the coordinates survive on the record,
// the clone source does not. A re-join needs none; a fresh start without one
// fails with backendharness.ErrCloneCredentialUnavailable.
func repositoryAttemptFromStored(stored *store.RunRepository) *repositoryAttempt {
	if stored == nil {
		return nil
	}
	return &repositoryAttempt{Repository: backendharness.Repository{
		RepositoryID: stored.RepositoryID, BaseCommit: stored.BaseCommit, CommitMessage: stored.CommitMessage,
	}}
}
