package applier_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"auto-qa/internal/applier"
)

// minimalYAML is a simple multi-doc YAML with a Namespace and a ConfigMap.
const minimalYAML = `
apiVersion: v1
kind: Namespace
metadata:
  name: test-applier-ns
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
  namespace: test-applier-ns
data:
  key: value
`

// writeYAML writes content to a temp file and returns the path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

// coreV1Mapper builds a DefaultRESTMapper populated with the core v1 resource
// types used in tests (Namespace, ConfigMap). This avoids relying on live
// cluster discovery.
func coreV1Mapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper(nil)

	// Namespace — cluster-scoped
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"},
		meta.RESTScopeRoot)
	// ConfigMap — namespace-scoped
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"},
		meta.RESTScopeNamespace)

	return mapper
}

// newTestApplier constructs an Applier backed by a fake dynamic client and a
// hand-built REST mapper so no live discovery is required.
// existingRaw is a list of unstructured objects to pre-seed in the fake tracker.
func newTestApplier(t *testing.T, existingRaw ...runtime.Object) *applier.Applier {
	t.Helper()

	scheme := runtime.NewScheme()
	gvrMap := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "namespaces"}: "NamespaceList",
		{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
	}
	dynClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrMap, existingRaw...)

	// The fake dynamic client does not implement server-side apply semantics.
	// Add a reactor that handles ApplyPatchType by returning an empty
	// Unstructured object, letting the applier logic continue without error.
	dynClient.Fake.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pa, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		if pa.GetPatchType() == types.ApplyPatchType {
			return true, &unstructured.Unstructured{}, nil
		}
		return false, nil, nil
	})

	return applier.NewWithMapper(dynClient, coreV1Mapper())
}

// ---------------------------------------------------------------------------
// ApplyFile
// ---------------------------------------------------------------------------

func TestApplyFile_ReturnsNoErrorOnValidYAML(t *testing.T) {
	path := writeYAML(t, minimalYAML)
	a := newTestApplier(t)

	if err := a.ApplyFile(context.Background(), path); err != nil {
		t.Fatalf("ApplyFile returned unexpected error: %v", err)
	}
}

func TestApplyFile_ReturnsErrorOnMissingFile(t *testing.T) {
	a := newTestApplier(t)

	err := a.ApplyFile(context.Background(), "/nonexistent/path/scenario.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestApplyFile_ReturnsErrorOnInvalidYAML(t *testing.T) {
	path := writeYAML(t, "this: is: not: valid: yaml: {{{{")
	a := newTestApplier(t)

	err := a.ApplyFile(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestApplyFile_SkipsEmptyDocuments(t *testing.T) {
	// A YAML file with only separators — no objects to apply, should be a no-op.
	path := writeYAML(t, "---\n---\n")
	a := newTestApplier(t)

	if err := a.ApplyFile(context.Background(), path); err != nil {
		t.Fatalf("ApplyFile on empty docs returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DeleteFile
// ---------------------------------------------------------------------------

func TestDeleteFile_ReturnsNoErrorWhenResourcesMissing(t *testing.T) {
	// DeleteFile should be a no-op (not an error) when resources don't exist.
	path := writeYAML(t, minimalYAML)
	a := newTestApplier(t)

	if err := a.DeleteFile(context.Background(), path); err != nil {
		t.Fatalf("DeleteFile returned unexpected error: %v", err)
	}
}

func TestDeleteFile_ReturnsErrorOnMissingFile(t *testing.T) {
	a := newTestApplier(t)

	err := a.DeleteFile(context.Background(), "/nonexistent/path/scenario.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestDeleteFile_DeletesExistingNamespace(t *testing.T) {
	path := writeYAML(t, `
apiVersion: v1
kind: Namespace
metadata:
  name: to-be-deleted
`)
	a := newTestApplier(t)
	// Delete should succeed whether or not the resource exists in the fake client.
	if err := a.DeleteFile(context.Background(), path); err != nil {
		t.Fatalf("DeleteFile returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Edge cases via ApplyFile
// ---------------------------------------------------------------------------

func TestApplyFile_SkipsDocumentsWithoutKind(t *testing.T) {
	// A YAML document that has no `kind` field should be silently skipped.
	noKind := `
apiVersion: v1
metadata:
  name: no-kind
`
	path := writeYAML(t, noKind)
	a := newTestApplier(t)

	if err := a.ApplyFile(context.Background(), path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkloadsFromFile_DefaultsEmptyNamespaceForNamespacedResources(t *testing.T) {
	path := writeYAML(t, `
apiVersion: v1
kind: Namespace
metadata:
  name: test-applier-ns
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: implicit-default
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: explicit-ns
  namespace: app-ns
`)

	a := newTestApplier(t)
	refs, err := a.WorkloadsFromFile(path)
	if err != nil {
		t.Fatalf("WorkloadsFromFile returned unexpected error: %v", err)
	}

	if len(refs) != 2 {
		t.Fatalf("expected 2 workload refs, got %d", len(refs))
	}

	if refs[0].Kind != "ConfigMap" || refs[0].Name != "implicit-default" || refs[0].Namespace != "default" {
		t.Fatalf("unexpected first ref: %#v", refs[0])
	}
	if refs[1].Kind != "ConfigMap" || refs[1].Name != "explicit-ns" || refs[1].Namespace != "app-ns" {
		t.Fatalf("unexpected second ref: %#v", refs[1])
	}
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func TestNew_ReturnsNonNilApplier(t *testing.T) {
	a := newTestApplier(t)
	if a == nil {
		t.Fatal("NewWithMapper() returned nil")
	}
}
