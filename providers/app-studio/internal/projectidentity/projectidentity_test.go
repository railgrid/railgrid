/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package projectidentity

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

const testProviderReferenceGroup = "databricks.railgrid.ai"

func TestRulesCompactManyProviderReferencesBelowHubLimit(t *testing.T) {
	const references = 100
	project := emptyProject()
	for i := 0; i < references; i++ {
		name := fmt.Sprintf("table-%03d", i)
		project.Spec.Environments[0].Bindings = append(project.Spec.Environments[0].Bindings,
			providerReference(name, "tables", grant("describe-table"), grant("query-table")),
		)
	}

	rules := Rules(project)
	if len(rules) >= 64 {
		t.Fatalf("100 equivalent references produced %d rules, want fewer than the hub limit of 64", len(rules))
	}
	if len(rules) != 4 {
		t.Fatalf("100 equivalent references produced %d total rules, want 2 base rules plus one read and one action rule: %#v", len(rules), rules)
	}

	read := findNamedRule(t, rules, testProviderReferenceGroup, "tables", "get")
	actions := findActionSetRule(t, rules, testProviderReferenceGroup, []string{"tables/describe-table", "tables/query-table"})
	if len(read.ResourceNames) != references || len(actions.ResourceNames) != references {
		t.Fatalf("compacted names: read=%d action=%d, want %d each", len(read.ResourceNames), len(actions.ResourceNames), references)
	}
	for i := 0; i < references; i++ {
		want := fmt.Sprintf("table-%03d", i)
		if read.ResourceNames[i] != want || actions.ResourceNames[i] != want {
			t.Fatalf("resource names are not sorted and complete at %d: read=%q action=%q, want %q", i, read.ResourceNames[i], actions.ResourceNames[i], want)
		}
	}
	if !reflect.DeepEqual(rules, Rules(reverseBindings(project))) {
		t.Fatal("identical Project input produced nondeterministically ordered rules")
	}
}

func TestRulesPreserveProviderReferenceActionSetsAndRevocation(t *testing.T) {
	project := emptyProject()
	project.Spec.Environments[0].Bindings = []aiv1alpha1.ProjectProviderBindingSpec{
		providerReference("alpha", "tables", grant("query-table"), grant("delete-table"), unreviewedGrant("unreviewed")),
		providerReference("beta", "tables", grant("query-table")),
		providerReference("gamma", "tables", grant("delete-table"), revokedGrant("drop-table")),
		providerReference("denied", "tables", revokedGrant("query-table")),
	}

	want := permissionSet(
		"tables|alpha|get", "tables|beta|get", "tables|gamma|get",
		"tables/query-table|alpha|create", "tables/query-table|beta|create",
		"tables/delete-table|alpha|create", "tables/delete-table|gamma|create",
	)
	assertPermissions(t, Rules(project), want)

	// Reordering references must not change the emitted ClusterRole rules.
	project.Spec.Environments[0].Bindings[0], project.Spec.Environments[0].Bindings[3] =
		project.Spec.Environments[0].Bindings[3], project.Spec.Environments[0].Bindings[0]
	if got := Rules(project); !reflect.DeepEqual(got, Rules(reverseBindings(project))) {
		t.Fatal("provider reference order changed the emitted rules")
	}

	// Revoking beta's only active action removes both its action and its parent
	// read from the compacted result, while alpha keeps the same action.
	project = reverseBindings(project)
	for i := range project.Spec.Environments[0].Bindings {
		binding := &project.Spec.Environments[0].Bindings[i]
		if binding.ResourceRef != nil && binding.ResourceRef.Name == "beta" {
			binding.AllowedActions[0].Revoked = true
		}
	}
	want = permissionSet(
		"tables|alpha|get", "tables|gamma|get",
		"tables/query-table|alpha|create",
		"tables/delete-table|alpha|create", "tables/delete-table|gamma|create",
	)
	assertPermissions(t, Rules(project), want)
}

func TestRulesUnionSameProviderReferenceAcrossEnvironments(t *testing.T) {
	project := emptyProject()
	project.Spec.Environments = []aiv1alpha1.ProjectEnvironmentSpec{
		{Name: "development", Bindings: []aiv1alpha1.ProjectProviderBindingSpec{
			providerReference("shared", "tables", grant("query-table")),
		}},
		{Name: "production", Bindings: []aiv1alpha1.ProjectProviderBindingSpec{
			providerReference("shared", "tables", grant("describe-table")),
		}},
	}

	assertPermissions(t, Rules(project), permissionSet(
		"tables|shared|get",
		"tables/query-table|shared|create", "tables/describe-table|shared|create",
	))
	findActionSetRule(t, Rules(project), testProviderReferenceGroup, []string{"tables/describe-table", "tables/query-table"})
}

func emptyProject() *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{Name: "development"}},
		},
	}
}

func providerReference(name, resource string, actions ...aiv1alpha1.ProjectProviderActionSpec) aiv1alpha1.ProjectProviderBindingSpec {
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name: name, Provider: "databricks", Kind: aiv1alpha1.ProjectBindingKindProviderReference,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name: name, APIVersion: testProviderReferenceGroup + "/v1alpha1", Kind: "Resource", Resource: resource,
		},
		AllowedActions: actions,
	}
}

func grant(name string) aiv1alpha1.ProjectProviderActionSpec {
	grantedAt := metav1.NewTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	return aiv1alpha1.ProjectProviderActionSpec{
		Name: name, Version: "v1", SchemaDigest: "sha256:" + strings.Repeat("a", 64),
		GrantedBy: "test-user", GrantedAt: &grantedAt,
	}
}

func revokedGrant(name string) aiv1alpha1.ProjectProviderActionSpec {
	action := grant(name)
	action.Revoked = true
	return action
}

func unreviewedGrant(name string) aiv1alpha1.ProjectProviderActionSpec {
	action := grant(name)
	action.GrantedBy = ""
	action.GrantedAt = nil
	return action
}

func findNamedRule(t *testing.T, rules []rbacv1.PolicyRule, group, resource, verb string) rbacv1.PolicyRule {
	t.Helper()
	for _, rule := range rules {
		if len(rule.APIGroups) == 1 && rule.APIGroups[0] == group &&
			len(rule.Resources) == 1 && rule.Resources[0] == resource &&
			len(rule.Verbs) == 1 && rule.Verbs[0] == verb {
			return rule
		}
	}
	t.Fatalf("no %s rule for %s/%s in %#v", verb, group, resource, rules)
	return rbacv1.PolicyRule{}
}

func findActionSetRule(t *testing.T, rules []rbacv1.PolicyRule, group string, resources []string) rbacv1.PolicyRule {
	t.Helper()
	for _, rule := range rules {
		if len(rule.APIGroups) == 1 && rule.APIGroups[0] == group &&
			len(rule.Verbs) == 1 && rule.Verbs[0] == "create" && reflect.DeepEqual(rule.Resources, resources) {
			return rule
		}
	}
	t.Fatalf("no create rule for %s resources %v in %#v", group, resources, rules)
	return rbacv1.PolicyRule{}
}

func permissionSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func assertPermissions(t *testing.T, rules []rbacv1.PolicyRule, want map[string]struct{}) {
	t.Helper()
	got := map[string]struct{}{}
	for _, rule := range rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != testProviderReferenceGroup {
			continue
		}
		if len(rule.ResourceNames) == 0 {
			t.Fatalf("provider reference emitted an unscoped rule: %#v", rule)
		}
		for _, resource := range rule.Resources {
			for _, name := range rule.ResourceNames {
				for _, verb := range rule.Verbs {
					got[resource+"|"+name+"|"+verb] = struct{}{}
				}
			}
		}
	}
	if reflect.DeepEqual(got, want) {
		return
	}
	gotValues := sortedSet(got)
	wantValues := sortedSet(want)
	t.Fatalf("provider reference permissions differ:\n got: %v\nwant: %v", gotValues, wantValues)
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func reverseBindings(project *aiv1alpha1.Project) *aiv1alpha1.Project {
	reversed := project.DeepCopy()
	bindings := reversed.Spec.Environments[0].Bindings
	for i, j := 0, len(bindings)-1; i < j; i, j = i+1, j-1 {
		bindings[i], bindings[j] = bindings[j], bindings[i]
	}
	return reversed
}
