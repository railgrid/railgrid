/*
Copyright 2026 The Railgrid Authors.

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

package cmd

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	cliauth "github.com/railgrid/railgrid/pkg/cli/auth"
)

func TestDeviceLoginRetriesBeforeAcknowledging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "remote-kubeconfig")
	setLoginKubeconfig(t, configPath)
	const serverURL = "https://hub.example.com/clusters/home"
	result := tenancyv1alpha1.LoginResponse{
		Kubeconfig: loginKubeconfig(t, serverURL),
		UserID:     "device-user",
		Email:      "user@example.com", IDToken: "id-token", RefreshToken: "refresh-token",
		IssuerURL: "https://issuer.example.com", ClientID: "railgrid-cli",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	deviceCode := oauth2.GenerateVerifier()
	ackNonce := oauth2.GenerateVerifier()
	var polls atomic.Int32
	var acknowledgments atomic.Int32
	var ciphertext atomic.Pointer[[]byte]
	router := http.NewServeMux()
	router.HandleFunc(apiurl.PathHealthz, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]bool{"oidc": true})
	})
	router.HandleFunc("POST "+apiurl.PathAuthDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		sealed, err := sealTestDeviceResult(r.FormValue("recipient_key"), deviceLoginResult{LoginResponse: result, AckNonce: ackNonce})
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ciphertext.Store(&sealed)
		_ = json.NewEncoder(w).Encode(oauth2.DeviceAuthResponse{
			DeviceCode: deviceCode, UserCode: "ABCD-2345", VerificationURI: "https://hub.example.com/auth/device",
			Expiry: time.Now().Add(time.Minute), Interval: 1,
		})
	})
	router.HandleFunc("POST "+apiurl.PathAuthDevicePoll, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("device_code") != deviceCode || r.URL.RawQuery != "" {
			t.Error("expected device credential only in the POST body")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sealed := *ciphertext.Load()
		switch polls.Add(1) {
		case 1:
			writeOAuthError(w, "authorization_pending")
			return
		case 2:
			w.Header().Set("Content-Length", strconv.Itoa(len(sealed)))
			sealed = sealed[:len(sealed)/2]
		}
		_, _ = w.Write(sealed)
	})
	router.HandleFunc("POST "+apiurl.PathAuthDeviceAck, func(w http.ResponseWriter, r *http.Request) {
		cache, err := cliauth.LoadTokenCache(result.IssuerURL, result.ClientID, result.UserID)
		_, configErr := os.Stat(configPath)
		if err != nil || cache.IDToken != result.IDToken || cache.RefreshToken != result.RefreshToken || configErr != nil ||
			r.ParseForm() != nil || r.PostForm.Get("device_code") != deviceCode || r.PostForm.Get("ack_nonce") != ackNonce {
			t.Error("acknowledgment must follow a successful local login and carry its device code")
		}
		acknowledgments.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewTLSServer(router)
	defer server.Close()
	previousTLS := globalInsecureTLS
	globalInsecureTLS = true
	t.Cleanup(func() { globalInsecureTLS = previousTLS })

	var out bytes.Buffer
	cmd := newLoginCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--hub-url", server.URL, "--device"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ABCD-2345") || !strings.Contains(out.String(), "Logged in as user@example.com") {
		t.Fatalf("unexpected login output %q", out.String())
	}
	if strings.Contains(out.String(), deviceCode) || polls.Load() != 3 || acknowledgments.Load() != 1 {
		t.Fatal("expected retry after truncated response and one acknowledgment, without displaying the private device credential")
	}
	if got := mergedServer(t, configPath); got != serverURL {
		t.Fatalf("kubeconfig server = %q, want %q", got, serverURL)
	}
}

func sealTestDeviceResult(encodedKey string, result deviceLoginResult) ([]byte, error) {
	keyBytes, err := base64.RawURLEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, err
	}
	key, err := ecdh.X25519().NewPublicKey(keyBytes)
	if err != nil {
		return nil, err
	}
	recipient, err := hpke.NewDHKEMPublicKey(key)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(apiurl.DeviceLoginHPKEInfo), payload)
}

func TestOIDCLoginCacheWriteFailurePreservesKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "kubeconfig")
	setLoginKubeconfig(t, configPath)
	const previousServer = "https://previous.example.com/clusters/previous"
	writeKubeconfigFile(t, configPath, previousServer)
	if err := os.WriteFile(filepath.Join(home, ".config"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := saveOIDCLogin(&out, tenancyv1alpha1.LoginResponse{
		Kubeconfig: loginKubeconfig(t, "https://hub.example.com/clusters/home"),
		IDToken:    "id-token", IssuerURL: "https://issuer.example.com", ClientID: "railgrid-cli",
	})
	if err == nil || !strings.Contains(err.Error(), "saving OIDC login") {
		t.Fatalf("expected a credential write error, got %v", err)
	}
	if out.Len() != 0 || mergedServer(t, configPath) != previousServer {
		t.Fatal("credential write failure changed the kubeconfig or reported a successful login")
	}
}

func TestOIDCLoginKubeconfigFailureRestoresTokenCache(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			setLoginKubeconfig(t, t.TempDir())
			old := cliauth.TokenCache{IssuerURL: "https://issuer.example.com", ClientID: "railgrid-cli", IDToken: "old-id", RefreshToken: "old-refresh"}
			if existing {
				if err := cliauth.SaveTokenCache(&old); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := saveOIDCLogin(&out, tenancyv1alpha1.LoginResponse{
				Kubeconfig: loginKubeconfig(t, "https://hub.example.com/clusters/home"),
				IDToken:    "new-id", RefreshToken: "new-refresh", IssuerURL: old.IssuerURL, ClientID: old.ClientID,
			})
			if err == nil || !strings.Contains(err.Error(), "writing kubeconfig") || out.Len() != 0 {
				t.Fatalf("expected login to fail on writing kubeconfig, got %v", err)
			}
			cache, err := cliauth.LoadTokenCache(old.IssuerURL, old.ClientID)
			if existing && (err != nil || *cache != old) {
				t.Fatal("original credentials were not restored")
			}
			if !existing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new cache survived the failed login, load error %v", err)
			}
		})
	}
}

func TestOIDCLoginKeepsAccountsSeparate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const issuer = "https://issuer.example.com"
	const client = "railgrid-cli"
	if err := cliauth.SaveTokenCache(&cliauth.TokenCache{IssuerURL: issuer, ClientID: client, IDToken: "legacy"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.Load(loginKubeconfig(t, "https://hub.example.com/clusters/home"))
	if err != nil {
		t.Fatal(err)
	}
	auth := cfg.AuthInfos["user-abc"]
	auth.Token = ""
	auth.Exec = &clientcmdapi.ExecConfig{Command: "railgrid", Args: []string{"get-token", "--oidc-issuer-url=" + issuer, "--oidc-client-id=" + client}}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"alice", "bob"} {
		setLoginKubeconfig(t, filepath.Join(home, account))
		err = saveOIDCLogin(&bytes.Buffer{}, tenancyv1alpha1.LoginResponse{
			Kubeconfig: data, UserID: account, IssuerURL: issuer, ClientID: client, IDToken: account,
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, account := range []string{"alice", "bob"} {
		cfg, err := clientcmd.LoadFromFile(filepath.Join(home, account))
		if err != nil {
			t.Fatal(err)
		}
		_, _, execAccount := execOIDCArgs(cfg.AuthInfos["user-abc"].Exec)
		if execAccount != account {
			t.Fatalf("exec account = %q, want %q", execAccount, account)
		}
		var out bytes.Buffer
		if err := runGetToken(t.Context(), &out, issuer, client, false, execAccount); err != nil {
			t.Fatal(err)
		}
		var credential execCredential
		if err := json.Unmarshal(out.Bytes(), &credential); err != nil || credential.Status.Token != account {
			t.Fatalf("exec returned incorrect credentials for %s", account)
		}
	}
	cache, err := cliauth.LoadTokenCache(issuer, client)
	if err != nil || cache.IDToken != "legacy" {
		t.Fatal("account-specific sign-in changed the legacy cache")
	}
}
