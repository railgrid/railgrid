package site

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

const (
	rbacControllerName = "site-rbac"
	// Namespace where site credentials are stored.
	siteNamespace = "kedge-system"
)

// RBACController watches Sites and creates dedicated credentials (token + kubeconfig)
// so each site agent authenticates with its own identity.
type RBACController struct {
	kedgeClient    *kedgeclient.Client
	kubeClient     kubernetes.Interface
	queue          workqueue.TypedRateLimitingInterface[string]
	siteInformer   cache.SharedIndexInformer
	hubExternalURL string
}

// NewRBACController creates a new site RBAC controller.
func NewRBACController(
	kedgeClient *kedgeclient.Client,
	kubeClient kubernetes.Interface,
	factory *kedgeclient.InformerFactory,
	hubExternalURL string,
) *RBACController {
	c := &RBACController{
		kedgeClient:    kedgeClient,
		kubeClient:     kubeClient,
		hubExternalURL: hubExternalURL,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: rbacControllerName},
		),
		siteInformer: factory.Sites(),
	}

	c.siteInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c
}

func (c *RBACController) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

// Run starts the RBAC controller.
func (c *RBACController) Run(ctx context.Context) error {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx).WithName(rbacControllerName)
	logger.Info("Starting site RBAC controller")

	// Ensure the namespace exists.
	if err := c.ensureNamespace(ctx); err != nil {
		return fmt.Errorf("ensuring namespace %s: %w", siteNamespace, err)
	}

	for i := 0; i < 2; i++ {
		go wait.UntilWithContext(ctx, c.worker, time.Second)
	}

	<-ctx.Done()
	logger.Info("Shutting down site RBAC controller")
	return nil
}

func (c *RBACController) worker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *RBACController) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	if err := c.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("reconciling %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *RBACController) reconcile(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("key", key)

	obj, exists, err := c.siteInformer.GetStore().GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	site, err := convertToSite(obj)
	if err != nil {
		return fmt.Errorf("converting to Site: %w", err)
	}

	siteName := site.Name
	secretName := "site-" + siteName
	secretRef := siteNamespace + "/" + secretName

	// Skip if credentials already provisioned.
	if site.Status.CredentialsSecretRef == secretRef {
		logger.V(4).Info("Credentials already provisioned", "site", siteName)
		return nil
	}

	logger.Info("Provisioning credentials for site", "site", siteName)

	// Generate a random token for this site.
	token, err := generateRandomToken(32)
	if err != nil {
		return fmt.Errorf("generating token: %w", err)
	}

	// Create or update the kubeconfig Secret.
	if err := c.ensureCredentialsSecret(ctx, secretName, siteName, token); err != nil {
		return fmt.Errorf("ensuring credentials secret: %w", err)
	}

	// Update Site status with the secret reference.
	logger.Info("Updating site credentials reference", "site", siteName, "secret", secretRef)
	patch := fmt.Sprintf(`{"status":{"credentialsSecretRef":"%s"}}`, secretRef)
	if _, err := c.kedgeClient.Sites().Patch(ctx, siteName, types.MergePatchType,
		[]byte(patch), metav1.PatchOptions{}, "status"); err != nil {
		return fmt.Errorf("patching site status: %w", err)
	}

	logger.Info("Site credentials provisioned", "site", siteName, "secret", secretRef)
	return nil
}

func (c *RBACController) ensureNamespace(ctx context.Context) error {
	_, err := c.kubeClient.CoreV1().Namespaces().Get(ctx, siteNamespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = c.kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: siteNamespace},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (c *RBACController) ensureCredentialsSecret(ctx context.Context, name, siteName, token string) error {
	// Build kubeconfig pointing to the hub.
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"kedge": {
				Server:                c.hubExternalURL,
				InsecureSkipTLSVerify: true,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"site-agent": {
				Token: token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"kedge": {
				Cluster:  "kedge",
				AuthInfo: "site-agent",
			},
		},
		CurrentContext: "kedge",
	}

	kubeconfigBytes, err := yaml.Marshal(kubeconfig)
	if err != nil {
		return fmt.Errorf("marshaling kubeconfig: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: siteNamespace,
			Labels: map[string]string{
				"kedge.faros.sh/site": siteName,
			},
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfigBytes,
			"token":      []byte(token),
			"server":     []byte(c.hubExternalURL),
		},
	}

	existing, err := c.kubeClient.CoreV1().Secrets(siteNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.kubeClient.CoreV1().Secrets(siteNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}

	// Secret exists — don't regenerate token, keep existing credentials.
	if len(existing.Data["token"]) > 0 {
		return nil
	}

	// Token is empty/missing — update with new credentials.
	existing.Data = secret.Data
	_, err = c.kubeClient.CoreV1().Secrets(siteNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func generateRandomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func convertToSite(obj interface{}) (*kedgev1alpha1.Site, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("expected *unstructured.Unstructured, got %T", obj)
	}
	var site kedgev1alpha1.Site
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &site); err != nil {
		return nil, err
	}
	return &site, nil
}
