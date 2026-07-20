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

package servicectrl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
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

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	"github.com/faroshq/provider-edges/internal/events"
	"github.com/faroshq/provider-edges/internal/haclient"
	"github.com/faroshq/provider-edges/internal/svccatalog"
)

// validationResyncInterval bounds how often a Ready Service is re-validated.
const validationResyncInterval = 10 * time.Minute

// ValidationReconciler validates a Service's credentials against the
// service (Home Assistant: GET /api/config) and stamps status.URL + conditions.
type ValidationReconciler struct {
	mgr                 mcmanager.Manager
	connManager         ConnManager
	edgeProxyPublicPath string
	// events, when non-nil, runs a per-Service event subscriber (UniFi Protect).
	events *events.Manager
}

// SetupValidationWithManager registers the validation reconciler (For Service).
// It also watches Secrets so an edited auth token is re-validated immediately,
// rather than waiting up to validationResyncInterval for the next resync.
func SetupValidationWithManager(mgr mcmanager.Manager, connManager ConnManager, edgeProxyPublicPath string, eventsMgr *events.Manager) error {
	r := &ValidationReconciler{mgr: mgr, connManager: connManager, edgeProxyPublicPath: edgeProxyPublicPath, events: eventsMgr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("service-validation").
		For(&edgesv1alpha1.Service{}).
		Watches(&corev1.Secret{}, mchandler.EnqueueRequestsFromMapFunc(r.mapSecretToServices)).
		Complete(r)
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

	es := &edgesv1alpha1.Service{}
	if err := c.Get(ctx, req.NamespacedName, es); err != nil {
		if apierrors.IsNotFound(err) {
			// Service deleted: tear down its subscriber and drop its events.
			if r.events != nil {
				r.events.Stop(ctx, eventsKey(req))
			}
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

	// Subscriber lifecycle: reconcile it on every return path from the captured
	// final state. It only runs for a Ready UniFi Protect Service; every other
	// outcome (not ready, wrong type, disabled) stops it and clears its events.
	var (
		subReady  bool
		subDialer haclient.Dialer
		subToken  string
		subTarget haclient.Target
	)
	defer func() {
		if r.events == nil || es.Spec.Type != edgesv1alpha1.ServiceTypeUniFiProtect {
			return
		}
		key := eventsKey(req)
		if subReady && subDialer != nil {
			r.events.Ensure(events.SubscriberConfig{
				Key:           key,
				ResolveDialer: r.dialerResolver(connResource(es), string(req.ClusterName), es.Spec.EdgeRef.Name),
				Target:        subTarget,
				Header:        unifiAuthHeader(ctx, subDialer, subTarget, def, subToken),
			})
		} else {
			r.events.Stop(ctx, key)
		}
	}()

	// Always keep status.URL current.
	es.Status.URL = r.statusURL(string(req.ClusterName), es.Name)

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
		return r.commit(ctx, c, orig, es, 30*time.Second)
	}

	subDialer = dialer // captured for the subscriber defer

	var token string
	if es.Spec.AuthSecretRef != nil {
		token, err = r.readToken(ctx, c, es)
		if err != nil {
			setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "SecretError", err.Error())
			es.Status.Phase = "Unreachable"
			return r.commit(ctx, c, orig, es, validationResyncInterval)
		}
	}
	subToken = token

	// Build the probe request and apply the type's auth. For the session-login
	// kinds (qBittorrent/Pi-hole) Apply performs the login, so a failure here is
	// the credential being rejected rather than a transport error.
	target := haclient.Target{
		Scheme: schemeString(es.Spec.Scheme),
		Host:   targetHost(es),
		Port:   es.Spec.Port,
	}
	subTarget = target // captured for the subscriber defer
	header := http.Header{}
	query := url.Values{}
	if err := svccatalog.Apply(ctx, dialer, target, def, token, header, query); err != nil {
		es.Status.Phase = "Unreachable"
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "Unauthorized", err.Error())
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "Unauthorized", err.Error())
		return r.commit(ctx, c, orig, es, validationResyncInterval)
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
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}
	defer resp.Body.Close() //nolint:errcheck

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
		subReady = true
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
			subReady = true
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
	return r.commit(ctx, c, orig, es, validationResyncInterval)
}

// eventsKey is the tenant+service scope events are stored and looked up under.
func eventsKey(req mcreconcile.Request) events.Key {
	return events.Key{Cluster: string(req.ClusterName), Service: req.Name}
}

// dialerResolver returns a closure that re-resolves the edge's tunnel dialer on
// demand, so a subscriber survives an edge tunnel that drops and re-registers.
func (r *ValidationReconciler) dialerResolver(resource, cluster, name string) func() (haclient.Dialer, bool) {
	key := connKey(resource, cluster, name)
	return func() (haclient.Dialer, bool) {
		d, ok := r.connManager.Load(key)
		if !ok || d == nil {
			return nil, false
		}
		return d, true
	}
}

// unifiAuthHeader builds the header the events WebSocket handshake needs, using
// the same catalog Apply as the data-plane proxy (UniFi → X-API-KEY). Apply does
// not dial for the API-key auth kind, so this is a pure header build.
func unifiAuthHeader(ctx context.Context, dialer haclient.Dialer, target haclient.Target, def svccatalog.Definition, token string) http.Header {
	h := http.Header{}
	_ = svccatalog.Apply(ctx, dialer, target, def, token, h, url.Values{})
	return h
}

// bodySnippet reads up to a small cap from an upstream response body and trims
// it to max runes for inclusion in a status condition / log — enough to surface
// an API's "why" message without dumping a whole page.
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

// statusURL builds the externalized svc-proxy base for a Service.
func (r *ValidationReconciler) statusURL(cluster, name string) string {
	if r.edgeProxyPublicPath == "" {
		return ""
	}
	return fmt.Sprintf("%s/clusters/%s/apis/%s/%s/services/%s/proxy",
		r.edgeProxyPublicPath, cluster,
		edgesv1alpha1.GroupName, edgesv1alpha1.Version, name)
}

func schemeString(s edgesv1alpha1.ServiceScheme) string {
	if s == edgesv1alpha1.ServiceSchemeHTTPS {
		return "https"
	}
	return "http"
}
