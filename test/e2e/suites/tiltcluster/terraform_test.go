/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tiltcluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

var (
	terraformGVR = schema.GroupVersionResource{
		Group: "infrakube.galleybytes.com", Version: "v1", Resource: "terraforms",
	}
	terraformSecretGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "secrets",
	}
	terraformLeaseGVR = schema.GroupVersionResource{
		Group: "coordination.k8s.io", Version: "v1", Resource: "leases",
	}
)

const (
	terraformTemplateName     = "terraform-stack"
	terraformStackResource    = "terraformstacks"
	terraformStackKind        = "TerraformStack"
	terraformCompositionRes   = "terraformcompositionstacks"
	terraformCompositionKind  = "TerraformCompositionStack"
	terraformNodeID           = "terraform"
	terraformCRDName          = "terraforms.infrakube.galleybytes.com"
	terraformFinalizer        = "finalizer.infrakube.galleybytes.com"
	terraformWorkspacePrefix  = "e2e-tf-"
	terraformTestLabel        = "faros.sh/e2e-terraform"
	terraformTestLabelValue   = "infra-operator-infrakube-demo"
	terraformCompositionOptIn = "FAROS_E2E_TERRAFORM_COMPOSITION"
	terraformSmokeOptIn       = "FAROS_E2E_TERRAFORM"

	terraformWait        = 12 * time.Minute
	terraformCleanupWait = 3 * time.Minute
)

// TestTerraformComposition proves the infrastructure operator path without
// installing Infrakube or executing Terraform. A minimal test-owned Terraform
// CRD is enough for KRO to accept the contrib graph and compose the exact child.
func TestTerraformComposition(t *testing.T) {
	if os.Getenv(terraformCompositionOptIn) != "1" {
		t.Skip("run only through make e2e-tilt-cluster-terraform")
	}
	requireStack(t)
	runtimeClient := infrastructureRuntimeClient(t)
	waitInfrastructureProviderReady(t, runtimeClient)

	if !ensureTestTerraformCRD(t, runtimeClient) {
		t.Skipf("Terraform CRD %q already exists; refusing to replace a non-test-owned Infrakube surface", terraformCRDName)
	}
	t.Cleanup(func() {
		if err := runtimeClient.Resource(customResourceDefinitionGVR).Delete(context.Background(), terraformCRDName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup test Terraform CRD: %v", err)
		}
		waitTiltResourceGone(t, runtimeClient.Resource(customResourceDefinitionGVR), terraformCRDName, terraformCleanupWait)
	})

	templateName := terraformTemplateName + "-composition-" + shortNonce()
	providerClient := kcpAdminDynamic(t, providerWorkspace)
	template := terraformTemplateFixture(t, templateName)
	if _, err := providerClient.Resource(infrastructureTemplateGVR).Create(context.Background(), template, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Terraform composition Template %q: %v", templateName, err)
	}
	t.Cleanup(func() {
		if err := providerClient.Resource(infrastructureTemplateGVR).Delete(context.Background(), templateName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup Template %q: %v", templateName, err)
		}
		waitTiltResourceGone(t, providerClient.Resource(infrastructureTemplateGVR), templateName, terraformCleanupWait)
		if err := runtimeClient.Resource(resourceGraphDefinitionGVR).Delete(context.Background(), templateName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup ResourceGraphDefinition %q: %v", templateName, err)
		}
		waitTiltResourceGone(t, runtimeClient.Resource(resourceGraphDefinitionGVR), templateName, terraformCleanupWait)
	})
	waitTerraformTemplatePublished(t, providerClient, runtimeClient, templateName, terraformCompositionRes)

	runTerraformTenantLifecycle(t, runtimeClient, false, terraformCompositionRes, terraformCompositionKind)
}

// TestTerraformInfrakubeSmoke uses only the stable Template installed by
// terraform-enable. It proves apply, state persistence, destroy, and the
// explicitly retained empty backend artifacts through the real controller.
func TestTerraformInfrakubeSmoke(t *testing.T) {
	if os.Getenv(terraformSmokeOptIn) != "1" {
		t.Skip("run only through make terraform-smoke")
	}
	requireStack(t)
	runtimeClient := infrastructureRuntimeClient(t)
	waitInfrastructureProviderReady(t, runtimeClient)
	requireHealthyInfrakube(t, runtimeClient)

	providerClient := kcpAdminDynamic(t, providerWorkspace)
	waitTerraformTemplatePublished(t, providerClient, runtimeClient, terraformTemplateName, terraformStackResource)
	runTerraformTenantLifecycle(t, runtimeClient, true, terraformStackResource, terraformStackKind)
}

func runTerraformTenantLifecycle(t *testing.T, runtimeClient dynamic.Interface, execute bool, instanceResource, instanceKind string) {
	t.Helper()
	instanceGVR := schema.GroupVersionResource{Group: infraGroup, Version: "v1alpha1", Resource: instanceResource}
	workspaceName := terraformWorkspacePrefix + shortNonce()
	parentClient := kcpAdminDynamic(t, "root:faros")
	workspacePath := createTerraformWorkspace(t, parentClient, workspaceName)
	t.Cleanup(func() {
		if err := parentClient.Resource(workspaceGVR).Delete(context.Background(), workspaceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup workspace %q: %v", workspaceName, err)
		}
		waitTiltResourceGone(t, parentClient.Resource(workspaceGVR), workspaceName, terraformCleanupWait)
	})

	tenantClient := kcpAdminDynamic(t, workspacePath)
	if _, err := tenantClient.Resource(apiBindingGVR).Create(context.Background(), infrastructureAPIBinding(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create infrastructure APIBinding in %s: %v", workspacePath, err)
	}
	t.Cleanup(func() {
		if err := tenantClient.Resource(apiBindingGVR).Delete(context.Background(), "infrastructure", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup infrastructure APIBinding in %s: %v", workspacePath, err)
		}
		waitTiltResourceGone(t, tenantClient.Resource(apiBindingGVR), "infrastructure", terraformCleanupWait)
	})
	waitTerraformBinding(t, tenantClient, workspacePath)

	stackName := "faros-tf-e2e-" + shortNonce()
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": infraGroup + "/v1alpha1",
		"kind":       instanceKind,
		"metadata":   map[string]any{"name": stackName},
		"spec": map[string]any{
			"name":    stackName,
			"message": "hello from Faros",
		},
	}}
	created := createTerraformStack(t, tenantClient, instanceGVR, instance)
	instanceUID := string(created.GetUID())
	if instanceUID == "" {
		t.Fatalf("TerraformStack %q has no UID", stackName)
	}
	instanceDeleted := false
	childNamespace := ""
	stateSecretName := "tfstate-default-" + stackName + "-faros"
	stateLeaseName := "lock-" + stateSecretName
	t.Cleanup(func() {
		if !instanceDeleted {
			_ = tenantClient.Resource(instanceGVR).Delete(context.Background(), stackName, metav1.DeleteOptions{})
		}
		if childNamespace != "" && waitTerraformChildrenGone(t, runtimeClient, instanceUID, terraformCleanupWait) && execute {
			deleteTerraformStateArtifact(t, runtimeClient, terraformSecretGVR, childNamespace, stateSecretName)
			deleteTerraformStateArtifact(t, runtimeClient, terraformLeaseGVR, childNamespace, stateLeaseName)
		}
	})

	child := waitTerraformChild(t, runtimeClient, instanceUID)
	childNamespace = child.GetNamespace()
	if childNamespace == "" {
		t.Fatalf("Terraform child %q has no runtime namespace", child.GetName())
	}
	if got := child.GetLabels()[kroInstanceIDLabel]; got != instanceUID {
		t.Fatalf("Terraform child instance-id label = %q, want %q", got, instanceUID)
	}
	if got := child.GetLabels()[kroNodeIDLabel]; got != terraformNodeID {
		t.Fatalf("Terraform child node-id label = %q, want %q", got, terraformNodeID)
	}
	if version, _, _ := unstructured.NestedString(child.Object, "spec", "terraformVersion"); version != "1.5.7" {
		t.Fatalf("Terraform child version = %q, want 1.5.7", version)
	}
	backend, _, _ := unstructured.NestedString(child.Object, "spec", "backend")
	if !strings.Contains(backend, `namespace = "`+childNamespace+`"`) {
		t.Fatalf("Terraform backend did not resolve runtime namespace %q: %q", childNamespace, backend)
	}
	if !execute {
		t.Logf("infrastructure operator composed Terraform child %s/%s from tenant TerraformStack %q; Infrakube execution was not exercised", childNamespace, child.GetName(), stackName)
		return
	}

	child = waitTerraformCompleted(t, runtimeClient, instanceUID)
	if !slices.Contains(child.GetFinalizers(), terraformFinalizer) {
		t.Fatalf("Terraform child finalizers = %v, want %q", child.GetFinalizers(), terraformFinalizer)
	}
	waitTerraformStackStatus(t, tenantClient, instanceGVR, stackName, childNamespace)
	beforeSecret := getTerraformStateArtifact(t, runtimeClient, terraformSecretGVR, childNamespace, stateSecretName)
	beforeSerial, beforeResources := terraformStateSummary(t, beforeSecret)
	if beforeResources == 0 {
		t.Fatal("Terraform state has no resources after apply")
	}
	assertTerraformStateLock(t, runtimeClient, childNamespace, stateLeaseName, stackName+"-faros")

	if err := tenantClient.Resource(instanceGVR).Delete(context.Background(), stackName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete TerraformStack %q: %v", stackName, err)
	}
	waitTerraformDeletePhaseAndGone(t, runtimeClient, instanceUID)
	waitTiltResourceGone(t, tenantClient.Resource(instanceGVR), stackName, terraformCleanupWait)
	instanceDeleted = true

	afterSecret := getTerraformStateArtifact(t, runtimeClient, terraformSecretGVR, childNamespace, stateSecretName)
	afterSerial, afterResources := terraformStateSummary(t, afterSecret)
	if afterResources != 0 || afterSerial <= beforeSerial || afterSecret.GetResourceVersion() == beforeSecret.GetResourceVersion() {
		t.Fatalf("Terraform destroy state = serial %d -> %d, resources=%d, resourceVersion %s -> %s", beforeSerial, afterSerial, afterResources, beforeSecret.GetResourceVersion(), afterSecret.GetResourceVersion())
	}
	assertTerraformStateLock(t, runtimeClient, childNamespace, stateLeaseName, stackName+"-faros")

	secretDeleted := deleteTerraformStateArtifact(t, runtimeClient, terraformSecretGVR, childNamespace, stateSecretName)
	leaseDeleted := deleteTerraformStateArtifact(t, runtimeClient, terraformLeaseGVR, childNamespace, stateLeaseName)
	if secretDeleted && leaseDeleted {
		childNamespace = ""
		t.Logf("Terraform apply/destroy completed through the infrastructure operator; state serial advanced %d -> %d and test-owned retained artifacts were removed", beforeSerial, afterSerial)
	} else {
		t.Logf("Terraform apply/destroy completed and state serial advanced %d -> %d, but deferred cleanup must retry at least one retained artifact", beforeSerial, afterSerial)
	}
}

func waitTerraformTemplatePublished(t *testing.T, providerClient, runtimeClient dynamic.Interface, name, instanceResource string) {
	t.Helper()
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		template, err := providerClient.Resource(infrastructureTemplateGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		status, reason, message := conditionState(template.Object, "Ready")
		return status == "True", fmt.Sprintf("Ready=%s reason=%s message=%s", status, reason, message)
	}) {
		t.Fatalf("Terraform Template %q never became Ready", name)
	}
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		export, err := providerClient.Resource(apiExportGVR).Get(context.Background(), infraAPIExportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return apiExportHasResource(export.Object, instanceResource, infraGroup), "APIExport does not list " + instanceResource
	}) {
		t.Fatalf("infrastructure APIExport never published %s", instanceResource)
	}
	if status, message := waitRGDGraphAccepted(t, runtimeClient, name); status != "True" {
		t.Fatalf("Terraform RGD %q was not GraphAccepted: status=%s message=%s", name, status, message)
	}
}

func terraformTemplateFixture(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	path := filepath.Join(repoRoot, "providers/infrastructure/contrib/terraform/terraform-stack-template.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Terraform contrib fixture %q: %v", path, err)
	}
	var object map[string]any
	if err := yaml.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode Terraform contrib fixture: %v", err)
	}
	template := &unstructured.Unstructured{Object: object}
	if template.GetAPIVersion() != infraGroup+"/v1alpha1" || template.GetKind() != "Template" {
		t.Fatalf("Terraform fixture type = %s/%s", template.GetAPIVersion(), template.GetKind())
	}
	template.SetName(name)
	if name != terraformTemplateName {
		if err := unstructured.SetNestedField(template.Object, terraformCompositionRes, "spec", "instanceCRD", "resource"); err != nil {
			t.Fatalf("set composition instance resource: %v", err)
		}
		if err := unstructured.SetNestedField(template.Object, terraformCompositionKind, "spec", "instanceCRD", "kind"); err != nil {
			t.Fatalf("set composition instance kind: %v", err)
		}
	}
	return template
}

func ensureTestTerraformCRD(t *testing.T, runtimeClient dynamic.Interface) bool {
	t.Helper()
	existing, err := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), terraformCRDName, metav1.GetOptions{})
	if err == nil {
		return existing.GetLabels()[terraformTestLabel] == terraformTestLabelValue
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get Terraform CRD: %v", err)
	}
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name":   terraformCRDName,
			"labels": map[string]any{terraformTestLabel: terraformTestLabelValue},
		},
		"spec": map[string]any{
			"group": "infrakube.galleybytes.com",
			"scope": "Namespaced",
			"names": map[string]any{"plural": "terraforms", "singular": "terraform", "kind": "Terraform", "listKind": "TerraformList"},
			"versions": []any{map[string]any{
				"name": "v1", "served": true, "storage": true,
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"spec":   map[string]any{"type": "object", "x-kubernetes-preserve-unknown-fields": true},
						"status": map[string]any{"type": "object", "x-kubernetes-preserve-unknown-fields": true},
					},
				}},
				"subresources": map[string]any{"status": map[string]any{}},
			}},
		},
	}}
	if _, err := runtimeClient.Resource(customResourceDefinitionGVR).Create(context.Background(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create test Terraform CRD: %v", err)
	}
	if !waitTilt(t, 60*time.Second, func() (bool, string) {
		got, err := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), terraformCRDName, metav1.GetOptions{})
		return err == nil && crdEstablished(got), fmt.Sprint(err)
	}) {
		t.Fatal("test Terraform CRD never became Established")
	}
	return true
}

func requireHealthyInfrakube(t *testing.T, runtimeClient dynamic.Interface) {
	t.Helper()
	crd, err := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), terraformCRDName, metav1.GetOptions{})
	if err != nil || !crdEstablished(crd) {
		t.Fatalf("real Infrakube Terraform CRD is not Established; run make terraform-install first: %v", err)
	}
	deploymentGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	deployment, err := runtimeClient.Resource(deploymentGVR).Namespace("infrakube-system").Get(context.Background(), "infrakube", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Infrakube controller Deployment: %v", err)
	}
	available, _, _ := unstructured.NestedInt64(deployment.Object, "status", "availableReplicas")
	if available < 1 {
		t.Fatalf("Infrakube controller has %d available replicas", available)
	}
}

func createTerraformWorkspace(t *testing.T, parent dynamic.Interface, name string) string {
	t.Helper()
	workspace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]any{terraformTestLabel: terraformTestLabelValue},
		},
	}}
	if _, err := parent.Resource(workspaceGVR).Create(context.Background(), workspace, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create isolated Terraform workspace %q: %v", name, err)
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
		t.Fatalf("isolated Terraform workspace %q never became Ready", name)
	}
	return path
}

func waitTerraformBinding(t *testing.T, tenantClient dynamic.Interface, workspacePath string) {
	t.Helper()
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		binding, err := tenantClient.Resource(apiBindingGVR).Get(context.Background(), "infrastructure", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatalf("infrastructure APIBinding in %s never reached Bound", workspacePath)
	}
}

func createTerraformStack(t *testing.T, tenantClient dynamic.Interface, instanceGVR schema.GroupVersionResource, instance *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	var created *unstructured.Unstructured
	if !waitTilt(t, infrastructureOperatorWait, func() (bool, string) {
		got, err := tenantClient.Resource(instanceGVR).Create(context.Background(), instance.DeepCopy(), metav1.CreateOptions{})
		if err == nil {
			created = got
			return true, ""
		}
		return false, err.Error()
	}) {
		t.Fatalf("create TerraformStack %q: API not served after binding", instance.GetName())
	}
	return created
}

func waitTerraformChild(t *testing.T, runtimeClient dynamic.Interface, instanceUID string) *unstructured.Unstructured {
	t.Helper()
	selector := fmt.Sprintf("%s=%s,%s=%s", kroInstanceIDLabel, instanceUID, kroNodeIDLabel, terraformNodeID)
	var child *unstructured.Unstructured
	if !waitTilt(t, terraformWait, func() (bool, string) {
		items, err := runtimeClient.Resource(terraformGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err.Error()
		}
		if len(items.Items) != 1 {
			return false, fmt.Sprintf("found %d KRO-labeled Terraform children", len(items.Items))
		}
		child = items.Items[0].DeepCopy()
		return true, ""
	}) {
		t.Fatalf("KRO never created one Terraform child for instance UID %s", instanceUID)
	}
	return child
}

func waitTerraformCompleted(t *testing.T, runtimeClient dynamic.Interface, instanceUID string) *unstructured.Unstructured {
	t.Helper()
	selector := fmt.Sprintf("%s=%s,%s=%s", kroInstanceIDLabel, instanceUID, kroNodeIDLabel, terraformNodeID)
	var child *unstructured.Unstructured
	if !waitTilt(t, terraformWait, func() (bool, string) {
		items, err := runtimeClient.Resource(terraformGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil || len(items.Items) != 1 {
			return false, fmt.Sprintf("children=%d err=%v", len(items.Items), err)
		}
		child = items.Items[0].DeepCopy()
		phase, _, _ := unstructured.NestedString(child.Object, "status", "phase")
		return phase == "completed", "phase=" + phase
	}) {
		t.Fatal("Infrakube Terraform child never completed")
	}
	return child
}

func waitTerraformStackStatus(t *testing.T, tenantClient dynamic.Interface, instanceGVR schema.GroupVersionResource, name, namespace string) {
	t.Helper()
	if !waitTilt(t, terraformWait, func() (bool, string) {
		instance, err := tenantClient.Resource(instanceGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(instance.Object, "status", "phase")
		message, _, _ := unstructured.NestedString(instance.Object, "status", "message")
		resourceID, _, _ := unstructured.NestedString(instance.Object, "status", "resourceID")
		runtimeNamespace, _, _ := unstructured.NestedString(instance.Object, "status", "runtimeNamespace")
		if _, found, _ := unstructured.NestedFieldNoCopy(instance.Object, "status", "internal_marker"); found {
			t.Fatalf("TerraformStack status exposes internal_marker: %v", instance.Object["status"])
		}
		return phase == "completed" && message == "hello from Faros" && resourceID != "" && runtimeNamespace == namespace,
			fmt.Sprintf("phase=%s message=%s resourceID=%s namespace=%s", phase, message, resourceID, runtimeNamespace)
	}) {
		t.Fatalf("TerraformStack %q never projected completed status", name)
	}
}

func waitTerraformDeletePhaseAndGone(t *testing.T, runtimeClient dynamic.Interface, instanceUID string) {
	t.Helper()
	selector := fmt.Sprintf("%s=%s,%s=%s", kroInstanceIDLabel, instanceUID, kroNodeIDLabel, terraformNodeID)
	sawDeletePhase := false
	if !waitTilt(t, terraformWait, func() (bool, string) {
		items, err := runtimeClient.Resource(terraformGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err.Error()
		}
		if len(items.Items) == 0 {
			return sawDeletePhase, fmt.Sprintf("child gone; observed delete phase=%t", sawDeletePhase)
		}
		phase, _, _ := unstructured.NestedString(items.Items[0].Object, "status", "phase")
		sawDeletePhase = sawDeletePhase || phase == "initializing-delete" || phase == "deleting" || phase == "deleted"
		return false, "phase=" + phase
	}) {
		t.Fatal("Infrakube child did not pass through delete and disappear")
	}
}

func waitTerraformChildrenGone(t *testing.T, runtimeClient dynamic.Interface, instanceUID string, timeout time.Duration) bool {
	t.Helper()
	selector := fmt.Sprintf("%s=%s", kroInstanceIDLabel, instanceUID)
	return waitTilt(t, timeout, func() (bool, string) {
		items, err := runtimeClient.Resource(terraformGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if apierrors.IsNotFound(err) {
			return true, "CRD absent"
		}
		if err != nil {
			return false, err.Error()
		}
		return len(items.Items) == 0, fmt.Sprintf("%d Terraform children remain", len(items.Items))
	})
}

func getTerraformStateArtifact(t *testing.T, runtimeClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	artifact, err := runtimeClient.Resource(gvr).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Terraform state artifact %s/%s: %v", namespace, name, err)
	}
	return artifact
}

func assertTerraformStateLock(t *testing.T, runtimeClient dynamic.Interface, namespace, name, suffix string) {
	t.Helper()
	lease := getTerraformStateArtifact(t, runtimeClient, terraformLeaseGVR, namespace, name)
	labels := lease.GetLabels()
	if labels["app.kubernetes.io/managed-by"] != "terraform" || labels["tfstate"] != "true" ||
		labels["tfstateSecretSuffix"] != suffix || labels["tfstateWorkspace"] != "default" {
		t.Fatalf("Terraform state Lease %s/%s has unexpected labels: %v", namespace, name, labels)
	}
}

func terraformStateSummary(t *testing.T, secret *unstructured.Unstructured) (int64, int) {
	t.Helper()
	encoded, found, err := unstructured.NestedString(secret.Object, "data", "tfstate")
	if err != nil || !found || encoded == "" {
		t.Fatalf("Terraform state Secret %s/%s has no data.tfstate", secret.GetNamespace(), secret.GetName())
	}
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode Terraform state: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open Terraform state gzip: %v", err)
	}
	stateJSON, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read Terraform state: %v", err)
	}
	var state struct {
		Serial    int64             `json:"serial"`
		Resources []json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		t.Fatalf("decode Terraform state JSON: %v", err)
	}
	return state.Serial, len(state.Resources)
}

func deleteTerraformStateArtifact(t *testing.T, runtimeClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) bool {
	t.Helper()
	err := runtimeClient.Resource(gvr).Namespace(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Errorf("delete Terraform state artifact %s/%s: %v", namespace, name, err)
		return false
	}
	if !waitTilt(t, 30*time.Second, func() (bool, string) {
		_, err := runtimeClient.Resource(gvr).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprint(err)
	}) {
		t.Errorf("Terraform state artifact %s/%s was not deleted", namespace, name)
		return false
	}
	return true
}
