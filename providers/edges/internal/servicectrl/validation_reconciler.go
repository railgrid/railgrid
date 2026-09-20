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

package servicectrl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	"github.com/railgrid/provider-edges/internal/haclient"
	"github.com/railgrid/provider-edges/internal/svccatalog"
)

const (
	// validationResyncInterval bounds how often a Ready Service is re-validated,
	// and caps the retry backoff of a failing one.
	validationResyncInterval = 10 * time.Minute
	// probeRetryInitial is the first retry delay after a probe that failed for
	// a reason that clears on its own (the target pod not up yet, a 5xx, no
	// tunnel). It doubles per consecutive failure up to
	// validationResyncInterval, so a Service created before its backend is
	// ready flips to Ready within seconds of the backend coming up instead of
	// on the next 10-minute cycle.
	probeRetryInitial = 5 * time.Second
)

// ValidationReconciler validates a Service's credentials against the
// service (Home Assistant: GET /api/config) and stamps status.URL + conditions.
type ValidationReconciler struct {
	mgr                 mcmanager.Manager
	connManager         ConnManager
	edgeProxyPublicPath string

	// retryMu guards retry: the current backoff delay per Service, keyed by
	// cluster + name. Absent means the last probe succeeded (or never ran).
	retryMu sync.Mutex
	retry   map[string]time.Duration
}

// SetupValidationWithManager registers the validation reconciler (For Service).
// A create or spec update probes immediately (controller-runtime enqueues the
// event); a failed probe is retried with exponential backoff (see
// probeRetryInitial) and a Ready Service is re-validated every
// validationResyncInterval. It also watches Secrets so an edited auth token is
// re-validated immediately rather than on the next resync.
func SetupValidationWithManager(mgr mcmanager.Manager, connManager ConnManager, edgeProxyPublicPath string) error {
	r := newValidationReconciler(mgr, connManager, edgeProxyPublicPath)
	return mcbuilder.ControllerManagedBy(mgr).
		Named("service-validation").
		For(&edgesv1alpha1.Service{}).
		Watches(&corev1.Secret{}, mchandler.EnqueueRequestsFromMapFunc(r.mapSecretToServices)).
		Complete(r)
}

func newValidationReconciler(mgr mcmanager.Manager, connManager ConnManager, edgeProxyPublicPath string) *ValidationReconciler {
	return &ValidationReconciler{
		mgr:                 mgr,
		connManager:         connManager,
		edgeProxyPublicPath: edgeProxyPublicPath,
		retry:               map[string]time.Duration{},
	}
}

// retryKey scopes the backoff state to one Service in one workspace.
func retryKey(req mcreconcile.Request) string {
	return string(req.ClusterName) + "/" + req.NamespacedName.String()
}

// nextRetry returns the delay before re-probing a Service whose probe just
// failed transiently, doubling per consecutive failure from probeRetryInitial
// up to validationResyncInterval.
func (r *ValidationReconciler) nextRetry(key string) time.Duration {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	d, ok := r.retry[key]
	if !ok || d <= 0 {
		d = probeRetryInitial
	} else {
		d *= 2
	}
	if d > validationResyncInterval {
		d = validationResyncInterval
	}
	r.retry[key] = d
	return d
}

// resetRetry forgets the backoff after a successful probe (or when the
// Service is gone) so the next failure starts from probeRetryInitial again.
func (r *ValidationReconciler) resetRetry(key string) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	delete(r.retry, key)
}

// mapSecretToServices re-enqueues every Service in the same workspace whose
// authSecretRef points at the changed Secret.
func (r *ValidationReconciler) mapSecretToServices(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterKey, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		clusterKey = multicluster.ClusterName(obj.GetAnnotations()["kcp.io/cluster"])
	}
	cl, err := r.mgr.GetCluster(ctx, clusterKey)
	if err != nil {
		klog.V(2).InfoS("mapSecretToServices: GetCluster failed", "cluster", clusterKey, "err", err)
		return nil
	}

	var svcList edgesv1alpha1.ServiceList
	if err := cl.GetClient().List(ctx, &svcList); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range svcList.Items {
		ref := svcList.Items[i].Spec.AuthSecretRef
		if ref == nil || ref.Name != obj.GetName() || ref.Namespace != obj.GetNamespace() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: svcList.Items[i].Namespace, Name: svcList.Items[i].Name},
		})
	}
	return requests
}

func (r *ValidationReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("service", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	rkey := retryKey(req)
	es := &edgesv1alpha1.Service{}
	if err := c.Get(ctx, req.NamespacedName, es); err != nil {
		if apierrors.IsNotFound(err) {
			r.resetRetry(rkey)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	orig := es.DeepCopy()

	// The catalog tells us how to probe this type: the health path, the auth
	// style, and whether a 2xx proves the credential (ProbeValidate) or merely
	// that the service is up (ProbeReachable). An unknown type falls back to a
	// bare reachability probe on "/". Declared here so the subscriber defer can
	// reuse it for the auth header.
	def, _ := svccatalog.Get(string(es.Spec.Type))

	// Always keep the published coordinates and the tool projection current.
	// They are what makes this Service discoverable from the object itself —
	// no catalog route, no provider-specific fetch.
	es.Status.URL = r.statusURL(string(req.ClusterName), es.Name)
	r.projectCatalog(es, string(req.ClusterName), def)

	// The CRD enum rejects unknown edge kinds, but keep the reconciler fail
	// closed for objects that predate that validation or arrive through a bypass.
	// In particular, never let an unknown kind fall through to LinuxServer's
	// ConnManager key and host-loopback policy.
	if !supportedEdgeKind(es) {
		es.Status.Phase = "Unreachable"
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "InvalidEdgeRef",
			"spec.edgeRef.kind must be LinuxServer, MacOSServer, or KubernetesCluster")
		setNotProbed(es, "the service references an unsupported edge kind")
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}

	// No credentials → nothing to validate, unless the type is usable
	// unauthenticated (e.g. Prometheus), in which case we still probe for
	// reachability below with an empty token.
	if es.Spec.AuthSecretRef == nil && !def.Credential.Optional {
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionUnknown, "NoCredentials", "no authSecretRef configured")
		if es.Status.Phase == "" {
			es.Status.Phase = "Detected"
		}
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}

	// A kube Service needs a targetRef to have anything to dial. The CRD's CEL
	// rule enforces this, but an object written before the rule (or by a client
	// that bypassed it) would otherwise silently validate against loopback.
	if isKube(es) && (es.Spec.TargetRef == nil || es.Spec.TargetRef.Name == "" || es.Spec.TargetRef.Namespace == "") {
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "MissingTargetRef",
			"spec.targetRef is required when spec.edgeRef.kind is KubernetesCluster")
		setNotProbed(es, "spec.targetRef is missing, so the service was never reached")
		es.Status.Phase = "Unreachable"
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}

	// Need a live tunnel to the edge to validate.
	key := connKey(connResource(es), string(req.ClusterName), es.Spec.EdgeRef.Name)
	dialer, ok := r.connManager.Load(key)
	if !ok {
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "EdgeDisconnected", "no live tunnel to the edge")
		setNotProbed(es, "the edge is disconnected, so the service was never reached")
		return r.commit(ctx, c, orig, es, r.nextRetry(rkey))
	}

	var token string
	if es.Spec.AuthSecretRef != nil {
		token, err = r.readToken(ctx, c, es)
		if err != nil {
			setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "SecretError", err.Error())
			es.Status.Phase = "Unreachable"
			return r.commit(ctx, c, orig, es, validationResyncInterval)
		}
	}

	// Build the probe request and apply the type's auth. For the session-login
	// kinds (qBittorrent/Pi-hole) Apply performs the login, so a failure here is
	// the credential being rejected rather than a transport error.
	target := haclient.Target{
		Scheme:                schemeString(es.Spec.Scheme),
		Host:                  targetHost(es),
		Port:                  es.Spec.Port,
		TLSInsecureSkipVerify: es.Spec.TLSInsecureSkipVerify,
	}
	header := http.Header{}
	query := url.Values{}
	if err := svccatalog.Apply(ctx, dialer, target, def, token, header, query); err != nil {
		es.Status.Phase = "Unreachable"
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "Unauthorized", err.Error())
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "Unauthorized", err.Error())
		// The login may have failed because the service is not up yet rather
		// than because the credential is wrong; back off rather than wait a
		// full cycle (a Secret edit re-enqueues immediately either way).
		return r.commit(ctx, c, orig, es, r.nextRetry(rkey))
	}

	probePath := def.ProbePath
	if probePath == "" {
		probePath = "/"
	}
	if q := query.Encode(); q != "" {
		probePath += "?" + q
	}

	resp, err := haclient.DoWith(ctx, dialer, target, http.MethodGet, probePath, header, nil)
	if err != nil {
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "ProbeFailed", err.Error())
		setNotProbed(es, "the service could not be reached, so the token was never checked")
		es.Status.Phase = "Unreachable"
		return r.commit(ctx, c, orig, es, r.nextRetry(rkey))
	}
	defer resp.Body.Close() //nolint:errcheck

	// The agent refused to dial spec.host (403 + X-Railgrid-Svc-Policy: enforce):
	// not a credential problem, and nothing was probed. Surface the agent's
	// reason so the operator knows to add the host to --svc-allow-cidr.
	if haclient.IsHostNotAllowed(resp) {
		es.Status.Phase = "Unreachable"
		reason := hostNotAllowedReason(resp.Body)
		logger.Info("agent refused the service host", "type", es.Spec.Type, "host", target.Host, "reason", reason)
		setCondition(&es.Status.Conditions, "HostAllowed", metav1.ConditionFalse, "HostNotAllowed", reason)
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "HostNotAllowed",
			"the edge agent refused to dial spec.host: "+reason)
		setNotProbed(es, "the edge agent refused to dial spec.host, so the service was never reached")
		return r.commit(ctx, c, orig, es, r.nextRetry(rkey))
	}
	if resp.Header.Get(haclient.SvcPolicyHeader) == haclient.SvcPolicyWarn {
		// Dialed under --svc-policy=warn: works today, refused once the agent
		// enforces. Say so on the object rather than only in the agent log.
		setCondition(&es.Status.Conditions, "HostAllowed", metav1.ConditionTrue, "WarnOnly",
			"spec.host is outside the edge agent's --svc-allow-cidr; it was dialed under --svc-policy=warn and will be refused under enforce")
	} else {
		// Name the host actually dialed: a targetRef Service has no spec.host,
		// and "accepts spec.host" sent readers looking for a field they never set.
		setCondition(&es.Status.Conditions, "HostAllowed", metav1.ConditionTrue, "Allowed", "the edge agent accepts the service target "+target.Host)
	}

	mode := def.ProbeMode
	if mode == "" {
		if def.ProbePath != "" {
			mode = svccatalog.ProbeValidate
		} else {
			mode = svccatalog.ProbeReachable
		}
	}

	switch mode {
	case svccatalog.ProbeReachable:
		// Any answer below 500 means the service is up. We can only claim the
		// credential is valid if we actually sent one and it wasn't rejected.
		if resp.StatusCode >= http.StatusInternalServerError {
			es.Status.Phase = "Unreachable"
			setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "ProbeFailed",
				fmt.Sprintf("service returned %d", resp.StatusCode))
			setNotProbed(es, fmt.Sprintf("service returned %d, so the token was never checked", resp.StatusCode))
			break
		}
		es.Status.Phase = "Ready"
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready", "service reachable")
		if token != "" && resp.StatusCode < http.StatusMultipleChoices {
			setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionTrue, "Validated", "credentials accepted by the service")
		} else {
			setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionUnknown, "NotVerified",
				"service reachable; credentials are not verified for this service type")
		}
	default: // ProbeValidate — the probe path requires auth, so status is decisive.
		switch {
		case resp.StatusCode < http.StatusMultipleChoices:
			// Home Assistant's /api/config returns { version, ... }.
			if es.Spec.Type == edgesv1alpha1.ServiceTypeHomeAssistant {
				var cfg struct {
					Version string `json:"version"`
				}
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cfg)
				if cfg.Version != "" {
					es.Status.Version = cfg.Version
				}
			}
			es.Status.Phase = "Ready"
			setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionTrue, "Validated", "credentials accepted by the service")
			setCondition(&es.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready", "service reachable and authenticated")
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			es.Status.Phase = "Unreachable"
			// Surface the upstream error body — it usually says WHY (e.g. UniFi
			// "Invalid API Key" vs "API key lacks Protect access"), which is the
			// difference between a bad key and a wrong endpoint/firmware.
			detail := bodySnippet(resp.Body, 300)
			logger.Info("service rejected the credentials", "type", es.Spec.Type,
				"status", resp.StatusCode, "probePath", probePath, "upstream", detail)
			msg := fmt.Sprintf("service rejected the credentials (%d)", resp.StatusCode)
			if detail != "" {
				msg += ": " + detail
			}
			setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "Unauthorized", msg)
			setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "Unauthorized",
				"service reachable but rejected the credentials")
		default:
			es.Status.Phase = "Unreachable"
			setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "ProbeFailed",
				fmt.Sprintf("service returned %d", resp.StatusCode))
			setNotProbed(es, fmt.Sprintf("service returned %d, so the credentials were never checked", resp.StatusCode))
		}
	}

	logger.V(4).Info("validated service", "phase", es.Status.Phase, "type", es.Spec.Type)
	if es.Status.Phase == "Ready" {
		// Healthy: forget the backoff and settle into the slow re-validation cycle.
		r.resetRetry(rkey)
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}
	// Not ready (5xx, unexpected status, rejected credentials): retry with
	// backoff. The common case is a Service created seconds before its pod
	// answers — a 502 from the agent's svc proxy — which must not wait a cycle.
	return r.commit(ctx, c, orig, es, r.nextRetry(rkey))
}

// bodySnippet reads up to a small cap from an upstream response body and trims
// it to max runes for inclusion in a status condition / log — enough to surface
// an API's "why" message without dumping a whole page.
// hostNotAllowedReason extracts the agent's reason from its 403 body
// ({"error":"target host not allowed","reason":"..."}), falling back to the
// raw snippet.
func hostNotAllowedReason(body io.Reader) string {
	raw := bodySnippet(body, 300)
	var js struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &js); err == nil && js.Reason != "" {
		return js.Reason
	}
	if raw == "" {
		return "target host not allowed by the edge agent's --svc-allow-cidr policy"
	}
	return raw
}

func bodySnippet(body io.Reader, max int) string {
	b, _ := io.ReadAll(io.LimitReader(body, 4<<10))
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// commit writes status only when it changed, then requeues.
func (r *ValidationReconciler) commit(ctx context.Context, c client.Client, orig, es *edgesv1alpha1.Service, requeue time.Duration) (ctrl.Result, error) {
	if equalStatus(&orig.Status, &es.Status) {
		return ctrl.Result{RequeueAfter: requeue}, nil
	}
	if err := c.Status().Update(ctx, es); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating service status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// readToken reads the "token" key from the Service's authSecretRef.
func (r *ValidationReconciler) readToken(ctx context.Context, c client.Client, es *edgesv1alpha1.Service) (string, error) {
	ref := es.Spec.AuthSecretRef
	secret := &corev1.Secret{}
	nn := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := c.Get(ctx, nn, secret); err != nil {
		return "", fmt.Errorf("fetching auth secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	tok, ok := secret.Data["token"]
	if !ok || len(tok) == 0 {
		return "", fmt.Errorf("auth secret %s/%s has no \"token\" key", ref.Namespace, ref.Name)
	}
	return string(tok), nil
}

// projectCatalog stamps what the provider's own service catalog says about
// this Service's type onto its status: the MCP endpoint (when the type has
// tools) and the tool list itself. An unknown type clears both rather than
// leaving a stale projection of a type the provider no longer serves.
func (r *ValidationReconciler) projectCatalog(es *edgesv1alpha1.Service, cluster string, def svccatalog.Definition) {
	if len(def.Tools) == 0 {
		es.Status.MCPURL = ""
		es.Status.Tools = nil
		return
	}
	tools := make([]edgesv1alpha1.ServiceTool, 0, len(def.Tools))
	for _, tool := range def.Tools {
		tools = append(tools, edgesv1alpha1.ServiceTool{Name: tool.Name, Description: tool.Desc})
	}
	es.Status.Tools = tools
	es.Status.MCPURL = r.verbURL(cluster, es.Name, "mcp")
}

// statusURL builds the externalized svc-proxy base for a Service, on the
// shared data-plane grammar:
//
//	{edgeProxyPublicPath}/clusters/{cluster}/services/{name}/proxy
func (r *ValidationReconciler) statusURL(cluster, name string) string {
	if r.edgeProxyPublicPath == "" {
		return ""
	}
	return r.verbURL(cluster, name, "proxy")
}

// verbURL renders one data-plane coordinate for a Service.
func (r *ValidationReconciler) verbURL(cluster, name, verb string) string {
	if r.edgeProxyPublicPath == "" {
		return ""
	}
	return fmt.Sprintf("%s/clusters/%s/services/%s/%s",
		strings.TrimRight(r.edgeProxyPublicPath, "/"), cluster, name, verb)
}

func schemeString(s edgesv1alpha1.ServiceScheme) string {
	if s == edgesv1alpha1.ServiceSchemeHTTPS {
		return "https"
	}
	return "http"
}
