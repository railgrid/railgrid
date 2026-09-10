/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The connections input of the shipped workload templates (simple-webapp,
// worker, cron-job): fixed slots that turn the values.name of a sibling
// database / redis-cache instance into a DATABASE_URL / REDIS_URL env var
// sourced from that instance's credentials Secret. These expressions are the
// contract with the database (`<name>-db-credentials`) and redis-cache
// (`<name>-credentials`) templates; both publish the full URI under key `uri`.
const (
	connDatabaseSecretName = `${schema.spec.connections.database == "" ? schema.spec.name + "-unbound-db" : schema.spec.connections.database + "-db-credentials"}`
	connDatabaseOptional   = `${schema.spec.connections.database == ""}`
	connCacheSecretName    = `${schema.spec.connections.cache == "" ? schema.spec.name + "-unbound-cache" : schema.spec.connections.cache + "-credentials"}`
	connCacheOptional      = `${schema.spec.connections.cache == ""}`
	connSlotPattern        = `pattern="^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"`
)

type connectionsCase struct {
	file       string
	workloadID string
	container  string
	// podSpecPath is where the pod spec sits inside the workload template.
	podSpecPath []string
	// devWorkloadID is the dev-overlay Deployment synthesized from the
	// workload; empty for templates without a development mode.
	devWorkloadID string
	envConfigMap  string
}

var connectionsCases = []connectionsCase{
	{
		file: "simple-webapp.yaml", workloadID: "appDeployment", container: "app",
		podSpecPath: []string{"spec", "template", "spec"}, devWorkloadID: "appDevDeployment",
		envConfigMap: "${appEnv.metadata.name}",
	},
	{
		file: "worker.yaml", workloadID: "workerDeployment", container: "worker",
		podSpecPath: []string{"spec", "template", "spec"}, devWorkloadID: "workerDevDeployment",
		envConfigMap: "${workerEnv.metadata.name}",
	},
	{
		file: "cron-job.yaml", workloadID: "cronJob", container: "job",
		podSpecPath:  []string{"spec", "jobTemplate", "spec", "template", "spec"},
		envConfigMap: "${jobEnv.metadata.name}",
	},
}

func buildSeedRGD(t *testing.T, file string) *unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	rgd, err := buildRGD(decodeTemplate(t, raw), testTokens())
	if err != nil {
		t.Fatalf("buildRGD(%s): %v", file, err)
	}
	return rgd
}

// connectionsContainer returns the named container of an RGD resource's
// workload template.
func connectionsContainer(t *testing.T, rgd *unstructured.Unstructured, id string, podSpecPath []string, name string) map[string]any {
	t.Helper()
	res := findResource(t, rgd, id)
	if res == nil {
		t.Fatalf("RGD is missing resource %q", id)
	}
	containers, found, err := unstructured.NestedSlice(res, append([]string{"template"}, append(podSpecPath, "containers")...)...)
	if err != nil || !found {
		t.Fatalf("resource %q has no containers (found=%t err=%v)", id, found, err)
	}
	for _, raw := range containers {
		c, _ := raw.(map[string]any)
		if got, _ := c["name"].(string); got == name {
			return c
		}
	}
	t.Fatalf("resource %q has no container %q", id, name)
	return nil
}

// envByName indexes a container's explicit env list, failing on duplicates
// (a duplicated name is last-wins in the kubelet and hides a bug).
func envByName(t *testing.T, container map[string]any) (map[string]map[string]any, []string) {
	t.Helper()
	env, _ := container["env"].([]any)
	out := make(map[string]map[string]any, len(env))
	var order []string
	for _, raw := range env {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		if _, dup := out[name]; dup {
			t.Fatalf("container %q declares env %q twice", container["name"], name)
		}
		out[name] = entry
		order = append(order, name)
	}
	return out, order
}

func assertSecretEnv(t *testing.T, env map[string]map[string]any, name, wantSecret, wantOptional string) {
	t.Helper()
	entry, ok := env[name]
	if !ok {
		t.Fatalf("env %s is missing", name)
	}
	if _, literal := entry["value"]; literal {
		t.Fatalf("env %s carries a literal value; credentials must come from the Secret: %#v", name, entry)
	}
	ref, found, err := unstructured.NestedMap(entry, "valueFrom", "secretKeyRef")
	if err != nil || !found {
		t.Fatalf("env %s is not a secretKeyRef: %#v", name, entry)
	}
	want := map[string]any{"name": wantSecret, "key": "uri", "optional": wantOptional}
	if !reflect.DeepEqual(ref, want) {
		t.Fatalf("env %s secretKeyRef = %#v, want %#v", name, ref, want)
	}
}

// TestSeedTemplatesWorkloadConnectionsRenderSecretEnv pins the authored RGD for
// each workload template: the connections schema reaches kro with "" defaults
// (so kro defaults the object to {} on its CRD and the name expression always
// has a value), and the workload container carries literal DATABASE_URL /
// REDIS_URL secretKeyRef entries next to — not instead of — PORT and the
// env-map ConfigMap.
func TestSeedTemplatesWorkloadConnectionsRenderSecretEnv(t *testing.T) {
	for _, tc := range connectionsCases {
		t.Run(tc.file, func(t *testing.T) {
			rgd := buildSeedRGD(t, tc.file)

			// kro SimpleSchema: nested object → nested map; each slot a string
			// leaf with default "" and a pattern that admits "". kro only gives
			// the connections object default {} when a child has a default, and
			// the apiserver rejects a CRD whose default fails its own pattern.
			conns, found, err := unstructured.NestedMap(rgd.Object, "spec", "schema", "spec", "connections")
			if err != nil || !found {
				t.Fatalf("RGD schema has no nested connections object (found=%t err=%v)", found, err)
			}
			for _, slot := range []string{"database", "cache"} {
				leaf, _ := conns[slot].(string)
				if !strings.HasPrefix(leaf, `string | default="" `) {
					t.Errorf("connections.%s = %q, want a string leaf with default \"\"", slot, leaf)
				}
				if !strings.Contains(leaf, connSlotPattern) {
					t.Errorf("connections.%s = %q, want %s", slot, leaf, connSlotPattern)
				}
			}
			if len(conns) != 2 {
				t.Errorf("connections slots = %v, want exactly database + cache", conns)
			}

			container := connectionsContainer(t, rgd, tc.workloadID, tc.podSpecPath, tc.container)
			env, order := envByName(t, container)
			assertSecretEnv(t, env, "DATABASE_URL", connDatabaseSecretName, connDatabaseOptional)
			assertSecretEnv(t, env, "REDIS_URL", connCacheSecretName, connCacheOptional)

			if tc.file == "simple-webapp.yaml" {
				port, ok := env["PORT"]
				if !ok || port["value"] != "${string(schema.spec.port)}" {
					t.Fatalf("PORT env = %#v, want the port input", port)
				}
				if order[0] != "PORT" {
					t.Fatalf("env order = %v, want PORT first", order)
				}
			}

			envFrom, _ := container["envFrom"].([]any)
			if len(envFrom) != 1 {
				t.Fatalf("envFrom = %#v, want the env-map ConfigMap only", container["envFrom"])
			}
			cm, _, _ := unstructured.NestedString(envFrom[0].(map[string]any), "configMapRef", "name")
			if cm != tc.envConfigMap {
				t.Fatalf("envFrom configMapRef = %q, want %q", cm, tc.envConfigMap)
			}
		})
	}
}

// assertCredentialsContract pins the producer side of connections: the
// template's credentials Secret is named wantName and its pwgen Job writes the
// full connection URI under key uri.
func assertCredentialsContract(t *testing.T, rgd *unstructured.Unstructured, wantName string) {
	t.Helper()
	creds := findResource(t, rgd, "credentials")
	if creds == nil {
		t.Fatal("RGD has no credentials Secret")
	}
	if kind, _, _ := unstructured.NestedString(creds, "template", "kind"); kind != "Secret" {
		t.Fatalf("credentials kind = %q, want Secret", kind)
	}
	if name, _, _ := unstructured.NestedString(creds, "template", "metadata", "name"); name != wantName {
		t.Fatalf("credentials Secret name = %q, want %q (workload connections resolve to it)", name, wantName)
	}
	pwgen := findResource(t, rgd, "pwgen")
	if pwgen == nil {
		t.Fatal("RGD has no pwgen Job")
	}
	containers, _, _ := unstructured.NestedSlice(pwgen, "template", "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("pwgen containers = %#v, want one", containers)
	}
	command, _, _ := unstructured.NestedStringSlice(containers[0].(map[string]any), "command")
	if script := strings.Join(command, " "); !strings.Contains(script, `\"uri\":`) {
		t.Fatalf("pwgen Job does not write the uri key: %q", script)
	}
}

// TestSeedTemplatesConnectionTargetsPublishURI pins both producers the
// connection slots point at.
func TestSeedTemplatesConnectionTargetsPublishURI(t *testing.T) {
	for file, want := range map[string]string{
		"database.yaml":    "${schema.spec.name}-db-credentials",
		"redis-cache.yaml": "${schema.spec.name}-credentials",
	} {
		t.Run(file, func(t *testing.T) {
			assertCredentialsContract(t, buildSeedRGD(t, file), want)
		})
	}
}

// TestSeedTemplatesConnectionsSurviveDevOverlay proves the connection env
// reaches development mode: the overlay copies the production container's
// literal env list into the dev Deployment (a CEL-built list would not
// survive), so a dev sandbox gets the same DATABASE_URL / REDIS_URL.
func TestSeedTemplatesConnectionsSurviveDevOverlay(t *testing.T) {
	for _, tc := range connectionsCases {
		if tc.devWorkloadID == "" {
			continue
		}
		t.Run(tc.file, func(t *testing.T) {
			rgd := buildSeedRGD(t, tc.file)

			prod, _ := envByName(t, connectionsContainer(t, rgd, tc.workloadID, tc.podSpecPath, tc.container))
			dev, _ := envByName(t, connectionsContainer(t, rgd, tc.devWorkloadID, []string{"spec", "template", "spec"}, tc.container))
			for _, name := range []string{"DATABASE_URL", "REDIS_URL"} {
				if !reflect.DeepEqual(dev[name], prod[name]) {
					t.Fatalf("dev overlay %s = %#v, want the production entry %#v", name, dev[name], prod[name])
				}
			}
			assertSecretEnv(t, dev, "DATABASE_URL", connDatabaseSecretName, connDatabaseOptional)
			assertSecretEnv(t, dev, "REDIS_URL", connCacheSecretName, connCacheOptional)
			if _, ok := dev["FAROS_DEV_START_COMMAND"]; !ok {
				t.Fatal("dev container lacks the runtime-supervisor env; wrong container inspected")
			}
			if tc.file == "simple-webapp.yaml" {
				if _, ok := dev["PORT"]; !ok {
					t.Fatal("dev overlay dropped PORT")
				}
			}
		})
	}
}
