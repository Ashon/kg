package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DecodeManifest splits a multi-document YAML manifest into objects.
func DecodeManifest(data []byte) ([]*unstructured.Unstructured, error) {
	reader := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)

	var objects []*unstructured.Unstructured
	for {
		obj := &unstructured.Unstructured{}
		err := reader.Decode(obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		// Separators and trailing newlines decode to empty documents.
		if len(obj.Object) == 0 {
			continue
		}
		objects = append(objects, obj)
	}
	return objects, nil
}

// ApplyManifest server-side applies a multi-document manifest.
func ApplyManifest(ctx context.Context, c client.Client, data []byte) error {
	objects, err := DecodeManifest(data)
	if err != nil {
		return err
	}

	for _, obj := range objects {
		if err := c.Patch(ctx, obj, client.Apply,
			client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// SetDeploymentImage rewrites the container image of the named Deployment in a
// decoded manifest. It is how a locally built provider image is used in place of
// the released one without maintaining a second copy of the manifest.
func SetDeploymentImage(objects []*unstructured.Unstructured, deployment, container, image string) error {
	for _, obj := range objects {
		if obj.GetKind() != "Deployment" || obj.GetName() != deployment {
			continue
		}

		containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		if err != nil || !found {
			return fmt.Errorf("deployment %s has no containers", deployment)
		}

		for i := range containers {
			c, ok := containers[i].(map[string]any)
			if !ok || c["name"] != container {
				continue
			}
			c["image"] = image
			if err := unstructured.SetNestedSlice(obj.Object, containers,
				"spec", "template", "spec", "containers"); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("deployment %s has no container named %s", deployment, container)
	}
	return fmt.Errorf("manifest has no Deployment named %s", deployment)
}

// ApplyObjects server-side applies already decoded objects.
func ApplyObjects(ctx context.Context, c client.Client, objects []*unstructured.Unstructured) error {
	for _, obj := range objects {
		if err := c.Patch(ctx, obj, client.Apply,
			client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// DeleteManifest removes every object in a manifest. Objects that are already
// gone are ignored, so teardown is safe to re-run.
func DeleteManifest(ctx context.Context, c client.Client, data []byte) error {
	objects, err := DecodeManifest(data)
	if err != nil {
		return err
	}

	// Reverse order: the Deployment goes before the CRDs whose objects it watches.
	for i := len(objects) - 1; i >= 0; i-- {
		obj := objects[i]
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}
