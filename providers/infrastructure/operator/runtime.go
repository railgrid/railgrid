/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// writeTempKubeconfig writes kubeconfig bytes to a 0600 temp file and returns
// its path plus a cleanup func. Used to hand a KUBECONFIG path to the helm CLI.
func writeTempKubeconfig(kubeconfig []byte) (string, func(), error) {
	f, err := os.CreateTemp("", "kedge-runtime-*.kubeconfig")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.Write(kubeconfig); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return f.Name(), cleanup, nil
}

// ensureNamespace creates the namespace on the runtime cluster if missing.
func ensureNamespace(ctx context.Context, cs kubernetes.Interface, name string) error {
	_, err := cs.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", name, err)
	}
	_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", name, err)
	}
	return nil
}

// upsertOpaqueSecret create-or-updates an Opaque Secret with one key on the
// runtime cluster — used to replicate the provider kubeconfig (and hub token)
// into the runtime cluster so the serve Deployment can mount them.
func upsertOpaqueSecret(ctx context.Context, cs kubernetes.Interface, ns, name, key string, value []byte) error {
	want := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{key: value},
	}
	existing, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Data = want.Data
		_, uerr := cs.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
		return uerr
	case apierrors.IsNotFound(err):
		_, cerr := cs.CoreV1().Secrets(ns).Create(ctx, want, metav1.CreateOptions{})
		if cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return cerr
		}
		return nil
	default:
		return err
	}
}

// runtimeClientset builds a typed clientset for the runtime cluster.
func runtimeClientset(cfg *rest.Config) (kubernetes.Interface, error) {
	return kubernetes.NewForConfig(cfg)
}
