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

package providers

// Rendering self-hosting install instructions.
//
// A provider declares how it is deployed once, in CatalogEntry.spec.selfHosting,
// and the hub turns that into the exact commands one organization needs to run
// its own copy against the workspace the hub just created for it. The provider
// is the only party that knows its chart; the hub is the only party that knows
// the org's workspace, credential, and hub URL. Neither could produce these
// instructions alone, which is why this lives here rather than in a doc.
//
// Everything rendered is deployment metadata. Nothing here grants privilege:
// the credential the instructions reference is minted separately and is
// workspace-scoped, and a provider cannot influence what it can reach by
// changing anything in this file's inputs.

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// selfHostingDocsURL is a nil-safe accessor for the catalog DTO.
func selfHostingDocsURL(p Provider) string {
	if p.SelfHosting == nil {
		return ""
	}
	return p.SelfHosting.DocsURL
}

// KubeconfigSecretName is the Secret the provider charts read their kcp
// credential from. The charts hardcode the data key `kubeconfig` and default
// the secret name to this, so the generated instructions must match exactly —
// a mismatch produces a pod that starts and then fails to reach kcp.
const KubeconfigSecretName = "faros-provider-kubeconfig"

// KubeconfigSecretKey is the required data key inside that Secret.
const KubeconfigSecretKey = "kubeconfig"

// InstallStep is one shell command, with a human explanation of what it does.
// Kept as discrete steps rather than one blob so the portal can render them
// individually with per-step copy buttons, and so a user who already has, say,
// the namespace can skip a step knowingly.
type InstallStep struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

// ResolvedValue is one Helm value in the generated command, carried alongside
// the command so the portal can explain each one and flag the ones the hub
// could not fill in.
type ResolvedValue struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	// Unresolved marks a value the installer must supply themselves — the
	// command contains a placeholder that will not work as-is.
	Unresolved bool `json:"unresolved,omitempty"`
}

// InstallInstructions is everything an org needs to stand up its own copy.
type InstallInstructions struct {
	Provider      string `json:"provider"`
	WorkspacePath string `json:"workspacePath"`
	Namespace     string `json:"namespace"`
	ReleaseName   string `json:"releaseName"`
	ChartRef      string `json:"chartRef"`
	ChartVersion  string `json:"chartVersion,omitempty"`
	// KubeconfigFilename is what the instructions assume the credential was
	// saved as, relative to the working directory.
	KubeconfigFilename string          `json:"kubeconfigFilename"`
	Steps              []InstallStep   `json:"steps"`
	Values             []ResolvedValue `json:"values,omitempty"`
	DocsURL            string          `json:"docsURL,omitempty"`
	// ValuesDoc is the chart's values reference in Markdown, carried inline by
	// the provider so the portal can render it without leaving the install
	// panel and without reaching the internet.
	ValuesDoc string `json:"valuesDoc,omitempty"`
	// Warnings surface anything the operator must resolve by hand before the
	// commands will work.
	Warnings []string `json:"warnings,omitempty"`
}

// APIExportIdentityReader reads an APIExport's kcp identity hash from a
// workspace. Implemented by *Provisioner; declared as an interface so the REST
// layer can depend on the capability rather than the concrete type.
type APIExportIdentityReader interface {
	ResolveAPIExportIdentityHash(ctx context.Context, workspacePath, exportName string) (string, error)
}

// IdentityResolver resolves an APIExport's kcp identity hash. Implemented by
// the hub against kcp; separated so instruction rendering stays unit-testable
// without a cluster.
type IdentityResolver interface {
	// ResolveIdentityHash returns the identity hash of exportName as visible to
	// orgUUID, or "" when it cannot be determined.
	ResolveIdentityHash(orgUUID, exportName string) string
}

// InstallOptions carries the per-organization facts the provider's own
// metadata cannot know.
type InstallOptions struct {
	// ProviderName is the name the org registered its copy under.
	ProviderName string
	// WorkspacePath is the org provider workspace the credential targets.
	WorkspacePath string
	// HubURL is the address the org's cluster reaches this hub at. It must be
	// externally reachable: the provider runs outside the platform, so an
	// in-cluster service DNS name would be unroutable for it.
	HubURL string
	// Identities resolves identity-hash values. Optional; when nil those values
	// are emitted as placeholders with a warning.
	Identities IdentityResolver
	// OrgUUID scopes identity resolution.
	OrgUUID string
}

// RenderInstallInstructions builds the install steps for one provider.
//
// It never fails: a provider whose metadata is incomplete still produces
// instructions, with placeholders and warnings for the parts the hub could not
// fill in. Returning an error instead would leave the user with a freshly
// created workspace, a live credential, and nothing telling them what to do
// next — strictly worse than imperfect instructions that say what is missing.
func RenderInstallInstructions(sh *SelfHosting, opts InstallOptions) InstallInstructions {
	name := opts.ProviderName
	namespace := firstNonEmpty(sh.namespaceOrEmpty(), "faros-provider-"+name)
	release := firstNonEmpty(sh.releaseOrEmpty(), name)
	kubeconfigFile := fmt.Sprintf("%s.kubeconfig", name)

	out := InstallInstructions{
		Provider:           name,
		WorkspacePath:      opts.WorkspacePath,
		Namespace:          namespace,
		ReleaseName:        release,
		KubeconfigFilename: kubeconfigFile,
	}
	if sh != nil {
		out.DocsURL = sh.DocsURL
		out.ValuesDoc = sh.ValuesDoc
		out.ChartVersion = sh.ChartVersion
	}

	chartRef := ""
	if sh != nil && sh.ChartRepo != "" && sh.ChartName != "" {
		chartRef = strings.TrimRight(sh.ChartRepo, "/") + "/" + sh.ChartName
	} else {
		chartRef = "<chart>"
		out.Warnings = append(out.Warnings,
			"This provider does not publish Helm chart coordinates, so the install command is incomplete. Ask the provider's maintainer for its chart, or consult its documentation.")
	}
	out.ChartRef = chartRef

	// Values the hub supplies when the recipe does not. These predate recipes
	// being able to name them, and they describe one particular way of
	// installing — so a recipe that names the same value knows better and wins.
	// Without that precedence the command carries the same --set twice, or
	// carries a flag for a mode the recipe is not using.
	declaredNames := map[string]bool{}
	if sh != nil {
		for _, v := range sh.RequiredValues {
			declaredNames[v.Name] = true
		}
	}
	var values []ResolvedValue
	addDefault := func(v ResolvedValue) {
		if !declaredNames[v.Name] {
			values = append(values, v)
		}
	}

	addDefault(ResolvedValue{
		Name:        "providerKubeconfig.secretName",
		Value:       KubeconfigSecretName,
		Description: "Secret holding the workspace-scoped kcp credential created in step 2.",
	})
	addDefault(ResolvedValue{
		Name:        "catalogEntry.enabled",
		Value:       "true",
		Description: "Registers the provider with faros so your workspaces can enable it.",
	})
	// hub.url must be the external address: the provider runs in the org's own
	// cluster, where an in-cluster service DNS name does not resolve.
	switch {
	case declaredNames["hub.url"]:
		// The recipe names it; resolved below with the rest of its values.
	case opts.HubURL != "":
		addDefault(ResolvedValue{
			Name:        "hub.url",
			Value:       opts.HubURL,
			Description: "The faros hub address your cluster reaches. Must be reachable from inside that cluster.",
		})
	default:
		addDefault(ResolvedValue{
			Name:        "hub.url",
			Value:       "<hub-url>",
			Description: "The faros hub address your cluster reaches.",
			Unresolved:  true,
		})
		out.Warnings = append(out.Warnings,
			"This hub has no external URL configured, so hub.url could not be filled in. Set it to an address your cluster can reach.")
	}

	// Provider-declared values, with identity hashes resolved where possible.
	if sh != nil {
		declared := append([]SelfHostingValue(nil), sh.RequiredValues...)
		sort.Slice(declared, func(i, j int) bool { return declared[i].Name < declared[j].Name })
		for _, v := range declared {
			if v.Name == "" {
				continue
			}
			resolved := ResolvedValue{Name: v.Name, Description: v.Description}
			switch {
			case v.IdentityFor != "":
				hash := ""
				if opts.Identities != nil {
					hash = opts.Identities.ResolveIdentityHash(opts.OrgUUID, v.IdentityFor)
				}
				if hash != "" {
					resolved.Value = hash
				} else {
					// A wrong or missing identity hash produces a provider that
					// binds successfully and then silently sees none of the
					// resources it claimed — so say so loudly rather than
					// letting it look like a working command.
					resolved.Value = "<identity-hash>"
					resolved.Unresolved = true
					out.Warnings = append(out.Warnings, fmt.Sprintf(
						"Could not resolve the identity hash for %s. Fill in %s manually — an incorrect value leaves the provider running but unable to see the resources it claims.",
						v.IdentityFor, v.Name))
				}
			case v.Value != "":
				expanded := expandPlaceholders(v.Value, opts.HubURL, out)
				// A placeholder the hub could not fill (e.g. {{hubURL}} on a hub
				// with no external URL configured) must not be pasted verbatim
				// into a command as if it were a real value.
				if strings.Contains(expanded, "{{") || expanded == "" {
					resolved = unresolvedValue(resolved, v.Name, &out)
				} else {
					resolved.Value = expanded
				}
			default:
				// The provider named a value but gave no way to fill it. Before
				// asking the user, check whether it is one the hub already knows
				// authoritatively — its own address, the Secret it just told them
				// to create. Recipes written before those placeholders existed
				// (or that simply forgot one) should not push a lookup onto the
				// user for something the hub can answer.
				if known := hubKnownValue(v.Name, opts.HubURL); known != "" {
					resolved.Value = known
				} else {
					resolved = unresolvedValue(resolved, v.Name, &out)
				}
			}
			values = append(values, resolved)
		}
	}
	out.Values = values

	helm := &strings.Builder{}
	fmt.Fprintf(helm, "helm upgrade --install %s %s", release, chartRef)
	if out.ChartVersion != "" {
		fmt.Fprintf(helm, " --version %s", out.ChartVersion)
	}
	fmt.Fprintf(helm, " \\\n  --namespace %s", namespace)
	for _, v := range values {
		fmt.Fprintf(helm, " \\\n  --set %s=%s", v.Name, shellQuote(v.Value))
	}

	out.Steps = []InstallStep{
		{
			Title: "Create the namespace",
			// Which credential these run with is the one thing the commands
			// cannot show, and getting it wrong fails deep inside helm rather
			// than up front. Three kubeconfigs are plausibly in scope here: the
			// one shown above (kcp, and only ever the content of a Secret), an
			// edge kubeconfig from `faros kubeconfig edge` (which authenticates
			// as the agent, whose ClusterRole is an allowlist that excludes
			// provider API groups), and the cluster-admin credential these
			// actually need. Say so where the reader is looking.
			Description: "Where the provider runs in your cluster. Run every command below with your OWN " +
				"cluster-admin credentials for that cluster — not the kubeconfig shown above, and not a " +
				"`faros kubeconfig edge` one.",
			Command: fmt.Sprintf("kubectl create namespace %s", namespace),
		},
		{
			Title: "Store the credential",
			Description: fmt.Sprintf(
				"Save the kubeconfig shown above as %s first. The data key must be %q — the chart reads that exact key.",
				kubeconfigFile, KubeconfigSecretKey),
			Command: fmt.Sprintf(
				"kubectl --namespace %s create secret generic %s \\\n  --from-file=%s=./%s",
				namespace, KubeconfigSecretName, KubeconfigSecretKey, kubeconfigFile),
		},
		{
			Title: "Install the provider",
			Description: "The chart's init job registers the provider into your organization's workspace. " +
				"It installs a CRD, ClusterRoles and its own custom resource, so it needs cluster-admin " +
				"in the target cluster.",
			Command: helm.String(),
		},
	}
	return out
}

// hubKnownValue answers a required value the hub can fill in authoritatively,
// or "" when it cannot.
//
// This is a safety net, not the primary path: a recipe should reference these
// through placeholders. It exists because the recipe travels with the provider's
// chart, so a cluster running a chart published before a placeholder existed
// would otherwise ask the user to look up something the hub is holding — and
// the hub's own address is the value most likely to be typed wrong.
func hubKnownValue(name, hubURL string) string {
	switch name {
	case "hub.url", "hub.externalURL", "hub.internalURL":
		return hubURL
	case "providerKubeconfig.secretName",
		"bootstrap.kcpKubeconfigSecretRef.name",
		"operator.providerKubeconfigSecret.name":
		return KubeconfigSecretName
	case "bootstrap.kcpKubeconfigSecretRef.key",
		"operator.providerKubeconfigSecret.key":
		return KubeconfigSecretKey
	default:
		return ""
	}
}

// unresolvedValue marks a value the installer must supply and records why.
func unresolvedValue(v ResolvedValue, name string, out *InstallInstructions) ResolvedValue {
	v.Value = "<value>"
	v.Unresolved = true
	out.Warnings = append(out.Warnings, fmt.Sprintf("Set %s before installing.", name))
	return v
}

// expandPlaceholders substitutes the handful of per-installation facts a
// provider's recipe cannot know when it is authored: which organization's
// workspace this copy targets, and what the hub called the surrounding
// resources.
//
// Deliberately a fixed, tiny token set rather than a template language. These
// values are authored by providers and rendered into commands a user pastes
// into a shell, so the substitution surface is kept to things the hub already
// computed and nothing that could introduce new syntax.
func expandPlaceholders(v, hubURL string, ctx InstallInstructions) string {
	replacer := strings.NewReplacer(
		"{{workspacePath}}", ctx.WorkspacePath,
		"{{namespace}}", ctx.Namespace,
		"{{releaseName}}", ctx.ReleaseName,
		"{{kubeconfigSecret}}", KubeconfigSecretName,
		"{{kubeconfigSecretKey}}", KubeconfigSecretKey,
		"{{hubURL}}", hubURL,
	)
	return replacer.Replace(v)
}

func (s *SelfHosting) namespaceOrEmpty() string {
	if s == nil {
		return ""
	}
	return s.Namespace
}

func (s *SelfHosting) releaseOrEmpty() string {
	if s == nil {
		return ""
	}
	return s.ReleaseName
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// shellQuote single-quotes a value when it contains anything the shell would
// interpret. The generated commands are meant to be pasted into a terminal, so
// a value carrying a space or a glob must survive that trip intact.
func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	safe := func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '<' || r == '>' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	if strings.IndexFunc(v, func(r rune) bool { return !safe(r) }) < 0 {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
