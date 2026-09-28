package kservemodule

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "github.com/onsi/gomega"
)

// accelPresetSpec builds a well-known accelerator preset whose spec carries an image, so
// content changes (image, labels, annotations) can be exercised by differential versioning.
func accelPresetSpec(name, accelerator, image string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha2",
		"kind":       "LLMInferenceServiceConfig",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "opendatahub",
			"labels": map[string]any{
				configTypeLabelKey: configTypeAcceleratorValue,
			},
			"annotations": map[string]any{
				wellKnownAnnotationKey:               wellKnownAnnotationValue,
				recommendedAcceleratorsAnnotationKey: `["` + accelerator + `"]`,
			},
		},
		"spec": map[string]any{
			"template": map[string]any{
				"containers": []any{
					map[string]any{"name": "main", "image": image},
				},
			},
		},
	}}
}

// accelPresetSpecNoLabel builds the same content as accelPresetSpec but WITHOUT the
// config-type label, so it is not identified as an accelerator preset for dedup purposes.
func accelPresetSpecNoLabel(name, accelerator, image string) unstructured.Unstructured {
	obj := accelPresetSpec(name, accelerator, image)
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels")
	return obj
}

func dedupeReconciler(existing ...unstructured.Unstructured) *KserveModuleReconciler {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(llmISVCConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(llmISVCConfigListGVK, &unstructured.UnstructuredList{})
	objs := make([]client.Object, len(existing))
	for i := range existing {
		objs[i] = &existing[i]
	}
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &KserveModuleReconciler{Client: cli, applicationsNamespace: "opendatahub"}
}

func TestPresetContentFingerprint(t *testing.T) {
	g := NewWithT(t)

	a := accelPresetSpec("v1-nvidia", "nvidia.com/gpu", "img:1")
	b := accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")
	c := accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:2")

	fpA, err := presetContentFingerprint(&a)
	g.Expect(err).ShouldNot(HaveOccurred())
	fpB, err := presetContentFingerprint(&b)
	g.Expect(err).ShouldNot(HaveOccurred())
	fpC, err := presetContentFingerprint(&c)
	g.Expect(err).ShouldNot(HaveOccurred())

	// Same content, different name → same fingerprint.
	g.Expect(fpA).Should(Equal(fpB))
	// Different image → different fingerprint.
	g.Expect(fpA).ShouldNot(Equal(fpC))
}

func TestPresetContentFingerprint_IgnoresPartOfLabel(t *testing.T) {
	g := NewWithT(t)

	rendered := accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")
	deployed := accelPresetSpec("v1-nvidia", "nvidia.com/gpu", "img:1")
	// The deployed copy has the operator-managed part-of label applied post-render,
	// on top of the config-type label both copies already carry.
	labels := deployed.GetLabels()
	labels["platform.opendatahub.io/part-of"] = "kserve"
	deployed.SetLabels(labels)

	fpRendered, err := presetContentFingerprint(&rendered)
	g.Expect(err).ShouldNot(HaveOccurred())
	fpDeployed, err := presetContentFingerprint(&deployed)
	g.Expect(err).ShouldNot(HaveOccurred())

	g.Expect(fpRendered).Should(Equal(fpDeployed))
}

func TestDedupeVersionedAcceleratorPresets(t *testing.T) {
	tests := []struct {
		name      string
		existing  []unstructured.Unstructured
		rendered  []unstructured.Unstructured
		wantNames []string
	}{
		{
			name:      "identical content under prior version is skipped",
			existing:  []unstructured.Unstructured{accelPresetSpec("v1-nvidia", "nvidia.com/gpu", "img:1")},
			rendered:  []unstructured.Unstructured{accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")},
			wantNames: nil,
		},
		{
			name:      "changed image is kept",
			existing:  []unstructured.Unstructured{accelPresetSpec("v1-nvidia", "nvidia.com/gpu", "img:1")},
			rendered:  []unstructured.Unstructured{accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:2")},
			wantNames: []string{"v2-nvidia"},
		},
		{
			name:      "no existing preset keeps rendered",
			existing:  nil,
			rendered:  []unstructured.Unstructured{accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")},
			wantNames: []string{"v2-nvidia"},
		},
		{
			name:      "same-name current version is kept",
			existing:  []unstructured.Unstructured{accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")},
			rendered:  []unstructured.Unstructured{accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")},
			wantNames: []string{"v2-nvidia"},
		},
		{
			name:      "non-accelerator preset ignored",
			existing:  []unstructured.Unstructured{accelPreset("kserve-config-llm-template")},
			rendered:  []unstructured.Unstructured{accelPreset("kserve-config-llm-template")},
			wantNames: []string{"kserve-config-llm-template"},
		},
		{
			// Identical content but no config-type label: not an accelerator preset, so
			// differential versioning leaves it alone (kept, not deduped).
			name:      "annotation without config-type label kept",
			existing:  []unstructured.Unstructured{accelPresetSpecNoLabel("v1-nvidia", "nvidia.com/gpu", "img:1")},
			rendered:  []unstructured.Unstructured{accelPresetSpecNoLabel("v2-nvidia", "nvidia.com/gpu", "img:1")},
			wantNames: []string{"v2-nvidia"},
		},
		{
			name: "only matching preset skipped, others kept",
			existing: []unstructured.Unstructured{
				accelPresetSpec("v1-nvidia", "nvidia.com/gpu", "img:1"),
			},
			rendered: []unstructured.Unstructured{
				accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1"),
				accelPresetSpec("v2-amd", "amd.com/gpu", "img:1"),
			},
			wantNames: []string{"v2-amd"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			r := dedupeReconciler(tc.existing...)
			result := r.dedupeVersionedAcceleratorPresets(context.Background(), tc.rendered)

			var names []string
			for _, res := range result {
				names = append(names, res.GetName())
			}
			g.Expect(names).Should(ConsistOf(tc.wantNames))
		})
	}
}

func TestDedupeVersionedAcceleratorPresets_ChangedAnnotationKept(t *testing.T) {
	g := NewWithT(t)

	existing := accelPresetSpec("v1-nvidia", "nvidia.com/gpu", "img:1")
	rendered := accelPresetSpec("v2-nvidia", "nvidia.com/gpu", "img:1")
	// A meaningful annotation change means the content differs, so it must be kept.
	anns := rendered.GetAnnotations()
	anns["opendatahub.io/extra"] = "changed"
	rendered.SetAnnotations(anns)

	r := dedupeReconciler(existing)
	result := r.dedupeVersionedAcceleratorPresets(context.Background(), []unstructured.Unstructured{rendered})
	g.Expect(result).Should(HaveLen(1))
}

func TestHasContentTwinUnderDifferentName(t *testing.T) {
	g := NewWithT(t)
	g.Expect(hasContentTwinUnderDifferentName(nil, "v2")).Should(BeFalse())
	g.Expect(hasContentTwinUnderDifferentName(map[string]struct{}{"v2": {}}, "v2")).Should(BeFalse())
	g.Expect(hasContentTwinUnderDifferentName(map[string]struct{}{"v1": {}}, "v2")).Should(BeTrue())
	g.Expect(hasContentTwinUnderDifferentName(map[string]struct{}{"v1": {}, "v2": {}}, "v2")).Should(BeTrue())
}
