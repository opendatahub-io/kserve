package kservemodule

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReferencedByNames(t *testing.T) {
	newConfig := func(refs ...map[string]any) *unstructured.Unstructured {
		cfg := &unstructured.Unstructured{Object: map[string]any{}}
		if refs != nil {
			list := make([]any, len(refs))
			for i := range refs {
				list[i] = refs[i]
			}
			_ = unstructured.SetNestedSlice(cfg.Object, list, "status", "referencedBy")
		}
		return cfg
	}

	t.Run("no status", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(referencedByNames(&unstructured.Unstructured{Object: map[string]any{}})).To(BeEmpty())
	})

	t.Run("namespaced names sorted", func(t *testing.T) {
		g := NewWithT(t)
		cfg := newConfig(
			map[string]any{"name": "svc-b", "namespace": "ns2"},
			map[string]any{"name": "svc-a", "namespace": "ns1"},
		)
		g.Expect(referencedByNames(cfg)).To(Equal([]string{"ns1/svc-a", "ns2/svc-b"}))
	})

	t.Run("skips malformed entries without a name", func(t *testing.T) {
		g := NewWithT(t)
		cfg := newConfig(
			map[string]any{"namespace": "ns1"},             // no name -> skipped
			map[string]any{"name": "", "namespace": "ns2"}, // empty name -> skipped
			map[string]any{"name": "svc-ok", "namespace": "ns3"},
		)
		g.Expect(referencedByNames(cfg)).To(Equal([]string{"ns3/svc-ok"}))
	})
}

func TestReferencedConfigBlockers(t *testing.T) {
	g := NewWithT(t)

	config := func(name string, refs ...map[string]any) unstructured.Unstructured {
		cfg := unstructured.Unstructured{Object: map[string]any{}}
		cfg.SetName(name)
		if refs != nil {
			list := make([]any, len(refs))
			for i := range refs {
				list[i] = refs[i]
			}
			_ = unstructured.SetNestedSlice(cfg.Object, list, "status", "referencedBy")
		}
		return cfg
	}

	configs := []unstructured.Unstructured{
		config("cfg-unused"),
		config("cfg-used", map[string]any{"name": "svc1", "namespace": "ns1"}),
	}

	blockers := referencedConfigBlockers(configs)
	g.Expect(blockers).To(ConsistOf("cfg-used (referenced by ns1/svc1)"))
}
