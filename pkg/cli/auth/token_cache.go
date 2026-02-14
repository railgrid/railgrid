package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenCache stores OIDC tokens for the exec credential plugin.
type TokenCache struct {
	IDToken      string `json:"idToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"` // Unix timestamp
	IssuerURL    string `json:"issuerUrl"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// IsExpired returns true if the cached token has expired (with 30s buffer).
func (t *TokenCache) IsExpired() bool {
	return time.Now().Unix() > t.ExpiresAt-30
}

// cacheDir returns the token cache directory.
func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "kedge", "tokens")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("creating cache directory: %w", err)
	}
	return dir, nil
}

// cacheKey generates a filename-safe key from issuer URL and client ID.
func cacheKey(issuerURL, clientID string) string {
	h := sha256.Sum256([]byte(issuerURL + "\n" + clientID))
	return hex.EncodeToString(h[:])[:32]
}

// LoadTokenCache reads the cached token for the given OIDC config.
func LoadTokenCache(issuerURL, clientID string) (*TokenCache, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, cacheKey(issuerURL, clientID)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading token cache: %w", err)
	}

	var cache TokenCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parsing token cache: %w", err)
	}

	return &cache, nil
}

// SaveTokenCache writes the token cache to disk.
func SaveTokenCache(cache *TokenCache) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling token cache: %w", err)
	}

	path := filepath.Join(dir, cacheKey(cache.IssuerURL, cache.ClientID)+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing token cache: %w", err)
	}

	return nil
}
