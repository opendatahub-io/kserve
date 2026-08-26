package kservemodule

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "github.com/onsi/gomega"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// accelPreset builds a well-known accelerator LLMInferenceServiceConfig preset carrying
// the recommended-accelerators annotation. Passing no accelerators omits the annotation,
// producing a generic (non-accelerator) preset.
func accelPreset(name string, accelerators ...string) unstructured.Unstructured {
	annotations := map[string]any{wellKnownAnnotationKey: wellKnownAnnotationValue}
	metadata := map[string]any{
		"name":        name,
		"namespace":   "opendatahub",
		"annotations": annotations,
	}
	if len(accelerators) > 0 {
		quoted := make([]string, len(accelerators))
		for i, a := range accelerators {
			quoted[i] = `"` + a + `"`
		}
		raw := "["
		for i, q := range quoted {
			if i > 0 {
				raw += ","
			}
			raw += q
		}
		raw += "]"
		annotations[recommendedAcceleratorsAnnotationKey] = raw
		// The config-type label is what identifies an accelerator preset (see
		// isAcceleratorPreset); omit it for the generic-preset case (no accelerators).
		metadata["labels"] = map[string]any{configTypeLabelKey: configTypeAcceleratorValue}
	}
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha2",
		"kind":       "LLMInferenceServiceConfig",
		"metadata":   metadata,
		"spec":       map[string]any{},
	}}
}

func node(name string, allocatable ...string) *corev1.Node {
	rl := corev1.ResourceList{}
	for _, a := range allocatable {
		rl[corev1.ResourceName(a)] = resource.MustParse("1")
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Allocatable: rl},
	}
}

func nodeSchemeReconciler(objs ...runtime.Object) *KserveModuleReconciler {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	s.AddKnownTypeWithName(llmISVCConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(llmISVCConfigListGVK, &unstructured.UnstructuredList{})
	cli := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()
	return &KserveModuleReconciler{Client: cli, applicationsNamespace: "opendatahub"}
}

// draResourceSliceGVKTest is the ResourceSlice GVK used in DRA-aware tests, mirroring the
// served GVK the reconciler discovers via the RESTMapper at setup.
var draResourceSliceGVKTest = schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "ResourceSlice"}

func resourceSlice(name, driver string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(draResourceSliceGVKTest)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, driver, "spec", "driver")
	return u
}

// draReconciler builds a reconciler whose scheme knows the ResourceSlice GVK and whose
// draResourceSliceGVK is set, so presentDRADrivers lists the seeded slices.
func draReconciler(objs ...runtime.Object) *KserveModuleReconciler {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	s.AddKnownTypeWithName(llmISVCConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(llmISVCConfigListGVK, &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(draResourceSliceGVKTest, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(draResourceSliceGVKTest.GroupVersion().WithKind("ResourceSliceList"), &unstructured.UnstructuredList{})
	cli := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()
	return &KserveModuleReconciler{
		Client:                cli,
		applicationsNamespace: "opendatahub",
		draResourceSliceGVK:   draResourceSliceGVKTest,
	}
}

func TestAcceleratorRequirements(t *testing.T) {
	tests := []struct {
		name string
		anno string
		want []string
	}{
		{"absent", "", nil},
		{"single", `["nvidia.com/gpu"]`, []string{"nvidia.com/gpu"}},
		{"multiple", `["nvidia.com/gpu","amd.com/gpu"]`, []string{"nvidia.com/gpu", "amd.com/gpu"}},
		{"empty array", `[]`, nil},
		{"empty entry dropped", `[""]`, nil},
		{"malformed", `not-json`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			obj := &unstructured.Unstructured{Object: map[string]any{}}
			if tc.anno != "" {
				obj.SetAnnotations(map[string]string{recommendedAcceleratorsAnnotationKey: tc.anno})
			}
			got := acceleratorRequirements(obj)
			if tc.want == nil {
				g.Expect(got).Should(BeEmpty())
				return
			}
			g.Expect(got).Should(Equal(tc.want))
		})
	}
}

func TestResourceDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"nvidia.com/gpu", "nvidia.com"},
		{"nvidia.com/mig-1g.5gb", "nvidia.com"},
		{"cpu", ""},
		{"memory", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			NewWithT(t).Expect(resourceDomain(tc.in)).Should(Equal(tc.want))
		})
	}
}

func TestAcceleratorPresent(t *testing.T) {
	names := map[string]struct{}{"nvidia.com/mig-1g.5gb": {}, "cpu": {}}
	domains := map[string]struct{}{"nvidia.com": {}}
	noDRA := map[string]struct{}{}

	g := NewWithT(t)
	// Exact match on a MIG device.
	g.Expect(acceleratorPresent("nvidia.com/mig-1g.5gb", names, domains, noDRA)).Should(BeTrue())
	// Domain match: nvidia.com/gpu satisfied by a MIG device under the same domain.
	g.Expect(acceleratorPresent("nvidia.com/gpu", names, domains, noDRA)).Should(BeTrue())
	// No node exposes amd.com at all.
	g.Expect(acceleratorPresent("amd.com/gpu", names, domains, noDRA)).Should(BeFalse())
	// A domain-less requirement only matches exact names.
	g.Expect(acceleratorPresent("cpu", names, domains, noDRA)).Should(BeTrue())
	g.Expect(acceleratorPresent("hugepages", names, domains, noDRA)).Should(BeFalse())
}

func TestAcceleratorPresent_DRA(t *testing.T) {
	g := NewWithT(t)
	// No device-plugin allocatable at all; presence comes only from DRA ResourceSlices.
	noNames := map[string]struct{}{}
	noDomains := map[string]struct{}{}
	draDrivers := map[string]struct{}{"gpu.nvidia.com": {}}

	// A driver under the vendor domain satisfies the requirement.
	g.Expect(acceleratorPresent("nvidia.com/gpu", noNames, noDomains, draDrivers)).Should(BeTrue())
	// A different vendor's requirement is not satisfied.
	g.Expect(acceleratorPresent("amd.com/gpu", noNames, noDomains, draDrivers)).Should(BeFalse())
	// A driver named exactly as the domain also matches.
	g.Expect(acceleratorPresent("amd.com/gpu", noNames, noDomains, map[string]struct{}{"amd.com": {}})).Should(BeTrue())
	// A domain-less requirement never matches a DRA driver.
	g.Expect(acceleratorPresent("cpu", noNames, noDomains, draDrivers)).Should(BeFalse())
}

// accelPresetNoLabel builds a well-known preset with the recommended-accelerators annotation
// but WITHOUT the config-type label, i.e. not identified as an accelerator preset.
func accelPresetNoLabel(name, accelerator string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha2",
		"kind":       "LLMInferenceServiceConfig",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "opendatahub",
			"annotations": map[string]any{
				wellKnownAnnotationKey:               wellKnownAnnotationValue,
				recommendedAcceleratorsAnnotationKey: `["` + accelerator + `"]`,
			},
		},
		"spec": map[string]any{},
	}}
}

func TestIsAcceleratorPreset(t *testing.T) {
	g := NewWithT(t)
	withLabel := accelPreset("nvidia", "nvidia.com/gpu")
	g.Expect(isAcceleratorPreset(&withLabel)).To(BeTrue())

	generic := accelPreset("kserve-config-llm-template")
	g.Expect(isAcceleratorPreset(&generic)).To(BeFalse())

	// Annotation present but config-type label missing: not an accelerator preset.
	noLabel := accelPresetNoLabel("nvidia", "nvidia.com/gpu")
	g.Expect(isAcceleratorPreset(&noLabel)).To(BeFalse())

	// Wrong label value.
	wrong := accelPreset("nvidia", "nvidia.com/gpu")
	wrong.SetLabels(map[string]string{configTypeLabelKey: "runtime"})
	g.Expect(isAcceleratorPreset(&wrong)).To(BeFalse())
}

func TestFilterHardwareUnavailablePresets(t *testing.T) {
	generic := accelPreset("kserve-config-llm-template")

	tests := []struct {
		name        string
		nodes       []runtime.Object
		enable      *bool
		resources   []unstructured.Unstructured
		wantNames   []string
		wantDropped []string
	}{
		{
			name:      "nvidia preset kept when gpu present",
			nodes:     []runtime.Object{node("n1", "nvidia.com/gpu", "cpu")},
			resources: []unstructured.Unstructured{accelPreset("nvidia", "nvidia.com/gpu")},
			wantNames: []string{"nvidia"},
		},
		{
			name:        "nvidia preset dropped when no gpu",
			nodes:       []runtime.Object{node("n1", "cpu")},
			resources:   []unstructured.Unstructured{accelPreset("nvidia", "nvidia.com/gpu")},
			wantDropped: []string{"nvidia"},
		},
		{
			name:      "mig device satisfies plain gpu via domain match",
			nodes:     []runtime.Object{node("n1", "nvidia.com/mig-1g.5gb")},
			resources: []unstructured.Unstructured{accelPreset("nvidia", "nvidia.com/gpu")},
			wantNames: []string{"nvidia"},
		},
		{
			name:  "mixed: only accelerators with matching hardware kept",
			nodes: []runtime.Object{node("n1", "nvidia.com/gpu")},
			resources: []unstructured.Unstructured{
				accelPreset("nvidia", "nvidia.com/gpu"),
				accelPreset("amd", "amd.com/gpu"),
				generic,
			},
			wantNames:   []string{"nvidia", "kserve-config-llm-template"},
			wantDropped: []string{"amd"},
		},
		{
			name:      "generic preset always kept",
			nodes:     []runtime.Object{node("n1", "cpu")},
			resources: []unstructured.Unstructured{generic},
			wantNames: []string{"kserve-config-llm-template"},
		},
		{
			// The recommended-accelerators annotation alone does not make a preset an
			// accelerator preset; without the config-type label it is treated as generic
			// and kept even when its accelerator is absent.
			name:      "annotation without config-type label kept",
			nodes:     []runtime.Object{node("n1", "cpu")},
			resources: []unstructured.Unstructured{accelPresetNoLabel("nvidia-nolabel", "nvidia.com/gpu")},
			wantNames: []string{"nvidia-nolabel"},
		},
		{
			name:      "disable switch keeps everything",
			nodes:     []runtime.Object{node("n1", "cpu")},
			enable:    ptr.To(false),
			resources: []unstructured.Unstructured{accelPreset("nvidia", "nvidia.com/gpu")},
			wantNames: []string{"nvidia"},
		},
		{
			name:      "multi-accelerator preset kept if any present",
			nodes:     []runtime.Object{node("n1", "amd.com/gpu")},
			resources: []unstructured.Unstructured{accelPreset("multi", "nvidia.com/gpu", "amd.com/gpu")},
			wantNames: []string{"multi"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			r := nodeSchemeReconciler(tc.nodes...)
			kserve := &platformv1alpha1.Kserve{}
			kserve.Spec.EnableHardwareAwarePresets = tc.enable

			result := r.filterHardwareUnavailablePresets(context.Background(), kserve, tc.resources)

			var names []string
			for _, res := range result {
				names = append(names, res.GetName())
			}
			g.Expect(names).Should(ConsistOf(tc.wantNames))
			for _, d := range tc.wantDropped {
				g.Expect(names).ShouldNot(ContainElement(d))
			}
		})
	}
}

func TestFilterHardwareUnavailablePresets_FailOpen(t *testing.T) {
	g := NewWithT(t)
	// A scheme without corev1 makes listing nodes fail; the filter must keep all presets.
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(llmISVCConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(llmISVCConfigListGVK, &unstructured.UnstructuredList{})
	cli := fake.NewClientBuilder().WithScheme(s).Build()
	r := &KserveModuleReconciler{Client: cli, applicationsNamespace: "opendatahub"}

	kserve := &platformv1alpha1.Kserve{}
	resources := []unstructured.Unstructured{accelPreset("nvidia", "nvidia.com/gpu")}
	result := r.filterHardwareUnavailablePresets(context.Background(), kserve, resources)
	g.Expect(result).Should(HaveLen(1))
}

func TestFilterHardwareUnavailablePresets_NilKserve(t *testing.T) {
	g := NewWithT(t)
	r := nodeSchemeReconciler(node("n1", "cpu"))
	resources := []unstructured.Unstructured{accelPreset("nvidia", "nvidia.com/gpu")}
	result := r.filterHardwareUnavailablePresets(context.Background(), nil, resources)
	g.Expect(result).Should(HaveLen(1))
}

func TestPresentDRADrivers(t *testing.T) {
	g := NewWithT(t)

	// GVK unset (DRA not served): empty set, no error, no list attempted.
	rNoDRA := &KserveModuleReconciler{applicationsNamespace: "opendatahub"}
	drivers, err := rNoDRA.presentDRADrivers(context.Background())
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(drivers).Should(BeEmpty())

	// GVK set: driver names collected from spec.driver of each ResourceSlice.
	r := draReconciler(
		resourceSlice("gpu-node-a", "gpu.nvidia.com"),
		resourceSlice("gpu-node-b", "gpu.nvidia.com"),
		resourceSlice("amd-node", "gpu.amd.com"),
	)
	drivers, err = r.presentDRADrivers(context.Background())
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(drivers).Should(HaveKey("gpu.nvidia.com"))
	g.Expect(drivers).Should(HaveKey("gpu.amd.com"))
	g.Expect(drivers).Should(HaveLen(2))
}

func TestFilterHardwareUnavailablePresets_DRA(t *testing.T) {
	g := NewWithT(t)

	// A cluster with no device-plugin GPU allocatable, but an nvidia DRA driver publishing a
	// ResourceSlice: the nvidia preset must be kept, the amd one dropped.
	r := draReconciler(
		node("n1", "cpu"),
		resourceSlice("gpu-node", "gpu.nvidia.com"),
	)
	kserve := &platformv1alpha1.Kserve{}
	resources := []unstructured.Unstructured{
		accelPreset("nvidia", "nvidia.com/gpu"),
		accelPreset("amd", "amd.com/gpu"),
	}
	result := r.filterHardwareUnavailablePresets(context.Background(), kserve, resources)

	var names []string
	for _, res := range result {
		names = append(names, res.GetName())
	}
	g.Expect(names).Should(ConsistOf("nvidia"))
}
