package kservemodule_test

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	kservemodule "github.com/opendatahub-io/kserve-module/pkg/kservemodule"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule/fixture"
)

// acceleratorPresetName is the well-known accelerator preset injected into the kserve
// overlay for this suite; it requires an nvidia.com/gpu allocatable to be rendered.
const acceleratorPresetName = "kserve-config-llm-nvidia-cuda"

func acceleratorPresetYAML(name string) string {
	return `apiVersion: serving.kserve.io/v1alpha2
kind: LLMInferenceServiceConfig
metadata:
  name: ` + name + `
  labels:
    opendatahub.io/config-type: accelerator
  annotations:
    serving.kserve.io/well-known-config: "true"
    opendatahub.io/recommended-accelerators: '["nvidia.com/gpu"]'
spec:
  template:
    containers:
    - name: main
      image: registry.example.com/vllm:latest
`
}

// deployInputContainsPreset reports whether any resource captured by the mock deployer
// carries the preset's base name. Presets are version-prefixed before deployment
// (e.g. v0-0-0-kserve-config-llm-nvidia-cuda), so a suffix match is used.
func deployInputContainsPreset(m *fixture.MockDeployer, name string) bool {
	for i := range m.Calls {
		for j := range m.Calls[i].Resources {
			if strings.HasSuffix(m.Calls[i].Resources[j].GetName(), name) {
				return true
			}
		}
	}
	return false
}

func deployedResourceNames(m *fixture.MockDeployer) []string {
	var names []string
	for i := range m.Calls {
		for j := range m.Calls[i].Resources {
			names = append(names, m.Calls[i].Resources[j].GetKind()+"/"+m.Calls[i].Resources[j].GetName())
		}
	}
	return names
}

var _ = Describe("Hardware-aware preset filtering", Ordered, func() {
	var kserve *platformv1alpha1.Kserve
	var overlayResourcePath string
	var originalManifest []byte

	BeforeAll(func(ctx SpecContext) {
		testEnv.Reconciler.Deployer = &fixture.MockDeployer{}
		testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)

		// Inject an accelerator preset into the kserve overlay so the render carries something
		// for hardware-aware filtering to act on; restored in AfterAll.
		overlayResourcePath = filepath.Join(testEnv.Reconciler.ManifestsTemplatePath,
			kservemodule.KserveComponentName, kservemodule.KserveManifestSourcePath, "resource.yaml")
		var err error
		originalManifest, err = os.ReadFile(overlayResourcePath)
		Expect(err).NotTo(HaveOccurred())
		withPreset := string(originalManifest) + "\n---\n" + acceleratorPresetYAML(acceleratorPresetName)
		Expect(os.WriteFile(overlayResourcePath, []byte(withPreset), 0o644)).To(Succeed())

		kserve = fixture.KserveCR()
		Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, kserve))).To(Succeed())
		Eventually(func(g Gomega) {
			err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)
			g.Expect(k8serr.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

		kserve = fixture.KserveCR()
		Expect(testEnv.Client.Create(ctx, kserve)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
			g.Expect(kserve.Status.ObservedGeneration).To(Equal(kserve.Generation))
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
	})

	AfterAll(func(ctx SpecContext) {
		if originalManifest != nil {
			Expect(os.WriteFile(overlayResourcePath, originalManifest, 0o644)).To(Succeed())
		}
		if kserve != nil {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, kserve))).To(Succeed())
			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue())
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		}
		testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)
	})

	It("omits the accelerator preset when no matching node hardware is present", func(ctx SpecContext) {
		// Fresh deployer so only this reconcile's rendered resources are inspected.
		testEnv.Reconciler.Deployer = &fixture.MockDeployer{}
		triggerReconcile(ctx, kserve, "hw-no-gpu")

		Eventually(func(g Gomega) {
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
			g.Expect(kserve.Status.ObservedGeneration).To(Equal(kserve.Generation))
			g.Expect(mockDeployer().LastCall()).NotTo(BeNil())
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

		Consistently(func(g Gomega) {
			g.Expect(deployInputContainsPreset(mockDeployer(), acceleratorPresetName)).To(BeFalse(),
				"accelerator preset must not be rendered on a cluster with no GPU nodes")
		}).WithContext(ctx).WithTimeout(3 * time.Second).Should(Succeed())
	})

	It("renders the accelerator preset once a GPU node appears", func(ctx SpecContext) {
		testEnv.Reconciler.Deployer = &fixture.MockDeployer{}

		gpuNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node"}}
		Expect(testEnv.Client.Create(ctx, gpuNode)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			client.IgnoreNotFound(testEnv.Client.Delete(ctx, gpuNode))
		})

		Expect(testEnv.Client.Get(ctx, client.ObjectKey{Name: "gpu-node"}, gpuNode)).To(Succeed())
		gpuNode.Status.Allocatable = corev1.ResourceList{
			"nvidia.com/gpu": resource.MustParse("1"),
			corev1.ResourceCPU: resource.MustParse("4"),
		}
		Expect(testEnv.Client.Status().Update(ctx, gpuNode)).To(Succeed())

		// Sanity: the direct client must reflect the allocatable update.
		Expect(testEnv.Client.Get(ctx, client.ObjectKey{Name: "gpu-node"}, gpuNode)).To(Succeed())
		Expect(gpuNode.Status.Allocatable).To(HaveKey(corev1.ResourceName("nvidia.com/gpu")))

		// The reconciler's cache must observe the node before a reconcile can keep the preset.
		Eventually(func(g Gomega) {
			nodes := &corev1.NodeList{}
			g.Expect(testEnv.Reconciler.Client.List(ctx, nodes)).To(Succeed())
			found := false
			for i := range nodes.Items {
				if _, ok := nodes.Items[i].Status.Allocatable["nvidia.com/gpu"]; ok {
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "reconciler cache should see a node with nvidia.com/gpu allocatable")
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

		// Re-trigger each poll: a reconcile must run after the cache observed the GPU.
		Eventually(func(g Gomega) {
			triggerReconcile(ctx, kserve, "hw-gpu-present")
			g.Expect(deployInputContainsPreset(mockDeployer(), acceleratorPresetName)).To(BeTrue(),
				"accelerator preset must be rendered once a node exposes nvidia.com/gpu; deployed=%v",
				deployedResourceNames(mockDeployer()))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})
})
