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

// Package providerinstall runs durable Helm installations through Kubernetes Edges.
// The Job and Helm release live on the selected cluster. The sealed provider
// Workspace records the plan; neither a browser nor a tenant-authored catalog
// entry can change the target or chart of a running attempt.
package providerinstall

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/strvals"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const planAnnotation = "providers.railgrid.ai/installation"
const ownerLabel = "railgrid.ai/provider-installation"
const attemptLabel = "providers.railgrid.ai/installation-attempt"
const attemptIDAnnotation = "providers.railgrid.ai/installation-attempt-id"
const installerImage = "alpine/helm:3.20.0"

const preparationLeaseDuration = 5 * time.Minute
const preparationTimeout = 2 * time.Minute
const preparationCleanupTimeout = 30 * time.Second
const runnerTokenRequestedLifetime = 15 * time.Minute
const runnerTokenMinimumLifetime = 12 * time.Minute
const runnerTokenMaximumLifetime = 16 * time.Minute

var errPreparationSuperseded = errors.New("installation attempt changed while preparing")

func runnerSecret(p *Plan) string { return p.Job + "-config" }

func attemptJobName(owner string, attempt int, attemptID string) string {
	digest := sha256.Sum256([]byte(owner))
	suffix := attemptID
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return fmt.Sprintf("railgrid-install-%s-%d-%s", hex.EncodeToString(digest[:6]), attempt, suffix)
}

var workspaceGVR = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}

type WorkspaceAccess interface {
	GetProviderEdgeRoute(context.Context, string, string) (*kcp.EdgeRoute, error)
	GetChildWorkspaceClusterName(context.Context, string, string) (string, error)
	ChildWorkspaceConfig(string, string) *rest.Config
}

type Installer struct {
	admin         *rest.Config
	workspaces    WorkspaceAccess
	hubURL        string
	insecure      bool
	kubeClient    func(*rest.Config) (kubernetes.Interface, error)
	dynamicClient func(*rest.Config) (dynamic.Interface, error)
}

func New(admin *rest.Config, workspaces WorkspaceAccess, hubURL string, insecure bool) *Installer {
	return &Installer{admin: rest.CopyConfig(admin), workspaces: workspaces, hubURL: hubURL, insecure: insecure, kubeClient: func(c *rest.Config) (kubernetes.Interface, error) { return kubernetes.NewForConfig(c) }, dynamicClient: func(c *rest.Config) (dynamic.Interface, error) { return dynamic.NewForConfig(c) }}
}

type Plan struct {
	Namespace            string         `json:"namespace"`
	Release              string         `json:"release"`
	Chart                string         `json:"chart"`
	Version              string         `json:"version"`
	Values               map[string]any `json:"values"`
	Job                  string         `json:"job"`
	Attempt              int            `json:"attempt"`
	Owner                string         `json:"owner"`
	AttemptID            string         `json:"attemptID,omitempty"`
	PreparationID        string         `json:"preparationID,omitempty"`
	PreparationExpiresAt time.Time      `json:"preparationExpiresAt,omitzero"`
}
type Status struct {
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	Namespace string `json:"namespace,omitempty"`
	Job       string `json:"job,omitempty"`
	Version   string `json:"version,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	CanRetry  bool   `json:"canRetry"`
}

// Authorize checks the caller's permission on this exact Edge before the hub
// uses its own credential for provisioning. Organization registration policy
// alone must not confer access to an Edge's Kubernetes API.
func (i *Installer) Authorize(ctx context.Context, org, workspace, edge, user string) error {
	cs, err := i.kubeClient(i.workspaces.ChildWorkspaceConfig(org, workspace))
	if err != nil {
		return err
	}
	for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		resource, subresource := "kubernetesclusters", "k8s"
		review, err := cs.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{User: user, Groups: []string{"system:authenticated"}, ResourceAttributes: &authzv1.ResourceAttributes{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: resource, Subresource: subresource, Name: edge, Verb: verb}}}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if !reviewAllows(review) {
			return apierrors.NewForbidden(schema.GroupResource{Group: "edges.railgrid.ai", Resource: resource + "/" + subresource}, edge, fmt.Errorf("you need Kubernetes API access to install on this Edge"))
		}
	}
	review, err := cs.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{User: user, Groups: []string{"system:authenticated"}, ResourceAttributes: &authzv1.ResourceAttributes{Group: "edges.railgrid.ai", Resource: "kubernetesclusters", Name: edge, Verb: "get"}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if !reviewAllows(review) {
		return apierrors.NewForbidden(schema.GroupResource{Group: "edges.railgrid.ai", Resource: "kubernetesclusters"}, edge, fmt.Errorf("you need access to this Edge"))
	}

	return nil
}

func reviewAllows(review *authzv1.SubjectAccessReview) bool {
	return review != nil && review.Status.Allowed && !review.Status.Denied && review.Status.EvaluationError == ""
}

func (i *Installer) edgeClients(ctx context.Context, org, name string) (kubernetes.Interface, kubernetes.Interface, *kcp.EdgeRoute, error) {
	route, err := i.workspaces.GetProviderEdgeRoute(ctx, org, name)
	if err != nil {
		return nil, nil, nil, err
	}
	if route == nil {
		return nil, nil, nil, fmt.Errorf("select an Edge before installing")
	}
	cluster, err := i.workspaces.GetChildWorkspaceClusterName(ctx, org, route.WorkspaceUUID)
	if err != nil {
		return nil, nil, nil, err
	}
	route.Cluster = cluster
	local, err := i.kubeClient(i.workspaces.ChildWorkspaceConfig(org, route.WorkspaceUUID))
	if err != nil {
		return nil, nil, nil, err
	}
	cfg := rest.CopyConfig(i.admin)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster) + "/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/" + route.EdgeName + "/k8s"
	cfg.Timeout = 20 * time.Second
	edge, err := i.kubeClient(cfg)
	return edge, local, route, err
}
func (i *Installer) parent(org string) (dynamic.ResourceInterface, error) {
	cfg := rest.CopyConfig(i.admin)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.OrgProvidersParent(org))
	cl, err := i.dynamicClient(cfg)
	if err != nil {
		return nil, err
	}
	return cl.Resource(workspaceGVR), nil
}
func (i *Installer) readPlan(ctx context.Context, org, name string) (*Plan, string, error) {
	cl, err := i.parent(org)
	if err != nil {
		return nil, "", err
	}
	ws, err := cl.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	owner := string(ws.GetUID())
	if owner == "" {
		return nil, "", fmt.Errorf("provider workspace has no UID for installation ownership")
	}
	var plan *Plan
	if raw := ws.GetAnnotations()[planAnnotation]; raw != "" {
		plan = &Plan{}
		if err := json.Unmarshal([]byte(raw), plan); err != nil {
			return nil, "", fmt.Errorf("invalid stored installation plan")
		}
	}
	if plan != nil && plan.Owner != owner {
		return nil, "", fmt.Errorf("installation belongs to an earlier provider registration")
	}
	return plan, owner, nil
}

// ResolvePlan takes only the platform's declared recipe. No shell text, chart
// URLs, Kubernetes objects, or values supplied by a browser are executed.
func ResolvePlan(in providers.InstallInstructions) (*Plan, error) {
	if len(in.Warnings) > 0 {
		return nil, fmt.Errorf("this provider needs manual installation: %s", strings.Join(in.Warnings, " "))
	}
	p := &Plan{Namespace: in.Namespace, Release: in.ReleaseName, Chart: in.ChartRef, Version: in.ChartVersion, Values: map[string]any{}}
	for _, v := range in.Values {
		if v.Unresolved {
			return nil, fmt.Errorf("set %s using manual installation", v.Name)
		}
		if err := strvals.ParseInto(v.Name+"="+v.Value, p.Values); err != nil {
			return nil, fmt.Errorf("invalid installation value %s", v.Name)
		}
	}
	if p.Version == "" {
		rc, err := registry.NewClient()
		if err != nil {
			return nil, err
		}
		options := action.NewInstall(&action.Configuration{RegistryClient: rc})
		path, err := options.LocateChart(p.Chart, cli.New())
		if err != nil {
			return nil, fmt.Errorf("could not resolve the provider chart")
		}
		ch, err := loader.Load(path)
		if err != nil {
			return nil, fmt.Errorf("could not load the provider chart")
		}
		p.Version = ch.Metadata.Version
	}
	if _, err := semver.StrictNewVersion(p.Version); err != nil {
		return nil, fmt.Errorf("provider chart version must be an exact semantic version")
	}
	return p, nil
}

// Start is idempotent while an attempt is active. A retry creates a new Job
// only after the previous one has failed, using its original pinned recipe.
func (i *Installer) Start(ctx context.Context, org, name string, proposed *Plan, credential []byte, retryFailed bool) (*Status, error) {
	edge, local, route, err := i.edgeClients(ctx, org, name)
	if err != nil {
		return nil, err
	}
	old, owner, err := i.readPlan(ctx, org, name)
	if err != nil {
		return nil, err
	}
	attempt := 1
	if old != nil {
		status, err := i.status(ctx, edge, old, false)
		if err != nil {
			return nil, err
		}
		if status.Phase == "Preparing" && preparationLeaseActive(old) {
			return status, nil
		}
		if !retryFailed || !status.CanRetry {
			if status.Phase != "Preparing" {
				return status, nil
			}
		}
		proposed = old
		attempt = old.Attempt
		if status.CanRetry && (retryFailed || status.Phase == "Preparing") {
			// A missing Job can mean that preparation failed before Job creation.
			// Revoke its credentials before starting another durable attempt.
			if err := i.cleanupAttempt(ctx, org, name, old.Job); err != nil {
				return nil, fmt.Errorf("cleaning previous installer attempt: %w", err)
			}
			attempt++
		}
	}
	if proposed == nil {
		return nil, fmt.Errorf("installation recipe is missing")
	}
	plan := *proposed
	plan.Owner = owner
	plan.Attempt = attempt
	plan.AttemptID, err = newInstallationID()
	if err != nil {
		return nil, err
	}
	plan.Job = attemptJobName(owner, attempt, plan.AttemptID)
	plan.PreparationID, err = newPreparationID()
	if err != nil {
		return nil, err
	}
	cl, err := i.parent(org)
	if err != nil {
		return nil, err
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := cl.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(ws.GetUID()) != owner {
			return apierrors.NewConflict(workspaceGVR.GroupResource(), name, fmt.Errorf("provider registration changed; reload before installing"))
		}
		// Compare the durable plan, not a process-local lock: concurrent POSTs on
		// different hub replicas must agree on the same attempt.
		current := ws.GetAnnotations()[planAnnotation]
		previous := ""
		if old != nil {
			raw, _ := json.Marshal(old)
			previous = string(raw)
		}
		if current != previous {
			return apierrors.NewConflict(workspaceGVR.GroupResource(), name, fmt.Errorf("installation changed; reload its status"))
		}
		annotations := ws.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		plan.PreparationExpiresAt = time.Now().Add(preparationLeaseDuration)
		raw, _ := json.Marshal(plan)
		annotations[planAnnotation] = string(raw)
		ws.SetAnnotations(annotations)
		_, err = cl.Update(ctx, ws, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, err
	}
	prepareCtx, cancel := context.WithTimeout(ctx, preparationTimeout)
	prepareErr := i.prepare(prepareCtx, org, name, edge, local, route, &plan, credential)
	cancel()
	if prepareErr != nil {
		// Stop leaving a runner token, ServiceAccount, and RBAC behind when
		// preparation fails or the request context is canceled. This cleanup is
		// detached from the request but bounded; it also inspects Job/Pod state so
		// an ambiguous successful Job Create is left intact.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), preparationCleanupTimeout)
		defer cleanupCancel()
		var releaseErr error
		if !errors.Is(prepareErr, errPreparationSuperseded) {
			releaseErr = i.releasePreparation(cleanupCtx, org, name, &plan)
		}
		cleanupErr := cleanupAttemptResources(cleanupCtx, edge, local, &plan)
		return nil, errors.Join(prepareErr, releaseErr, cleanupErr)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), preparationCleanupTimeout)
	defer cleanupCancel()
	if err := i.releasePreparation(cleanupCtx, org, name, &plan); err != nil {
		return nil, err
	}
	return i.status(ctx, edge, &plan, false)
}

func (i *Installer) Status(ctx context.Context, org, name string, ready bool) (*Status, error) {
	plan, _, err := i.readPlan(ctx, org, name)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return &Status{Phase: "NotStarted", Message: "Installation has not started."}, nil
	}
	edge, _, _, err := i.edgeClients(ctx, org, name)
	if err != nil {
		return nil, err
	}
	return i.status(ctx, edge, plan, ready)
}
func (i *Installer) status(ctx context.Context, edge kubernetes.Interface, plan *Plan, ready bool) (*Status, error) {
	out := &Status{Phase: "Queued", Message: "Waiting for the installer to start.", Namespace: plan.Namespace, Job: plan.Job, Version: plan.Version, Attempt: plan.Attempt}
	job, err := edge.BatchV1().Jobs(plan.Namespace).Get(ctx, plan.Job, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		out.Phase = "Preparing"
		out.Message = "The installer Job is unavailable. Retry to start a new attempt."
		out.CanRetry = !preparationLeaseActive(plan)
		if !out.CanRetry {
			out.Message = "The installer is still being prepared. Retry will be available if preparation stops."
		}
		active, listErr := hasActiveAttemptPod(ctx, edge, plan)
		if listErr != nil {
			return nil, listErr
		}
		if active {
			out.Phase = "Installing"
			out.Message = "The installer Job is unavailable while its Pod is still active. Wait for it to stop before retrying."
			out.CanRetry = false
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !jobBelongsToPlan(job, plan) {
		return nil, fmt.Errorf("installer Job ownership does not match")
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			active, err := hasActiveAttemptPod(ctx, edge, plan)
			if err != nil {
				return nil, err
			}
			if job.Status.Active > 0 || active {
				out.Phase = "Installing"
				out.Message = "The previous installer Pod is still stopping. Wait for it to stop before retrying."
				return out, nil
			}
			out.Phase = "Failed"
			out.Message = "Helm installation failed. Retry the installation, or inspect the installer Job in your cluster."
			out.CanRetry = true
			return out, nil
		}
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			out.Phase = "Checking"
			out.Message = "Helm finished. Waiting for the provider to become healthy."
			if ready {
				out.Phase = "Ready"
				out.Message = "Your provider is ready."
			} else if !c.LastTransitionTime.IsZero() && time.Since(c.LastTransitionTime.Time) > 5*time.Minute {
				out.Phase = "Failed"
				out.Message = "The provider is installed but has not become healthy. Check its pods and retry."
				out.CanRetry = true
			}
			return out, nil
		}
	}
	if job.Status.Active > 0 {
		out.Phase = "Installing"
		out.Message = "Installing the Helm release on your cluster."
	}
	return out, nil
}

func (i *Installer) prepare(ctx context.Context, org, name string, edge, local kubernetes.Interface, route *kcp.EdgeRoute, p *Plan, credential []byte) error {
	if err := i.assertPreparation(ctx, org, name, p); err != nil {
		return err
	}
	ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns, err = edge.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, Labels: map[string]string{ownerLabel: p.Owner}}}, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("preparing installation namespace: %w", err)
	}
	if ns.Labels[ownerLabel] != p.Owner {
		return fmt.Errorf("namespace %s already exists outside this installation; use manual installation or choose a clean namespace", p.Namespace)
	}
	if err := putSecret(ctx, edge, p.Namespace, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: providers.KubeconfigSecretName, Labels: map[string]string{ownerLabel: p.Owner}}, Data: map[string][]byte{"kubeconfig": credential}}, p.Owner); err != nil {
		return err
	}
	// The runner has no local Kubernetes role or automounted token. Its only
	// execution credential expires after 15 minutes and addresses this Edge.
	identity := "railgrid-" + p.Job
	sa, err := local.CoreV1().ServiceAccounts("default").Get(ctx, identity, metav1.GetOptions{})
	saMissing := apierrors.IsNotFound(err)
	if err != nil && !saMissing {
		return err
	}
	if !saMissing && !ownedByAttempt(sa, p.Owner, p.AttemptID) {
		return fmt.Errorf("installer identity ownership does not match")
	}
	role := &rbacv1.ClusterRole{ObjectMeta: attemptObjectMeta(identity, p), Rules: []rbacv1.PolicyRule{{APIGroups: []string{"edges.railgrid.ai"}, Resources: []string{"kubernetesclusters", "kubernetesclusters/k8s"}, ResourceNames: []string{route.EdgeName}, Verbs: []string{"*"}}}}
	if err = ensureClusterRole(ctx, local, role, p.Owner); err != nil {
		return err
	}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: attemptObjectMeta(identity, p), RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: identity}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: identity, Namespace: "default"}}}
	if err = ensureClusterRoleBinding(ctx, local, binding, p.Owner); err != nil {
		return err
	}
	// Create the identity only after verifying any same-name binding. Otherwise
	// a pre-existing broad binding could grant its permissions to the SA at the
	// instant we create it, before we discover the mismatch.
	if saMissing {
		sa, err = local.CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{ObjectMeta: attemptObjectMeta(identity, p)}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if !ownedByAttempt(sa, p.Owner, p.AttemptID) {
			return fmt.Errorf("installer identity ownership does not match")
		}
	}
	expiration := int64(runnerTokenRequestedLifetime.Seconds())
	token, err := local.CoreV1().ServiceAccounts("default").CreateToken(ctx, identity, &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &expiration, Audiences: []string{"https://kcp.default.svc"}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if strings.TrimSpace(token.Status.Token) == "" {
		return fmt.Errorf("TokenRequest returned an empty runner token")
	}
	tokenLifetime := time.Until(token.Status.ExpirationTimestamp.Time)
	if token.Status.ExpirationTimestamp.IsZero() || tokenLifetime < runnerTokenMinimumLifetime || tokenLifetime > runnerTokenMaximumLifetime {
		return fmt.Errorf("TokenRequest did not honor the bounded runner token lifetime")
	}
	config := clientcmdapi.Config{Clusters: map[string]*clientcmdapi.Cluster{"edge": {Server: strings.TrimRight(i.hubURL, "/") + "/clusters/" + route.Cluster + "/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/" + route.EdgeName + "/k8s", InsecureSkipTLSVerify: i.insecure}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"installer": {Token: token.Status.Token}}, Contexts: map[string]*clientcmdapi.Context{"install": {Cluster: "edge", AuthInfo: "installer", Namespace: p.Namespace}}, CurrentContext: "install"}
	data, err := clientcmd.Write(config)
	if err != nil {
		return err
	}
	values, err := json.Marshal(p.Values)
	if err != nil {
		return err
	}
	if err = putSecret(ctx, edge, p.Namespace, &corev1.Secret{ObjectMeta: attemptObjectMeta(runnerSecret(p), p), Data: map[string][]byte{"kubeconfig": data, "values.json": values}}, p.Owner); err != nil {
		return err
	}
	if err := i.assertPreparation(ctx, org, name, p); err != nil {
		return err
	}
	desiredJob := installerJob(p)
	_, err = edge.BatchV1().Jobs(p.Namespace).Create(ctx, desiredJob, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := edge.BatchV1().Jobs(p.Namespace).Get(ctx, p.Job, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if !jobBelongsToPlan(existing, p) || !installerJobSpecMatches(existing, desiredJob) {
			return fmt.Errorf("installer Job %s already exists with different ownership or configuration", p.Job)
		}
		return nil
	}
	return err
}
func putSecret(ctx context.Context, edge kubernetes.Interface, namespace string, secret *corev1.Secret, owner string) error {
	existing, err := edge.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = edge.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, secret.Annotations[attemptIDAnnotation]) {
		return fmt.Errorf("secret %s is not owned by this installation", secret.Name)
	}
	existing.Data = secret.Data
	_, err = edge.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func attemptObjectMeta(name string, p *Plan) metav1.ObjectMeta {
	annotations := map[string]string{}
	if p.AttemptID != "" {
		annotations[attemptIDAnnotation] = p.AttemptID
	}
	return metav1.ObjectMeta{
		Name:        name,
		Labels:      map[string]string{ownerLabel: p.Owner, attemptLabel: p.Job},
		Annotations: annotations,
	}
}

func ownedByAttempt(obj metav1.Object, owner, attemptID string) bool {
	if obj == nil || obj.GetLabels()[ownerLabel] != owner {
		return false
	}
	return attemptID == "" || obj.GetAnnotations()[attemptIDAnnotation] == attemptID
}
func ensureClusterRole(ctx context.Context, local kubernetes.Interface, desired *rbacv1.ClusterRole, owner string) error {
	_, err := local.RbacV1().ClusterRoles().Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := local.RbacV1().ClusterRoles().Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, desired.Annotations[attemptIDAnnotation]) || existing.AggregationRule != nil || !apiequality.Semantic.DeepEqual(existing.Rules, desired.Rules) {
		return fmt.Errorf("installer ClusterRole %s is not owned by this installation or has unexpected permissions", desired.Name)
	}
	return nil
}

func ensureClusterRoleBinding(ctx context.Context, local kubernetes.Interface, desired *rbacv1.ClusterRoleBinding, owner string) error {
	_, err := local.RbacV1().ClusterRoleBindings().Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := local.RbacV1().ClusterRoleBindings().Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, desired.Annotations[attemptIDAnnotation]) || !apiequality.Semantic.DeepEqual(existing.RoleRef, desired.RoleRef) || !apiequality.Semantic.DeepEqual(existing.Subjects, desired.Subjects) {
		return fmt.Errorf("installer ClusterRoleBinding %s is not owned by this installation or has unexpected subjects", desired.Name)
	}
	return nil
}

func newPreparationID() (string, error) {
	return newRandomID("preparation")
}

func newInstallationID() (string, error) {
	return newRandomID("attempt")
}

func newRandomID(purpose string) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("creating installation %s ID: %w", purpose, err)
	}
	return hex.EncodeToString(id[:]), nil
}

func preparationLeaseActive(p *Plan) bool {
	return p != nil && p.PreparationID != "" && time.Now().Before(p.PreparationExpiresAt)
}

// assertPreparation fences a delayed hub replica before it can create the Job
// after another replica has claimed a later attempt.
func (i *Installer) assertPreparation(ctx context.Context, org, name string, p *Plan) error {
	current, _, err := i.readPlan(ctx, org, name)
	if err != nil {
		return err
	}
	if current == nil || current.Owner != p.Owner || current.Job != p.Job || current.AttemptID != p.AttemptID || current.PreparationID != p.PreparationID {
		return errPreparationSuperseded
	}
	return nil
}

func (i *Installer) releasePreparation(ctx context.Context, org, name string, p *Plan) error {
	cl, err := i.parent(org)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := cl.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annotations := ws.GetAnnotations()
		current := &Plan{}
		if err := json.Unmarshal([]byte(annotations[planAnnotation]), current); err != nil {
			return fmt.Errorf("invalid stored installation plan")
		}
		if string(ws.GetUID()) != p.Owner || current.Owner != p.Owner || current.Job != p.Job || current.AttemptID != p.AttemptID || current.PreparationID != p.PreparationID {
			return errPreparationSuperseded
		}
		current.PreparationID = ""
		current.PreparationExpiresAt = time.Time{}
		raw, err := json.Marshal(current)
		if err != nil {
			return err
		}
		annotations[planAnnotation] = string(raw)
		ws.SetAnnotations(annotations)
		_, err = cl.Update(ctx, ws, metav1.UpdateOptions{})
		return err
	})
}

func podMatchesAttempt(pod *corev1.Pod, job string) bool {
	return pod.Labels[attemptLabel] == job || pod.Labels["batch.kubernetes.io/job-name"] == job || pod.Labels["job-name"] == job
}

func hasActiveAttemptPod(ctx context.Context, edge kubernetes.Interface, p *Plan) (bool, error) {
	pods, err := edge.CoreV1().Pods(p.Namespace).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for idx := range pods.Items {
		pod := &pods.Items[idx]
		if podMatchesAttempt(pod, p.Job) && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return true, nil
		}
	}
	return false, nil
}

func jobBelongsToPlan(job *batchv1.Job, p *Plan) bool {
	if job == nil || p == nil || job.Labels[ownerLabel] != p.Owner || job.Labels[attemptLabel] != p.Job {
		return false
	}
	return p.AttemptID == "" || job.Annotations[attemptIDAnnotation] == p.AttemptID
}

func installerJobSpecMatches(got, want *batchv1.Job) bool {
	if got == nil || want == nil || got.Spec.ActiveDeadlineSeconds == nil || want.Spec.ActiveDeadlineSeconds == nil || *got.Spec.ActiveDeadlineSeconds != *want.Spec.ActiveDeadlineSeconds ||
		got.Spec.BackoffLimit == nil || want.Spec.BackoffLimit == nil || *got.Spec.BackoffLimit != *want.Spec.BackoffLimit {
		return false
	}
	gotPod, wantPod := got.Spec.Template.Spec, want.Spec.Template.Spec
	if gotPod.RestartPolicy != wantPod.RestartPolicy || gotPod.AutomountServiceAccountToken == nil || *gotPod.AutomountServiceAccountToken ||
		(gotPod.ServiceAccountName != "" && gotPod.ServiceAccountName != "default") || len(gotPod.InitContainers) != 0 || len(gotPod.Containers) != 1 || len(gotPod.Volumes) != len(wantPod.Volumes) {
		return false
	}
	gotContainer, wantContainer := gotPod.Containers[0], wantPod.Containers[0]
	if gotContainer.Name != wantContainer.Name || gotContainer.Image != wantContainer.Image ||
		!apiequality.Semantic.DeepEqual(gotContainer.Command, wantContainer.Command) ||
		!apiequality.Semantic.DeepEqual(gotContainer.Args, wantContainer.Args) ||
		!apiequality.Semantic.DeepEqual(gotContainer.Env, wantContainer.Env) ||
		!apiequality.Semantic.DeepEqual(gotContainer.VolumeMounts, wantContainer.VolumeMounts) ||
		!apiequality.Semantic.DeepEqual(gotContainer.SecurityContext, wantContainer.SecurityContext) {
		return false
	}
	for idx := range wantPod.Volumes {
		if !apiequality.Semantic.DeepEqual(gotPod.Volumes[idx], wantPod.Volumes[idx]) {
			return false
		}
	}
	return true
}

func deleteOwnedClusterRoleBinding(ctx context.Context, local kubernetes.Interface, name, owner, attemptID string) error {
	existing, err := local.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, attemptID) {
		return fmt.Errorf("installer ClusterRoleBinding %s is not owned by this installation", name)
	}
	options := metav1.DeleteOptions{}
	if existing.UID != "" {
		options.Preconditions = &metav1.Preconditions{UID: &existing.UID}
	}
	err = local.RbacV1().ClusterRoleBindings().Delete(ctx, name, options)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func deleteOwnedClusterRole(ctx context.Context, local kubernetes.Interface, name, owner, attemptID string) error {
	existing, err := local.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, attemptID) {
		return fmt.Errorf("installer ClusterRole %s is not owned by this installation", name)
	}
	options := metav1.DeleteOptions{}
	if existing.UID != "" {
		options.Preconditions = &metav1.Preconditions{UID: &existing.UID}
	}
	err = local.RbacV1().ClusterRoles().Delete(ctx, name, options)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func deleteOwnedServiceAccount(ctx context.Context, local kubernetes.Interface, name, owner, attemptID string) error {
	existing, err := local.CoreV1().ServiceAccounts("default").Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, attemptID) {
		return fmt.Errorf("installer ServiceAccount %s is not owned by this installation", name)
	}
	options := metav1.DeleteOptions{}
	if existing.UID != "" {
		options.Preconditions = &metav1.Preconditions{UID: &existing.UID}
	}
	err = local.CoreV1().ServiceAccounts("default").Delete(ctx, name, options)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func deleteOwnedSecret(ctx context.Context, edge kubernetes.Interface, namespace, name, owner, attemptID string) error {
	existing, err := edge.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ownedByAttempt(existing, owner, attemptID) {
		return fmt.Errorf("installer Secret %s is not owned by this installation", name)
	}
	options := metav1.DeleteOptions{}
	if existing.UID != "" {
		options.Preconditions = &metav1.Preconditions{UID: &existing.UID}
	}
	err = edge.CoreV1().Secrets(namespace).Delete(ctx, name, options)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func installerJob(p *Plan) *batchv1.Job {
	no, yes := false, true
	uid, deadline, backoff, mode := int64(1000), int64(600), int32(0), int32(0440)
	container := corev1.Container{
		Name: "helm", Image: installerImage, Command: []string{"helm"},
		Args: []string{"upgrade", "--install", p.Release, p.Chart, "--namespace", p.Namespace,
			"--version", p.Version, "--values", "/run/install/values.json", "--wait", "--atomic", "--timeout", "4m", "--history-max", "3"},
		Env: []corev1.EnvVar{
			{Name: "KUBECONFIG", Value: "/run/install/kubeconfig"}, {Name: "HOME", Value: "/tmp"},
			{Name: "HELM_CACHE_HOME", Value: "/tmp/cache"}, {Name: "HELM_CONFIG_HOME", Value: "/tmp/config"}, {Name: "HELM_DATA_HOME", Value: "/tmp/data"},
		},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/run/install", ReadOnly: true}, {Name: "tmp", MountPath: "/tmp"}},
	}
	return &batchv1.Job{
		ObjectMeta: attemptObjectMeta(p.Job, p),
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: attemptObjectMeta("", p),
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no,
					SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers:      []corev1.Container{container},
					Volumes: []corev1.Volume{
						{Name: "config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: runnerSecret(p), DefaultMode: &mode}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

// Cleanup removes the expired runner's remaining execution identity after a
// terminal result is observed. The provider credential remains for its runtime.
func (i *Installer) Cleanup(ctx context.Context, org, name, job string) {
	if err := i.cleanupAttempt(ctx, org, name, job); err != nil {
		klog.FromContext(ctx).Error(err, "failed to clean up provider installer credentials", "org", org, "provider", name, "job", job)
	}
}

func (i *Installer) cleanupAttempt(ctx context.Context, org, name, job string) error {
	p, _, err := i.readPlan(ctx, org, name)
	if err != nil || p == nil || p.Job != job {
		return err
	}
	// A cleanup call can be delayed or arrive after a same-name Job was
	// recreated. Never revoke credentials while this attempt is still being
	// prepared, has a live Job, or has a live orphan Pod.
	if preparationLeaseActive(p) {
		return nil
	}
	edge, local, _, err := i.edgeClients(ctx, org, name)
	if err != nil {
		return err
	}
	return cleanupAttemptResources(ctx, edge, local, p)
}

func cleanupResources(ctx context.Context, edge, local kubernetes.Interface, p *Plan) error {
	identity := "railgrid-" + p.Job
	var errs []error
	if err := deleteOwnedClusterRoleBinding(ctx, local, identity, p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	if err := deleteOwnedClusterRole(ctx, local, identity, p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	if err := deleteOwnedServiceAccount(ctx, local, identity, p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	if err := deleteOwnedSecret(ctx, edge, p.Namespace, runnerSecret(p), p.Owner, p.AttemptID); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func cleanupAttemptResources(ctx context.Context, edge, local kubernetes.Interface, p *Plan) error {
	job, err := edge.BatchV1().Jobs(p.Namespace).Get(ctx, p.Job, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !jobBelongsToPlan(job, p) {
			// A name collision is not our Job. Revoke only the credentials and
			// RBAC bearing this attempt's unguessable ID so that a colliding Job
			// cannot use them; never delete the Job itself.
			return cleanupResources(ctx, edge, local, p)
		}
		if job.Status.Active > 0 || !jobTerminal(job) {
			return nil
		}
	}
	active, err := hasActiveAttemptPod(ctx, edge, p)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	return cleanupResources(ctx, edge, local, p)
}

func jobTerminal(job *batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) {
			return true
		}
	}
	return false
}

func (i *Installer) Target(ctx context.Context, org, name string) (*kcp.EdgeRoute, error) {
	return i.workspaces.GetProviderEdgeRoute(ctx, org, name)
}
