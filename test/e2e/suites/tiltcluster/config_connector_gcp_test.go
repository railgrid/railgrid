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
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

var (
	configConnectorGVR = schema.GroupVersionResource{
		Group: "core.cnrm.cloud.google.com", Version: "v1beta1", Resource: "configconnectors",
	}
	configConnectorPubSubTopicGVR = schema.GroupVersionResource{
		Group: "pubsub.cnrm.cloud.google.com", Version: "v1beta1", Resource: "pubsubtopics",
	}
	configConnectorPubSubInstanceGVR = schema.GroupVersionResource{
		Group: infraGroup, Version: "v1alpha1", Resource: "gcppubsubtopics",
	}
)

const (
	configConnectorGCPOptIn             = "FAROS_E2E_CONFIG_CONNECTOR_GCP"
	configConnectorGCPProjectEnv        = "FAROS_E2E_GCP_PROJECT"
	configConnectorGCPCredentialsEnv    = "FAROS_E2E_GCP_CREDENTIALS_FILE"
	configConnectorName                 = "configconnector.core.cnrm.cloud.google.com"
	configConnectorPubSubCRDName        = "pubsubtopics.pubsub.cnrm.cloud.google.com"
	configConnectorPubSubTemplatePrefix = "gcp-pubsub-kcc-demo-"
	configConnectorPubSubTemplateName   = "gcp-pubsub-topic"
	configConnectorPubSubWorkspacePref  = "e2e-pubsub-"
	configConnectorPubSubNode           = "pubSubTopic"
	configConnectorPubSubResource       = "gcppubsubtopics"
	configConnectorGCPTokenURI          = "https://oauth2.googleapis.com/token"
	configConnectorGCPPubSubScope       = "https://www.googleapis.com/auth/pubsub"

	configConnectorGCPWait        = 12 * time.Minute
	configConnectorGCPCleanupWait = 2 * time.Minute
	configConnectorGCPPoll        = 5 * time.Second
)

var (
	configConnectorProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	configConnectorTopicPattern   = regexp.MustCompile(`^faros-kcc-e2e-[0-9a-f]{8}$`)
)

// TestConfigConnectorGCPPubSubLifecycle is the isolated real-cloud extension
// of TestConfigConnectorComposition. It is selected only by its dedicated Make
// target because it installs no fake CRD and creates a billable cloud resource.
func TestConfigConnectorGCPPubSubLifecycle(t *testing.T) {
	runConfigConnectorGCPPubSubLifecycle(t, false)
}

// TestConfigConnectorGCPPubSubSmoke uses the stable Template enabled by the
// manual Tilt action. It deliberately does not create or update that Template;
// the only cloud object it owns is the generated Pub/Sub topic.
func TestConfigConnectorGCPPubSubSmoke(t *testing.T) {
	runConfigConnectorGCPPubSubLifecycle(t, true)
}

// TestConfigConnectorPubSubTemplateFixture is a read-only guard that runs
// without a Tilt stack. The real-cloud tests must consume this exact YAML
// rather than carrying a second, drift-prone Go representation.
func TestConfigConnectorPubSubTemplateFixture(t *testing.T) {
	template := configConnectorPubSubTemplate(t, configConnectorPubSubTemplateName)
	resource, found, err := unstructured.NestedString(template.Object, "spec", "instanceCRD", "resource")
	if err != nil || !found || resource != configConnectorPubSubResource {
		t.Fatalf("fixture instanceCRD.resource = %q (found=%t err=%v), want %q", resource, found, err, configConnectorPubSubResource)
	}
	resources, found, err := unstructured.NestedSlice(template.Object, "spec", "backendConfig", "resources")
	if err != nil || !found || len(resources) != 1 {
		t.Fatalf("fixture backendConfig.resources = %#v (found=%t err=%v), want one resource", resources, found, err)
	}
	resourceObject, ok := resources[0].(map[string]any)
	if !ok || resourceObject["id"] != configConnectorPubSubNode {
		t.Fatalf("fixture backendConfig resource = %#v, want id %q", resources[0], configConnectorPubSubNode)
	}
}

func runConfigConnectorGCPPubSubLifecycle(t *testing.T, useEnabledTemplate bool) {
	if os.Getenv(configConnectorGCPOptIn) != "1" {
		t.Skip("run only through an explicit Config Connector Make target")
	}

	projectID := os.Getenv(configConnectorGCPProjectEnv)
	credentialsFile := os.Getenv(configConnectorGCPCredentialsEnv)
	if projectID == "" || credentialsFile == "" {
		t.Fatalf("%s and %s are required when %s=1", configConnectorGCPProjectEnv, configConnectorGCPCredentialsEnv, configConnectorGCPOptIn)
	}
	if !configConnectorProjectPattern.MatchString(projectID) {
		t.Fatalf("%s is not a conservative Google Cloud project ID", configConnectorGCPProjectEnv)
	}

	credentialJSON, err := os.ReadFile(credentialsFile)
	if err != nil {
		// Do not include err: os.PathError contains the credential path.
		t.Fatalf("%s could not be read", configConnectorGCPCredentialsEnv)
	}
	defer clear(credentialJSON)
	accessToken := mintConfigConnectorGCPAccessToken(t, credentialJSON)

	if !stackReady {
		t.Fatal("tilt-cluster stack is required when the real-cloud Config Connector E2E is enabled")
	}
	runtimeClient := requiredConfigConnectorRuntimeClient(t)
	waitInfrastructureProviderReady(t, runtimeClient)
	requireHealthyConfigConnector(t, runtimeClient)

	providerClient := kcpAdminDynamic(t, providerWorkspace)
	parentClient := kcpAdminDynamic(t, "root:faros")
	topicName := "faros-kcc-e2e-" + configConnectorGCPNonce()
	if !configConnectorTopicPattern.MatchString(topicName) {
		t.Fatalf("generated topic %q is outside the E2E ownership boundary", topicName)
	}
	topicResource := fmt.Sprintf("projects/%s/topics/%s", projectID, topicName)
	initialStatus, err := getConfigConnectorPubSubTopic(accessToken, topicResource)
	if err != nil {
		t.Fatalf("preflight Pub/Sub topic ownership check failed: %v", err)
	}
	if initialStatus != http.StatusNotFound {
		t.Fatalf("refusing topic name %q because the preflight REST GET returned HTTP %d instead of 404", topicName, initialStatus)
	}
	templateName := configConnectorPubSubTemplatePrefix + shortNonce()
	if useEnabledTemplate {
		templateName = configConnectorPubSubTemplateName
	}
	workspaceName := configConnectorPubSubWorkspacePref + shortNonce()

	var (
		templateCreated  bool
		workspaceCreated bool
		bindingCreated   bool
		instanceCreated  bool
		instanceDeleted  bool
		parentGone       bool
		cloudCleanupOK   bool
		cloudAbsent      bool
		workspacePath    string
		instanceUID      string
		childName        string
		childNamespace   string
		tenantClient     dynamic.Interface
	)

	t.Cleanup(func() {
		parentGone = instanceDeleted
		if instanceCreated && !instanceDeleted && workspacePath != "" {
			if err := tenantClient.Resource(configConnectorPubSubInstanceGVR).Delete(context.Background(), topicName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup GCPPubSubTopic %q: %v", topicName, err)
			}
			parentGone = waitTilt(t, configConnectorGCPCleanupWait, func() (bool, string) {
				_, err := tenantClient.Resource(configConnectorPubSubInstanceGVR).Get(context.Background(), topicName, metav1.GetOptions{})
				return apierrors.IsNotFound(err), fmt.Sprintf("get cleanup parent: %v", err)
			})
			if !parentGone {
				t.Errorf("cleanup did not observe GCPPubSubTopic parent %q gone", topicName)
			}
		}

		childrenGone := waitConfigConnectorChildrenGone(t, runtimeClient, instanceUID, configConnectorGCPCleanupWait)
		if !childrenGone {
			childrenGone = waitConfigConnectorChildrenGone(t, runtimeClient, instanceUID, configConnectorGCPCleanupWait)
		}
		safeCloudCleanup := cloudCleanupOK && ((instanceUID != "" && childrenGone) || (instanceUID == "" && parentGone))
		if safeCloudCleanup && !cloudAbsent {
			if !waitConfigConnectorPubSubState(t, accessToken, topicResource, false, configConnectorGCPCleanupWait) {
				if err := deleteConfigConnectorPubSubTopic(accessToken, topicResource); err != nil {
					t.Errorf("emergency cleanup of test-owned Pub/Sub topic %q: %v", topicName, err)
				} else if !waitConfigConnectorPubSubState(t, accessToken, topicResource, false, configConnectorGCPCleanupWait) {
					t.Errorf("emergency cleanup did not prove Pub/Sub topic %q absent", topicName)
				}
			}
		} else if cloudCleanupOK && !cloudAbsent {
			t.Errorf("refusing emergency Pub/Sub deletion because the controller child cleanup boundary was not proven")
		}

		if !childrenGone {
			t.Errorf("cleanup did not observe PubSubTopic children for instance %q gone", instanceUID)
		} else if instanceUID == "" && childName != "" {
			waitTiltResourceGone(t, runtimeClient.Resource(configConnectorPubSubTopicGVR).Namespace(childNamespace), childName, configConnectorGCPCleanupWait)
		}
		if bindingCreated && workspacePath != "" {
			if err := tenantClient.Resource(apiBindingGVR).Delete(context.Background(), "infrastructure", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup infrastructure APIBinding in %s: %v", workspacePath, err)
			}
		}
		if workspaceCreated {
			if err := parentClient.Resource(workspaceGVR).Delete(context.Background(), workspaceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup workspace %q: %v", workspaceName, err)
			}
			waitTiltResourceGone(t, parentClient.Resource(workspaceGVR), workspaceName, configConnectorGCPCleanupWait)
		}
		if templateCreated {
			if err := providerClient.Resource(infrastructureTemplateGVR).Delete(context.Background(), templateName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup Template %q: %v", templateName, err)
			}
			waitTiltResourceGone(t, providerClient.Resource(infrastructureTemplateGVR), templateName, configConnectorGCPCleanupWait)
			if err := runtimeClient.Resource(resourceGraphDefinitionGVR).Delete(context.Background(), templateName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup ResourceGraphDefinition %q: %v", templateName, err)
			}
			waitTiltResourceGone(t, runtimeClient.Resource(resourceGraphDefinitionGVR), templateName, configConnectorGCPCleanupWait)
			if !waitTilt(t, configConnectorGCPCleanupWait, func() (bool, string) {
				export, err := providerClient.Resource(apiExportGVR).Get(context.Background(), infraAPIExportName, metav1.GetOptions{})
				if err != nil {
					return false, err.Error()
				}
				return !apiExportHasResource(export.Object, configConnectorPubSubResource, infraGroup), "APIExport still lists gcppubsubtopics"
			}) {
				t.Errorf("cleanup did not observe gcppubsubtopics removed from the infrastructure APIExport")
			}
		}
	})

	if !useEnabledTemplate {
		if _, err := providerClient.Resource(infrastructureTemplateGVR).Create(context.Background(), configConnectorPubSubTemplate(t, templateName), metav1.CreateOptions{}); err != nil {
			t.Fatalf("create test Template %q: %v", templateName, err)
		}
		templateCreated = true
	}
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		got, err := providerClient.Resource(infrastructureTemplateGVR).Get(context.Background(), templateName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		status, reason, message := conditionState(got.Object, "Ready")
		return status == "True", fmt.Sprintf("Ready=%s reason=%s message=%s", status, reason, message)
	}) {
		t.Fatalf("test Template %q never became Ready", templateName)
	}
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		export, err := providerClient.Resource(apiExportGVR).Get(context.Background(), infraAPIExportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return apiExportHasResource(export.Object, configConnectorPubSubResource, infraGroup), "APIExport does not list gcppubsubtopics"
	}) {
		t.Fatal("infrastructure APIExport never listed gcppubsubtopics")
	}
	if status, message := waitRGDGraphAccepted(t, runtimeClient, templateName); status != "True" {
		t.Fatalf("test Template RGD %q was not GraphAccepted: status=%s message=%s", templateName, status, message)
	}

	workspacePath = createConfigConnectorWorkspace(t, parentClient, workspaceName)
	workspaceCreated = true
	tenantClient = kcpAdminDynamic(t, workspacePath)
	if _, err := tenantClient.Resource(apiBindingGVR).Create(context.Background(), infrastructureAPIBinding(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create infrastructure APIBinding in %s: %v", workspacePath, err)
	}
	bindingCreated = true
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		got, err := tenantClient.Resource(apiBindingGVR).Get(context.Background(), "infrastructure", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatalf("infrastructure APIBinding in %s never reached Bound", workspacePath)
	}

	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": infraGroup + "/v1alpha1",
		"kind":       "GCPPubSubTopic",
		"metadata":   map[string]any{"name": topicName},
		"spec": map[string]any{
			"projectID": projectID,
			"topicName": topicName,
		},
	}}
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		created, err := tenantClient.Resource(configConnectorPubSubInstanceGVR).Create(context.Background(), instance.DeepCopy(), metav1.CreateOptions{})
		if err == nil {
			instanceUID = string(created.GetUID())
			return true, ""
		}
		if apierrors.IsAlreadyExists(err) {
			created, getErr := tenantClient.Resource(configConnectorPubSubInstanceGVR).Get(context.Background(), topicName, metav1.GetOptions{})
			if getErr == nil {
				instanceUID = string(created.GetUID())
				return true, "already exists after an uncertain create response"
			}
			return false, getErr.Error()
		}
		return false, err.Error()
	}) {
		t.Fatalf("create GCPPubSubTopic %q in %s", topicName, workspacePath)
	}
	instanceCreated = true
	cloudCleanupOK = true
	if instanceUID == "" && !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		got, err := tenantClient.Resource(configConnectorPubSubInstanceGVR).Get(context.Background(), topicName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		instanceUID = string(got.GetUID())
		return instanceUID != "", "instance UID not assigned"
	}) {
		t.Fatalf("GCPPubSubTopic %q never received a UID", topicName)
	}

	selector := fmt.Sprintf("%s=%s,%s=%s", kroInstanceIDLabel, instanceUID, kroNodeIDLabel, configConnectorPubSubNode)
	var child *unstructured.Unstructured
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		items, err := runtimeClient.Resource(configConnectorPubSubTopicGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err.Error()
		}
		if len(items.Items) != 1 {
			return false, fmt.Sprintf("found %d KRO-labeled PubSubTopic children", len(items.Items))
		}
		child = items.Items[0].DeepCopy()
		return true, ""
	}) {
		t.Fatalf("KRO never created one PubSubTopic child for %q", topicName)
	}
	childName, childNamespace = child.GetName(), child.GetNamespace()
	if childNamespace == "" {
		t.Fatalf("runtime PubSubTopic child %q has no namespace", childName)
	}
	if childName != topicName {
		t.Fatalf("runtime PubSubTopic child name = %q, want %q", childName, topicName)
	}
	if got := child.GetAnnotations()["cnrm.cloud.google.com/project-id"]; got != projectID {
		t.Fatalf("runtime PubSubTopic project annotation = %q, want %q", got, projectID)
	}
	childSpec, found, err := unstructured.NestedMap(child.Object, "spec")
	if err != nil || !found || len(childSpec) != 0 {
		t.Fatalf("runtime PubSubTopic spec = %v (found=%t err=%v), want an explicit empty spec", childSpec, found, err)
	}
	waitConfigConnectorChildReady(t, runtimeClient, childNamespace, childName)
	if !waitConfigConnectorPubSubState(t, accessToken, topicResource, true, configConnectorGCPWait) {
		t.Fatalf("Pub/Sub REST API never proved topic %q exists", topicResource)
	}

	if err := tenantClient.Resource(configConnectorPubSubInstanceGVR).Delete(context.Background(), topicName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete GCPPubSubTopic %q: %v", topicName, err)
	}
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		_, err := tenantClient.Resource(configConnectorPubSubInstanceGVR).Get(context.Background(), topicName, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("get parent: %v", err)
	}) {
		t.Fatalf("GCPPubSubTopic parent %q was not deleted", topicName)
	}
	instanceDeleted = true
	if !waitTilt(t, configConnectorGCPWait, func() (bool, string) {
		_, err := runtimeClient.Resource(configConnectorPubSubTopicGVR).Namespace(childNamespace).Get(context.Background(), childName, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("get child: %v", err)
	}) {
		t.Fatalf("PubSubTopic child %s/%s was not deleted", childNamespace, childName)
	}
	if !waitConfigConnectorPubSubState(t, accessToken, topicResource, false, configConnectorGCPWait) {
		t.Fatalf("Pub/Sub REST API never proved topic %q absent", topicResource)
	}
	cloudAbsent = true
	t.Logf("KRO and Config Connector created Pub/Sub topic %q, direct REST proved it existed, and deleting the Faros parent removed both child and cloud topic", topicResource)
}

func requireHealthyConfigConnector(t *testing.T, runtimeClient dynamic.Interface) {
	t.Helper()
	crd, err := runtimeClient.Resource(customResourceDefinitionGVR).Get(context.Background(), configConnectorPubSubCRDName, metav1.GetOptions{})
	if err != nil || !crdEstablished(crd) {
		t.Fatalf("real Config Connector PubSubTopic CRD is not Established; run make e2e-tilt-cluster-config-connector-gcp-install first: %v", err)
	}
	connector, err := runtimeClient.Resource(configConnectorGVR).Get(context.Background(), configConnectorName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ConfigConnector %q: %v", configConnectorName, err)
	}
	healthy, _, _ := unstructured.NestedBool(connector.Object, "status", "healthy")
	observed, _, _ := unstructured.NestedInt64(connector.Object, "status", "observedGeneration")
	if !healthy || observed != connector.GetGeneration() {
		errors, _, _ := unstructured.NestedStringSlice(connector.Object, "status", "errors")
		phase, _, _ := unstructured.NestedString(connector.Object, "status", "phase")
		t.Fatalf("ConfigConnector is not current and healthy: healthy=%t observedGeneration=%d generation=%d phase=%s errors=%v", healthy, observed, connector.GetGeneration(), phase, errors)
	}
}

func requiredConfigConnectorRuntimeClient(t *testing.T) dynamic.Interface {
	t.Helper()
	path := envOr("FAROS_E2E_TILT_RUNTIME_KUBECONFIG", filepath.Join(repoRoot, ".faros-cluster.kubeconfig"))
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("runtime kubeconfig is required at %q when the real-cloud E2E is enabled", path)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("runtime kubeconfig %q is not usable: %v", path, err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("runtime client from %q is unavailable: %v", path, err)
	}
	return client
}

func configConnectorPubSubTemplate(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	path := filepath.Join(repoRoot, "providers/infrastructure/contrib/config-connector/pubsub-template.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Config Connector Pub/Sub Template fixture %q: %v", path, err)
	}
	var object map[string]any
	if err := yaml.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode Config Connector Pub/Sub Template fixture %q: %v", path, err)
	}
	template := &unstructured.Unstructured{Object: object}
	if template.GetAPIVersion() != infraGroup+"/v1alpha1" || template.GetKind() != "Template" {
		t.Fatalf("Config Connector Pub/Sub Template fixture has type %s/%s", template.GetAPIVersion(), template.GetKind())
	}
	if template.GetName() != configConnectorPubSubTemplateName {
		t.Fatalf("Config Connector Pub/Sub Template fixture name = %q, want %q", template.GetName(), configConnectorPubSubTemplateName)
	}
	template.SetName(name)
	return template
}

func waitConfigConnectorChildReady(t *testing.T, runtimeClient dynamic.Interface, namespace, name string) {
	t.Helper()
	deadline := time.Now().Add(configConnectorGCPWait)
	for {
		child, err := runtimeClient.Resource(configConnectorPubSubTopicGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil {
			status, reason, message := conditionState(child.Object, "Ready")
			observed, _, _ := unstructured.NestedInt64(child.Object, "status", "observedGeneration")
			if status == "False" {
				t.Fatalf("Config Connector PubSubTopic %s/%s reported Ready=False: reason=%s message=%s", namespace, name, reason, message)
			}
			if status == "True" && observed == child.GetGeneration() {
				return
			}
			t.Logf("waiting for Config Connector PubSubTopic %s/%s: Ready=%s observedGeneration=%d generation=%d reason=%s message=%s", namespace, name, status, observed, child.GetGeneration(), reason, message)
		} else if !apierrors.IsNotFound(err) {
			t.Logf("waiting for Config Connector PubSubTopic %s/%s: %v", namespace, name, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("Config Connector PubSubTopic %s/%s did not become Ready at its current generation", namespace, name)
		}
		time.Sleep(configConnectorGCPPoll)
	}
}

func waitConfigConnectorChildrenGone(t *testing.T, runtimeClient dynamic.Interface, instanceUID string, timeout time.Duration) bool {
	t.Helper()
	if instanceUID == "" {
		return true
	}
	selector := fmt.Sprintf("%s=%s,%s=%s", kroInstanceIDLabel, instanceUID, kroNodeIDLabel, configConnectorPubSubNode)
	return waitTilt(t, timeout, func() (bool, string) {
		items, err := runtimeClient.Resource(configConnectorPubSubTopicGVR).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if apierrors.IsNotFound(err) {
			return true, "PubSubTopic CRD is absent"
		}
		if err != nil {
			return false, err.Error()
		}
		return len(items.Items) == 0, fmt.Sprintf("%d PubSubTopic child(ren) remain", len(items.Items))
	})
}

type configConnectorServiceAccountCredentials struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

type configConnectorTokenResponse struct {
	AccessToken string `json:"access_token"`
}

func mintConfigConnectorGCPAccessToken(t *testing.T, credentialJSON []byte) string {
	t.Helper()
	var credentials configConnectorServiceAccountCredentials
	if err := json.Unmarshal(credentialJSON, &credentials); err != nil {
		t.Fatal("GCP credentials are not valid JSON")
	}
	if credentials.Type != "service_account" || credentials.ClientEmail == "" || credentials.PrivateKey == "" {
		t.Fatal("GCP credentials must be a service_account JSON key")
	}
	if credentials.TokenURI != configConnectorGCPTokenURI {
		t.Fatalf("GCP service-account token URI must be %s", configConnectorGCPTokenURI)
	}
	block, _ := pem.Decode([]byte(credentials.PrivateKey))
	if block == nil {
		t.Fatal("GCP service-account private key is not PEM encoded")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	clear(block.Bytes)
	if err != nil {
		t.Fatal("GCP service-account private key is not valid PKCS8")
	}
	privateKey, ok := parsedKey.(*rsa.PrivateKey)
	if !ok {
		t.Fatal("GCP service-account private key is not RSA")
	}

	now := time.Now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": credentials.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{
		"iss": credentials.ClientEmail, "scope": configConnectorGCPPubSubScope,
		"aud": configConnectorGCPTokenURI, "iat": now, "exp": now + 3600,
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal("sign GCP service-account assertion")
	}
	assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	clear(signature)

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, configConnectorGCPTokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal("build GCP token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("GCP token exchange failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GCP token exchange returned HTTP %d", resp.StatusCode)
	}
	var token configConnectorTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil || token.AccessToken == "" {
		t.Fatal("GCP token exchange returned no access token")
	}
	return token.AccessToken
}

func waitConfigConnectorPubSubState(t *testing.T, accessToken, resource string, wantExists bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		status, err := getConfigConnectorPubSubTopic(accessToken, resource)
		if err == nil {
			switch status {
			case http.StatusOK:
				if wantExists {
					return true
				}
				last = "HTTP 200: topic still exists"
			case http.StatusNotFound:
				if !wantExists {
					return true
				}
				last = "HTTP 404: topic not created yet"
			case http.StatusUnauthorized, http.StatusForbidden:
				t.Logf("Pub/Sub REST authorization failed with HTTP %d", status)
				return false
			default:
				last = fmt.Sprintf("HTTP %d", status)
			}
		} else {
			last = err.Error()
		}
		if !time.Now().Before(deadline) {
			t.Logf("Pub/Sub REST state wait timed out after %s: %s", timeout, last)
			return false
		}
		time.Sleep(configConnectorGCPPoll)
	}
}

func getConfigConnectorPubSubTopic(accessToken, resource string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://pubsub.googleapis.com/v1/"+resource, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

func deleteConfigConnectorPubSubTopic(accessToken, resource string) error {
	parts := strings.Split(resource, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[2] != "topics" {
		return fmt.Errorf("refusing malformed Pub/Sub resource %q", resource)
	}
	topicName := parts[3]
	if !configConnectorTopicPattern.MatchString(topicName) {
		return fmt.Errorf("refusing to delete non-test-owned topic %q", topicName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "https://pubsub.googleapis.com/v1/"+resource, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("Pub/Sub delete returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func configConnectorGCPNonce() string {
	return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
}
