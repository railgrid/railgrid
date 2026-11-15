// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package commandhook receives a Repository's GitHub "Issue comments"
// webhook and turns a "/railgrid <command>" comment into a PullRequestCommand.
//
// Code authenticates the raw delivery with the repository's webhook secret.
// The command reconciler then reads the comment, the author's permissions and
// the pull request back from GitHub before a consumer can act on it.
package commandhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PathPrefix is where the hub forwards this provider's webhooks.
const PathPrefix = "/services/providers/code/webhooks/github/"

// Pattern is the route the provider serves, below the hub prefix.
const Pattern = "/webhooks/github/{cluster}/{name}"

// MaxCommandAge bounds the age of a comment at both intake and verification.
const MaxCommandAge = time.Hour

// SecretKey is the default key of the Repository's webhook signing Secret.
const SecretKey = "secret"

const signaturePrefix = "sha256="
const signatureLength = len(signaturePrefix) + sha256.Size*2

// Path is the hub-relative webhook URL for a Repository.
func Path(clusterID, name string) string {
	return PathPrefix + clusterID + "/" + name
}

func validSignature(secret, signature string, body []byte) bool {
	if secret == "" || len(signature) != signatureLength {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(signature), []byte(signaturePrefix+hex.EncodeToString(mac.Sum(nil))))
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
