/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// Dev-overlay synthesis (docs/app-studio-template-sandboxes.md §1, §6.1).
//
// A Template that declares spec.development gets its RGD mechanically
// extended so instances provisioned with farosMode: development run the
// declared components on platform-managed dev images with the faros-dev-agent,
// while everything else in the graph (databases, routes, services) runs
// exactly as declared. Template authors write the development block, never a
// second graph; this file is the "backend-synthesized overlay" decided in the
// design doc (kro CEL conditionals were rejected — the string-templating
// gotchas live in Go here instead, tested).
//
// Mechanics, per declared component:
//
//   - The production workload resource (graph id == component name, or
//     component name + "Deployment") gets includeWhen
//     ${schema.spec.farosMode != "development"} appended.
//   - A dev variant of the workload is synthesized (same Kubernetes name, so
//     the production Service selectors keep routing) with includeWhen
//     == "development". It contains three deliberately separate processes:
//     a platform coordinator, the production-authority app runtime, and a
//     stateless executor. Only the app keeps the production container's env,
//     mounts, and service-account contract.
//   - A per-component RWO workspace PVC, a separate fixed-size platform-state
//     PVC, and a per-component control Service are added.
//
// Once per template, an instance-wide control-token Secret + generator Job
// (the sandbox-runner pattern) is added, and the RGD status is extended with
// controlSecretRef + components.<name>.controlServiceRef so the declared
// dataPlane components can resolve.

const (
	// The first two ports are public and are part of the existing data-plane
	// Service contract. The latter two are private sidecar ports: the
	// coordinator reaches the runtime supervisor and stateless executor over
	// the pod network.
	devAgentPort         = 7070
	devExecPort          = 7071
	devRuntimePort       = 7072
	devExecRunnerPort    = 7073
	devPlatformStateDir  = "/faros/state"
	devRuntimeAddress    = "127.0.0.1:7072"
	devExecutorAddress   = "127.0.0.1:7073"
	devServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

	// devAgentBinDir is where the injector init container installs the agent
	// binary and the dev container executes it from.
	devAgentBinDir = "/faros/bin"

	// devModeCondition / prodModeCondition are the includeWhen expressions
	// keyed on the platform-injected farosMode instance field.
	devModeCondition  = `${schema.spec.farosMode == "development"}`
	prodModeCondition = `${schema.spec.farosMode != "development"}`

	// PVC sizes are deliberately constants for now — knobs here would be
	// tenant-facing API surface.
	devWorkspaceSize     = "1Gi"
	devPlatformStateSize = "1Gi"

	// Provider Actions uses a short-lived projected service-account token for
	// the coordinator-to-hub exchange. The app sees only the atomically
	// refreshed token file, never this bootstrap projection.
	devActionsBootstrapVolumeName = "faros-actions-bootstrap"
	devActionsTokenVolumeName     = "faros-actions-token"
	devActionsBootstrapDir        = "/var/run/secrets/faros/actions-bootstrap"
	devActionsDir                 = "/var/run/secrets/faros/actions"
	devActionsBootstrapAudience   = "faros-provider-actions-bootstrap"
	devActionsTokenExpiration     = int64(600)
	devActionsTokenFile           = devActionsDir + "/token"
	// The public CA bundle is optional. The ConfigMap is included only when
	// App Studio supplied non-empty action trust material; the pod volume and
	// trust environment remain harmless when that ConfigMap is absent.
	devActionsCABundleVolumeName = "faros-actions-ca-bundle"
	// Keep the mounted file in a dedicated directory so the ConfigMap cannot
	// mask the image's system CA directory; trust envs augment, rather than
	// replace, the system roots.
	devActionsCABundlePath = "/etc/faros/actions-ca/faros-actions-ca-bundle.pem"

	// Provider Actions context is optional for development sandboxes. Keep the
	// fields in every development RGD so the coordinator/app contract is
	// stable, but default omitted values to empty strings: kro evaluates every
	// referenced schema field even when a project has no action grant.
	devActionsSchemaFieldMarker = `string | default=""`

	devTokenBootstrapActiveDeadlineSeconds = int64(120)
)

// applyDevOverlay extends simpleSpec (farosMode field), the resource graph,
// and the status mapping for a Template with a development block. Returns the
// extended resources and status; simpleSpec is mutated in place.
func applyDevOverlay(tmpl *infrav1alpha1.Template, simpleSpec map[string]any, resources []any, status map[string]any, tokens map[string]string) ([]any, map[string]any, error) {
	dev := tmpl.Spec.Development

	// The RGD's own schema must accept the farosMode field the platform
	// injects into the kcp-side CRD, and the includeWhen expressions below
	// reference it.
	if _, exists := simpleSpec[infrav1alpha1.FarosModeField]; exists {
		return nil, nil, fmt.Errorf("template %q: schema declares reserved field %q", tmpl.Name, infrav1alpha1.FarosModeField)
	}
	simpleSpec[infrav1alpha1.FarosModeField] = fmt.Sprintf("string | enum=%q default=%q",
		infrav1alpha1.FarosModeProduction+","+infrav1alpha1.FarosModeDevelopment, infrav1alpha1.FarosModeProduction)
	// These fields are reserved platform inputs for the development Provider
	// Actions contract. App Studio supplies the trusted values on the binding;
	// the overlay projects them into coordinator/app environment without
	// exposing the bootstrap service-account token to the app. The CA bundle is
	// public trust material and defaults empty, so it never replaces system
	// roots for actionless or production-mode instances.
	for _, field := range []string{
		infrav1alpha1.FarosActionsExchangeURLField,
		infrav1alpha1.FarosActionsBaseURLField,
		infrav1alpha1.FarosActionsTenantPathField,
		infrav1alpha1.FarosActionsOrgField,
		infrav1alpha1.FarosActionsWorkspaceField,
		infrav1alpha1.FarosActionsProjectField,
		infrav1alpha1.FarosActionsProjectUIDField,
		infrav1alpha1.FarosActionsEnvironmentField,
		infrav1alpha1.FarosActionsInstanceField,
		infrav1alpha1.FarosActionsCABundleField,
	} {
		if _, exists := simpleSpec[field]; exists {
			return nil, nil, fmt.Errorf("template %q: schema declares reserved field %q", tmpl.Name, field)
		}
		simpleSpec[field] = devActionsSchemaFieldMarker
	}
	if _, exists := simpleSpec[infrav1alpha1.FarosNetworkPhaseField]; exists {
		return nil, nil, fmt.Errorf("template %q: schema declares reserved field %q", tmpl.Name, infrav1alpha1.FarosNetworkPhaseField)
	}
	simpleSpec[infrav1alpha1.FarosNetworkPhaseField] = fmt.Sprintf("string | enum=%q default=%q",
		infrav1alpha1.FarosNetworkPhaseSetup+","+infrav1alpha1.FarosNetworkPhaseRuntime, infrav1alpha1.FarosNetworkPhaseSetup)

	agentImage := tokens[devAgentImageToken]
	if agentImage == "" {
		return nil, nil, fmt.Errorf("template %q: dev agent image is not configured; set FAROS_DEV_AGENT_IMAGE", tmpl.Name)
	}
	previewBridgeVerificationJWKS := tokens[previewBridgeVerificationJWKSConfigKey]
	runtimeClassName := strings.TrimSpace(tokens[sandboxRuntimeClassNameConfigKey])
	providerActions := true
	if dev.ProviderActions != nil {
		providerActions = *dev.ProviderActions
	}

	byID, err := indexResources(resources)
	if err != nil {
		return nil, nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}

	if status == nil {
		status = map[string]any{}
	}
	statusComponents := map[string]any{}

	// Deterministic order — a stable RGD avoids spurious revisions.
	names := make([]string, 0, len(dev.Components))
	for name := range dev.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	var namespaceExpr string
	var synthesized []any
	for _, name := range names {
		comp := dev.Components[name]

		workloadID, workload, err := findComponentWorkload(byID, name)
		if err != nil {
			return nil, nil, fmt.Errorf("template %q component %q: %w", tmpl.Name, name, err)
		}

		devImage := tokens[comp.DevImage]
		if devImage == "" {
			return nil, nil, fmt.Errorf("template %q component %q: dev image token %s is not configured; set %s",
				tmpl.Name, name, comp.DevImage, devImageEnvName(comp.DevImage))
		}

		// Gate the production workload out of development mode.
		if err := appendIncludeWhen(workload, prodModeCondition); err != nil {
			return nil, nil, fmt.Errorf("template %q component %q (%s): %w", tmpl.Name, name, workloadID, err)
		}

		devRes, ns, err := synthesizeComponent(
			tmpl.Name,
			name,
			comp,
			workloadID,
			workload,
			devImage,
			agentImage,
			previewBridgeVerificationJWKS,
			runtimeClassName,
			providerActions,
			byID,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("template %q component %q: %w", tmpl.Name, name, err)
		}
		if namespaceExpr == "" {
			namespaceExpr = ns
		}
		synthesized = append(synthesized, devRes...)

		statusComponents[name] = map[string]any{
			"controlServiceRef": map[string]any{
				"name":      fmt.Sprintf("${%sDevControlService.metadata.name}", name),
				"namespace": fmt.Sprintf("${%sDevControlService.metadata.namespace}", name),
			},
		}
	}

	tokenRes, err := synthesizeControlToken(tmpl.Name, namespaceExpr, agentImage, byID)
	if err != nil {
		return nil, nil, err
	}
	synthesized = append(synthesized, tokenRes...)

	// Status additions — author-declared keys win.
	if _, ok := status["runtimeNamespace"]; !ok {
		status["runtimeNamespace"] = "${farosDevControlSecret.metadata.namespace}"
	}
	if _, ok := status["controlSecretRef"]; !ok {
		status["controlSecretRef"] = map[string]any{
			"name":      "${farosDevControlSecret.metadata.name}",
			"namespace": "${farosDevControlSecret.metadata.namespace}",
		}
	}
	if _, ok := status["components"]; !ok {
		status["components"] = statusComponents
	}

	return append(resources, synthesized...), status, nil
}

// findComponentWorkload maps a development component name to its workload
// resource: graph id == name, or name + "Deployment" (the convention the seed
// templates follow: frontend → frontendDeployment, runner → runnerDeployment).
func findComponentWorkload(byID map[string]map[string]any, name string) (string, map[string]any, error) {
	for _, id := range []string{name, name + "Deployment"} {
		if res, ok := byID[id]; ok {
			kind, _, _ := nestedString(res, "template", "kind")
			if kind != "Deployment" {
				return "", nil, fmt.Errorf("workload resource %q is a %s; only Deployment components are supported", id, kind)
			}
			return id, res, nil
		}
	}
	return "", nil, fmt.Errorf("no graph resource with id %q or %q", name, name+"Deployment")
}

// synthesizeComponent builds the dev-mode resources for one component: the
// workspace PVC, the dev variant of the workload, and the control Service.
// Returns the resources plus the namespace expression the workload deploys to.
func synthesizeComponent(templateName, name string, comp infrav1alpha1.TemplateDevelopmentComponent, workloadID string, workload map[string]any, devImage, agentImage, previewBridgeVerificationJWKS, runtimeClassName string, providerActions bool, byID map[string]map[string]any) ([]any, string, error) {
	prodTemplate, _ := workload["template"].(map[string]any)
	namespace, _, _ := nestedString(prodTemplate, "metadata", "namespace")
	if namespace == "" {
		return nil, "", fmt.Errorf("workload %q declares no metadata.namespace", workloadID)
	}
	workloadName, _, _ := nestedString(prodTemplate, "metadata", "name")
	if workloadName == "" {
		return nil, "", fmt.Errorf("workload %q declares no metadata.name", workloadID)
	}

	for _, id := range []string{name + "DevWorkspace", name + "DevPlatformState", name + "DevDeployment", name + "DevControlService", name + "DevCABundle"} {
		if _, taken := byID[id]; taken {
			return nil, "", fmt.Errorf("graph already declares resource id %q (reserved for the dev overlay)", id)
		}
	}

	workingDir := strings.TrimSpace(comp.WorkingDir)
	if workingDir == "" {
		workingDir = "/workspace"
	}

	pvcName := "${schema.spec.name}-dev-" + name
	labels := devLabels(templateName)

	devDeployment, selector, mountedWorkspace, err := synthesizeDevDeployment(
		name,
		comp,
		prodTemplate,
		devImage,
		agentImage,
		previewBridgeVerificationJWKS,
		runtimeClassName,
		providerActions,
		workingDir,
		pvcName,
		"${schema.spec.name}-dev-"+name+"-platform-state",
		name+"DevCABundle",
	)
	if err != nil {
		return nil, "", err
	}

	out := []any{devDeployment}
	// The per-component workspace PVC exists only when the overlay added the
	// workspace mount. A workload that already mounts a volume at workingDir
	// (the sandbox-runner's own PVC) keeps its wiring — the preserve rule.
	if mountedWorkspace {
		out = append(out, map[string]any{
			"id":          name + "DevWorkspace",
			"includeWhen": []any{devModeCondition},
			"template": map[string]any{
				"apiVersion": "v1",
				"kind":       "PersistentVolumeClaim",
				"metadata": map[string]any{
					"name":      pvcName,
					"namespace": namespace,
					"labels":    labels,
				},
				"spec": map[string]any{
					"accessModes": []any{"ReadWriteOnce"},
					"resources":   map[string]any{"requests": map[string]any{"storage": devWorkspaceSize}},
				},
			},
		})
	}
	out = append(out, map[string]any{
		"id":          name + "DevPlatformState",
		"includeWhen": []any{devModeCondition},
		"template": map[string]any{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata": map[string]any{
				"name":      "${schema.spec.name}-dev-" + name + "-platform-state",
				"namespace": namespace,
				"labels":    labels,
			},
			"spec": map[string]any{
				"accessModes": []any{"ReadWriteOnce"},
				"resources":   map[string]any{"requests": map[string]any{"storage": devPlatformStateSize}},
			},
		},
	})
	// The CA is public trust material, so a ConfigMap is sufficient. Keep one
	// per dev component even when the value is empty: that makes the generated
	// Deployment's volume reference unconditional and therefore never dangling.
	// The empty file is mounted in a dedicated directory, while trust envs are
	// empty in the default case and leave image/system roots untouched.
	out = append(out, map[string]any{
		"id":          name + "DevCABundle",
		"includeWhen": []any{devModeCondition},
		"template": map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      "${schema.spec.name}-dev-" + name + "-actions-ca",
				"namespace": namespace,
				"labels":    labels,
			},
			"data": map[string]any{
				"ca-bundle.pem": "${schema.spec." + infrav1alpha1.FarosActionsCABundleField + "}",
			},
		},
	})

	controlService := map[string]any{
		"id":          name + "DevControlService",
		"includeWhen": []any{devModeCondition},
		"template": map[string]any{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]any{
				"name":      "${schema.spec.name}-dev-" + name + "-control",
				"namespace": namespace,
				"labels":    labels,
			},
			"spec": map[string]any{
				"type": "ClusterIP",
				// Reachable while the dev server is still installing deps.
				"publishNotReadyAddresses": true,
				"selector":                 selector,
				"ports": []any{
					map[string]any{"name": "control", "port": int64(devAgentPort), "targetPort": int64(devAgentPort)},
					map[string]any{"name": "exec", "port": int64(devExecPort), "targetPort": int64(devExecPort)},
				},
			},
		},
	}

	return append(out, controlService), namespace, nil
}

// synthesizeDevDeployment deep-copies the production workload and turns it
// into a three-container development pod. The app container is the only
// container that inherits production authority; the coordinator and executor
// are built from scratch with their own mounts and minimal environments.
// mountedWorkspace reports whether the overlay added the workspace mount (and
// so needs the per-component workspace PVC).
func synthesizeDevDeployment(name string, comp infrav1alpha1.TemplateDevelopmentComponent, prodTemplate map[string]any, devImage, agentImage, previewBridgeVerificationJWKS, runtimeClassName string, providerActions bool, workingDir, pvcName, statePVCName, caBundleResourceID string) (dev, selector map[string]any, mountedWorkspace bool, err error) {
	tmplCopy, err := deepCopyMap(prodTemplate)
	if err != nil {
		return nil, nil, false, err
	}

	spec, _ := tmplCopy["spec"].(map[string]any)
	if spec == nil {
		return nil, nil, false, fmt.Errorf("workload has no spec")
	}
	// One dev pod, one workspace PVC; Recreate avoids the RWO rolling-update
	// deadlock (the new pod can't mount until the old one releases).
	spec["replicas"] = int64(1)
	spec["strategy"] = map[string]any{"type": "Recreate"}

	selectorLabels, _, _ := nestedMap(spec, "selector", "matchLabels")

	podSpec, _, _ := nestedMap(spec, "template", "spec")
	if podSpec == nil {
		return nil, nil, false, fmt.Errorf("workload has no pod template spec")
	}
	podTemplate, _, _ := nestedMap(spec, "template")
	podMetadata, _, _ := nestedMap(podTemplate, "metadata")
	podLabels, _, _ := nestedMap(podMetadata, "labels")
	if podLabels == nil {
		podLabels = map[string]any{}
		podMetadata["labels"] = podLabels
	}
	// NetworkPolicy resources can select the development pod without granting
	// the pod a ServiceAccount. The Instance controller owns this phase value:
	// setup is narrowly allowed during graph bootstrap, runtime is default
	// deny after readiness.
	podLabels["faros.sh/network-phase"] = "${schema.spec." + infrav1alpha1.FarosNetworkPhaseField + "}"
	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 1 {
		return nil, nil, false, fmt.Errorf("workload has %d containers; the dev overlay supports exactly one production container", len(containers))
	}
	app, _ := containers[0].(map[string]any)
	if app == nil {
		return nil, nil, false, fmt.Errorf("workload container is malformed")
	}

	appPort := firstContainerPort(app)
	app["image"] = devImage
	app["command"] = []any{devAgentBinDir + "/faros-dev-agent", "--runtime-supervisor"}
	delete(app, "args")
	app["workingDir"] = workingDir
	delete(app, "livenessProbe")
	delete(app, "readinessProbe")
	delete(app, "startupProbe")
	appReadOnlyRoot := false
	if existingSecurity, _ := app["securityContext"].(map[string]any); existingSecurity != nil {
		appReadOnlyRoot, _ = existingSecurity["readOnlyRootFilesystem"].(bool)
	}
	app["securityContext"] = devContainerSecurityContext(appReadOnlyRoot)
	if _, hasResources := app["resources"]; !hasResources {
		app["resources"] = devAppResources()
	}
	app["env"] = appendDevRuntimeEnv(app, comp, workingDir, appPort, providerActions)
	ensureContainerPort(app, "runtime", devRuntimePort)
	app["livenessProbe"] = devExecProbe(devRuntimeAddress, 1)
	app["readinessProbe"] = devExecProbe(devRuntimeAddress, 1)

	// A production volume mount at workingDir is reused for all three
	// containers. Otherwise the overlay creates the per-component workspace
	// PVC. Runtime-only mounts are deliberately separate for each container.
	mounts, _ := app["volumeMounts"].([]any)
	var extraVolumes []any
	if !hasMountPath(mounts, workingDir, true) {
		mountedWorkspace = true
		mounts = append(mounts, map[string]any{"name": "faros-dev-workspace", "mountPath": workingDir})
		extraVolumes = append(extraVolumes, map[string]any{"name": "faros-dev-workspace", "persistentVolumeClaim": map[string]any{"claimName": pvcName}})
	}
	if !hasMountPath(mounts, devAgentBinDir, false) {
		mounts = append(mounts, map[string]any{"name": "faros-dev-agent-bin", "mountPath": devAgentBinDir, "readOnly": true})
		extraVolumes = append(extraVolumes, map[string]any{"name": "faros-dev-agent-bin", "emptyDir": map[string]any{}})
	}
	if !hasMountPath(mounts, "/tmp", true) {
		mounts = append(mounts, map[string]any{"name": "faros-dev-runtime-tmp", "mountPath": "/tmp"})
		extraVolumes = append(extraVolumes, map[string]any{"name": "faros-dev-runtime-tmp", "emptyDir": map[string]any{}})
	}
	workspaceMount := mountForPath(mounts, workingDir, true)
	if workspaceMount == nil {
		return nil, nil, false, fmt.Errorf("development workspace mount is unavailable")
	}
	agentBinMount := mountForPath(mounts, devAgentBinDir, false)
	if agentBinMount == nil {
		return nil, nil, false, fmt.Errorf("development agent-bin mount is unavailable")
	}
	app["volumeMounts"] = mounts

	workspaceForSidecar := copyVolumeMount(workspaceMount, workingDir)
	stateVolume := map[string]any{"name": "faros-dev-platform-state", "persistentVolumeClaim": map[string]any{"claimName": statePVCName}}
	noServiceAccountVolume := map[string]any{"name": "faros-dev-no-serviceaccount", "emptyDir": map[string]any{}}
	caBundleVolume := map[string]any{
		"name": devActionsCABundleVolumeName,
		"configMap": map[string]any{
			"name": "${" + caBundleResourceID + ".metadata.name}",
			"items": []any{map[string]any{
				"key":  "ca-bundle.pem",
				"path": "faros-actions-ca-bundle.pem",
			}},
		},
	}
	actionsBootstrapVolume := map[string]any{
		"name": devActionsBootstrapVolumeName,
		"projected": map[string]any{
			"sources": []any{
				map[string]any{"serviceAccountToken": map[string]any{
					"path":              "token",
					"expirationSeconds": devActionsTokenExpiration,
					"audience":          devActionsBootstrapAudience,
				}},
			},
		},
	}
	actionsTokenVolume := map[string]any{"name": devActionsTokenVolumeName, "emptyDir": map[string]any{"medium": "Memory"}}
	extraVolumes = append(extraVolumes, stateVolume, noServiceAccountVolume, caBundleVolume)
	if providerActions {
		extraVolumes = append(extraVolumes, actionsBootstrapVolume, actionsTokenVolume)
	}
	// Coding-only pods receive no automatic or inherited ServiceAccount token.
	// Keep the legacy Provider Actions posture byte-for-byte when enabled: its
	// existing app SA mount and pod automount setting remain authoritative.
	if !providerActions {
		podSpec["automountServiceAccountToken"] = false
		appMounts, _ := app["volumeMounts"].([]any)
		if saMount := mountForPath(appMounts, devServiceAccountDir, false); saMount != nil {
			saMount["name"] = "faros-dev-no-serviceaccount"
			saMount["readOnly"] = true
		}
	}

	coordinator := map[string]any{
		"name":            "faros-platform-coordinator",
		"image":           agentImage,
		"imagePullPolicy": "IfNotPresent",
		// No mode flag selects the finalized default coordinator mode. The
		// coordinator owns the public :7070/:7071 servers and FAROS_DEV_STATE_DIR.
		"command": []any{"/faros-dev-agent"},
		"ports": []any{
			map[string]any{"name": "control", "containerPort": int64(devAgentPort)},
			map[string]any{"name": "exec", "containerPort": int64(devExecPort)},
		},
		"env":       devCoordinatorEnv(comp, workingDir, providerActions),
		"resources": devCoordinatorResources(),
		"volumeMounts": []any{
			workspaceForSidecar,
			map[string]any{"name": "faros-dev-platform-state", "mountPath": devPlatformStateDir},
			map[string]any{"name": "faros-dev-coordinator-tmp", "mountPath": "/tmp"},
			map[string]any{"name": "faros-dev-no-serviceaccount", "mountPath": devServiceAccountDir, "readOnly": true},
			map[string]any{"name": devActionsCABundleVolumeName, "mountPath": "/etc/faros/actions-ca", "readOnly": true},
		},
		"livenessProbe":   devHTTPProbePath(devAgentPort, 0, "/healthz"),
		"readinessProbe":  devHTTPProbePath(devAgentPort, 0, "/readyz"),
		"securityContext": devContainerSecurityContext(true),
	}
	if providerActions {
		coordinator["volumeMounts"] = append(coordinator["volumeMounts"].([]any),
			map[string]any{"name": devActionsBootstrapVolumeName, "mountPath": devActionsBootstrapDir, "readOnly": true},
			map[string]any{"name": devActionsTokenVolumeName, "mountPath": devActionsDir},
		)
	}
	extraVolumes = append(extraVolumes, map[string]any{"name": "faros-dev-coordinator-tmp", "emptyDir": map[string]any{}})

	executor := map[string]any{
		"name":            "faros-exec-runner",
		"image":           devImage,
		"imagePullPolicy": "IfNotPresent",
		"command":         []any{devAgentBinDir + "/faros-dev-agent", "--executor"},
		"workingDir":      workingDir,
		"ports":           []any{map[string]any{"name": "exec-runner", "containerPort": int64(devExecRunnerPort)}},
		"env": []any{
			map[string]any{"name": "FAROS_DEV_WORKDIR", "value": workingDir},
			map[string]any{"name": "HOME", "value": "/tmp/faros-exec-home"},
			map[string]any{"name": "TMPDIR", "value": "/tmp"},
		},
		"resources": devExecutorResources(),
		"volumeMounts": []any{
			copyVolumeMount(workspaceMount, workingDir),
			map[string]any{"name": agentBinMount["name"], "mountPath": devAgentBinDir, "readOnly": true},
			map[string]any{"name": "faros-dev-exec-tmp", "mountPath": "/tmp"},
			map[string]any{"name": "faros-dev-no-serviceaccount", "mountPath": devServiceAccountDir, "readOnly": true},
		},
		"livenessProbe":   devExecProbe(devExecutorAddress, 1),
		"readinessProbe":  devExecProbe(devExecutorAddress, 1),
		"securityContext": devContainerSecurityContext(true),
	}
	// The app gets only the refreshed token and non-secret action context. It
	// deliberately has no bootstrap volume, exchange URL, or service-account
	// token mount.
	for _, reservedPath := range []string{devActionsDir, devActionsBootstrapDir, "/etc/faros/actions-ca"} {
		if hasMountPath(mounts, reservedPath, false) {
			return nil, nil, false, fmt.Errorf("production workload already mounts reserved Provider Actions path %q", reservedPath)
		}
	}
	if providerActions {
		app["volumeMounts"] = append(app["volumeMounts"].([]any), map[string]any{
			"name": devActionsTokenVolumeName, "mountPath": devActionsDir, "readOnly": true,
		})
		app["volumeMounts"] = append(app["volumeMounts"].([]any), map[string]any{
			"name": devActionsCABundleVolumeName, "mountPath": "/etc/faros/actions-ca", "readOnly": true,
		})
	}
	// These annotations let the attestor bind the reviewed projected token to
	// the exact tenant/project instance that requested the exchange. They are
	// non-secret CEL-resolved identity context and are checked in addition to
	// the pod-bound TokenReview identity.
	podTemplateMetadata, _, _ := nestedMap(tmplCopy, "spec", "template", "metadata")
	if podTemplateMetadata == nil {
		podTemplateMetadata = map[string]any{}
		tmplSpec, _, _ := nestedMap(tmplCopy, "spec")
		tmplTemplate, _, _ := nestedMap(tmplSpec, "template")
		tmplTemplate["metadata"] = podTemplateMetadata
	}
	annotations, _ := podTemplateMetadata["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	if providerActions {
		annotations["faros.sh/actions-tenant"] = "${schema.spec." + infrav1alpha1.FarosActionsTenantPathField + "}"
		annotations["faros.sh/actions-project"] = "${schema.spec." + infrav1alpha1.FarosActionsProjectField + "}"
		annotations["faros.sh/actions-project-uid"] = "${schema.spec." + infrav1alpha1.FarosActionsProjectUIDField + "}"
		annotations["faros.sh/actions-environment"] = "${schema.spec." + infrav1alpha1.FarosActionsEnvironmentField + "}"
		annotations["faros.sh/actions-instance"] = "${schema.spec." + infrav1alpha1.FarosActionsInstanceField + "}"
	}
	podTemplateMetadata["annotations"] = annotations
	extraVolumes = append(extraVolumes, map[string]any{"name": "faros-dev-exec-tmp", "emptyDir": map[string]any{}})
	podSpec["containers"] = []any{coordinator, app, executor}

	initContainer := map[string]any{
		"name":  "faros-dev-agent-installer",
		"image": agentImage,
		// The default-for-:latest Always policy would force a registry pull
		// even when the image is side-loaded (kind/local dev) and fail the pod
		// if the registry copy is missing. Production pins digests via
		// FAROS_DEV_AGENT_IMAGE, where IfNotPresent is equivalent.
		"imagePullPolicy": "IfNotPresent",
		"command":         []any{"/faros-dev-agent", "--install", devAgentBinDir},
		"volumeMounts": []any{
			map[string]any{"name": "faros-dev-agent-bin", "mountPath": devAgentBinDir},
			map[string]any{"name": "faros-dev-no-serviceaccount", "mountPath": devServiceAccountDir, "readOnly": true},
		},
		"securityContext": devContainerSecurityContext(true),
	}
	if previewBridgeVerificationJWKS != "" {
		initContainer["env"] = []any{map[string]any{
			"name":  "FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS",
			"value": previewBridgeVerificationJWKS,
		}}
	}
	initContainers, _ := podSpec["initContainers"].([]any)
	initContainers = append(initContainers, initContainer)
	podSpec["initContainers"] = initContainers

	volumes, _ := podSpec["volumes"].([]any)
	podSpec["volumes"] = append(volumes, extraVolumes...)

	podSpec["securityContext"] = map[string]any{
		"runAsNonRoot":   true,
		"runAsUser":      int64(1000),
		"runAsGroup":     int64(1000),
		"fsGroup":        int64(1000),
		"seccompProfile": map[string]any{"type": "RuntimeDefault"},
	}
	podSpec["shareProcessNamespace"] = false
	// The PSS-restricted profile above is not a complete untrusted-code
	// sandbox: the tenant's code still shares the host kernel. When the
	// platform configures a RuntimeClass (gVisor, Kata) every development pod
	// is scheduled with it. An empty value never reaches the pod spec —
	// runtimeClassName: "" is an invalid PodSpec — so the cluster default
	// runtime applies and whatever the production workload declared is kept.
	if runtimeClassName != "" {
		podSpec["runtimeClassName"] = runtimeClassName
	}

	return map[string]any{
		"id":          name + "DevDeployment",
		"includeWhen": []any{devModeCondition},
		"template":    tmplCopy,
	}, selectorLabels, mountedWorkspace, nil
}

func mountForPath(mounts []any, target string, matchExpressions bool) map[string]any {
	var expressionMatch map[string]any
	for _, raw := range mounts {
		mount, _ := raw.(map[string]any)
		mountPath, _ := mount["mountPath"].(string)
		if mountPath == target {
			return mount
		}
		if matchExpressions && strings.Contains(mountPath, "${") {
			if expressionMatch != nil {
				return nil
			}
			expressionMatch = mount
		}
	}
	return expressionMatch
}

func copyVolumeMount(source map[string]any, mountPath string) map[string]any {
	result := map[string]any{"name": source["name"], "mountPath": mountPath}
	for _, key := range []string{"subPath", "subPathExpr"} {
		if value, ok := source[key]; ok {
			result[key] = value
		}
	}
	return result
}

func devContainerSecurityContext(readOnlyRootFilesystem bool) map[string]any {
	return map[string]any{
		"runAsNonRoot":             true,
		"runAsUser":                int64(1000),
		"runAsGroup":               int64(1000),
		"allowPrivilegeEscalation": false,
		"readOnlyRootFilesystem":   readOnlyRootFilesystem,
		"capabilities":             map[string]any{"drop": []any{"ALL"}},
	}
}

func devCoordinatorResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{"cpu": "25m", "memory": "32Mi"},
		"limits":   map[string]any{"cpu": "250m", "memory": "256Mi"},
	}
}

func devAppResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
		"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
	}
}

func devExecutorResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{"cpu": "25m", "memory": "32Mi"},
		"limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
	}
}

func devCoordinatorEnv(comp infrav1alpha1.TemplateDevelopmentComponent, workingDir string, providerActions bool) []any {
	env := []any{
		map[string]any{"name": "FAROS_DEV_WORKDIR", "value": workingDir},
		map[string]any{"name": "FAROS_DEV_STATE_DIR", "value": devPlatformStateDir},
		map[string]any{"name": "FAROS_DEV_RUNTIME_URL", "value": "http://" + devRuntimeAddress},
		map[string]any{"name": "FAROS_DEV_EXECUTOR_URL", "value": "http://" + devExecutorAddress},
	}
	if comp.Reload != nil {
		if comp.Reload.Strategy != "" {
			env = append(env, map[string]any{"name": "FAROS_DEV_RELOAD_STRATEGY", "value": comp.Reload.Strategy})
		}
		if len(comp.Reload.Rules) > 0 {
			rules, _ := json.Marshal(comp.Reload.Rules)
			env = append(env, map[string]any{"name": "FAROS_DEV_RELOAD_RULES", "value": string(rules)})
		}
	}
	if providerActions {
		env = append(env, devActionsEnv(true)...)
		env = append(env, devActionsTrustEnv(true)...)
	}
	return append(env, map[string]any{
		"name": "FAROS_DEV_CONTROL_TOKEN",
		"valueFrom": map[string]any{
			"secretKeyRef": map[string]any{"name": "${farosDevControlSecret.metadata.name}", "key": "token"},
		},
	})
}

// devActionsTrustEnv points Provider Actions TLS clients at the optional,
// public CA bundle. A CEL expression yields an empty value when App Studio
// omitted the bundle. The coordinator loads FAROS_ACTIONS_CA_FILE into a pool
// that starts with system roots; Node treats NODE_EXTRA_CA_CERTS as additive.
// Do not set SSL_CERT_FILE here: on some images it replaces, rather than
// augments, the system trust store.
func devActionsTrustEnv(coordinator bool) []any {
	path := `${schema.spec.farosActionsCABundle != "" ? "` + devActionsCABundlePath + `" : ""}`
	if coordinator {
		return []any{map[string]any{"name": "FAROS_ACTIONS_CA_FILE", "value": path}}
	}
	return []any{map[string]any{"name": "NODE_EXTRA_CA_CERTS", "value": path}}
}

func devActionsEnv(includeExchange bool) []any {
	env := []any{
		map[string]any{"name": "FAROS_ACTIONS_TOKEN_FILE", "value": devActionsTokenFile},
		map[string]any{"name": "FAROS_ACTIONS_BASE_URL", "value": "${schema.spec." + infrav1alpha1.FarosActionsBaseURLField + "}"},
		map[string]any{"name": "FAROS_PROJECT", "value": "${schema.spec." + infrav1alpha1.FarosActionsProjectField + "}"},
		map[string]any{"name": "FAROS_PROJECT_UID", "value": "${schema.spec." + infrav1alpha1.FarosActionsProjectUIDField + "}"},
		map[string]any{"name": "FAROS_ACTIONS_ENVIRONMENT", "value": "${schema.spec." + infrav1alpha1.FarosActionsEnvironmentField + "}"},
		map[string]any{"name": "FAROS_ACTIONS_INSTANCE", "value": "${schema.spec." + infrav1alpha1.FarosActionsInstanceField + "}"},
		map[string]any{"name": "FAROS_ACTIONS_TENANT_PATH", "value": "${schema.spec." + infrav1alpha1.FarosActionsTenantPathField + "}"},
		map[string]any{"name": "FAROS_ACTIONS_ORG", "value": "${schema.spec." + infrav1alpha1.FarosActionsOrgField + "}"},
		map[string]any{"name": "FAROS_ACTIONS_WORKSPACE", "value": "${schema.spec." + infrav1alpha1.FarosActionsWorkspaceField + "}"},
	}
	if includeExchange {
		env = append(env,
			map[string]any{"name": "FAROS_ACTIONS_BOOTSTRAP_TOKEN_FILE", "value": devActionsBootstrapDir + "/token"},
			map[string]any{"name": "FAROS_ACTIONS_EXCHANGE_URL", "value": "${schema.spec." + infrav1alpha1.FarosActionsExchangeURLField + "}"},
		)
	}
	return env
}

// devHTTPProbe uses the agent's immediate health endpoint. The generous
// failure threshold keeps a slow image pull or dependency install from
// removing the pod while the public Service still publishes not-ready
// addresses so initial synchronization can proceed.
func devHTTPProbe(port int64, initialDelay int64) map[string]any {
	return devHTTPProbePath(port, initialDelay, "/healthz")
}

func devHTTPProbePath(port int64, initialDelay int64, path string) map[string]any {
	return map[string]any{
		"httpGet": map[string]any{
			"path": path,
			"port": port,
		},
		"initialDelaySeconds": initialDelay,
		"periodSeconds":       int64(5),
		"timeoutSeconds":      int64(2),
		"failureThreshold":    int64(12),
		"successThreshold":    int64(1),
	}
}

// devExecProbe runs the injected static agent inside the target container.
// Runtime-supervisor and executor listeners bind only to pod loopback, so a
// kubelet tcpSocket probe against the pod IP would never reach them.
func devExecProbe(address string, initialDelay int64) map[string]any {
	return map[string]any{
		"exec": map[string]any{
			"command": []any{devAgentBinDir + "/faros-dev-agent", "--healthcheck", address},
		},
		"initialDelaySeconds": initialDelay,
		"periodSeconds":       int64(5),
		"timeoutSeconds":      int64(2),
		"failureThreshold":    int64(12),
		"successThreshold":    int64(1),
	}
}

// ensureContainerPort adds a fixed private port only when the production
// container did not already declare it. CEL-expression ports are intentionally
// not considered equal to a fixed port because they resolve per instance.
func ensureContainerPort(container map[string]any, name string, port int64) {
	if hasContainerPort(container, port) {
		return
	}
	ports, _ := container["ports"].([]any)
	container["ports"] = append(ports, map[string]any{"name": name, "containerPort": port})
}

// hasContainerPort reports whether the container already declares the numeric
// containerPort (as a JSON number; CEL-expression ports never equal the fixed
// agent port).
func hasContainerPort(container map[string]any, port int64) bool {
	ports, _ := container["ports"].([]any)
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		switch v := pm["containerPort"].(type) {
		case float64:
			if int64(v) == port {
				return true
			}
		case int64:
			if v == port {
				return true
			}
		}
	}
	return false
}

// hasMountPath reports whether any volumeMount already targets the path.
// With matchExpressions, a ${...} mountPath (resolvable only at instance
// reconcile time) also counts as a match — the conservative reading for
// paths where a resolved duplicate would fail server-side apply.
func hasMountPath(mounts []any, path string, matchExpressions bool) bool {
	for _, m := range mounts {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		mp, _ := mm["mountPath"].(string)
		if mp == path || (matchExpressions && strings.Contains(mp, "${")) {
			return true
		}
	}
	return false
}

// appendDevRuntimeEnv preserves the production container's environment and
// adds only the runtime-supervisor contract. Platform credentials are removed
// from a copied production container so an old overlay cannot leak them into
// the app process; the coordinator is the sole token consumer.
func appendDevRuntimeEnv(container map[string]any, comp infrav1alpha1.TemplateDevelopmentComponent, workingDir, port string, providerActions bool) []any {
	var env []any
	if providerActions {
		env = append(env, devActionsEnv(false)...)
		env = append(env, devActionsTrustEnv(false)...)
	}
	env = append(env, []any{
		map[string]any{"name": "FAROS_DEV_WORKDIR", "value": workingDir},
		map[string]any{"name": "FAROS_DEV_START_COMMAND", "value": comp.StartCommand},
	}...)
	if port != "" {
		env = append(env, map[string]any{"name": "FAROS_DEV_PORT", "value": port})
	}
	if comp.Reload != nil {
		if comp.Reload.Strategy != "" {
			env = append(env, map[string]any{"name": "FAROS_DEV_RELOAD_STRATEGY", "value": comp.Reload.Strategy})
		}
		if len(comp.Reload.Rules) > 0 {
			// Single-line JSON; contains no ${...}, so kro passes it through.
			rules, _ := json.Marshal(comp.Reload.Rules)
			env = append(env, map[string]any{"name": "FAROS_DEV_RELOAD_RULES", "value": string(rules)})
		}
	}
	for _, e := range []struct{ name, value string }{
		{"HOME", "/tmp/faros-runtime-home"},
		{"NPM_CONFIG_CACHE", "/tmp/faros-cache/npm"},
		{"XDG_CACHE_HOME", "/tmp/faros-cache"},
	} {
		if !hasEnv(container, e.name) {
			env = append(env, map[string]any{"name": e.name, "value": e.value})
		}
	}

	// Replace values in the platform-reserved namespace while retaining all
	// user/application environment entries, including secretKeyRef values and
	// envFrom on the app container.
	reserved := map[string]bool{
		"FAROS_DEV_CONTROL_TOKEN":            true,
		"FAROS_ACTIONS_EXCHANGE_URL":         true,
		"FAROS_ACTIONS_BOOTSTRAP_TOKEN_FILE": true,
		"FAROS_ACTIONS_CA_FILE":              true,
		"NODE_EXTRA_CA_CERTS":                true,
	}
	for _, raw := range env {
		entry, _ := raw.(map[string]any)
		if name, _ := entry["name"].(string); name != "" {
			reserved[name] = true
		}
	}
	existing, _ := container["env"].([]any)
	filtered := make([]any, 0, len(existing)+len(env))
	for _, raw := range existing {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		if reserved[name] || (!providerActions && (strings.HasPrefix(name, "FAROS_ACTIONS_") || strings.HasPrefix(name, "FAROS_PROJECT"))) {
			continue
		}
		filtered = append(filtered, raw)
	}
	return append(filtered, env...)
}

// synthesizeControlToken is the instance-wide control-token Secret + one-shot
// generator Job (the proven sandbox-runner pattern), gated to development
// mode. The token authenticates every component's data-plane control calls.
func synthesizeControlToken(templateName, namespace, agentImage string, byID map[string]map[string]any) ([]any, error) {
	for _, id := range []string{"farosDevControlSecret", "farosDevTokenAccount", "farosDevTokenRole", "farosDevTokenBinding", "farosDevTokenJob"} {
		if _, taken := byID[id]; taken {
			return nil, fmt.Errorf("template %q: graph already declares resource id %q (reserved for the dev overlay)", templateName, id)
		}
	}
	labels := devLabels(templateName)
	meta := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": namespace, "labels": labels}
	}
	include := []any{devModeCondition}

	jobSpec := map[string]any{
		"activeDeadlineSeconds": devTokenBootstrapActiveDeadlineSeconds,
		"backoffLimit":          int64(2),
		// The universal sandbox is a warm, platform-owned workspace. Its
		// completed bootstrap Job must remain present for the cache lifetime;
		// finalization removes this exact Job (and any orphaned pod) when the
		// Instance is deleted. Ordinary development templates keep their
		// existing short-lived Job behavior below.
		"template": map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec": map[string]any{
				"serviceAccountName":           "${farosDevTokenAccount.metadata.name}",
				"automountServiceAccountToken": true,
				"restartPolicy":                "OnFailure",
				"securityContext": map[string]any{
					"runAsNonRoot": true,
					"runAsUser":    int64(1001),
					"runAsGroup":   int64(1001),
					"seccompProfile": map[string]any{
						"type": "RuntimeDefault",
					},
				},
				"containers": []any{map[string]any{
					"name":            "token",
					"image":           agentImage,
					"imagePullPolicy": "IfNotPresent",
					"command":         []any{"/faros-dev-agent", "--bootstrap-control-token", "${farosDevControlSecret.metadata.name}"},
					"securityContext": map[string]any{
						"runAsNonRoot":             true,
						"runAsUser":                int64(1001),
						"runAsGroup":               int64(1001),
						"allowPrivilegeEscalation": false,
						"readOnlyRootFilesystem":   true,
						"capabilities": map[string]any{
							"drop": []any{"ALL"},
						},
					},
				}},
			},
		},
	}
	if templateName != infrav1alpha1.UniversalCodingSandboxTemplateName {
		jobSpec["ttlSecondsAfterFinished"] = int64(600)
	}

	return []any{
		map[string]any{
			"id": "farosDevControlSecret", "includeWhen": include,
			"template": map[string]any{
				"apiVersion": "v1", "kind": "Secret",
				"metadata": meta("${schema.spec.name}-dev-control"),
				"type":     "Opaque",
			},
		},
		map[string]any{
			"id": "farosDevTokenAccount", "includeWhen": include,
			"template": map[string]any{
				"apiVersion": "v1", "kind": "ServiceAccount",
				"metadata": meta("${schema.spec.name}-dev-token"),
			},
		},
		map[string]any{
			"id": "farosDevTokenRole", "includeWhen": include,
			"template": map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
				"metadata": meta("${schema.spec.name}-dev-token"),
				"rules": []any{map[string]any{
					"apiGroups":     []any{""},
					"resources":     []any{"secrets"},
					"resourceNames": []any{"${farosDevControlSecret.metadata.name}"},
					"verbs":         []any{"get", "patch"},
				}},
			},
		},
		map[string]any{
			"id": "farosDevTokenBinding", "includeWhen": include,
			"template": map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
				"metadata": meta("${schema.spec.name}-dev-token"),
				"roleRef": map[string]any{
					"apiGroup": "rbac.authorization.k8s.io", "kind": "Role",
					"name": "${farosDevTokenRole.metadata.name}",
				},
				"subjects": []any{map[string]any{
					"kind": "ServiceAccount", "name": "${farosDevTokenAccount.metadata.name}", "namespace": namespace,
				}},
			},
		},
		map[string]any{
			"id": "farosDevTokenJob", "includeWhen": include,
			"template": map[string]any{
				"apiVersion": "batch/v1", "kind": "Job",
				"metadata": meta("${schema.spec.name}-dev-token"),
				"spec":     jobSpec,
			},
		},
	}, nil
}

func devLabels(templateName string) map[string]any {
	return map[string]any{
		"app.kubernetes.io/name":       templateName,
		"app.kubernetes.io/component":  "faros-dev",
		"app.kubernetes.io/managed-by": "faros-infrastructure",
	}
}

// indexResources maps graph resources by id, verifying shape.
func indexResources(resources []any) (map[string]map[string]any, error) {
	byID := make(map[string]map[string]any, len(resources))
	for i, r := range resources {
		res, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("resources[%d] is not an object", i)
		}
		id, _ := res["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("resources[%d] has no id", i)
		}
		byID[id] = res
	}
	return byID, nil
}

// appendIncludeWhen adds a condition to a resource's includeWhen list.
func appendIncludeWhen(res map[string]any, condition string) error {
	existing, ok := res["includeWhen"]
	if !ok {
		res["includeWhen"] = []any{condition}
		return nil
	}
	list, ok := existing.([]any)
	if !ok {
		return fmt.Errorf("includeWhen is not a list")
	}
	res["includeWhen"] = append(list, condition)
	return nil
}

// firstContainerPort renders the production container's first containerPort as
// the string the dev server must bind (a number, or a ${schema.spec.*}
// expression kro resolves per instance). Empty when the container exposes none.
// A bare ${expr} is wrapped as ${string(expr)}: containerPort declares the
// expression as int, but an env var VALUE must type-check as string — kro's
// CEL validation rejects the unwrapped form ("returns type \"int\" but
// expected \"string\"", caught by the dev-mode e2e).
func firstContainerPort(container map[string]any) string {
	ports, _ := container["ports"].([]any)
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		switch v := pm["containerPort"].(type) {
		case string:
			return celStringify(v)
		case float64:
			return strconv.FormatInt(int64(v), 10)
		case int64:
			return strconv.FormatInt(v, 10)
		}
	}
	return ""
}

// celStringify wraps a bare ${expr} kro expression in a string() conversion so
// it type-checks in a string field. Literals and already-converted or
// composite values pass through untouched.
func celStringify(v string) string {
	inner, ok := strings.CutPrefix(v, "${")
	if !ok {
		return v
	}
	inner, ok = strings.CutSuffix(inner, "}")
	if !ok || strings.Contains(inner, "${") || strings.HasPrefix(inner, "string(") {
		return v
	}
	return "${string(" + inner + ")}"
}

func hasEnv(container map[string]any, name string) bool {
	env, _ := container["env"].([]any)
	for _, e := range env {
		em, _ := e.(map[string]any)
		if em != nil && em["name"] == name {
			return true
		}
	}
	return false
}

// deepCopyMap round-trips through JSON — the maps come from JSON decoding, so
// this is lossless and keeps the overlay from aliasing the production graph.
func deepCopyMap(in map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// nestedString / nestedMap are small typed readers over decoded JSON maps.
func nestedString(m map[string]any, path ...string) (string, bool, error) {
	cur := any(m)
	for _, p := range path {
		cm, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur, ok = cm[p]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

func nestedMap(m map[string]any, path ...string) (map[string]any, bool, error) {
	cur := any(m)
	for _, p := range path {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur, ok = cm[p]
		if !ok {
			return nil, false, nil
		}
	}
	out, ok := cur.(map[string]any)
	return out, ok, nil
}

// devImageEnvName maps a ${faros.devImage.<toolchain>} token to the env var
// that configures it (FAROS_DEV_IMAGE_<TOOLCHAIN>).
func devImageEnvName(token string) string {
	tc := strings.TrimSuffix(strings.TrimPrefix(token, "${faros.devImage."), "}")
	return "FAROS_DEV_IMAGE_" + strings.ToUpper(strings.ReplaceAll(tc, "-", "_"))
}
