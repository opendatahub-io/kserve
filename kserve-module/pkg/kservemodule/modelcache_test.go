package kservemodule

import (
	"context"
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func testKserveWithModelCache(managementState common.ManagementState, cacheSize string, nodeNames []string) *platformv1alpha1.Kserve {
	qty := resource.MustParse(cacheSize)
	kserve := &platformv1alpha1.Kserve{
		ObjectMeta: metav1.ObjectMeta{Name: platformv1alpha1.KserveInstanceName},
		Spec: platformv1alpha1.KserveSpec{
			ModelCache: &platformv1alpha1.ModelCacheSpec{
				ManagementState: managementState,
				CacheSize:       &qty,
				NodeNames:       nodeNames,
			},
		},
	}
	return kserve
}

func TestIsModelCacheEnabled(t *testing.T) {
	tests := []struct {
		name     string
		kserve   *platformv1alpha1.Kserve
		expected bool
	}{
		{
			name: "nil ModelCache returns false",
			kserve: &platformv1alpha1.Kserve{
				Spec: platformv1alpha1.KserveSpec{},
			},
			expected: false,
		},
		{
			name:     "Managed returns true",
			kserve:   testKserveWithModelCache(common.Managed, "100Gi", []string{"node1"}),
			expected: true,
		},
		{
			name:     "Removed returns false",
			kserve:   testKserveWithModelCache(common.Removed, "100Gi", []string{"node1"}),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(isModelCacheEnabled(tt.kserve)).To(Equal(tt.expected))
		})
	}
}

func TestCreateOrUpdateModelCachePV(t *testing.T) {
	g := NewWithT(t)

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	r := newReconcilerWithFakeClient(node)

	kserve := testKserveWithModelCache(common.Managed, "500Gi", []string{"worker-1"})
	err := r.createOrUpdateModelCachePV(context.Background(), kserve)
	g.Expect(err).NotTo(HaveOccurred())

	pv := &corev1.PersistentVolume{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: modelCachePVName}, pv)).To(Succeed())
	g.Expect(pv.Spec.Capacity[corev1.ResourceStorage]).To(Equal(resource.MustParse("500Gi")))
	g.Expect(pv.Spec.StorageClassName).To(Equal("local-storage"))
	g.Expect(pv.Spec.PersistentVolumeReclaimPolicy).To(Equal(corev1.PersistentVolumeReclaimRetain))
	g.Expect(pv.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
	g.Expect(pv.Spec.HostPath).NotTo(BeNil())
	g.Expect(pv.Spec.HostPath.Path).To(Equal(modelCacheHostDir))
	g.Expect(pv.Spec.NodeAffinity).NotTo(BeNil())
	g.Expect(pv.Spec.NodeAffinity.Required.NodeSelectorTerms).To(HaveLen(1))
	g.Expect(pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Key).To(Equal(modelCacheLabelKey))
	g.Expect(pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Values).To(ContainElement(modelCacheLabelValue))
	g.Expect(pv.OwnerReferences).To(HaveLen(1))
	g.Expect(pv.OwnerReferences[0].Name).To(Equal(platformv1alpha1.KserveInstanceName))
}

func TestCreateOrUpdateModelCachePV_NilCacheSize(t *testing.T) {
	g := NewWithT(t)

	r := newReconcilerWithFakeClient()
	kserve := &platformv1alpha1.Kserve{
		ObjectMeta: metav1.ObjectMeta{Name: platformv1alpha1.KserveInstanceName},
		Spec: platformv1alpha1.KserveSpec{
			ModelCache: &platformv1alpha1.ModelCacheSpec{
				ManagementState: common.Managed,
			},
		},
	}

	err := r.createOrUpdateModelCachePV(context.Background(), kserve)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("cacheSize is required"))
}

func TestCreateOrUpdateModelCachePVC(t *testing.T) {
	g := NewWithT(t)

	r := newReconcilerWithFakeClient()
	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"node1"})

	err := r.createOrUpdateModelCachePVC(context.Background(), kserve)
	g.Expect(err).NotTo(HaveOccurred())

	pvc := &corev1.PersistentVolumeClaim{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: modelCachePVCName, Namespace: "test-ns"}, pvc)).To(Succeed())
	g.Expect(pvc.Spec.VolumeName).To(Equal(modelCachePVName))
	g.Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("100Gi")))
	g.Expect(*pvc.Spec.StorageClassName).To(Equal("local-storage"))
	g.Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
	g.Expect(pvc.OwnerReferences).To(HaveLen(1))
	g.Expect(pvc.OwnerReferences[0].Name).To(Equal(platformv1alpha1.KserveInstanceName))
}

func TestCreateOrUpdateLocalModelNodeGroup(t *testing.T) {
	g := NewWithT(t)

	r := newReconcilerWithFakeClient()
	kserve := testKserveWithModelCache(common.Managed, "200Gi", []string{"node1"})

	err := r.createOrUpdateLocalModelNodeGroup(context.Background(), kserve)
	g.Expect(err).NotTo(HaveOccurred())

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(localModelNodeGroupGVK)
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: localModelNodeGroupName}, obj)).To(Succeed())
	g.Expect(obj.GetOwnerReferences()).To(HaveLen(1))
	g.Expect(obj.GetOwnerReferences()[0].Name).To(Equal(platformv1alpha1.KserveInstanceName))

	spec, found, err := unstructured.NestedMap(obj.Object, "spec")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(spec["storageLimit"]).To(Equal("200Gi"))

	pvSpec, ok := spec["persistentVolumeSpec"].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(pvSpec["storageClassName"]).To(Equal("local-storage"))

	hostPath, ok := pvSpec["hostPath"].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(hostPath["path"]).To(Equal(modelCacheHostDir))
}

func TestLabelModelCacheNodes_ByNodeNames(t *testing.T) {
	g := NewWithT(t)

	node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-2"}}
	r := newReconcilerWithFakeClient(node1, node2)

	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1", "worker-2"})
	g.Expect(r.labelModelCacheNodes(context.Background(), kserve)).To(Succeed())

	for _, name := range []string{"worker-1", "worker-2"} {
		node := &corev1.Node{}
		g.Expect(r.Get(context.Background(), client.ObjectKey{Name: name}, node)).To(Succeed())
		g.Expect(node.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))
	}
}

func TestLabelModelCacheNodes_ByNodeSelector(t *testing.T) {
	g := NewWithT(t)

	gpuNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "gpu-node-1",
		Labels: map[string]string{"nvidia.com/gpu": "true"},
	}}
	cpuNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "cpu-node-1",
		Labels: map[string]string{"role": "compute"},
	}}
	r := newReconcilerWithFakeClient(gpuNode, cpuNode)

	qty := resource.MustParse("100Gi")
	kserve := &platformv1alpha1.Kserve{
		ObjectMeta: metav1.ObjectMeta{Name: platformv1alpha1.KserveInstanceName},
		Spec: platformv1alpha1.KserveSpec{
			ModelCache: &platformv1alpha1.ModelCacheSpec{
				ManagementState: common.Managed,
				CacheSize:       &qty,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"nvidia.com/gpu": "true"},
				},
			},
		},
	}

	g.Expect(r.labelModelCacheNodes(context.Background(), kserve)).To(Succeed())

	gpu := &corev1.Node{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "gpu-node-1"}, gpu)).To(Succeed())
	g.Expect(gpu.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))

	cpu := &corev1.Node{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "cpu-node-1"}, cpu)).To(Succeed())
	_, hasLabel := cpu.Labels[modelCacheLabelKey]
	g.Expect(hasLabel).To(BeFalse())
}

func TestUpdateNamespacePSA(t *testing.T) {
	tests := []struct {
		name          string
		level         string
		expectLabel   string
		expectAnnot   bool
	}{
		{
			name:        "privileged sets label and annotation",
			level:       "privileged",
			expectLabel: "privileged",
			expectAnnot: true,
		},
		{
			name:        "baseline sets label and removes annotation",
			level:       "baseline",
			expectLabel: "baseline",
			expectAnnot: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)

			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-ns",
					Labels:      map[string]string{securityEnforceLabel: "restricted"},
					Annotations: map[string]string{psaElevatedByAnnotation: psaElevatedByValue},
				},
			}

			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
			r := &KserveModuleReconciler{Client: cli, applicationsNamespace: "test-ns"}

			err := r.updateNamespacePSA(context.Background(), tt.level)
			g.Expect(err).NotTo(HaveOccurred())

			updated := &corev1.Namespace{}
			g.Expect(cli.Get(context.Background(), client.ObjectKey{Name: "test-ns"}, updated)).To(Succeed())
			g.Expect(updated.Labels[securityEnforceLabel]).To(Equal(tt.expectLabel))

			annot, exists := updated.Annotations[psaElevatedByAnnotation]
			if tt.expectAnnot {
				g.Expect(exists).To(BeTrue())
				g.Expect(annot).To(Equal(psaElevatedByValue))
			} else {
				g.Expect(exists).To(BeFalse())
			}
		})
	}
}

func TestUpdateNamespacePSA_NoOpWhenAlreadySet(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-ns",
			Labels:      map[string]string{securityEnforceLabel: "privileged"},
			Annotations: map[string]string{psaElevatedByAnnotation: psaElevatedByValue},
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	r := &KserveModuleReconciler{Client: cli, applicationsNamespace: "test-ns"}

	err := r.updateNamespacePSA(context.Background(), "privileged")
	g.Expect(err).NotTo(HaveOccurred())
}

func toUnstructuredConfigMap(cm *corev1.ConfigMap) unstructured.Unstructured {
	raw, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	u := unstructured.Unstructured{Object: raw}
	u.SetGroupVersionKind(configMapGVK)
	return u
}

func TestUpdateLocalModelConfig(t *testing.T) {
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "test-ns"},
		Data: map[string]string{
			localModelConfigKeyName: `{"enabled": false}`,
		},
	}

	resources := []unstructured.Unstructured{toUnstructuredConfigMap(cm)}

	result, err := updateLocalModelConfig(resources, true, "my-namespace")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(HaveLen(1))

	_, updatedCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).NotTo(HaveOccurred())

	var localModelData map[string]any
	err = json.Unmarshal([]byte(updatedCM.Data[localModelConfigKeyName]), &localModelData)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(localModelData["enabled"]).To(Equal(true))
	g.Expect(localModelData["jobNamespace"]).To(Equal("my-namespace"))
}

func TestForceReconcileKserveAgentImage(t *testing.T) {
	t.Run("updates image when env var differs", func(t *testing.T) {
		g := NewWithT(t)

		t.Setenv("RELATED_IMAGE_ODH_KSERVE_AGENT_IMAGE", "quay.io/new-agent:v2")

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "test-ns"},
			Data: map[string]string{
				openshiftConfigKeyName: `{"modelcachePermissionFixImage": "quay.io/old-agent:v1"}`,
			},
		}

		resources := []unstructured.Unstructured{toUnstructuredConfigMap(cm)}

		result, err := forceReconcileKserveAgentImage(resources)
		g.Expect(err).NotTo(HaveOccurred())

		_, updatedCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
		g.Expect(err).NotTo(HaveOccurred())

		var cfg map[string]any
		err = json.Unmarshal([]byte(updatedCM.Data[openshiftConfigKeyName]), &cfg)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(cfg["modelcachePermissionFixImage"]).To(Equal("quay.io/new-agent:v2"))
	})

	t.Run("no-op when image already matches", func(t *testing.T) {
		g := NewWithT(t)

		t.Setenv("RELATED_IMAGE_ODH_KSERVE_AGENT_IMAGE", "quay.io/agent:v1")

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "test-ns"},
			Data: map[string]string{
				openshiftConfigKeyName: `{"modelcachePermissionFixImage": "quay.io/agent:v1"}`,
			},
		}

		resources := []unstructured.Unstructured{toUnstructuredConfigMap(cm)}

		result, err := forceReconcileKserveAgentImage(resources)
		g.Expect(err).NotTo(HaveOccurred())

		_, updatedCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(updatedCM.Data[openshiftConfigKeyName]).To(ContainSubstring("quay.io/agent:v1"))
	})

	t.Run("no-op when env var not set", func(t *testing.T) {
		g := NewWithT(t)

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "test-ns"},
			Data: map[string]string{
				openshiftConfigKeyName: `{"modelcachePermissionFixImage": "quay.io/agent:v1"}`,
			},
		}

		resources := []unstructured.Unstructured{toUnstructuredConfigMap(cm)}

		result, err := forceReconcileKserveAgentImage(resources)
		g.Expect(err).NotTo(HaveOccurred())

		_, updatedCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(updatedCM.Data[openshiftConfigKeyName]).To(ContainSubstring("quay.io/agent:v1"))
	})
}

func TestModelCacheResources(t *testing.T) {
	g := NewWithT(t)

	objects := modelCacheResources("test-ns")
	g.Expect(objects).To(HaveLen(3))

	pvc := objects[0].(*corev1.PersistentVolumeClaim)
	g.Expect(pvc.Name).To(Equal(modelCachePVCName))
	g.Expect(pvc.Namespace).To(Equal("test-ns"))

	pv := objects[1].(*corev1.PersistentVolume)
	g.Expect(pv.Name).To(Equal(modelCachePVName))

	lmng := objects[2].(*unstructured.Unstructured)
	g.Expect(lmng.GetName()).To(Equal(localModelNodeGroupName))
	g.Expect(lmng.GroupVersionKind()).To(Equal(localModelNodeGroupGVK))
}

func newReconcilerWithFakeClient(objects ...client.Object) *KserveModuleReconciler {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = platformv1alpha1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
	}
	allObjects := append([]client.Object{ns}, objects...)

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(allObjects...).Build()
	return &KserveModuleReconciler{
		Client:                cli,
		Scheme:                scheme,
		applicationsNamespace: "test-ns",
	}
}

func TestIsModelCacheEnabled_ControlsComponentRouting(t *testing.T) {
	g := NewWithT(t)

	g.Expect(isModelCacheEnabled(&platformv1alpha1.Kserve{Spec: platformv1alpha1.KserveSpec{}})).To(BeFalse())
	g.Expect(isModelCacheEnabled(testKserveWithModelCache(common.Removed, "100Gi", []string{"node1"}))).To(BeFalse())
	g.Expect(isModelCacheEnabled(testKserveWithModelCache(common.Managed, "100Gi", []string{"node1"}))).To(BeTrue())
}

func TestReconcileModelCacheResources_CreatesResources(t *testing.T) {
	g := NewWithT(t)

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	r := newReconcilerWithFakeClient(node)

	kserve := testKserveWithModelCache(common.Managed, "500Gi", []string{"worker-1"})
	err := r.reconcileModelCacheResources(context.Background(), kserve)
	g.Expect(err).NotTo(HaveOccurred())

	pv := &corev1.PersistentVolume{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: modelCachePVName}, pv)).To(Succeed())
	g.Expect(pv.Spec.Capacity[corev1.ResourceStorage]).To(Equal(resource.MustParse("500Gi")))

	pvc := &corev1.PersistentVolumeClaim{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: modelCachePVCName, Namespace: "test-ns"}, pvc)).To(Succeed())

	updatedNode := &corev1.Node{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "worker-1"}, updatedNode)).To(Succeed())
	g.Expect(updatedNode.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))

	ns := &corev1.Namespace{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "test-ns"}, ns)).To(Succeed())
	g.Expect(ns.Labels[securityEnforceLabel]).To(Equal("privileged"))
}

func TestLabelModelCacheNodes(t *testing.T) {
	t.Run("labels desired nodes and unlabels stale ones", func(t *testing.T) {
		g := NewWithT(t)

		desired := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
		stale := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   "worker-2",
			Labels: map[string]string{modelCacheLabelKey: modelCacheLabelValue},
		}}

		r := newReconcilerWithFakeClient(desired, stale)
		kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1"})

		err := r.labelModelCacheNodes(context.Background(), kserve)
		g.Expect(err).NotTo(HaveOccurred())

		labeled := &corev1.Node{}
		g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "worker-1"}, labeled)).To(Succeed())
		g.Expect(labeled.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))

		unlabeled := &corev1.Node{}
		g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "worker-2"}, unlabeled)).To(Succeed())
		_, hasLabel := unlabeled.Labels[modelCacheLabelKey]
		g.Expect(hasLabel).To(BeFalse())
	})

	t.Run("skips already labeled nodes", func(t *testing.T) {
		g := NewWithT(t)

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   "worker-1",
			Labels: map[string]string{modelCacheLabelKey: modelCacheLabelValue},
		}}

		r := newReconcilerWithFakeClient(node)
		kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1"})

		err := r.labelModelCacheNodes(context.Background(), kserve)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &corev1.Node{}
		g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "worker-1"}, updated)).To(Succeed())
		g.Expect(updated.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))
	})
}

func TestCleanupModelCache_UnlabelsNodes(t *testing.T) {
	g := NewWithT(t)

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "worker-1",
		Labels: map[string]string{modelCacheLabelKey: modelCacheLabelValue},
	}}

	r := newReconcilerWithFakeClient(node)

	err := r.cleanupModelCache(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	updated := &corev1.Node{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "worker-1"}, updated)).To(Succeed())
	_, hasLabel := updated.Labels[modelCacheLabelKey]
	g.Expect(hasLabel).To(BeFalse())

	ns := &corev1.Namespace{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "test-ns"}, ns)).To(Succeed())
	g.Expect(ns.Labels[securityEnforceLabel]).To(Equal("baseline"))
}
