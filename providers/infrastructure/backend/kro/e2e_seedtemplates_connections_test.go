//go:build e2e

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

package kro

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// TestE2ESeedTemplatesConnections proves, against real kro, what the unit
// tests can only assert about the authored RGD: kro defaults the nested
// connections object from its children's "" defaults (an instance that omits
// it still renders), and the name/optional CEL expressions evaluate to the
// string/bool the pod spec requires — for both an unconnected and a connected
// instance.
func TestE2ESeedTemplatesConnections(t *testing.T) {
	dyn, _ := e2eClients(t)
	runID := fmt.Sprintf("%016x", time.Now().UnixNano())

	for _, tc := range connectionsCases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", tc.file))
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			tmpl := decodeTemplate(t, raw)
			rgd, err := buildRGD(tmpl, testTokens())
			if err != nil {
				t.Fatalf("buildRGD: %v", err)
			}
			applyRGD(t, dyn, rgd)
			t.Cleanup(func() { _ = dyn.Resource(rgdGVR).Delete(context.Background(), rgd.GetName(), metav1.DeleteOptions{}) })
			if status, msg := waitGraphAccepted(t, dyn, rgd.GetName()); status != "True" {
				t.Fatalf("kro rejected RGD: GraphAccepted=%s: %s", status, msg)
			}
			instGVR := schema.GroupVersionResource{
				Group:    tmpl.Spec.InstanceCRD.Group,
				Version:  tmpl.Spec.InstanceCRD.Version,
				Resource: tmpl.Spec.InstanceCRD.Resource,
			}

			for _, variant := range []struct {
				suffix      string
				connections map[string]any
				wantDB      map[string]any
				wantCache   map[string]any
			}{
				{
					// sampleValues omit connections entirely.
					suffix:    "u",
					wantDB:    map[string]any{"key": "uri", "optional": true},
					wantCache: map[string]any{"key": "uri", "optional": true},
				},
				{
					suffix:      "c",
					connections: map[string]any{"database": "e2e-db", "cache": "e2e-cache"},
					wantDB:      map[string]any{"name": "e2e-db-db-credentials", "key": "uri", "optional": false},
					wantCache:   map[string]any{"name": "e2e-cache-credentials", "key": "uri", "optional": false},
				},
			} {
				inst := e2eInstance(t, tmpl, runID)
				name := inst.GetName() + "-" + variant.suffix
				inst.SetName(name)
				spec, _ := inst.Object["spec"].(map[string]any)
				spec["name"] = name
				if variant.connections != nil {
					spec["connections"] = variant.connections
				}
				if variant.wantDB["name"] == nil {
					variant.wantDB["name"] = name + "-unbound-db"
					variant.wantCache["name"] = name + "-unbound-cache"
				}

				createInstance(t, dyn, instGVR, inst)
				t.Cleanup(func() {
					_ = dyn.Resource(instGVR).Namespace(e2eInstanceNamespace).Delete(context.Background(), name, metav1.DeleteOptions{})
				})
				created, err := dyn.Resource(instGVR).Namespace(e2eInstanceNamespace).Get(context.Background(), name, metav1.GetOptions{})
				if err != nil {
					t.Fatalf("get instance %q: %v", name, err)
				}
				simulateGatewayController(t, dyn, string(created.GetUID()))
				waitInstanceApplied(t, dyn, instGVR, name, tmpl.Name)

				env := waitWorkloadEnv(t, dyn, rgd, tc, string(created.GetUID()))
				for envName, want := range map[string]map[string]any{"DATABASE_URL": variant.wantDB, "REDIS_URL": variant.wantCache} {
					ref, _, _ := unstructured.NestedMap(env[envName], "valueFrom", "secretKeyRef")
					if !reflect.DeepEqual(ref, want) {
						t.Errorf("instance %q env %s secretKeyRef = %#v, want %#v", name, envName, ref, want)
					}
				}
				if tc.file == "simple-webapp.yaml" {
					if got, _, _ := unstructured.NestedString(env["PORT"], "value"); got != "80" {
						t.Errorf("instance %q PORT = %q, want the sample port 80", name, got)
					}
				}
			}
		})
	}
}

// waitWorkloadEnv returns the materialized workload container's env, keyed by
// name, once kro has created the child object.
func waitWorkloadEnv(t *testing.T, dyn dynamic.Interface, rgd *unstructured.Unstructured, tc connectionsCase, instanceUID string) map[string]map[string]any {
	t.Helper()
	res := findResource(t, rgd, tc.workloadID)
	apiVersion, _, _ := unstructured.NestedString(res, "template", "apiVersion")
	kind, _, _ := unstructured.NestedString(res, "template", "kind")
	gv, _ := schema.ParseGroupVersion(apiVersion)
	resource := map[string]string{"Deployment": "deployments", "CronJob": "cronjobs"}[kind]
	gvr := gv.WithResource(resource)
	selector := kroInstanceIDLabel + "=" + instanceUID + "," + kroNodeIDLabel + "=" + tc.workloadID

	deadline := time.Now().Add(e2eInstanceWait)
	for time.Now().Before(deadline) {
		list, err := dyn.Resource(gvr).Namespace(e2eInstanceNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err == nil && len(list.Items) == 1 {
			containers, _, _ := unstructured.NestedSlice(list.Items[0].Object, append(tc.podSpecPath, "containers")...)
			for _, raw := range containers {
				c, _ := raw.(map[string]any)
				if c["name"] != tc.container {
					continue
				}
				out := map[string]map[string]any{}
				env, _ := c["env"].([]any)
				for _, e := range env {
					entry, _ := e.(map[string]any)
					n, _ := entry["name"].(string)
					out[n] = entry
				}
				return out
			}
		}
		time.Sleep(e2ePollEvery)
	}
	t.Fatalf("kro never created %s %q for instance %s", kind, tc.workloadID, instanceUID)
	return nil
}
