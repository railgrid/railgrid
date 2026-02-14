package site

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const (
	// HeartbeatTimeout is the duration after which a site is considered disconnected.
	HeartbeatTimeout = 5 * time.Minute
	// GCTimeout is the duration after which a disconnected site is garbage collected.
	GCTimeout = 24 * time.Hour
)

// Controller manages Site lifecycle (heartbeat monitoring, GC).
type Controller struct {
	client          *kedgeclient.Client
	informerFactory *kedgeclient.InformerFactory
	siteInformer    cache.SharedIndexInformer
}

// NewController creates a new site lifecycle controller.
func NewController(client *kedgeclient.Client, factory *kedgeclient.InformerFactory) *Controller {
	return &Controller{
		client:          client,
		informerFactory: factory,
		siteInformer:    factory.Sites(),
	}
}

// Run starts the site lifecycle controller.
func (c *Controller) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("site-controller")
	logger.Info("Starting site lifecycle controller")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Shutting down site lifecycle controller")
			return nil
		case <-ticker.C:
			c.checkHeartbeats(ctx, logger)
		}
	}
}

func (c *Controller) checkHeartbeats(ctx context.Context, logger klog.Logger) {
	now := time.Now()

	for _, obj := range c.siteInformer.GetStore().List() {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		var site kedgev1alpha1.Site
		data, err := json.Marshal(u.Object)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &site); err != nil {
			continue
		}

		if site.Status.LastHeartbeatTime == nil {
			continue
		}

		elapsed := now.Sub(site.Status.LastHeartbeatTime.Time)

		if elapsed > HeartbeatTimeout && site.Status.Phase != kedgev1alpha1.SitePhaseDisconnected {
			logger.Info("Site heartbeat stale, marking disconnected",
				"site", site.Name,
				"elapsed", elapsed,
			)
			patch := fmt.Sprintf(`{"status":{"phase":"%s","tunnelConnected":false}}`, kedgev1alpha1.SitePhaseDisconnected)
			_, err := c.client.Sites().Patch(ctx, site.Name, types.MergePatchType,
				[]byte(patch), metav1.PatchOptions{}, "status")
			if err != nil {
				logger.Error(err, "Failed to update site status", "site", site.Name)
			}
		}
	}
}
