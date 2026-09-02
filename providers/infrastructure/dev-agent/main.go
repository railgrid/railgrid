/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command faros-dev-agent provides four capability-separated modes for
// development components. Default mode is the trusted coordinator: it serves
// authenticated public control on :7070 and execution sessions on :7071,
// owns workspace sync serialization, and stores durable records only beneath
// FAROS_DEV_STATE_DIR. --runtime-supervisor is the unprivileged app-container
// process supervisor. --executor is an unprivileged stateless direct-argv
// executor. The two internal modes bind loopback-only narrow APIs and receive
// neither the public control token nor coordinator state.
//
// The public control contract remains:
//
//	GET  /healthz  liveness; no auth.
//	GET  /readyz   coordinator readiness; no auth.
//	POST /sync     write/delete workspace files; restart: ""|"auto"|"always".
//	POST /restart  stop + start the dev process.
//	POST /env      set non-secret env for the dev process; optional restart.
//	GET  /logs     current dev-process attempt output (text/plain).
//	GET  /status   current child-process and declared-port readiness (JSON).
//
// Every endpoint except /healthz and /readyz requires X-Sandbox-Control-Token (constant-
// time compared against FAROS_DEV_CONTROL_TOKEN, read once then cleared).
// File writes are confined to the workdir via os.Root.
//
// Invoked as `faros-dev-agent --install <dir>` it copies its own executable
// into <dir> and exits — the init-container injection mode, which is what
// lets the dev image stay a plain toolchain image with nothing faros-specific
// baked in.
// Invoked as `faros-dev-agent --healthcheck <address>` it performs a
// container-local TCP health check and exits. This mode is used by the
// runtime-supervisor and executor Kubernetes exec probes; it intentionally
// does not load the coordinator configuration or expose a shell.
// Invoked as `faros-dev-agent --bootstrap-control-token <secret-name>` it
// performs the one-shot control-Secret GET/merge-patch used by the platform's
// development token bootstrap Job. It uses only the projected ServiceAccount
// token and Kubernetes CA, and never starts a listener.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	defaultControlAddr      = ":7070"
	defaultExecAddr         = ":7071"
	defaultRuntimeAddr      = "127.0.0.1:7072"
	defaultExecutorAddr     = "127.0.0.1:7073"
	runtimeOperationTimeout = 5 * time.Minute
	controlTokenHeader      = "X-Sandbox-Control-Token"
	agentBinaryName         = "faros-dev-agent"

	previewBridgePluginName     = "preview-bridge-plugin.mjs"
	previewBridgeJWKSName       = "preview-bridge-jwks.json"
	previewBridgeJWKSEnv        = "FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS"
	workspaceManifestName       = ".faros-workspace-manifest.json"
	serviceAccountTokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	serviceAccountCAPath        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	serviceAccountNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	kubernetesServiceHostEnv    = "KUBERNETES_SERVICE_HOST"
	kubernetesServicePortEnv    = "KUBERNETES_SERVICE_PORT_HTTPS"
)

//go:embed preview-bridge-plugin.mjs
var previewBridgePlugin []byte

// reloadRule mirrors TemplateDevelopmentReloadRule: changed-path globs that
// require a command before the process restarts.
type reloadRule struct {
	Paths   []string `json:"paths"`
	Command string   `json:"command"`
}

type agentConfig struct {
	WorkDir              string
	StartCommand         string
	Port                 string
	ControlToken         string
	ReloadStrategy       string // "process" (default) | "container"
	ReloadRules          []reloadRule
	AllowInsecureControl bool
	StateDir             string
	RuntimeURL           string
	ExecutorURL          string
	// Provider Actions identity exchange. The bootstrap token is read from a
	// projected file only in coordinator mode; the app receives only the
	// refreshed token file and non-secret context.
	ActionsBootstrapTokenFile string
	ActionsTokenFile          string
	ActionsExchangeURL        string
	ActionsBaseURL            string
	ActionsProject            string
	ActionsProjectUID         string
	ActionsEnvironment        string
	ActionsInstance           string
	ActionsTenantPath         string
	ActionsOrg                string
	ActionsWorkspace          string
	ActionsCAFile             string
	actionsTokenState         *actionsTokenState
	actionsHTTPClient         *http.Client
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "--install" {
		if err := installSelf(os.Args[2]); err != nil {
			log.Fatalf("install: %v", err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "--healthcheck" {
		if len(os.Args) != 3 {
			log.Printf("healthcheck: one address is required")
			os.Exit(1)
		}
		if err := runHealthcheck(os.Args[2]); err != nil {
			log.Printf("healthcheck %s: %v", os.Args[2], err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "--bootstrap-control-token" {
		if err := runControlTokenBootstrap(os.Args[2]); err != nil {
			log.Fatalf("bootstrap control token: %v", err)
		}
		return
	}
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) >= 2 && os.Args[1] == "--runtime-supervisor" {
		cfg.ControlToken = ""
		cfg.StateDir = ""
		if err := runRuntimeSupervisor(ctx, cfg); err != nil {
			log.Fatalf("runtime supervisor: %v", err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "--executor" {
		cfg.ControlToken = ""
		cfg.StateDir = ""
		cfg.StartCommand = ""
		cfg.ReloadRules = nil
		if err := runStatelessExecutor(ctx, cfg); err != nil {
			log.Fatalf("stateless executor: %v", err)
		}
		return
	}
	if err := runCoordinator(ctx, cfg); err != nil {
		log.Fatalf("coordinator: %v", err)
	}
}

// runControlTokenBootstrap is a deliberately narrow init/Job mode. It uses
// only the projected ServiceAccount credential to GET and, when needed,
// merge-patch one named Secret in the current namespace. Keeping this in the
// scratch dev-agent image avoids shipping a shell or a second mutable utility
// image in the token bootstrap Job.
func runControlTokenBootstrap(secretName string) error {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" || strings.ContainsAny(secretName, "/\\") {
		return errors.New("secret name is required and must not contain a slash")
	}
	bearer, err := os.ReadFile(serviceAccountTokenPath)
	if err != nil {
		return fmt.Errorf("read ServiceAccount token: %w", err)
	}
	namespace, err := os.ReadFile(serviceAccountNamespacePath)
	if err != nil {
		return fmt.Errorf("read ServiceAccount namespace: %w", err)
	}
	caPEM, err := os.ReadFile(serviceAccountCAPath)
	if err != nil {
		return fmt.Errorf("read ServiceAccount CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("ServiceAccount CA is not a valid certificate")
	}
	apiServer, err := kubernetesAPIServerURL()
	if err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return bootstrapControlToken(ctx, client, apiServer, strings.TrimSpace(string(namespace)), secretName, strings.TrimSpace(string(bearer)))
}

func kubernetesAPIServerURL() (string, error) {
	host := strings.TrimSpace(os.Getenv(kubernetesServiceHostEnv))
	port := strings.TrimSpace(os.Getenv(kubernetesServicePortEnv))
	if port == "" {
		port = strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	}
	if host == "" || port == "" {
		return "", fmt.Errorf("%s and Kubernetes service port are required", kubernetesServiceHostEnv)
	}
	return "https://" + net.JoinHostPort(host, port), nil
}

// bootstrapControlToken is kept independent from the projected-file wiring
// so its request/patch behavior can be tested without a Kubernetes cluster.
func bootstrapControlToken(ctx context.Context, client *http.Client, apiServer, namespace, secretName, bearer string) error {
	if client == nil {
		return errors.New("HTTP client is required")
	}
	if namespace == "" || secretName == "" || bearer == "" {
		return errors.New("namespace, Secret name, and ServiceAccount token are required")
	}
	secretURL := strings.TrimRight(apiServer, "/") + "/api/v1/namespaces/" + url.PathEscape(namespace) + "/secrets/" + url.PathEscape(secretName)
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, secretURL, nil)
	if err != nil {
		return fmt.Errorf("build Secret GET: %w", err)
	}
	get.Header.Set("Authorization", "Bearer "+bearer)
	get.Header.Set("Accept", "application/json")
	response, err := client.Do(get)
	if err != nil {
		return fmt.Errorf("GET control Secret: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read control Secret response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return kubernetesResponseError("GET control Secret", response, body)
	}
	var secret struct {
		Data      map[string]string `json:"data"`
		Immutable *bool             `json:"immutable"`
		Metadata  struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &secret); err != nil {
		return fmt.Errorf("decode control Secret: %w", err)
	}
	if strings.TrimSpace(secret.Data["token"]) != "" {
		if secret.Immutable != nil && *secret.Immutable {
			return nil
		}
		return patchControlSecret(ctx, client, secretURL, bearer, secret.Metadata.ResourceVersion, map[string]any{
			"immutable": true,
		})
	}
	if secret.Immutable != nil && *secret.Immutable {
		return errors.New("control Secret is immutable but has no token")
	}
	if strings.TrimSpace(secret.Metadata.ResourceVersion) == "" {
		return errors.New("control Secret response did not include resourceVersion")
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return fmt.Errorf("generate control token: %w", err)
	}
	token := hex.EncodeToString(rawToken)
	return patchControlSecret(ctx, client, secretURL, bearer, secret.Metadata.ResourceVersion, map[string]any{
		"immutable":  true,
		"stringData": map[string]string{"token": token},
	})
}

func patchControlSecret(ctx context.Context, client *http.Client, secretURL, bearer, resourceVersion string, fields map[string]any) error {
	resourceVersion = strings.TrimSpace(resourceVersion)
	if resourceVersion == "" {
		return errors.New("control Secret response did not include resourceVersion")
	}
	patchDocument := map[string]any{
		"metadata": map[string]string{"resourceVersion": resourceVersion},
	}
	for key, value := range fields {
		patchDocument[key] = value
	}
	patch, err := json.Marshal(patchDocument)
	if err != nil {
		return fmt.Errorf("encode control Secret patch: %w", err)
	}
	patchRequest, err := http.NewRequestWithContext(ctx, http.MethodPatch, secretURL, bytes.NewReader(patch))
	if err != nil {
		return fmt.Errorf("build control Secret patch: %w", err)
	}
	patchRequest.Header.Set("Authorization", "Bearer "+bearer)
	patchRequest.Header.Set("Accept", "application/json")
	patchRequest.Header.Set("Content-Type", "application/merge-patch+json")
	response, err := client.Do(patchRequest)
	if err != nil {
		return fmt.Errorf("patch control Secret: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read control Secret patch response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return kubernetesResponseError("patch control Secret", response, body)
	}
	return nil
}

// kubernetesResponseError intentionally extracts only the bounded Status
// reason. Kubernetes Status.message and arbitrary response bodies can contain
// echoed Secret data, so neither is ever included in a bootstrap error.
func kubernetesResponseError(operation string, response *http.Response, body []byte) error {
	status := fmt.Sprintf("HTTP %d", response.StatusCode)
	if statusText := http.StatusText(response.StatusCode); statusText != "" {
		status += " " + statusText
	}
	var statusObject struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &statusObject); err == nil {
		reason := strings.TrimSpace(statusObject.Reason)
		if kubernetesReasonSafe(reason) {
			return fmt.Errorf("%s: Kubernetes API returned %s (reason %s)", operation, status, reason)
		}
	}
	return fmt.Errorf("%s: Kubernetes API returned %s", operation, status)
}

func kubernetesReasonSafe(reason string) bool {
	if reason == "" || len(reason) > 128 {
		return false
	}
	for _, r := range reason {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

const healthcheckTimeout = time.Second

// runHealthcheck verifies that an internal control listener is accepting TCP
// connections from the current container. Callers provide a fixed loopback
// address from the generated development deployment; no HTTP client, shell,
// or pod-network address is involved.
func runHealthcheck(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return errors.New("address is required")
	}
	conn, err := net.DialTimeout("tcp", address, healthcheckTimeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

func runRuntimeSupervisor(ctx context.Context, cfg *agentConfig) error {
	logs := newRingLog(500)
	supervisor := newSupervisor(ctx, cfg, logs)
	if cfg.StartCommand != "" {
		if err := supervisor.start(ctx); err != nil {
			log.Printf("initial process start failed: %v", err)
		}
	}
	srv := &http.Server{Addr: defaultRuntimeAddr, Handler: newRuntimeSupervisorServer(supervisor, logs, nil), ReadHeaderTimeout: 10 * time.Second}
	return serveUntilDone(ctx, srv, func() { _ = supervisor.stop() })
}

func runStatelessExecutor(ctx context.Context, cfg *agentConfig) error {
	srv := &http.Server{Addr: defaultExecutorAddr, Handler: &statelessExecutor{workspace: cfg.WorkDir, exit: os.Exit}, ReadHeaderTimeout: 10 * time.Second}
	return serveUntilDone(ctx, srv, nil)
}

func runCoordinator(ctx context.Context, cfg *agentConfig) error {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return errors.New("FAROS_DEV_STATE_DIR is required in coordinator mode")
	}
	if cfg.actionsTokenState == nil {
		cfg.actionsTokenState = newActionsTokenState(strings.TrimSpace(cfg.ActionsExchangeURL) != "")
	}
	mutationMu := &sync.Mutex{}
	runtime := &httpRuntimeClient{baseURL: cfg.RuntimeURL, client: &http.Client{Timeout: runtimeOperationTimeout}}
	control := newCoordinatorServer(cfg, runtime, mutationMu)
	execCoordinator, err := newExecCoordinator(cfg.WorkDir, cfg.StateDir, cfg.ControlToken,
		&httpExecDispatcher{url: strings.TrimRight(cfg.ExecutorURL, "/") + "/internal/exec", client: &http.Client{}}, mutationMu)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ActionsExchangeURL) != "" {
		go runActionsTokenRefreshLoop(ctx, cfg)
	}
	controlSrv := &http.Server{Addr: defaultControlAddr, Handler: control, ReadHeaderTimeout: 10 * time.Second}
	execSrv := &http.Server{Addr: defaultExecAddr, Handler: execCoordinator, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 2)
	for _, srv := range []*http.Server{controlSrv, execSrv} {
		go func(server *http.Server) {
			err := server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errCh <- err
		}(srv)
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		serveErr = err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = controlSrv.Shutdown(shutdown)
	_ = execSrv.Shutdown(shutdown)
	return serveErr
}

func serveUntilDone(ctx context.Context, srv *http.Server, cleanup func()) error {
	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return err
		}
	}
	if cleanup != nil {
		cleanup()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// installSelf atomically installs the agent executable, the platform-owned
// preview-bridge Vite plugin, and its optional trusted public JWKS into dir,
// the shared emptyDir the dev container mounts at /faros/bin. Plain copies are
// used because the injector image may be scratch. Application dependencies are
// deliberately not projected here: generated applications install their
// declared package aliases through the component toolchain.
//
// Missing or invalid verification configuration disables the optional browser
// bridge without blocking the application. Any stale JWKS is removed so an old
// platform key set cannot be trusted accidentally after a bad rollout.
func installSelf(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomicInstall(dir, agentBinaryName, 0o755, src); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	if err := atomicInstall(dir, previewBridgePluginName, 0o644, bytes.NewReader(previewBridgePlugin)); err != nil {
		return fmt.Errorf("install preview bridge plugin: %w", err)
	}

	jwksPath := filepath.Join(dir, previewBridgeJWKSName)
	rawJWKS := strings.TrimSpace(os.Getenv(previewBridgeJWKSEnv))
	if rawJWKS == "" {
		if err := os.Remove(jwksPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale preview bridge JWKS: %w", err)
		}
		log.Printf("preview bridge disabled: %s is unset", previewBridgeJWKSEnv)
	} else if jwks, err := normalizePreviewBridgeJWKS([]byte(rawJWKS)); err != nil {
		if removeErr := os.Remove(jwksPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove stale preview bridge JWKS after invalid configuration: %w", removeErr)
		}
		log.Printf("preview bridge disabled: invalid %s: %v", previewBridgeJWKSEnv, err)
	} else if err := atomicInstall(dir, previewBridgeJWKSName, 0o644, bytes.NewReader(jwks)); err != nil {
		return fmt.Errorf("install preview bridge JWKS: %w", err)
	}

	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	log.Printf("installed %s and %s", filepath.Join(dir, agentBinaryName), filepath.Join(dir, previewBridgePluginName))
	return nil
}

func atomicInstall(dir, name string, mode os.FileMode, src io.Reader) (retErr error) {
	target := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

type publicVerificationJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

func normalizePreviewBridgeJWKS(raw []byte) ([]byte, error) {
	var document struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}
	if len(document.Keys) < 1 || len(document.Keys) > 2 {
		return nil, fmt.Errorf("want current key and optional previous key, got %d", len(document.Keys))
	}

	seen := map[string]bool{}
	keys := make([]publicVerificationJWK, 0, len(document.Keys))
	for i, fields := range document.Keys {
		if _, private := fields["d"]; private {
			return nil, fmt.Errorf("keys[%d] contains private key material", i)
		}
		var key publicVerificationJWK
		for name, dst := range map[string]*string{
			"kty": &key.Kty,
			"crv": &key.Crv,
			"kid": &key.Kid,
			"x":   &key.X,
			"y":   &key.Y,
			"alg": &key.Alg,
			"use": &key.Use,
		} {
			value, ok := fields[name]
			if !ok {
				continue
			}
			if err := json.Unmarshal(value, dst); err != nil {
				return nil, fmt.Errorf("keys[%d].%s must be a string", i, name)
			}
		}
		if key.Kty != "EC" || key.Crv != "P-256" || strings.TrimSpace(key.Kid) == "" {
			return nil, fmt.Errorf("keys[%d] must be a named EC P-256 key", i)
		}
		if key.Alg != "" && key.Alg != "ES256" {
			return nil, fmt.Errorf("keys[%d].alg must be ES256", i)
		}
		if key.Use != "" && key.Use != "sig" {
			return nil, fmt.Errorf("keys[%d].use must be sig", i)
		}
		if seen[key.Kid] {
			return nil, fmt.Errorf("duplicate key id %q", key.Kid)
		}
		seen[key.Kid] = true
		for name, coordinate := range map[string]string{"x": key.X, "y": key.Y} {
			decoded, err := base64.RawURLEncoding.DecodeString(coordinate)
			if err != nil || len(decoded) != 32 {
				return nil, fmt.Errorf("keys[%d].%s must be a 32-byte base64url coordinate", i, name)
			}
		}
		key.Alg = "ES256"
		key.Use = "sig"
		keys = append(keys, key)
	}
	return json.Marshal(struct {
		Keys []publicVerificationJWK `json:"keys"`
	}{Keys: keys})
}

func configFromEnv() (*agentConfig, error) {
	workdir := strings.TrimSpace(os.Getenv("FAROS_DEV_WORKDIR"))
	if workdir == "" {
		workdir = "/workspace"
	}
	token := strings.TrimSpace(os.Getenv("FAROS_DEV_CONTROL_TOKEN"))
	_ = os.Unsetenv("FAROS_DEV_CONTROL_TOKEN")

	strategy := strings.ToLower(strings.TrimSpace(os.Getenv("FAROS_DEV_RELOAD_STRATEGY")))
	switch strategy {
	case "", "process":
		strategy = "process"
	case "container":
	default:
		return nil, fmt.Errorf("unknown FAROS_DEV_RELOAD_STRATEGY %q", strategy)
	}

	rules, err := reloadRulesFromEnv(os.Getenv("FAROS_DEV_RELOAD_RULES"))
	if err != nil {
		return nil, err
	}

	insecure := strings.TrimSpace(os.Getenv("FAROS_DEV_ALLOW_INSECURE_CONTROL"))
	cfg := &agentConfig{
		WorkDir:                   workdir,
		StartCommand:              strings.TrimSpace(os.Getenv("FAROS_DEV_START_COMMAND")),
		Port:                      strings.TrimSpace(os.Getenv("FAROS_DEV_PORT")),
		ControlToken:              token,
		ReloadStrategy:            strategy,
		ReloadRules:               rules,
		AllowInsecureControl:      strings.EqualFold(insecure, "true"),
		StateDir:                  strings.TrimSpace(os.Getenv("FAROS_DEV_STATE_DIR")),
		RuntimeURL:                strings.TrimSpace(os.Getenv("FAROS_DEV_RUNTIME_URL")),
		ExecutorURL:               strings.TrimSpace(os.Getenv("FAROS_DEV_EXECUTOR_URL")),
		ActionsBootstrapTokenFile: envOrDefault("FAROS_ACTIONS_BOOTSTRAP_TOKEN_FILE", "/var/run/secrets/faros/actions-bootstrap/token"),
		ActionsTokenFile:          envOrDefault("FAROS_ACTIONS_TOKEN_FILE", "/var/run/secrets/faros/actions/token"),
		ActionsExchangeURL:        strings.TrimSpace(os.Getenv("FAROS_ACTIONS_EXCHANGE_URL")),
		ActionsBaseURL:            strings.TrimSpace(os.Getenv("FAROS_ACTIONS_BASE_URL")),
		ActionsProject:            strings.TrimSpace(os.Getenv("FAROS_PROJECT")),
		ActionsProjectUID:         strings.TrimSpace(os.Getenv("FAROS_PROJECT_UID")),
		ActionsEnvironment:        strings.TrimSpace(os.Getenv("FAROS_ACTIONS_ENVIRONMENT")),
		ActionsInstance:           strings.TrimSpace(os.Getenv("FAROS_ACTIONS_INSTANCE")),
		ActionsTenantPath:         strings.TrimSpace(os.Getenv("FAROS_ACTIONS_TENANT_PATH")),
		ActionsOrg:                strings.TrimSpace(os.Getenv("FAROS_ACTIONS_ORG")),
		ActionsWorkspace:          strings.TrimSpace(os.Getenv("FAROS_ACTIONS_WORKSPACE")),
		ActionsCAFile:             strings.TrimSpace(os.Getenv("FAROS_ACTIONS_CA_FILE")),
	}
	if cfg.RuntimeURL == "" {
		cfg.RuntimeURL = "http://" + defaultRuntimeAddr
	}
	if cfg.ExecutorURL == "" {
		cfg.ExecutorURL = "http://" + defaultExecutorAddr
	}
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func reloadRulesFromEnv(raw string) ([]reloadRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []reloadRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("FAROS_DEV_RELOAD_RULES is not a JSON rule list: %w", err)
	}
	for i, r := range rules {
		if len(r.Paths) == 0 || strings.TrimSpace(r.Command) == "" {
			return nil, fmt.Errorf("FAROS_DEV_RELOAD_RULES[%d] needs paths and a command", i)
		}
	}
	return rules, nil
}

// matchReloadRules returns the commands whose path globs match any of the
// changed workspace paths, in declaration order, deduplicated. Globs are
// path.Match patterns against the workdir-relative path; a pattern without a
// slash also matches by basename ("package.json" matches "web/package.json").
func matchReloadRules(rules []reloadRule, changed []string) []string {
	var commands []string
	seen := map[string]bool{}
	for _, rule := range rules {
		if seen[rule.Command] {
			continue
		}
		for _, pattern := range rule.Paths {
			if matchAny(pattern, changed) {
				commands = append(commands, rule.Command)
				seen[rule.Command] = true
				break
			}
		}
	}
	return commands
}

// mergeReloadCommands preserves declaration order while carrying unfinished
// commands across an authoritative retry. A failed dependency install must not
// become an idempotent no-op merely because the source bytes already landed.
func mergeReloadCommands(groups ...[]string) []string {
	var commands []string
	seen := map[string]bool{}
	for _, group := range groups {
		for _, raw := range group {
			command := strings.TrimSpace(raw)
			if command == "" || seen[command] {
				continue
			}
			seen[command] = true
			commands = append(commands, command)
		}
	}
	return commands
}

func matchAny(pattern string, changed []string) bool {
	for _, p := range changed {
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if ok, _ := path.Match(pattern, path.Base(p)); ok {
				return true
			}
		}
	}
	return false
}

type syncFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type syncRequest struct {
	Files          []syncFile `json:"files"`
	DeletePaths    []string   `json:"deletePaths"`
	Restart        string     `json:"restart"`
	SourceRevision uint64     `json:"sourceRevision,omitempty"`
	SourceDigest   string     `json:"sourceDigest,omitempty"`
}

type syncResponse struct {
	Phase          string   `json:"phase"`
	Changed        []string `json:"changed"`
	Deleted        []string `json:"deleted,omitempty"`
	ReloadRuns     []string `json:"reloadRuns,omitempty"`
	Restarted      bool     `json:"restarted"`
	ReloadError    string   `json:"reloadError,omitempty"`
	SourceRevision uint64   `json:"sourceRevision,omitempty"`
	SourceDigest   string   `json:"sourceDigest,omitempty"`
}

// workspaceManifest is synchronization metadata, not a security boundary.
// The agent rehashes every listed file before execution, so a stale or
// tampered manifest cannot make an old workspace look current. Runtime-created
// files (node_modules, build output, logs) are intentionally absent and are
// never deleted by authoritative sync.
type workspaceManifest struct {
	SourceRevision        uint64   `json:"sourceRevision"`
	SourceDigest          string   `json:"sourceDigest"`
	Files                 []string `json:"files"`
	PendingReloadCommands []string `json:"pendingReloadCommands,omitempty"`
}

type envRequest struct {
	Env     map[string]string `json:"env"`
	Restart bool              `json:"restart"`
}

type envResponse struct {
	Phase     string   `json:"phase"`
	Applied   []string `json:"applied"`
	Restarted bool     `json:"restarted"`
}

type agentServer struct {
	mux          *http.ServeMux
	config       *agentConfig
	actionsState *actionsTokenState
	supervisor   *supervisor
	logs         *ringLog
	runtime      runtimeOperations
	mutationMu   *sync.Mutex
	checkpoints  map[string]workspaceCheckpoint
}

func newAgentServer(ctx context.Context, cfg *agentConfig) *agentServer {
	logs := newRingLog(500)
	s := &agentServer{config: cfg, actionsState: ensureActionsTokenState(cfg), logs: logs, mutationMu: &sync.Mutex{}, checkpoints: map[string]workspaceCheckpoint{}}
	s.supervisor = newSupervisor(ctx, cfg, logs)
	s.runtime = &localRuntime{supervisor: s.supervisor, logs: logs}
	s.initMux()
	return s
}

func newCoordinatorServer(cfg *agentConfig, runtime runtimeOperations, mutationMu *sync.Mutex) *agentServer {
	s := &agentServer{config: cfg, actionsState: ensureActionsTokenState(cfg), runtime: runtime, mutationMu: mutationMu, checkpoints: map[string]workspaceCheckpoint{}}
	s.initMux()
	return s
}

func (s *agentServer) initMux() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/sync", s.handleSync)
	mux.HandleFunc("/restart", s.handleRestart)
	mux.HandleFunc("/env", s.handleEnv)
	mux.HandleFunc("/logs", s.handleLogs)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/workspace/", s.handleWorkspace)
	s.mux = mux
}

func (s *agentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *agentServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *agentServer) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	status := s.actionsState.snapshot(time.Now())
	response := map[string]any{
		"status":         "ok",
		"actionsEnabled": status.Enabled,
		"actionsReady":   status.Ready,
	}
	if !status.Ready {
		response["status"] = "not_ready"
	}
	if !status.ExpiresAt.IsZero() {
		response["actionsTokenExpiresAt"] = status.ExpiresAt.UTC()
	}
	if !status.Ready {
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *agentServer) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var req syncRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	authoritative := req.SourceRevision != 0 || strings.TrimSpace(req.SourceDigest) != ""
	if authoritative && (req.SourceRevision == 0 || strings.TrimSpace(req.SourceDigest) == "") {
		http.Error(w, "sourceRevision and sourceDigest must be supplied together", http.StatusBadRequest)
		return
	}
	previous, found, err := readWorkspaceManifest(root)
	if err != nil {
		if req.SourceRevision == 0 && strings.TrimSpace(req.SourceDigest) == "" {
			http.Error(w, "read workspace manifest: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// A protected manifest is synchronization metadata, not source of truth.
		// If it is corrupted, an authoritative full-file sync can safely rebuild
		// it, but must not delete paths that are no longer known to be managed.
		log.Printf("workspace manifest is invalid; rebuilding from authoritative sync: %v", err)
		previous, found = workspaceManifest{}, false
	}
	var incomingPaths map[string]struct{}
	cleanDeletePaths := make([]string, 0, len(req.DeletePaths))
	for _, raw := range req.DeletePaths {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if authoritative {
			if err := validateManagedWorkspacePath(clean); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		cleanDeletePaths = append(cleanDeletePaths, clean)
	}
	if authoritative {
		incomingPaths, err = validateSyncFiles(req.Files)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotDigest, err := digestSyncFiles(req.Files)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if normalizeSourceDigest(req.SourceDigest) != gotDigest {
			http.Error(w, "sourceDigest does not match the supplied workspace files", http.StatusConflict)
			return
		}
		if found {
			switch {
			case req.SourceRevision < previous.SourceRevision:
				http.Error(w, "workspace sync revision is older than the applied revision", http.StatusConflict)
				return
			case req.SourceRevision == previous.SourceRevision && normalizeSourceDigest(previous.SourceDigest) != gotDigest:
				http.Error(w, "workspace sync revision was already applied with a different digest", http.StatusConflict)
				return
			case req.SourceRevision == previous.SourceRevision && len(previous.PendingReloadCommands) == 0 && verifyWorkspaceManifest(root, previous) == nil:
				writeJSON(w, http.StatusOK, syncResponse{Phase: "Synced", SourceRevision: previous.SourceRevision, SourceDigest: previous.SourceDigest})
				return
			}
		}
	}

	changed := make([]string, 0, len(req.Files))
	for _, f := range req.Files {
		clean, err := cleanWorkspacePath(f.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if authoritative {
			if err := validateManagedWorkspacePath(clean); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := ensureExecPathNoSymlink(root, clean, false); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusConflict)
			return
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusConflict)
			return
		}
		content := []byte(f.Content)
		if !workspaceFileContentChanged(root, clean, content) {
			continue
		}
		if err := writeWorkspaceFile(root, clean, content); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusInternalServerError)
			return
		}
		changed = append(changed, clean)
	}
	deleted := make([]string, 0, len(req.DeletePaths))
	if authoritative {
		var err error
		deleted, err = deleteAuthoritativeWorkspaceCandidates(root, previous, found, cleanDeletePaths, incomingPaths)
		if err != nil {
			http.Error(w, "delete authoritative workspace files: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		for _, clean := range cleanDeletePaths {
			if err := root.RemoveAll(clean); err != nil {
				http.Error(w, fmt.Sprintf("delete %q: %v", clean, err), http.StatusInternalServerError)
				return
			}
			deleted = append(deleted, clean)
		}
	}

	resp := syncResponse{Phase: "Synced", Changed: changed, Deleted: deleted, SourceRevision: req.SourceRevision, SourceDigest: req.SourceDigest}
	var appliedManifest workspaceManifest
	if authoritative {
		appliedManifest = workspaceManifest{SourceRevision: req.SourceRevision, SourceDigest: req.SourceDigest, Files: make([]string, 0, len(incomingPaths))}
		for clean := range incomingPaths {
			appliedManifest.Files = append(appliedManifest.Files, clean)
		}
		slices.Sort(appliedManifest.Files)
	}

	// The Template-declared reload procedure: run matching rule commands
	// first (dependency installs), then restart per policy/strategy.
	touched := append(append([]string{}, changed...), deleted...)
	ruleCommands := matchReloadRules(s.config.ReloadRules, touched)
	if authoritative && found {
		ruleCommands = mergeReloadCommands(previous.PendingReloadCommands, ruleCommands)
	}
	if authoritative {
		appliedManifest.PendingReloadCommands = append([]string(nil), ruleCommands...)
		if err := writeWorkspaceManifest(root, appliedManifest); err != nil {
			http.Error(w, "write workspace manifest: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	restartNeeded, err := s.shouldRestartAfterSync(r.Context(), req.Restart, len(ruleCommands) > 0, len(s.config.ReloadRules) > 0, touched)
	if err != nil {
		http.Error(w, "runtime status: "+err.Error(), http.StatusBadGateway)
		return
	}
	if restartNeeded && len(ruleCommands) > 0 {
		resp.ReloadRuns = ruleCommands
		reloadErr := s.runtime.Reload(r.Context(), ruleCommands)
		if authoritative {
			// Reload hooks are allowed to install dependencies, but they must not
			// become a second source of truth for managed files. A hook can mutate
			// package-lock.json (or fail after doing so), so restore the exact
			// authoritative bundle and verify the original manifest before any
			// restart or response is accepted.
			if err := restoreAuthoritativeWorkspaceFiles(root, req.Files); err != nil {
				http.Error(w, "restore authoritative workspace after reload: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if err := writeWorkspaceManifest(root, appliedManifest); err != nil {
				http.Error(w, "restore workspace manifest after reload: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if _, err := deleteAuthoritativeWorkspaceCandidates(root, previous, found, cleanDeletePaths, incomingPaths); err != nil {
				http.Error(w, "re-delete authoritative workspace files after reload: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if err := verifyWorkspaceManifest(root, appliedManifest); err != nil {
				http.Error(w, "workspace manifest verification after reload: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if reloadErr != nil {
			// Keep the sync result; surface the reload failure for the caller
			// (the dev process keeps running against the old dependencies). The
			// authoritative source has already been restored above.
			resp.ReloadError = reloadErr.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if authoritative {
			appliedManifest.PendingReloadCommands = nil
			if err := writeWorkspaceManifest(root, appliedManifest); err != nil {
				http.Error(w, "clear completed workspace reload: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	if restartNeeded {
		if s.config.ReloadStrategy == "container" {
			// The runtime supervisor lives in the app container. Ask that narrow
			// internal authority to exit; the coordinator must remain available.
			if err := s.runtime.ExitContainer(r.Context()); err != nil {
				http.Error(w, "restart container: "+err.Error(), http.StatusBadGateway)
				return
			}
			resp.Restarted = true
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if err := s.runtime.Restart(r.Context()); err != nil {
			http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Restarted = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func validateSyncFiles(files []syncFile) (map[string]struct{}, error) {
	paths := make(map[string]struct{}, len(files))
	for _, file := range files {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return nil, err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return nil, err
		}
		if !utf8.ValidString(file.Content) || strings.ContainsRune(file.Content, '\x00') {
			return nil, fmt.Errorf("source file %q must be UTF-8 text without NUL bytes", clean)
		}
		if _, exists := paths[clean]; exists {
			return nil, fmt.Errorf("duplicate source path %q", clean)
		}
		paths[clean] = struct{}{}
	}
	return paths, nil
}

func digestSyncFiles(files []syncFile) (string, error) {
	type digestEntry struct {
		path    string
		content string
	}
	entries := make([]digestEntry, 0, len(files))
	for _, file := range files {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return "", err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return "", err
		}
		entries = append(entries, digestEntry{path: clean, content: file.Content})
	}
	slices.SortFunc(entries, func(a, b digestEntry) int { return strings.Compare(a.path, b.path) })
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(entry.content))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readWorkspaceManifest(root *os.Root) (workspaceManifest, bool, error) {
	raw, err := root.ReadFile(workspaceManifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return workspaceManifest{}, false, nil
	}
	if err != nil {
		return workspaceManifest{}, false, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return workspaceManifest{}, false, err
	}
	if manifest.SourceRevision == 0 || normalizeSourceDigest(manifest.SourceDigest) == "" {
		return workspaceManifest{}, false, errors.New("manifest has no source revision or digest")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for i, rawPath := range manifest.Files {
		clean, err := cleanWorkspacePath(rawPath)
		if err != nil {
			return workspaceManifest{}, false, fmt.Errorf("manifest files[%d]: %w", i, err)
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return workspaceManifest{}, false, fmt.Errorf("manifest files[%d]: %w", i, err)
		}
		if _, exists := seen[clean]; exists {
			return workspaceManifest{}, false, fmt.Errorf("manifest duplicates path %q", clean)
		}
		seen[clean] = struct{}{}
		manifest.Files[i] = clean
	}
	if len(manifest.PendingReloadCommands) > 32 {
		return workspaceManifest{}, false, errors.New("manifest has too many pending reload commands")
	}
	for i, rawCommand := range manifest.PendingReloadCommands {
		command := strings.TrimSpace(rawCommand)
		if command == "" || len(command) > 4096 {
			return workspaceManifest{}, false, fmt.Errorf("manifest pendingReloadCommands[%d] is empty or too large", i)
		}
		manifest.PendingReloadCommands[i] = command
	}
	slices.Sort(manifest.Files)
	return manifest, true, nil
}

func writeWorkspaceManifest(root *os.Root, manifest workspaceManifest) error {
	slices.Sort(manifest.Files)
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	tmp := workspaceManifestName + ".tmp"
	if err := root.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := root.Rename(tmp, workspaceManifestName); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

func removeManagedWorkspaceFile(root *os.Root, raw string) (bool, error) {
	clean, err := cleanWorkspacePath(raw)
	if err != nil {
		return false, err
	}
	if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	info, err := root.Lstat(clean)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("managed path is not a regular file")
	}
	if err := root.Remove(clean); err != nil {
		return false, err
	}
	return true, nil
}

func normalizeSourceDigest(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "sha256:")
}

func validateManagedWorkspacePath(clean string) error {
	for _, part := range strings.Split(clean, "/") {
		switch strings.ToLower(part) {
		case ".git", "node_modules", ".assistant-snapshots":
			return fmt.Errorf("workspace path contains reserved component %q", part)
		}
	}
	return nil
}

func verifyWorkspaceManifest(root *os.Root, manifest workspaceManifest) error {
	if manifest.SourceRevision == 0 || normalizeSourceDigest(manifest.SourceDigest) == "" {
		return errors.New("workspace manifest has no source revision or digest")
	}
	entries := make([]syncFile, 0, len(manifest.Files))
	for _, raw := range manifest.Files {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			return err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return err
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			return err
		}
		info, err := root.Lstat(clean)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed path %q is not a regular file", clean)
		}
		content, err := root.ReadFile(clean)
		if err != nil {
			return err
		}
		entries = append(entries, syncFile{Path: clean, Content: string(content)})
	}
	got, err := digestSyncFiles(entries)
	if err != nil {
		return err
	}
	if normalizeSourceDigest(manifest.SourceDigest) != got {
		return fmt.Errorf("workspace manifest digest does not match managed files")
	}
	return nil
}

// shouldRestartAfterSync decides the post-sync restart. "always" restarts
// unconditionally; "auto" restarts when a reload rule fired, when the process
// isn't running, or — for templates that declare no rules — when the legacy
// startup-affecting heuristic matches (the sandbox-runner behavior).
func (s *agentServer) shouldRestartAfterSync(ctx context.Context, policy string, ruleFired, rulesDeclared bool, touched []string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always":
		return true, nil
	case "auto":
		status, err := s.runtime.Status(ctx)
		if err != nil {
			return false, err
		}
		if !status.Configured {
			return false, nil
		}
		if !status.Running || ruleFired {
			return true, nil
		}
		if !rulesDeclared {
			for _, p := range touched {
				if isStartupAffectingPath(p) {
					return true, nil
				}
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

func openWorkspaceRoot(workdir string) (*os.Root, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenRoot(workdir)
}

func writeWorkspaceFile(root *os.Root, clean string, content []byte) error {
	parent := path.Dir(clean)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create parent: %w", err)
		}
	}
	return root.WriteFile(clean, content, 0o644)
}

func workspaceFileContentChanged(root *os.Root, clean string, next []byte) bool {
	current, err := root.ReadFile(clean)
	return err != nil || !bytes.Equal(current, next)
}

// restoreAuthoritativeWorkspaceFiles repairs managed source files after a
// reload hook. Hooks may legitimately create runtime output, but they must not
// mutate the submitted source bundle or make the manifest claim hook output as
// the current source digest.
func restoreAuthoritativeWorkspaceFiles(root *os.Root, files []syncFile) error {
	if _, err := validateSyncFiles(files); err != nil {
		return err
	}
	for _, file := range files {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return err
		}
		if err := ensureExecPathNoSymlink(root, clean, false); err != nil {
			return err
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			return err
		}
		content := []byte(file.Content)
		if workspaceFileContentChanged(root, clean, content) {
			if err := writeWorkspaceFile(root, clean, content); err != nil {
				return fmt.Errorf("restore %q: %w", clean, err)
			}
		}
	}
	return nil
}

// deleteAuthoritativeWorkspaceCandidates removes only files that the previous
// manifest managed or that the caller explicitly asked to delete. Runtime-only
// files are never swept. The same bounded set is replayed after reload hooks so
// a hook cannot recreate a source file that the authoritative sync removed.
func deleteAuthoritativeWorkspaceCandidates(root *os.Root, previous workspaceManifest, found bool, deletePaths []string, incoming map[string]struct{}) ([]string, error) {
	candidates := make(map[string]struct{}, len(previous.Files)+len(deletePaths))
	for _, raw := range previous.Files {
		candidates[raw] = struct{}{}
	}
	for _, raw := range deletePaths {
		candidates[raw] = struct{}{}
	}
	paths := make([]string, 0, len(candidates))
	for raw := range candidates {
		paths = append(paths, raw)
	}
	slices.Sort(paths)
	managed := make(map[string]struct{}, len(previous.Files))
	for _, raw := range previous.Files {
		managed[raw] = struct{}{}
	}
	deleted := make([]string, 0, len(paths))
	for _, raw := range paths {
		if _, keep := incoming[raw]; keep {
			continue
		}
		// When a valid prior manifest exists, explicit deletion hints are
		// advisory and may remove only paths that manifest managed. If the
		// manifest was missing/corrupt, explicit hints still allow safe
		// convergence without broad deletion of unknown runtime files.
		if found {
			if _, ok := managed[raw]; !ok {
				continue
			}
		}
		removed, err := removeManagedWorkspaceFile(root, raw)
		if err != nil {
			return nil, fmt.Errorf("delete %q: %w", raw, err)
		}
		if removed {
			deleted = append(deleted, raw)
		}
	}
	return deleted, nil
}

// isStartupAffectingPath is the legacy node-shaped heuristic, used only when
// the template declares no reload rules.
func isStartupAffectingPath(clean string) bool {
	switch path.Base(clean) {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "server.js":
		return true
	}
	return strings.HasPrefix(path.Base(clean), "vite.config.")
}

func (s *agentServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	if err := s.runtime.Restart(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarted": true})
}

func (s *agentServer) handleEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	var req envRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	applied, err := s.runtime.SetEnv(r.Context(), req.Env)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	restarted := false
	if req.Restart {
		if err := s.runtime.Restart(r.Context()); err != nil {
			http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
			return
		}
		restarted = true
	}
	writeJSON(w, http.StatusOK, envResponse{Phase: "EnvUpdated", Applied: applied, Restarted: restarted})
}

func (s *agentServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	logs, err := s.runtime.Logs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_, _ = io.WriteString(w, logs)
}

type processStatusResponse struct {
	AttemptID               uint64 `json:"attemptID"`
	AttemptStartedUnixMilli int64  `json:"attemptStartedUnixMilli,omitempty"`
	Configured              bool   `json:"configured"`
	Running                 bool   `json:"running"`
	Port                    string `json:"port,omitempty"`
	PortReachable           bool   `json:"portReachable,omitempty"`
	ActionsEnabled          bool   `json:"actionsEnabled"`
	ActionsReady            bool   `json:"actionsReady"`
	ActionsTokenExpiresAt   int64  `json:"actionsTokenExpiresAtUnixMilli,omitempty"`
	SourceRevision          uint64 `json:"sourceRevision,omitempty"`
	SourceDigest            string `json:"sourceDigest,omitempty"`
}

type runtimeOperations interface {
	Restart(context.Context) error
	ExitContainer(context.Context) error
	Reload(context.Context, []string) error
	SetEnv(context.Context, map[string]string) ([]string, error)
	Logs(context.Context) (string, error)
	Status(context.Context) (processStatusResponse, error)
}

type localRuntime struct {
	supervisor *supervisor
	logs       *ringLog
}

func (r *localRuntime) Restart(ctx context.Context) error { return r.supervisor.restart(ctx) }
func (r *localRuntime) ExitContainer(context.Context) error {
	return errors.New("container exit is unavailable in the in-process runtime adapter")
}
func (r *localRuntime) Reload(ctx context.Context, commands []string) error {
	return r.supervisor.runReloadCommands(ctx, commands)
}
func (r *localRuntime) SetEnv(_ context.Context, env map[string]string) ([]string, error) {
	return r.supervisor.setEnv(env)
}
func (r *localRuntime) Logs(context.Context) (string, error) {
	return strings.Join(r.logs.lines(), "\n"), nil
}
func (r *localRuntime) Status(context.Context) (processStatusResponse, error) {
	return r.supervisor.status(), nil
}

type httpRuntimeClient struct {
	baseURL string
	client  *http.Client
}

func (c *httpRuntimeClient) call(ctx context.Context, method, operation string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		raw, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+"/internal/"+operation, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("runtime supervisor %s: %w", operation, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("runtime supervisor %s rejected request: %s", operation, strings.TrimSpace(string(raw)))
	}
	if responseBody != nil && len(raw) != 0 {
		if err := json.Unmarshal(raw, responseBody); err != nil {
			return err
		}
	}
	return nil
}

func (c *httpRuntimeClient) Restart(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "restart", struct{}{}, nil)
}
func (c *httpRuntimeClient) ExitContainer(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "exit", struct{}{}, nil)
}
func (c *httpRuntimeClient) Reload(ctx context.Context, commands []string) error {
	return c.call(ctx, http.MethodPost, "reload", map[string]any{"commands": commands}, nil)
}
func (c *httpRuntimeClient) SetEnv(ctx context.Context, env map[string]string) ([]string, error) {
	var response struct {
		Applied []string `json:"applied"`
	}
	err := c.call(ctx, http.MethodPost, "env", map[string]any{"env": env}, &response)
	return response.Applied, err
}
func (c *httpRuntimeClient) Logs(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/internal/logs", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("runtime supervisor logs: %s", strings.TrimSpace(string(raw)))
	}
	return string(raw), nil
}
func (c *httpRuntimeClient) Status(ctx context.Context) (processStatusResponse, error) {
	var status processStatusResponse
	err := c.call(ctx, http.MethodGet, "status", nil, &status)
	return status, err
}

func newRuntimeSupervisorServer(supervisor *supervisor, logs *ringLog, exit func(int)) http.Handler {
	if exit == nil {
		exit = os.Exit
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := supervisor.restart(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"restarted": true})
	})
	mux.HandleFunc("/internal/exit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct{}
		if err := decodeBoundedJSON(w, r, 1024, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"exiting": true})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			log.Print("reload strategy container: exiting runtime supervisor")
			exit(0)
		}()
	})
	mux.HandleFunc("/internal/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Commands []string `json:"commands"`
		}
		if err := decodeBoundedJSON(w, r, 64<<10, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Commands) == 0 || len(req.Commands) > 32 {
			http.Error(w, "reload requires 1 to 32 commands", http.StatusBadRequest)
			return
		}
		for _, command := range req.Commands {
			if strings.TrimSpace(command) == "" || len(command) > 4096 {
				http.Error(w, "reload command is empty or too large", http.StatusBadRequest)
				return
			}
		}
		if err := supervisor.runReloadCommands(r.Context(), req.Commands); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"reloaded": true})
	})
	mux.HandleFunc("/internal/env", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Env map[string]string `json:"env"`
		}
		if err := decodeBoundedJSON(w, r, 64<<10, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		applied, err := supervisor.setEnv(req.Env)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"applied": applied})
	})
	mux.HandleFunc("/internal/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, strings.Join(logs.lines(), "\n"))
	})
	mux.HandleFunc("/internal/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, supervisor.status())
	})
	return mux
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (s *agentServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	status, err := s.runtime.Status(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	root, manifestErr := openWorkspaceRoot(s.config.WorkDir)
	if manifestErr == nil {
		manifest, found, readErr := readWorkspaceManifest(root)
		current := readErr == nil && found && len(manifest.PendingReloadCommands) == 0 && verifyWorkspaceManifest(root, manifest) == nil
		_ = root.Close()
		if current {
			status.SourceRevision = manifest.SourceRevision
			status.SourceDigest = manifest.SourceDigest
		}
	}
	actions := s.actionsState.snapshot(time.Now())
	status.ActionsEnabled = actions.Enabled
	status.ActionsReady = actions.Ready
	if !actions.ExpiresAt.IsZero() {
		status.ActionsTokenExpiresAt = actions.ExpiresAt.UnixMilli()
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *agentServer) authorizeControl(w http.ResponseWriter, r *http.Request) bool {
	token := strings.TrimSpace(s.config.ControlToken)
	if token == "" {
		if s.config.AllowInsecureControl {
			return true
		}
		http.Error(w, "dev agent control token is not configured", http.StatusUnauthorized)
		return false
	}
	if subtleConstantTimeCompare(r.Header.Get(controlTokenHeader), token) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func subtleConstantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func cleanWorkspacePath(raw string) (string, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if raw == "" {
		return "", errors.New("path is required")
	}
	if path.IsAbs(raw) {
		return "", fmt.Errorf("absolute path %q is not allowed", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes workspace", raw)
	}
	if clean == workspaceManifestName {
		return "", fmt.Errorf("path %q is reserved for the platform sync manifest", raw)
	}
	return clean, nil
}

type supervisor struct {
	config         *agentConfig
	logs           *ringLog
	ctx            context.Context
	mu             sync.Mutex
	cmd            *exec.Cmd
	done           chan struct{}
	attempt        uint64
	attemptStarted time.Time
	// customEnv holds non-secret environment variables set at runtime via
	// /env, merged over the process environment on the next (re)start.
	customEnv map[string]string
}

const maxRuntimeEnvKeys = 32

// reservedEnvPrefixes protect the agent's own control plane from being
// overridden through /env or child env merging.
var reservedEnvPrefixes = []string{"FAROS_DEV_"}

func hasReservedEnvPrefix(name string) bool {
	return slices.ContainsFunc(reservedEnvPrefixes, func(p string) bool {
		return strings.HasPrefix(name, p)
	})
}

func (s *supervisor) setEnv(env map[string]string) ([]string, error) {
	if len(env) == 0 {
		return nil, fmt.Errorf("at least one environment variable is required")
	}
	if len(env) > maxRuntimeEnvKeys {
		return nil, fmt.Errorf("at most %d environment variables may be set in one call", maxRuntimeEnvKeys)
	}
	for key := range env {
		name := strings.TrimSpace(key)
		if !isValidRuntimeEnvName(name) {
			return nil, fmt.Errorf("invalid environment variable name %q; use letters, digits, and underscores", key)
		}
		if hasReservedEnvPrefix(name) {
			return nil, fmt.Errorf("environment variable %q is reserved for the dev agent", name)
		}
		if isSecretLikeRuntimeEnvName(name) {
			return nil, fmt.Errorf("secret-looking environment variable %q cannot be set through /env", name)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.customEnv == nil {
		s.customEnv = map[string]string{}
	}
	applied := make([]string, 0, len(env))
	for key, value := range env {
		name := strings.TrimSpace(key)
		s.customEnv[name] = value
		applied = append(applied, name)
	}
	sort.Strings(applied)
	return applied, nil
}

func isValidRuntimeEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isSecretLikeRuntimeEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "ACCESS_KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return upper == "KEY" || strings.HasSuffix(upper, "_KEY")
}

func newSupervisor(ctx context.Context, cfg *agentConfig, logs *ringLog) *supervisor {
	if ctx == nil {
		ctx = context.Background()
	}
	return &supervisor{config: cfg, logs: logs, ctx: ctx}
}

func (s *supervisor) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.StartCommand == "" {
		return nil
	}
	return s.startLocked(ctx)
}

func (s *supervisor) restart(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stopLocked(); err != nil {
		return err
	}
	if s.config.StartCommand == "" {
		return nil
	}
	return s.startLocked(s.ctx)
}

// runReloadCommands executes rule commands sequentially in the workdir,
// logging their output into the same ring buffer as the dev process so the
// caller sees "npm install" progress in /logs. Fails on the first error.
func (s *supervisor) runReloadCommands(ctx context.Context, commands []string) error {
	// Serialize reload work with start/restart. A successful restart begins a
	// new log epoch; allowing an older reload command to keep writing after
	// that boundary would reintroduce stale failures into the current attempt.
	s.mu.Lock()
	defer s.mu.Unlock()
	childEnv := make(map[string]string, len(s.customEnv))
	maps.Copy(childEnv, s.customEnv)
	for _, command := range commands {
		s.logs.append("[faros reload] " + command)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", command)
		cmd.Dir = s.config.WorkDir
		cmd.Env = mergeChildEnv(os.Environ(), childEnv, s.config.Port)
		out, err := cmd.CombinedOutput()
		for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
			if line != "" {
				s.logs.append(line)
			}
		}
		if err != nil {
			s.logs.append("[faros reload] failed: " + err.Error())
			return fmt.Errorf("reload command %q: %w", command, err)
		}
	}
	return nil
}

func (s *supervisor) hasCommand() bool {
	return strings.TrimSpace(s.config.StartCommand) != ""
}

func (s *supervisor) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return processRunning(s.done, s.cmd)
}

func (s *supervisor) status() processStatusResponse {
	s.mu.Lock()
	status := processStatusResponse{
		AttemptID:  s.attempt,
		Configured: strings.TrimSpace(s.config.StartCommand) != "",
		Running:    processRunning(s.done, s.cmd),
		Port:       strings.TrimSpace(s.config.Port),
	}
	if !s.attemptStarted.IsZero() {
		status.AttemptStartedUnixMilli = s.attemptStarted.UnixMilli()
	}
	s.mu.Unlock()
	if status.Running && status.Port != "" {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", status.Port), 250*time.Millisecond)
		if err == nil {
			status.PortReachable = true
			_ = conn.Close()
		}
	}
	return status
}

func processRunning(done chan struct{}, cmd *exec.Cmd) bool {
	if cmd == nil || done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

func (s *supervisor) stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *supervisor) startLocked(ctx context.Context) error {
	if err := os.MkdirAll(s.config.WorkDir, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", s.config.StartCommand)
	cmd.Dir = s.config.WorkDir
	cmd.Env = mergeChildEnv(os.Environ(), s.customEnv, s.config.Port)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	attempt := s.logs.beginAttempt()
	s.cmd = cmd
	s.attempt = attempt
	s.attemptStarted = time.Now()
	done := make(chan struct{})
	s.done = done
	go s.scanOutput(attempt, stdout)
	go s.scanOutput(attempt, stderr)
	go func() {
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			s.logs.appendAttempt(attempt, "process exited: "+err.Error())
		}
		close(done)
	}()
	return nil
}

func sanitizedChildEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if hasReservedEnvPrefix(name) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// mergeChildEnv layers custom runtime env over the sanitized process
// environment. Reserved-prefix names are skipped so custom env cannot touch
// the control plane. When the component declares a dev port, PORT is exported
// for the child unless already set.
func mergeChildEnv(base []string, custom map[string]string, devPort string) []string {
	out := sanitizedChildEnv(base)
	index := make(map[string]int, len(out))
	for i, entry := range out {
		if name, _, ok := strings.Cut(entry, "="); ok {
			index[name] = i
		}
	}
	set := func(name, value string, override bool) {
		if i, ok := index[name]; ok {
			if override {
				out[i] = name + "=" + value
			}
			return
		}
		index[name] = len(out)
		out = append(out, name+"="+value)
	}
	if devPort = strings.TrimSpace(devPort); devPort != "" {
		set("PORT", devPort, false)
	}
	names := make([]string, 0, len(custom))
	for name := range custom {
		if name == "" || hasReservedEnvPrefix(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		set(name, custom[name], true)
	}
	return out
}

func (s *supervisor) stopLocked() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	pid := s.cmd.Process.Pid
	done := s.done
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	s.cmd = nil
	s.done = nil
	return nil
}

func (s *supervisor) scanOutput(attempt uint64, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		s.logs.appendAttempt(attempt, scanner.Text())
	}
}

type ringLog struct {
	mu       sync.Mutex
	limit    int
	attempt  uint64
	linesBuf []string
}

func newRingLog(limit int) *ringLog {
	return &ringLog{limit: limit}
}

func (r *ringLog) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLocked(line)
}

func (r *ringLog) beginAttempt() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt++
	r.linesBuf = nil
	return r.attempt
}

func (r *ringLog) appendAttempt(attempt uint64, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if attempt != r.attempt {
		return
	}
	r.appendLocked(line)
}

func (r *ringLog) appendLocked(line string) {
	r.linesBuf = append(r.linesBuf, line)
	if len(r.linesBuf) > r.limit {
		copy(r.linesBuf, r.linesBuf[len(r.linesBuf)-r.limit:])
		r.linesBuf = r.linesBuf[:r.limit]
	}
}

func (r *ringLog) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.linesBuf))
	copy(out, r.linesBuf)
	return out
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
