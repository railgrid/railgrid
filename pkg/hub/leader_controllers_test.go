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

package hub

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	railgridv1alpha1 "github.com/railgrid/railgrid/apis/railgrid/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/controllers/mcpserver"
	"github.com/railgrid/railgrid/pkg/hub/controllers/membershipindex"
	"github.com/railgrid/railgrid/pkg/hub/controllers/organization"
	"github.com/railgrid/railgrid/pkg/hub/controllers/softdelete"
	"github.com/railgrid/railgrid/pkg/hub/leaderelection"
)

func TestSingleClusterLeaderManagersCanRebuildControllers(t *testing.T) {
	for name, factory := range map[string]func(*rest.Config, *runtime.Scheme) (manager.Manager, error){
		"organization":    organization.NewManager,
		"softdelete":      softdelete.NewManager,
		"membershipindex": membershipindex.NewManager,
	} {
		t.Run(name, func(t *testing.T) {
			for term := 0; term < 2; term++ {
				// Construction needs no API calls. An unreachable endpoint makes accidental
				// cluster access fail instead of using the host's kubeconfig.
				mgr, err := factory(&rest.Config{Host: "https://127.0.0.1:1"}, runtime.NewScheme())
				if err != nil {
					t.Fatal(err)
				}
				_, err = controller.New("test-leader-"+name, mgr, controller.Options{Reconciler: reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) { return reconcile.Result{}, nil })})
				if err != nil {
					t.Fatalf("term %d: %v", term+1, err)
				}
			}
		})
	}
}

func TestMCPControllerCanBeRegisteredInSuccessiveTerms(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := railgridv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cfg := &rest.Config{Host: "https://127.0.0.1:1"}
	for term := 0; term < 2; term++ {
		mgr, err := mcmanager.New(cfg, nil, manager.Options{
			Scheme:     scheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: leaderelection.ControllerOptions(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := mcpserver.SetupWithManager(mgr, cfg, "https://hub.invalid", nil, nil); err != nil {
			t.Fatalf("term %d: %v", term+1, err)
		}
	}
}
