package site

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	rbacControllerName = "site-rbac"
	// siteNamespace is the namespace where site credentials are stored.
	siteNamespace = "kedge-system"
)

func generateRandomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
