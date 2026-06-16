package kservemodule

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
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

func TestIsWVAEnabled(t *testing.T) {
	tests := []struct {
		name     string
		state    common.ManagementState
		expected bool
	}{
		{"Managed returns true", common.Managed, true},
		{"Removed returns false", common.Removed, false},
		{"empty returns false", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			kserve := &platformv1alpha1.Kserve{
				Spec: platformv1alpha1.KserveSpec{
					WVA: platformv1alpha1.WVASpec{
						ManagementState: tt.state,
					},
				},
			}
			g.Expect(isWVAEnabled(kserve)).To(Equal(tt.expected))
		})
	}
}

func TestComponentsConfig_WVAHasEnabled(t *testing.T) {
	g := NewWithT(t)
	var wva *componentConfig
	for i := range components {
		if components[i].name == WVAComponentName {
			wva = &components[i]
			break
		}
	}
	g.Expect(wva).ShouldNot(BeNil(), "WVA component not registered")
	g.Expect(wva.enabled).ShouldNot(BeNil(), "WVA must have enabled predicate")
	g.Expect(wva.sourcePathXKS).Should(BeEmpty(), "WVA is OCP-only, must not have XKS overlay")
}

func TestModelControllerExtraParams(t *testing.T) {
	tests := []struct {
		name           string
		kserveState    common.ManagementState
		nimState       common.ManagementState
		expectedNIM    string
		expectedKserve string
	}{
		{
			name:           "Kserve Managed + NIM Managed",
			kserveState:    common.Managed,
			nimState:       common.Managed,
			expectedNIM:    "managed",
			expectedKserve: "managed",
		},
		{
			name:           "Kserve Managed + NIM Removed",
			kserveState:    common.Managed,
			nimState:       common.Removed,
			expectedNIM:    "removed",
			expectedKserve: "managed",
		},
		{
			name:           "Kserve Managed + NIM empty defaults to managed",
			kserveState:    common.Managed,
			nimState:       "",
			expectedNIM:    "managed",
			expectedKserve: "managed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			kserve := &platformv1alpha1.Kserve{
				Spec: platformv1alpha1.KserveSpec{
					ManagementSpec: common.ManagementSpec{
						ManagementState: tt.kserveState,
					},
					NIM: platformv1alpha1.NIMSpec{
						ManagementState: tt.nimState,
					},
				},
			}
			params := modelControllerExtraParams(kserve)
			g.Expect(params["nim-state"]).To(Equal(tt.expectedNIM))
			g.Expect(params["kserve-state"]).To(Equal(tt.expectedKserve))
		})
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
