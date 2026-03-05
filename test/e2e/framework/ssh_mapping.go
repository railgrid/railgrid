/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package framework provides shared test infrastructure for kedge e2e tests.
package framework

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	gossh "golang.org/x/crypto/ssh"
)

// GenerateTestSSHKeypair generates a 2048-bit RSA keypair for use in e2e tests.
// It returns the RSA private key, the SSH public key, and the PEM-encoded
// private key bytes (PKCS#1 RSA PRIVATE KEY format accepted by gossh.ParsePrivateKey).
func GenerateTestSSHKeypair() (*rsa.PrivateKey, gossh.PublicKey, []byte, error) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating RSA key: %w", err)
	}

	sshPub, err := gossh.NewPublicKey(&privKey.PublicKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating SSH public key: %w", err)
	}

	privDER := x509.MarshalPKCS1PrivateKey(privKey)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privDER,
	})

	return privKey, sshPub, privPEM, nil
}

// IndentLines prepends prefix to every non-empty line in text.
// Used for embedding PEM blocks in YAML manifests.
func IndentLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// ResolveCallerIdentity performs a TokenReview against the hub to discover the
// username associated with the hub kubeconfig's bearer token.  Returns an
// empty string (without error) if the token is unauthenticated or the server
// does not support TokenReview.
func ResolveCallerIdentity(ctx context.Context, kubeconfigPath string) (string, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	if cfg.BearerToken == "" {
		return "", nil
	}

	k8s, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("creating kubernetes client: %w", err)
	}

	tr := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{Token: cfg.BearerToken},
	}
	result, err := k8s.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		// Non-fatal: the cluster might not support TokenReview (e.g. no kcp).
		return "", nil //nolint:nilerr
	}
	if !result.Status.Authenticated {
		return "", nil
	}
	return result.Status.User.Username, nil
}

// --- Context helpers ---------------------------------------------------------

type sshPrivateKeyPEMKey struct{}
type callerIdentityKey struct{}

// WithSSHPrivateKeyPEM stores a PEM-encoded SSH private key in the context.
func WithSSHPrivateKeyPEM(ctx context.Context, pem []byte) context.Context {
	return context.WithValue(ctx, sshPrivateKeyPEMKey{}, pem)
}

// SSHPrivateKeyPEMFromContext retrieves a PEM-encoded SSH private key from the context.
func SSHPrivateKeyPEMFromContext(ctx context.Context) []byte {
	v, _ := ctx.Value(sshPrivateKeyPEMKey{}).([]byte)
	return v
}

// WithCallerIdentity stores the expected caller identity in the context.
func WithCallerIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, callerIdentityKey{}, identity)
}

// CallerIdentityFromContext retrieves the expected caller identity from the context.
func CallerIdentityFromContext(ctx context.Context) string {
	v, _ := ctx.Value(callerIdentityKey{}).(string)
	return v
}
