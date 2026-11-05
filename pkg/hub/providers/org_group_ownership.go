// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

var orgGroupReservationGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

func orgGroupReservationName(org string) string {
	hash := sha256.Sum256([]byte(org))
	return "provider-groups-" + hex.EncodeToString(hash[:16])
}

func orgGroupClient(config *rest.Config) (dynamic.Interface, error) {
	cfg := rest.CopyConfig(config)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemControllers)
	return dynamic.NewForConfig(cfg)
}

// reserveOrgProviderGroups atomically claims the entire declaration in a single
// Hub-only object. Failed publication retains its claims for an idempotent retry;
// groups are released only after registration teardown drains its APIExports.
func reserveOrgProviderGroups(ctx context.Context, client dynamic.Interface, registrations dynamic.Interface, identity OrgBootstrapIdentity, declaration sdkinstall.OrganizationBootstrap) error {
	groups := map[string]bool{}
	for _, objects := range [][]*unstructured.Unstructured{declaration.Schemas, declaration.PrivateCRDs} {
		for _, object := range objects {
			group, _, _ := unstructured.NestedString(object.Object, "spec", "group")
			if group != "" {
				groups[group] = true
			}
		}
	}
	return updateOrgGroupReservations(ctx, client, identity.OrgUUID, func(owners map[string]string) error {
		owner := identity.Cluster + "/" + identity.RegistrationUID
		if identity.Cluster == "" || identity.RegistrationUID == "" {
			return fmt.Errorf("registration identity is required for group ownership")
		}
		// Read registration state after the reservation resourceVersion. A
		// concurrent ownership write then forces a fresh state read on retry.
		// This also reclaims claims left by an interrupted teardown or a
		// bootstrap whose original workspace disappeared during publication.
		live := map[string]bool{}
		if registrations != nil {
			workspaces, err := registrations.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			callerLive := false
			for _, workspace := range workspaces.Items {
				cluster, _, _ := unstructured.NestedString(workspace.Object, "spec", "cluster")
				key := cluster + "/" + string(workspace.GetUID())
				live[key] = true // terminating exports retain ownership until gone
				if key == owner && !orgProviderWorkspaceDeleting(&workspace) {
					callerLive = true
				}
			}
			if !callerLive {
				return fmt.Errorf("provider registration is no longer active")
			}
		}
		for group := range groups {
			if existing := owners[group]; existing != "" && registrations != nil && !live[existing] {
				delete(owners, group)
			}
			if existing := owners[group]; existing != "" && existing != owner {
				return apierrors.NewConflict(orgGroupReservationGVR.GroupResource(), group, fmt.Errorf("API group is reserved by another organization provider registration"))
			}
		}
		for group := range groups {
			owners[group] = owner
		}
		return nil
	})
}

func updateOrgGroupReservations(ctx context.Context, client dynamic.Interface, org string, change func(map[string]string) error) error {
	maps := client.Resource(orgGroupReservationGVR).Namespace(kcppaths.HubSystemNamespace)
	name := orgGroupReservationName(org)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current, err := maps.Get(ctx, name, metav1.GetOptions{})
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		if create {
			current = &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": name, "namespace": kcppaths.HubSystemNamespace},
			}}
		}
		owners, _, err := unstructured.NestedStringMap(current.Object, "data")
		if err != nil {
			return err
		}
		if owners == nil {
			owners = map[string]string{}
		}
		if err := change(owners); err != nil {
			return err
		}
		if err := unstructured.SetNestedStringMap(current.Object, owners, "data"); err != nil {
			return err
		}
		if create {
			_, err = maps.Create(ctx, current, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(orgGroupReservationGVR.GroupResource(), name, err)
			}
		} else {
			_, err = maps.Update(ctx, current, metav1.UpdateOptions{})
		}
		return err
	})
}

// ReleaseOrgProviderGroups is UID-fenced and called only after export drain.
// A delayed teardown cannot release a replacement registration's reservations.
func ReleaseOrgProviderGroups(ctx context.Context, config *rest.Config, org, cluster, uid string) error {
	client, err := orgGroupClient(config)
	if err != nil {
		return err
	}
	return releaseOrgProviderGroups(ctx, client, org, cluster, uid)
}

func releaseOrgProviderGroups(ctx context.Context, client dynamic.Interface, org, cluster, uid string) error {
	return updateOrgGroupReservations(ctx, client, org, func(owners map[string]string) error {
		for group, owner := range owners {
			if owner == cluster+"/"+uid {
				delete(owners, group)
			}
		}
		return nil
	})
}
