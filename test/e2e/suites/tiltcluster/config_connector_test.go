/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tiltcluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// Shared infrastructure-operator API surfaces. Config Connector and
	// Terraform use the same provider publication, tenant binding, and runtime
	// composition path; backend-specific resources remain declared separately.
	infrastructureProviderGVR = schema.GroupVersionResource{
		Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "infrastructureproviders",
	}
	workspaceGVR = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	apiBindingGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings",
	}
	customResourceDefinitionGVR = schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}
	resourceGraphDefinitionGVR = schema.GroupVersionResource{
		Group: "kro.run", Version: "v1alpha1", Resource: "resourcegraphdefinitions",
	}
	configConnectorStorageBucketGVR = schema.GroupVersionResource{
		Group: "storage.cnrm.cloud.google.com", Version: "v1beta1", Resource: "storagebuckets",
	}
	infrastructureTemplateGVR = schema.GroupVersionResource{
		Group: infraGroup, Version: "v1alpha1", Resource: "templates",
	}
)

const (
	infrastructureOperatorNamespace  = "faros-infrastructure-operator"
	infrastructureOperatorName       = "infrastructure"
	configConnectorTemplatePrefix    = "gcs-bucket-kcc-demo-"
	configConnectorWorkspacePrefix   = "e2e-gcs-"
	configConnectorStorageBucketNode = "storageBucket"
	configConnectorTestLabel         = "faros.sh/e2e-config-connector"
	configConnectorTestLabelValue    = "infra-operator-kcc-demo"

	infrastructureOperatorWait = 3 * time.Minute
	infrastructureOperatorPoll = 2 * time.Second
)

const (
	kroInstanceIDLabel = "kro.run/instance-id"
	kroNodeIDLabel     = "kro.run/node-id"
)

// TestConfigConnectorComposition demonstrates how the infrastructure operator
// can publish a Template whose KRO graph creates a Config Connector CR in the
// operator-managed Tilt runtime:
//
//	InfrastructureProvider Ready -> test-owned StorageBucket CRD -> Template
//	Ready -> KRO GraphAccepted -> isolated tenant/APIExport binding ->
//	GCSBucket instance -> KRO-labeled StorageBucket child.
//
// The StorageBucket CRD is deliberately minimal and no Config Connector
// controller is installed. This proves only that KRO composes the expected CR
// with the expected fields; it does not contact GCP or reconcile a bucket.
func TestConfigConnectorComposition(t *testing.T) {
	if os.Getenv("FAROS_E2E_CONFIG_CONNECTOR_COMPOSITION") != "1" {
		t.Skip("run only through make e2e-tilt-cluster-config-connector")
	}
	requireStack(t)
	runtimeClient := infrastructureRuntimeClient(t)
	waitInfrastructureProviderReady(t, runtimeClient)

	crdOwned := ensureStorageBucketCRD(t, runtimeClient)
	if crdOwned {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := runtimeClient.Resource(customResourceDefinitionGVR).Delete(ctx, "storagebuckets.storage.cnrm.cloud.google.com", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup StorageBucket CRD: %v", err)
			}
			cancel()
			waitTiltResourceGone(t, runtimeClient.Resource(customResourceDefinitionGVR), "storagebuckets.storage.cnrm.cloud.google.com", 30*time.Second)
		})
	}

	templateName := configConnectorTemplatePrefix + shortNonce()
	providerClient := kcpAdminDynamic(t, providerWorkspace)
	tmpl := configConnectorTemplate(templateName)
	if _, err := providerClient.Resource(infrastructureTemplateGVR).Create(context.Background(), tmpl, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create test Template %q: %v", templateName, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := providerClient.Resource(infrastructureTemplateGVR).Delete(ctx, templateName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup Template %q: %v", templateName, err)
		}
		cancel()
		waitTiltResourceGone(t, providerClient.Resource(infrastructureTemplateGVR), templateName, 60*time.Second)
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if err := runtimeClient.Resource(resourceGraphDefinitionGVR).Delete(ctx, templateName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup ResourceGraphDefinition %q: %v", templateName, err)
		}
		cancel()
		waitTiltResourceGone(t, runtimeClient.Resource(resourceGraphDefinitionGVR), templateName, 60*time.Second)
	})

	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, err := providerClient.Resource(infrastructureTemplateGVR).Get(context.Background(), templateName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		status, reason, message := conditionState(got.Object, "Ready")
		return status == "True", fmt.Sprintf("Ready=%s reason=%s message=%s", status, reason, message)
	}) {
		t.Fatalf("test Template %q never became Ready", templateName)
	}

	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		export, err := providerClient.Resource(apiExportGVR).Get(context.Background(), infraAPIExportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if apiExportHasResource(export.Object, "gcsbuckets", infraGroup) {
			return true, ""
		}
		return false, "APIExport does not list gcsbuckets yet"
	}) {
		t.Fatalf("infrastructure APIExport never listed test resource gcsbuckets")
	}

	if status, message := waitRGDGraphAccepted(t, runtimeClient, templateName); status != "True" {
		t.Fatalf("test Template RGD %q was not GraphAccepted: status=%s message=%s", templateName, status, message)
	}

	workspaceName := configConnectorWorkspacePrefix + shortNonce()
	parentClient := kcpAdminDynamic(t, "root:faros")
	workspacePath := createConfigConnectorWorkspace(t, parentClient, workspaceName)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := parentClient.Resource(workspaceGVR).Delete(ctx, workspaceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup workspace %q: %v", workspaceName, err)
		}
		waitTiltResourceGone(t, parentClient.Resource(workspaceGVR), workspaceName, 60*time.Second)
	})

	tenantClient := kcpAdminDynamic(t, workspacePath)
	binding := infrastructureAPIBinding()
	if _, err := tenantClient.Resource(apiBindingGVR).Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create infrastructure APIBinding in %s: %v", workspacePath, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tenantClient.Resource(apiBindingGVR).Delete(ctx, "infrastructure", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup infrastructure APIBinding in %s: %v", workspacePath, err)
		}
		waitTiltResourceGone(t, tenantClient.Resource(apiBindingGVR), "infrastructure", 30*time.Second)
	})
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, err := tenantClient.Resource(apiBindingGVR).Get(context.Background(), "infrastructure", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatalf("infrastructure APIBinding in %s never reached Bound", workspacePath)
	}

	instanceName := "gcs-bucket-" + shortNonce()
	instanceGVR := schema.GroupVersionResource{Group: infraGroup, Version: "v1alpha1", Resource: "gcsbuckets"}
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": infraGroup + "/v1alpha1",
		"kind":       "GCSBucket",
		"metadata":   map[string]any{"name": instanceName},
		"spec": map[string]any{
			"name":                     instanceName,
			"location":                 "US",
			"uniformBucketLevelAccess": true,
		},
	}}
	var createdInstance *unstructured.Unstructured
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		created, err := tenantClient.Resource(instanceGVR).Create(context.Background(), instance.DeepCopy(), metav1.CreateOptions{})
		if err == nil {
			createdInstance = created
			return true, ""
		}
		if apierrors.IsAlreadyExists(err) {
			created, getErr := tenantClient.Resource(instanceGVR).Get(context.Background(), instanceName, metav1.GetOptions{})
			if getErr == nil {
				createdInstance = created
				return true, "already exists"
			}
			return false, getErr.Error()
		}
		return false, err.Error()
	}) {
		t.Fatalf("create GCSBucket %q in %s: API not served after binding", instanceName, workspacePath)
	}

	var instanceUID string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tenantClient.Resource(instanceGVR).Delete(ctx, instanceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup GCSBucket %q: %v", instanceName, err)
		}
		if instanceUID != "" {
			deleteRuntimeStorageBucketChildren(t, runtimeClient, instanceUID)
		}
	})
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, err := tenantClient.Resource(instanceGVR).Get(context.Background(), instanceName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		instanceUID = string(got.GetUID())
		if instanceUID == "" {
			return false, "GCSBucket UID not assigned yet"
		}
		return true, ""
	}) {
		t.Fatalf("GCSBucket %q never received a UID (create response=%v)", instanceName, createdInstance)
	}

	var child *unstructured.Unstructured
	selector := fmt.Sprintf("%s=%s,%s=%s", kroInstanceIDLabel, instanceUID, kroNodeIDLabel, configConnectorStorageBucketNode)
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		items, err := runtimeClient.Resource(configConnectorStorageBucketGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err.Error()
		}
		if len(items.Items) == 0 {
			return false, "no KRO-labeled StorageBucket child yet"
		}
		child = items.Items[0].DeepCopy()
		return true, fmt.Sprintf("found %s/%s", child.GetNamespace(), child.GetName())
	}) {
		t.Fatalf("KRO never created a StorageBucket child for GCSBucket %q", instanceName)
	}

	if got := child.GetKind(); got != "StorageBucket" {
		t.Fatalf("runtime child kind = %q, want StorageBucket", got)
	}
	if got := child.GetLabels()[kroInstanceIDLabel]; got != instanceUID {
		t.Fatalf("runtime child %s instance-id label = %q, want %q", child.GetName(), got, instanceUID)
	}
	if got := child.GetLabels()[kroNodeIDLabel]; got != configConnectorStorageBucketNode {
		t.Fatalf("runtime child %s node-id label = %q, want %q", child.GetName(), got, configConnectorStorageBucketNode)
	}
	if got, found, err := unstructured.NestedString(child.Object, "spec", "location"); err != nil || !found || got != "US" {
		t.Fatalf("runtime StorageBucket %s spec.location = %q (found=%t err=%v), want US", child.GetName(), got, found, err)
	}
	if got, found, err := unstructured.NestedBool(child.Object, "spec", "uniformBucketLevelAccess"); err != nil || !found || !got {
		t.Fatalf("runtime StorageBucket %s spec.uniformBucketLevelAccess = %t (found=%t err=%v), want true", child.GetName(), got, found, err)
	}
	t.Logf("KRO composed StorageBucket %s/%s from tenant GCSBucket %q; no Config Connector/GCP reconciliation was exercised", child.GetNamespace(), child.GetName(), instanceName)
}

func infrastructureRuntimeClient(t *testing.T) dynamic.Interface {
	t.Helper()
	path := envOr("FAROS_E2E_TILT_RUNTIME_KUBECONFIG", filepath.Join(repoRoot, ".faros-cluster.kubeconfig"))
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Skipf("runtime kubeconfig %q is absent; start `make tilt-cluster` first", path)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Skipf("runtime kubeconfig %q is not usable: %v", path, err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Skipf("runtime client from %q is unavailable: %v", path, err)
	}
	return client
}

func waitInfrastructureProviderReady(t *testing.T, runtimeClient dynamic.Interface) {
	t.Helper()
	namespace := envOr("FAROS_E2E_TILT_OPERATOR_NAMESPACE", infrastructureOperatorNamespace)
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		provider, err := runtimeClient.Resource(infrastructureProviderGVR).Namespace(namespace).Get(context.Background(), infrastructureOperatorName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(provider.Object, "status", "phase")
		for _, condition := range []string{"Bootstrapped", "KroReleased", "ProviderDeployed", "Registered"} {
			status, reason, message := conditionState(provider.Object, condition)
			if status != "True" {
				return false, fmt.Sprintf("phase=%s %s=%s reason=%s message=%s", phase, condition, status, reason, message)
			}
		}
		return phase == "Ready", "phase=" + phase
	}) {
		t.Fatalf("InfrastructureProvider %s/%s never reached Ready with lifecycle conditions true", namespace, infrastructureOperatorName)
	}
	t.Logf("InfrastructureProvider %s/%s lifecycle ready", namespace, infrastructureOperatorName)
}

// ensureStorageBucketCRD creates only the minimal API surface KRO needs to
// apply its child. A real Config Connector CRD is intentionally not modified;
// the test skips in that case so it cannot accidentally invoke cloud behavior.
func ensureStorageBucketCRD(t *testing.T, runtimeClient dynamic.Interface) bool {
	t.Helper()
	const name = "storagebuckets.storage.cnrm.cloud.google.com"
	existing, err := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err == nil {
		if existing.GetLabels()[configConnectorTestLabel] != configConnectorTestLabelValue {
			t.Skipf("StorageBucket CRD %q already exists; refusing to replace a non-test-owned Config Connector surface", name)
		}
		if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
			got, getErr := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), name, metav1.GetOptions{})
			return crdEstablished(got), fmt.Sprintf("get=%v", getErr)
		}) {
			t.Fatalf("test-owned StorageBucket CRD %q is not Established", name)
		}
		return true
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get StorageBucket CRD %q: %v", name, err)
	}

	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				configConnectorTestLabel: configConnectorTestLabelValue,
			},
		},
		"spec": map[string]any{
			"group": "storage.cnrm.cloud.google.com",
			"names": map[string]any{
				"kind":       "StorageBucket",
				"listKind":   "StorageBucketList",
				"plural":     "storagebuckets",
				"singular":   "storagebucket",
				"shortNames": []any{"gcpstoragebucket"},
			},
			"scope": "Namespaced",
			"versions": []any{map[string]any{
				"name":    "v1beta1",
				"served":  true,
				"storage": true,
				"schema": map[string]any{
					"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"location":                 map[string]any{"type": "string"},
									"uniformBucketLevelAccess": map[string]any{"type": "boolean"},
								},
							},
							"status": map[string]any{
								"type":                                 "object",
								"x-kubernetes-preserve-unknown-fields": true,
							},
						},
					},
				},
				"subresources": map[string]any{"status": map[string]any{}},
			}},
		},
	}}
	if _, err := runtimeClient.Resource(customResourceDefinitionGVR).Create(context.Background(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create test-owned StorageBucket CRD: %v", err)
	}
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, getErr := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), name, metav1.GetOptions{})
		if getErr != nil {
			return false, getErr.Error()
		}
		if crdEstablished(got) {
			return true, ""
		}
		return false, "Established condition is not True"
	}) {
		t.Fatalf("test-owned StorageBucket CRD %q never became Established", name)
	}
	return true
}

func crdEstablished(crd *unstructured.Unstructured) bool {
	if crd == nil {
		return false
	}
	status, _, _ := conditionState(crd.Object, "Established")
	return status == "True"
}

func configConnectorTemplate(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": infraGroup + "/v1alpha1",
		"kind":       "Template",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				configConnectorTestLabel: configConnectorTestLabelValue,
			},
		},
		"spec": map[string]any{
			"displayName": "GCS bucket Config Connector composition demo",
			"description": "Test-only infrastructure operator composition from a tenant GCSBucket to a Config Connector StorageBucket CR.",
			"category":    "Test",
			"version":     "0.0.1",
			"backend":     "kro",
			"instanceCRD": map[string]any{
				"group":    infraGroup,
				"version":  "v1alpha1",
				"resource": "gcsbuckets",
				"kind":     "GCSBucket",
			},
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":                     map[string]any{"type": "string"},
					"location":                 map[string]any{"type": "string"},
					"uniformBucketLevelAccess": map[string]any{"type": "boolean"},
				},
				"required": []any{"name", "location", "uniformBucketLevelAccess"},
			},
			"backendConfig": map[string]any{
				"resources": []any{map[string]any{
					"id": configConnectorStorageBucketNode,
					"template": map[string]any{
						"apiVersion": "storage.cnrm.cloud.google.com/v1beta1",
						"kind":       "StorageBucket",
						"metadata": map[string]any{
							"name":      "${schema.spec.name}",
							"namespace": "default",
						},
						"spec": map[string]any{
							"location":                 "${schema.spec.location}",
							"uniformBucketLevelAccess": "${schema.spec.uniformBucketLevelAccess}",
						},
					},
				}},
			},
		},
	}}
}

func infrastructureAPIBinding() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "infrastructure"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"path": providerWorkspace,
					"name": infraAPIExportName,
				},
			},
		},
	}}
}

func createConfigConnectorWorkspace(t *testing.T, parent dynamic.Interface, name string) string {
	t.Helper()
	workspace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				configConnectorTestLabel: configConnectorTestLabelValue,
			},
		},
	}}
	if _, err := parent.Resource(workspaceGVR).Create(context.Background(), workspace, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create isolated workspace %q: %v", name, err)
	}
	var path string
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, err := parent.Resource(workspaceGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		path, _, _ = unstructured.NestedString(got.Object, "spec", "cluster")
		return phase == "Ready" && path != "", fmt.Sprintf("phase=%s cluster=%s", phase, path)
	}) {
		t.Fatalf("isolated workspace %q never became Ready", name)
	}
	return path
}

func waitRGDGraphAccepted(t *testing.T, runtimeClient dynamic.Interface, name string) (string, string) {
	t.Helper()
	var status, message string
	waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, err := runtimeClient.Resource(resourceGraphDefinitionGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		status, _, message = conditionState(got.Object, "GraphAccepted")
		if status == "True" || status == "False" {
			return true, message
		}
		return false, "GraphAccepted condition not reported yet"
	})
	return status, message
}

func conditionState(obj map[string]any, want string) (status, reason, message string) {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok || condition["type"] != want {
			continue
		}
		status, _, _ = unstructured.NestedString(condition, "status")
		reason, _, _ = unstructured.NestedString(condition, "reason")
		message, _, _ = unstructured.NestedString(condition, "message")
		return status, reason, message
	}
	return "", "", ""
}

func waitTilt(t *testing.T, timeout time.Duration, condition func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		ok, message := condition()
		if ok {
			return true
		}
		last = message
		if !time.Now().Before(deadline) {
			t.Logf("wait timeout after %s: %s", timeout, last)
			return false
		}
		time.Sleep(infrastructureOperatorPoll)
	}
}

func waitTiltResourceGone(t *testing.T, resource dynamic.ResourceInterface, name string, timeout time.Duration) {
	t.Helper()
	if !waitTilt(t, timeout, func() (bool, string) {
		_, err := resource.Get(context.Background(), name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, "resource still exists"
	}) {
		t.Errorf("cleanup did not observe %q gone", name)
	}
}

func deleteRuntimeStorageBucketChildren(t *testing.T, runtimeClient dynamic.Interface, instanceUID string) {
	t.Helper()
	selector := fmt.Sprintf("%s=%s", kroInstanceIDLabel, instanceUID)
	if !waitTilt(t, 30*time.Second, func() (bool, string) {
		items, err := runtimeClient.Resource(configConnectorStorageBucketGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, "CRD gone"
			}
			return false, err.Error()
		}
		if len(items.Items) == 0 {
			return true, ""
		}
		for _, item := range items.Items {
			resource := runtimeClient.Resource(configConnectorStorageBucketGVR).Namespace(item.GetNamespace())
			if err := resource.Delete(context.Background(), item.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup runtime StorageBucket %s/%s: %v", item.GetNamespace(), item.GetName(), err)
			}
		}
		return false, fmt.Sprintf("%d runtime StorageBucket child(ren) remain", len(items.Items))
	}) {
		t.Errorf("cleanup did not observe runtime StorageBucket children for instance %q gone", instanceUID)
	}
}

func shortNonce() string {
	return strings.ToLower(fmt.Sprintf("%x", time.Now().UnixNano()&0xffffffff))
}
