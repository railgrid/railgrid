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

package metering

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/klog/v2"
)

// discoveryFixture is a Discovery over a fake clientset whose ConfigMap read
// the test steers: present, absent, or failing.
type discoveryFixture struct {
	d   *Discovery
	err error
}

func newDiscoveryFixture(t *testing.T, objs ...runtime.Object) *discoveryFixture {
	t.Helper()
	client := kubefake.NewClientset(objs...)
	f := &discoveryFixture{d: &Discovery{client: client, workspace: "root:railgrid:system:metering", logger: klog.Background()}}
	client.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if f.err != nil {
			return true, nil, f.err
		}
		return false, nil, nil
	})
	return f
}

func endpointsConfigMapObject() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceDefault, Name: endpointsConfigMap},
		Data:       map[string]string{"portalURL": "http://metering/"},
	}
}

func expectInstalled(t *testing.T, d *Discovery, wantInstalled, wantKnown bool) {
	t.Helper()
	installed, known := d.Installed()
	if installed != wantInstalled || known != wantKnown {
		t.Fatalf("Installed() = (%v, %v), want (%v, %v)", installed, known, wantInstalled, wantKnown)
	}
	if _, ok := d.Current(); ok != wantInstalled {
		t.Fatalf("Current() ok = %v, want %v", ok, wantInstalled)
	}
}

// Before the first poll nothing is known; a definite answer from kcp, in
// either direction, is.
func TestDiscoveryKnowsNothingUntilItHasAsked(t *testing.T) {
	f := newDiscoveryFixture(t)
	expectInstalled(t, f.d, false, false)

	f.d.refresh(context.Background())
	expectInstalled(t, f.d, false, true)

	g := newDiscoveryFixture(t, endpointsConfigMapObject())
	g.d.refresh(context.Background())
	expectInstalled(t, g.d, true, true)
}

// A poll that fails for a reason other than "not there" keeps the last
// answer: an installed deployment stays installed through a kcp hiccup, so
// the gates that read it (the free-organizations cap, plan assignment) do
// not open on a transient error; and a hub that has never reached kcp stays
// unknown rather than reporting "not installed".
func TestDiscoveryKeepsTheLastAnswerOnATransientError(t *testing.T) {
	f := newDiscoveryFixture(t, endpointsConfigMapObject())
	f.d.refresh(context.Background())
	expectInstalled(t, f.d, true, true)

	f.err = errors.New("etcdserver: request timed out")
	f.d.refresh(context.Background())
	expectInstalled(t, f.d, true, true)

	// kcp says it is gone: that is definite.
	f.err = apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, endpointsConfigMap)
	f.d.refresh(context.Background())
	expectInstalled(t, f.d, false, true)

	// And a failure before any answer leaves the state unknown.
	g := newDiscoveryFixture(t)
	g.err = errors.New("dial tcp: connection refused")
	g.d.refresh(context.Background())
	expectInstalled(t, g.d, false, false)
}
