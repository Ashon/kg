// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package kube holds the small client helpers the CLI needs: building a typed
// client for a kubeconfig, applying rendered objects and serialising them.
package kube

import (
	"bytes"
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
)

// FieldManager identifies kgenesis in server-side apply, so an operator editing
// the same objects by hand gets a real conflict instead of a silent overwrite.
const FieldManager = "kgenesis"

// Scheme knows every type kgenesis reads or writes.
func Scheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		clusterv1.AddToScheme,
		bootstrapv1.AddToScheme,
		controlplanev1.AddToScheme,
		infrav1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}

// NewClient builds a controller-runtime client for a kubeconfig file.
func NewClient(kubeconfigPath string) (client.Client, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig %s: %w", kubeconfigPath, err)
	}

	scheme, err := Scheme()
	if err != nil {
		return nil, err
	}

	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build a client for %s: %w", kubeconfigPath, err)
	}
	return c, nil
}

// Apply server-side applies every object, which makes `cluster create` safe to
// re-run against a cluster that is already partly built.
func Apply(ctx context.Context, c client.Client, objects []client.Object) error {
	for _, obj := range objects {
		if err := c.Patch(ctx, obj, client.Apply,
			client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w",
				obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// ToYAML serialises objects into a multi-document manifest, which is what
// `--dry-run -o yaml` prints.
func ToYAML(objects []client.Object) ([]byte, error) {
	scheme, err := Scheme()
	if err != nil {
		return nil, err
	}
	serializer := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme, scheme,
		json.SerializerOptions{Yaml: true})

	var out bytes.Buffer
	for i, obj := range objects {
		if i > 0 {
			out.WriteString("---\n")
		}
		if err := serializer.Encode(obj, &out); err != nil {
			return nil, fmt.Errorf("encode %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return out.Bytes(), nil
}
