package applier

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	"auto-qa/internal/constants"
)

// Applier applies and deletes Kubernetes resources described by YAML files.
// It uses server-side apply so it works cleanly on repeated runs and plays
// nicely with other field managers.
type Applier struct {
	dynamic   dynamic.Interface
	discovery discovery.DiscoveryInterface
	// mapper, when non-nil, is used instead of building one from discovery.
	// Intended for testing only.
	mapper meta.RESTMapper
}

const kubeDefaultNamespace = "default"

// New returns an Applier backed by the given dynamic and discovery clients.
func New(dyn dynamic.Interface, disc discovery.DiscoveryInterface) *Applier {
	return &Applier{dynamic: dyn, discovery: disc}
}

// NewWithMapper returns an Applier with a pre-built REST mapper, bypassing
// live discovery. Intended for use in tests only.
func NewWithMapper(dyn dynamic.Interface, m meta.RESTMapper) *Applier {
	return &Applier{dynamic: dyn, mapper: m}
}

// ApplyFile reads the YAML file at path, splits it on "---" document
// boundaries, and server-side applies each resource to the cluster.
func (a *Applier) ApplyFile(ctx context.Context, path string) error {
	objects, err := decodeFile(path)
	if err != nil {
		return err
	}

	mapper, err := a.buildMapper()
	if err != nil {
		return err
	}

	for _, obj := range objects {
		if err := a.applyObject(ctx, mapper, obj); err != nil {
			return err
		}
	}
	return nil
}

// DeleteFile reads the YAML file at path and deletes every resource it
// describes. Resources that no longer exist are silently skipped.
func (a *Applier) DeleteFile(ctx context.Context, path string) error {
	objects, err := decodeFile(path)
	if err != nil {
		return err
	}

	mapper, err := a.buildMapper()
	if err != nil {
		return err
	}

	for _, obj := range objects {
		if err := a.deleteObject(ctx, mapper, obj); err != nil {
			return err
		}
	}
	return nil
}

// applyObject performs a server-side apply (force-conflicts) for a single object.
func (a *Applier) applyObject(ctx context.Context, mapper meta.RESTMapper, obj *unstructured.Unstructured) error {
	ri, err := resourceInterface(a.dynamic, mapper, obj)
	if err != nil {
		return err
	}

	data, err := obj.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshalling %s/%s: %w", obj.GetKind(), obj.GetName(), err)
	}

	_, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: constants.FieldManager,
		Force:        boolPtr(true),
	})
	if err != nil {
		return fmt.Errorf("applying %s/%s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

// deleteObject deletes a single object; NotFound is treated as a no-op.
func (a *Applier) deleteObject(ctx context.Context, mapper meta.RESTMapper, obj *unstructured.Unstructured) error {
	ri, err := resourceInterface(a.dynamic, mapper, obj)
	if err != nil {
		return err
	}

	propagation := metav1.DeletePropagationForeground
	err = ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting %s/%s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

// buildMapper fetches the API discovery data and builds a REST mapper,
// unless one was injected at construction time (for testing).
func (a *Applier) buildMapper() (meta.RESTMapper, error) {
	if a.mapper != nil {
		return a.mapper, nil
	}
	gr, err := restmapper.GetAPIGroupResources(a.discovery)
	if err != nil {
		return nil, fmt.Errorf("fetching API group resources: %w", err)
	}
	return restmapper.NewDiscoveryRESTMapper(gr), nil
}

// resourceInterface returns a dynamic.ResourceInterface scoped to the object's
// namespace (or cluster-scoped if the mapping is not namespaced).
func resourceInterface(dyn dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("mapping %s: %w", gvk.Kind, err)
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = kubeDefaultNamespace
		}
		return dyn.Resource(mapping.Resource).Namespace(ns), nil
	}
	return dyn.Resource(mapping.Resource), nil
}

// decodeFile reads a YAML file and returns all non-empty documents as
// unstructured objects.
func decodeFile(path string) ([]*unstructured.Unstructured, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}

	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	var objects []*unstructured.Unstructured
	for {
		var rawObj map[string]interface{}
		if err := decoder.Decode(&rawObj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decoding %q: %w", path, err)
		}
		if len(rawObj) == 0 {
			continue
		}

		// Re-encode to JSON then decode into Unstructured via the typed decoder
		// so that TypeMeta (apiVersion/kind) is properly populated.
		obj := &unstructured.Unstructured{Object: rawObj}
		gvk := obj.GroupVersionKind()
		if gvk.Kind == "" {
			continue // skip documents without a kind (e.g. comment-only blocks)
		}

		// Use the YAML decoder to ensure full type metadata is resolved.
		typedObj := &unstructured.Unstructured{}
		jsonData, err := marshalRawObject(rawObj)
		if err != nil {
			return nil, fmt.Errorf("encoding object in %q: %w", path, err)
		}
		if _, _, err := dec.Decode(jsonData, nil, typedObj); err != nil {
			return nil, fmt.Errorf("decoding object in %q: %w", path, err)
		}
		objects = append(objects, typedObj)
	}
	return objects, nil
}

// marshalRawObject converts a raw map back to JSON bytes for re-decoding.
func marshalRawObject(v map[string]interface{}) ([]byte, error) {
	u := &unstructured.Unstructured{Object: v}
	data, err := u.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return data, nil
}

// WorkloadRef identifies a single namespaced Kubernetes resource.
type WorkloadRef struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// WorkloadsFromFile reads the YAML file at path and returns a WorkloadRef for
// every namespaced resource it contains. Namespace-scoped resources without an
// explicit metadata.namespace are resolved to the Kubernetes "default"
// namespace, matching apply semantics.
func (a *Applier) WorkloadsFromFile(path string) ([]WorkloadRef, error) {
	objects, err := decodeFile(path)
	if err != nil {
		return nil, err
	}

	mapper, err := a.buildMapper()
	if err != nil {
		return nil, err
	}

	return workloadsFromObjects(objects, mapper)
}

// WorkloadsFromFile reads the YAML file at path and returns a WorkloadRef for
// every namespaced resource it contains. Cluster-scoped resources (empty
// namespace) are omitted. The returned slice preserves document order.
func WorkloadsFromFile(path string) ([]WorkloadRef, error) {
	objects, err := decodeFile(path)
	if err != nil {
		return nil, err
	}
	var result []WorkloadRef
	for _, obj := range objects {
		ns := obj.GetNamespace()
		if ns == "" {
			continue
		}
		result = append(result, WorkloadRef{
			APIVersion: obj.GetAPIVersion(),
			Kind:       obj.GetKind(),
			Namespace:  ns,
			Name:       obj.GetName(),
		})
	}
	return result, nil
}

func workloadsFromObjects(objects []*unstructured.Unstructured, mapper meta.RESTMapper) ([]WorkloadRef, error) {
	var result []WorkloadRef
	for _, obj := range objects {
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, fmt.Errorf("mapping %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
			continue
		}

		ns := obj.GetNamespace()
		if ns == "" {
			ns = kubeDefaultNamespace
		}

		result = append(result, WorkloadRef{
			APIVersion: obj.GetAPIVersion(),
			Kind:       obj.GetKind(),
			Namespace:  ns,
			Name:       obj.GetName(),
		})
	}
	return result, nil
}

func boolPtr(b bool) *bool { return &b }
