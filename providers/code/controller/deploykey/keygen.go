/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package deploykey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// generateED25519 mints a fresh ed25519 keypair and returns the private key in
// OpenSSH PEM form and the public key in authorized_keys form (the format the
// git host expects). ed25519 is the modern default: small, fast, widely
// supported by GitHub/GitLab.
func generateED25519(comment string) (privPEM []byte, pubAuthorized []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, fmt.Errorf("wrap public key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(sshPub), nil
}
