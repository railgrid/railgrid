// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package commandhook receives a Repository's GitHub "Issue comments"
// webhook and turns a "/railgrid <command>" comment into a PullRequestCommand.
//
// The webhook URL is the credential: an HMAC over the workspace and
// Repository name under a provider-wide key, with nothing stored. Even so a
// delivery is only a doorbell. The handler takes from the payload nothing
// but which comment to look at and records it unverified; the PullRequestCommand
// reconciler reads the comment, its author's permission and the pull request
// back from GitHub with the Repository's own credential before anything can
// act on it. A forged or replayed payload can at most make Code read a real
// comment.
package commandhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// PathPrefix is where the hub forwards this provider's webhooks.
const PathPrefix = "/services/providers/code/webhooks/github/"

// Pattern is the route the provider serves, below the hub prefix.
const Pattern = "/webhooks/github/{cluster}/{name}/{token}"

const kubeconfigSalt = "railgrid-code-commands-webhook"

// Key resolves the signing key: the explicit key when the operator set one
// (CODE_WEBHOOK_KEY), else one derived from the provider kubeconfig, which is
// stable across restarts and identical on every replica. Nil means no URL can
// be minted or accepted.
func Key(explicit, providerKubeconfig string) []byte {
	if explicit != "" {
		return []byte(explicit)
	}
	if providerKubeconfig != "" {
		if b, err := os.ReadFile(providerKubeconfig); err == nil {
			sum := sha256.Sum256(append(b, []byte(kubeconfigSalt)...))
			return sum[:]
		}
	}
	return nil
}

// KeyFromEnv is Key from this provider's environment.
func KeyFromEnv() []byte {
	kubeconfig := os.Getenv("RAILGRID_PROVIDER_KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.Getenv("CODE_KUBECONFIG")
	}
	return Key(os.Getenv("CODE_WEBHOOK_KEY"), kubeconfig)
}

// Token is the HMAC guarding one Repository's webhook URL; empty without a
// key.
func Token(key []byte, clusterID, name string) string {
	if len(key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(clusterID + "/" + name))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// Path is the hub-relative webhook URL for a Repository, or "" without a key.
func Path(key []byte, clusterID, name string) string {
	token := Token(key, clusterID, name)
	if token == "" || clusterID == "" || name == "" {
		return ""
	}
	return PathPrefix + clusterID + "/" + name + "/" + token
}

// ValidToken reports whether token guards clusterID/name, in constant time.
func ValidToken(key []byte, clusterID, name, token string) bool {
	want := Token(key, clusterID, name)
	return want != "" && hmac.Equal([]byte(want), []byte(token))
}

// ObjectName is the PullRequestCommand recorded for one comment. A
// Repository name too long to carry is replaced by its digest.
func ObjectName(repository string, commentID int64) string {
	name := fmt.Sprintf("%s-comment-%d", repository, commentID)
	if len(name) <= 253 {
		return name
	}
	sum := sha256.Sum256([]byte(repository))
	return fmt.Sprintf("repository-%s-comment-%d", hex.EncodeToString(sum[:8]), commentID)
}

var (
	commandPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	argumentPattern = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,64}$`)
)

// Prefix opens every command.
const Prefix = "/railgrid"

// MaxArgs bounds a command's arguments.
const MaxArgs = 8

// LooksLikeCommand is the cheap test the webhook applies to a payload before
// recording anything: the comment's first non-empty line starts "/railgrid".
func LooksLikeCommand(body string) bool {
	line := firstLine(body)
	return line == Prefix || strings.HasPrefix(line, Prefix+" ") || strings.HasPrefix(line, Prefix+"\t")
}

// Parse reads the command from a comment's first non-empty line:
// "/railgrid <command> [args...]". ok is false for anything else, with a
// reason a person can act on.
func Parse(body string) (command string, args []string, reason string, ok bool) {
	if !LooksLikeCommand(body) {
		return "", nil, "the comment does not start with /railgrid", false
	}
	fields := strings.Fields(firstLine(body))[1:]
	if len(fields) == 0 {
		return "", nil, "the comment names no command after /railgrid", false
	}
	if !commandPattern.MatchString(fields[0]) {
		return "", nil, "the command is not a lower-case word", false
	}
	if len(fields)-1 > MaxArgs {
		return "", nil, fmt.Sprintf("the command has more than %d arguments", MaxArgs), false
	}
	for _, arg := range fields[1:] {
		if !argumentPattern.MatchString(arg) {
			return "", nil, "a command argument contains unsupported characters", false
		}
	}
	return fields[0], append([]string(nil), fields[1:]...), "", true
}

func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
