# Kedge Security Review Findings

**Review Date:** 2026-05-05  
**Reviewer:** Security Analysis  
**Files Reviewed:** 7 security-critical files

---

## Executive Summary

The kedge codebase demonstrates good security practices overall with proper OIDC/PKCE flow implementation, constant-time token comparison, and delegated authorization via kcp. However, several security concerns were identified that should be addressed.

---

## Detailed Findings

### 1. JWT Parsing Without Signature Verification

**File:** `pkg/server/proxy/proxy.go` (lines 457-476), `pkg/virtual/builder/auth.go` (lines 24-47), `pkg/agent/tunnel/tunneler.go` (lines 211-231)

**Severity:** High

**Description:**  
The `parseServiceAccountToken` function parses JWT tokens without verifying the cryptographic signature:

```go
// pkg/virtual/builder/auth.go:24-47
func parseServiceAccountToken(token string) (saTokenClaims, bool) {
    // ...
    payload, err := base64.RawURLEncoding.DecodeString(parts[1])
    // ...
    if claims.Issuer != "kubernetes/serviceaccount" || claims.ClusterName == "" {
        return saTokenClaims{}, false
    }
    return claims, true
}
```

The code relies on kcp to verify the token signature when forwarding requests. However, if kcp's verification fails silently or if there are intermediate components that act on the unverified claims before reaching kcp, this could allow attackers to forge cluster identity.

**Recommendation:**  
- Document this trust assumption clearly in security architecture docs
- Consider adding an optional local signature verification as defense-in-depth
- Ensure all code paths that use `clusterName` from JWT are protected by kcp TokenReview

---

### 2. HTTP Server Missing Timeouts

**File:** `pkg/cli/auth/authenticator.go` (lines 58-73)

**Severity:** Medium

**Description:**  
The localhost callback HTTP server is created without configured timeouts:

```go
// pkg/cli/auth/authenticator.go:58-73
func (a *LocalhostCallbackAuthenticator) Start() error {
    // ...
    a.server = &http.Server{Handler: mux}  // No timeouts configured
    go func() {
        if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
```

**Vulnerabilities:**
- **Slow Client Attack (Slowloris):** An attacker could open many connections and send partial requests, exhausting server resources
- **Resource Exhaustion:** No read/write timeouts could allow long-lived connections
- **Connection Timeout:** No idle connection timeout

**Recommendation:**  
Add appropriate timeouts:
```go
a.server = &http.Server{
    Handler: mux,
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 10 * time.Second,
    IdleTimeout:  120 * time.Second,
}
```

---

### 4. Dev Mode TLS InsecureSkipVerify

**File:** Multiple files

**Severity:** Low (Informational - Dev Mode Only)

**Description:**  
Multiple locations use `InsecureSkipVerify: true` in dev mode:

- `pkg/server/auth/handler.go:69` - OIDC provider discovery
- `pkg/server/auth/handler.go:227` - Token exchange
- `pkg/server/proxy/proxy.go:105` - OIDC verification
- `pkg/cli/cmd/login.go:90,115` - CLI login flow
- `pkg/cli/cmd/ssh.go:125,261` - SSH connections
- `pkg/cli/cmd/get_token.go:107` - Token retrieval

All instances have `//nolint:gosec // dev mode only` comments.

**Assessment:**  
This is acceptable as long as:
1. Dev mode is strictly controlled and not accidentally enabled in production
2. Environment variables/flags clearly indicate dev mode
3. Production deployments use proper TLS certificates

**Recommendation:**  
- Add runtime checks that verify dev mode is not enabled in production
- Consider using a mock OIDC provider in tests instead of skipping TLS verification

---

### 5. Static Token Authentication Bypass

**File:** `pkg/virtual/builder/edges_proxy_builder.go` (lines 72-75)

**Severity:** Medium

**Description:**  
Static tokens bypass the `authorizeFn` (SubjectAccessReview) entirely:

```go
// pkg/virtual/builder/edges_proxy_builder.go:72-75
_, isStaticToken := p.staticTokens[token]
if !isStaticToken && p.kcpConfig != nil {
    // Only run TokenReview + SubjectAccessReview for non-static tokens
```

This means static tokens only go through basic token matching and do not have workspace-level authorization checks performed by kcp.

**Recommendation:**  
- Evaluate if SubjectAccessReview should also be performed for static tokens
- If static tokens represent long-lived credentials, consider requiring more stringent authorization
- Document the security model clearly for static token users

---

### 6. No Rate Limiting on Auth Endpoints

**File:** Multiple auth endpoints

**Severity:** Medium

**Description:**  
No rate limiting is implemented on authentication endpoints:
- `/auth/authorize` - Could be used for OAuth state enumeration
- `/auth/callback` - Could be used for token exchange attempts
- `/auth/token-login` - Could be used for static token brute-forcing

**Recommendation:**  
Implement rate limiting using a middleware library like `golang.org/x/time/rate` or use a Redis-backed rate limiter for distributed deployments.

---

### 7. Token Prefix in Logs

**File:** `pkg/server/proxy/proxy.go` (line 180)

**Severity:** Low

**Description:**  
The proxy logs partial token information:

```go
// pkg/server/proxy/proxy.go:178-180
p.logger.Info("proxy auth: no match — returning 401", 
    "path", r.URL.Path, 
    "tokenPrefix", firstN(token, 12))
```

While only 12 characters are logged (and only on auth failure), this could potentially leak information about valid tokens over time.

**Recommendation:**  
Consider logging only hash of token prefix rather than actual characters, or reduce logging verbosity for auth failures.

---

### 8. Missing Request Size Limits

**File:** `pkg/cli/auth/authenticator.go`, `pkg/server/auth/handler.go`

**Severity:** Low

**Description:**  
HTTP request handlers do not explicitly limit request body sizes. While Go's `http.Request` has a default limit of 10MB, explicit limits provide defense-in-depth.

**Recommendation:**  
Add explicit body size limits:
```go
maxBodySize := 32 * 1024 // 32KB
r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
```

---

### 9. SSH Command Injection Prevention (Positive Finding)

**File:** `pkg/util/ssh/ssh.go`

**Severity:** N/A (Positive)

**Description:**  
The SSH implementation properly handles command injection prevention:
- Commands are base64-encoded in WebSocket messages (line 132-135)
- Input is properly sanitized before writing to the SSH pipe (line 150-155)

**Positive Finding:** Command injection is mitigated through encoding.

---

### 10. Crypto Implementation Quality (Positive Finding)

**File:** Multiple files

**Severity:** N/A (Positive)

**Description:**  
Good cryptographic practices observed:
- **PKCE Implementation:** `pkg/server/auth/handler.go:161` uses `oauth2.S256ChallengeOption` for PKCE
- **Constant-Time Comparison:** `pkg/server/proxy/proxy.go:120` uses `subtle.ConstantTimeCompare`
- **SHA256 for Hashing:** Used appropriately for non-reversible hashing of user identifiers
- **Secure Random:** `crypto/rand` used in `pkg/cli/cmd/login.go:163`

---

### 11. Authorization Model (Positive Finding)

**File:** `pkg/virtual/builder/auth.go`, `pkg/server/proxy/proxy.go`

**Severity:** N/A (Positive)

**Description:**  
The authorization flow follows Kubernetes best practices:
1. **TokenReview** for authentication (verifies token with kcp)
2. **SubjectAccessReview** for authorization (checks RBAC permissions)

Test coverage exists for cluster mismatch scenarios (issue #68 fix) as shown in `pkg/virtual/builder/auth_cluster_test.go`.

---

## Summary Table

| Finding | Severity | Status |
|---------|----------|--------|
| JWT Parsing Without Signature Verification | High | Requires Review |
| HTTP Server Missing Timeouts | Medium | Requires Fix |
| Dev Mode TLS Skip | Low | Informational |
| Static Token Auth Bypass | Medium | Requires Review |
| No Rate Limiting | Medium | Requires Fix |
| Token Prefix in Logs | Low | Low Priority |
| Missing Request Size Limits | Low | Low Priority |

---

## Recommendations Priority

### Critical (Fix Before Production)
1. Add HTTP server timeouts in `pkg/cli/auth/authenticator.go`
2. Review and document the JWT trust model with kcp

### High (Fix Within Sprint)
3. Implement rate limiting on auth endpoints

### Medium (Address in Next Release)
4. Add request size limits
5. Review static token authorization model
6. Add production mode detection to warn about dev-only settings

### Low (Good to Have)
7. Reduce token information in logs
8. Add local signature verification option for defense-in-depth