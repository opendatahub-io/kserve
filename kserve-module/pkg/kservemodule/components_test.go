package kservemodule

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

func TestComponentsConfig_AllHaveRequiredFields(t *testing.T) {
	g := NewWithT(t)
	for _, comp := range components {
		g.Expect(comp.name).ShouldNot(BeEmpty(), "component has empty name")
		g.Expect(comp.sourcePath).ShouldNot(BeEmpty(), "component %q has empty sourcePath", comp.name)
		g.Expect(comp.imageMap).ShouldNot(BeNil(), "component %q has nil imageMap", comp.name)
	}
}

func TestComponentsConfig_UniqueNames(t *testing.T) {
	g := NewWithT(t)
	seen := map[string]bool{}
	for _, comp := range components {
		g.Expect(seen[comp.name]).Should(BeFalse(), "duplicate component name: %q", comp.name)
		seen[comp.name] = true
	}
}

func TestApplyManagedByLabel(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": "test"},
		}},
		{Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]any{"name": "test-deploy"},
		}},
	}

	applyManagedByLabel(resources, "kserve")

	for _, r := range resources {
		g.Expect(r.GetLabels()).Should(HaveKeyWithValue("platform.opendatahub.io/part-of", "kserve"))
	}
}
