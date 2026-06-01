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

package restapi

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gorilla/mux"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// downloadKubeconfig serves a workspace-scoped kubeconfig YAML for the
// caller. Two shapes are emitted depending on how the hub authenticates:
//
//   - OIDC mode (KubeconfigConfig.OIDCIssuerURL set): the kubeconfig
//     points users at `kedge get-token` via the exec credential plugin
//     — the same shape /auth/callback writes — so refresh keeps working.
//   - Static-token mode: the caller's bearer token is embedded directly.
//     The downloaded file behaves the same way the live REST call did.
//
// Either way the cluster URL is HubExternalURL + /clusters/<clusterName>,
// where clusterName is the kcp logical-cluster hash for the workspace
// (resolved via the bootstrapper).
func (h *Handler) downloadKubeconfig(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	if h.mgr.kubeconfig.HubExternalURL == "" {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable",
			"kubeconfig download is not configured on this hub")
		return
	}

	orgUUID := mux.Vars(r)["org"]
	wsUUID := mux.Vars(r)["ws"]

	clusterName, err := h.mgr.bootstrapper.GetChildWorkspaceClusterName(r.Context(), orgUUID, wsUUID)
	if err != nil {
		writeError(w, err)
		return
	}

	var staticToken string
	if h.mgr.kubeconfig.OIDCIssuerURL == "" {
		ah := r.Header.Get("Authorization")
		if !strings.HasPrefix(ah, "Bearer ") {
			writeStatus(w, http.StatusUnauthorized, "Unauthorized", "missing bearer token")
			return
		}
		staticToken = strings.TrimPrefix(ah, "Bearer ")
	}

	cfg, err := h.mgr.buildWorkspaceKubeconfig(tc.User, clusterName, staticToken)
	if err != nil {
		writeError(w, err)
		return
	}

	// Best-effort: prefer the display name in the filename. Falls back to
	// the UUID when the workspace has no display-name annotation (the
	// personal-org default workspace, today).
	dn, _ := h.mgr.bootstrapper.GetWorkspaceDisplayName(r.Context(), orgUUID, wsUUID)
	filename := kubeconfigFilename(dn, wsUUID)

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	_, _ = w.Write(cfg)
}

// kubeconfigFilename returns kedge-<slug>.kubeconfig where <slug> is the
// workspace's display name (sanitised to filesystem-safe chars) or the
// UUID when there's no display name.
func kubeconfigFilename(displayName, uuid string) string {
	base := strings.TrimSpace(displayName)
	if base == "" {
		base = uuid
	}
	base = filenameSafe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = uuid
	}
	return "kedge-" + base + ".kubeconfig"
}

var filenameSafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (m *Manager) buildWorkspaceKubeconfig(userID, clusterName, staticToken string) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	serverURL := apiurl.HubServerURL(m.kubeconfig.HubExternalURL, clusterName)

	cfg.Clusters["kedge"] = &clientcmdapi.Cluster{
		Server:                serverURL,
		InsecureSkipTLSVerify: m.kubeconfig.DevMode,
	}

	authInfo := &clientcmdapi.AuthInfo{}
	if m.kubeconfig.OIDCIssuerURL != "" {
		// PKCE public client — no --oidc-client-secret. Matches the args
		// pkg/server/auth.Handler.generateKubeconfig writes on first login.
		args := []string{
			"get-token",
			"--oidc-issuer-url=" + m.kubeconfig.OIDCIssuerURL,
			"--oidc-client-id=" + m.kubeconfig.OIDCClientID,
		}
		if m.kubeconfig.DevMode {
			args = append(args, "--insecure-skip-tls-verify")
		}
		authInfo.Exec = &clientcmdapi.ExecConfig{
			APIVersion: "client.authentication.k8s.io/v1beta1",
			Command:    "kedge",
			Args:       args,
		}
	} else {
		authInfo.Token = staticToken
	}
	cfg.AuthInfos[userID] = authInfo

	cfg.Contexts["kedge"] = &clientcmdapi.Context{
		Cluster:  "kedge",
		AuthInfo: userID,
	}
	cfg.CurrentContext = "kedge"

	return clientcmd.Write(*cfg)
}
