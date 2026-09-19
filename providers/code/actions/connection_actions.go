// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ConnectionInput is the input of every connection-bound action. The UID pins
// what the caller saw to what this provider reads with its own identity: a
// Connection deleted and recreated under the same name between the two reads
// is a different credential, and the action must fail rather than serve it.
type ConnectionInput struct {
	ConnectionUID string `json:"connectionUID"`
}

// RegistryTokenOutput is what mint_registry_token returns. It is a pull
// credential and its metadata — never the Connection's own credential, and
// never anything about the Secret it came from.
type RegistryTokenOutput struct {
	// Registry is the OCI host to authenticate to.
	Registry string `json:"registry"`
	// Username accompanies the token in a docker config.
	Username string `json:"username"`
	// Token is the pull credential.
	Token string `json:"token"`
	// ExpiresAt is RFC 3339, or absent when the credential does not expire
	// on its own.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// Scoped reports whether the token was issued with pull-only
	// permissions. A consumer that needs a genuinely least-privilege pull
	// secret can refuse to store an unscoped one.
	Scoped bool `json:"scoped"`
}

// runConnection is the executor for connection-bound actions, after both
// gates have passed and the body has been decoded.
func (s *Server) runConnection(ctx context.Context, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	var in ConnectionInput
	if err := decodeStrict(raw, &in); err != nil {
		return nil, wireError("invalid_action_input")
	}
	conn, data, store, secretKey, err := s.resolveConnection(ctx, req.ClusterID, req.Name, visible, in)
	if err != nil {
		return nil, wireError("action_forbidden")
	}
	switch req.Verb {
	case MintRegistryToken:
		token, err := s.Credentials.MintRegistryToken(ctx, conn, conn.Status.Login, secretKey, data, store)
		if err != nil {
			return nil, wireError("registry_token_unavailable")
		}
		out := RegistryTokenOutput{Registry: token.Registry, Username: token.Username, Token: token.Token, Scoped: token.Scoped}
		if !token.ExpiresAt.IsZero() {
			out.ExpiresAt = token.ExpiresAt.UTC().Format(time.RFC3339)
		}
		return out, nil
	default:
		return nil, wireError("unsupported_action")
	}
}

// resolveConnection pins what gate 1 returned against this provider's own read
// of the Connection through its APIExport, then opens the credential Secret.
// The caller never reads that Secret: it holds the credential the action
// exists to avoid handing out.
func (s *Server) resolveConnection(ctx context.Context, cluster, name string, visible *unstructured.Unstructured, in ConnectionInput) (*api.Connection, map[string][]byte, tenant.SecretStore, string, error) {
	fail := func() (*api.Connection, map[string][]byte, tenant.SecretStore, string, error) {
		return nil, nil, nil, "", errors.New("connection action denied")
	}
	if s.Authority == nil || in.ConnectionUID == "" || string(visible.GetUID()) != in.ConnectionUID {
		return fail()
	}
	provider, err := s.Authority(ctx, cluster, connections, name)
	if err != nil {
		return fail()
	}
	authoritative, err := provider.Resource(connections).Get(ctx, name, metav1.GetOptions{})
	if err != nil || string(authoritative.GetUID()) != in.ConnectionUID || authoritative.GetDeletionTimestamp() != nil ||
		!reflect.DeepEqual(authoritative.Object["spec"], visible.Object["spec"]) {
		return fail()
	}
	var conn api.Connection
	if runtime.DefaultUnstructuredConverter.FromUnstructured(authoritative.Object, &conn) != nil {
		return fail()
	}
	ns := conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	store := &tenant.DynamicSecretStore{Client: provider, Namespace: ns, Name: conn.Spec.SecretRef.Name}
	data, _, err := store.Load(ctx)
	if err != nil {
		return fail()
	}
	return &conn, data, store, tenant.CredentialSecretID(&conn, ns), nil
}

// decodeStrict reads an action's own input the way the envelope around it is
// read: an unknown member is a caller bug and is refused, not ignored.
func decodeStrict(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err == nil {
		return errors.New("unexpected trailing action input")
	}
	return nil
}
