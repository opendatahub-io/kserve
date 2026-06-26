package kservemodule

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

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

func TestBuildModelCachePV(t *testing.T) {
	g := NewWithT(t)

	pv, err := buildModelCachePV(resource.MustParse("500Gi"))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(pv.Name).To(Equal(modelCachePVName))
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
}

func TestBuildModelCachePVC(t *testing.T) {
	g := NewWithT(t)

	pvc, err := buildModelCachePVC(resource.MustParse("100Gi"), "test-ns")
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(pvc.Name).To(Equal(modelCachePVCName))
	g.Expect(pvc.Namespace).To(Equal("test-ns"))
	g.Expect(pvc.Spec.VolumeName).To(Equal(modelCachePVName))
	g.Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("100Gi")))
	g.Expect(*pvc.Spec.StorageClassName).To(Equal("local-storage"))
	g.Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
}

func TestBuildLocalModelNodeGroup(t *testing.T) {
	g := NewWithT(t)

	obj := buildLocalModelNodeGroup("200Gi")

	g.Expect(obj.GetName()).To(Equal(localModelNodeGroupName))
	g.Expect(obj.GroupVersionKind()).To(Equal(localModelNodeGroupGVK))

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

func TestBuildModelCacheResources_NilCacheSize(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{
		Spec: platformv1alpha1.KserveSpec{
			ModelCache: &platformv1alpha1.ModelCacheSpec{
				ManagementState: common.Managed,
			},
		},
	}
	_, err := buildModelCacheResources(kserve, "test-ns")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("cacheSize is required"))
}

func TestBuildModelCacheResources(t *testing.T) {
	g := NewWithT(t)

	kserve := testKserveWithModelCache(common.Managed, "500Gi", []string{"node1"})
	resources, err := buildModelCacheResources(kserve, "test-ns")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resources).To(HaveLen(3))

	g.Expect(resources[0].GetKind()).To(Equal("PersistentVolume"))
	g.Expect(resources[0].GetName()).To(Equal(modelCachePVName))
	g.Expect(resources[1].GetKind()).To(Equal("PersistentVolumeClaim"))
	g.Expect(resources[1].GetName()).To(Equal(modelCachePVCName))
	g.Expect(resources[2].GetKind()).To(Equal("LocalModelNodeGroup"))
	g.Expect(resources[2].GetName()).To(Equal(localModelNodeGroupName))
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

func TestEnsureModelCacheNamespace_CreatesNamespace(t *testing.T) {
	g := NewWithT(t)
	r := newReconcilerWithFakeClient()

	err := r.ensureModelCacheNamespace(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	ns := &corev1.Namespace{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: modelCacheNamespace}, ns)).To(Succeed())
	g.Expect(ns.Labels[securityEnforceLabel]).To(Equal("privileged"))
}

func TestEnsureModelCacheNamespace_PatchesExistingNamespace(t *testing.T) {
	g := NewWithT(t)
	existing := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   modelCacheNamespace,
			Labels: map[string]string{securityEnforceLabel: "baseline"},
		},
	}
	r := newReconcilerWithFakeClient(existing)

	err := r.ensureModelCacheNamespace(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	ns := &corev1.Namespace{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: modelCacheNamespace}, ns)).To(Succeed())
	g.Expect(ns.Labels[securityEnforceLabel]).To(Equal("privileged"))
}

func toUnstructuredConfigMap(cm *corev1.ConfigMap) unstructured.Unstructured {
	raw, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	u := unstructured.Unstructured{Object: raw}
	u.SetGroupVersionKind(configMapGVK)
	return u
}

func TestLocalModelConfigViaCustomizeKserveConfigMap(t *testing.T) {
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "test-ns"},
		Data: map[string]string{
			localModelConfigKeyName: `{"enabled": false}`,
			ingressConfigKeyName:    `{}`,
			serviceConfigKeyName:    `{}`,
		},
	}

	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"node1"})
	resources := []unstructured.Unstructured{toUnstructuredConfigMap(cm)}

	result, err := customizeKserveConfigMap(resources, kserve)
	g.Expect(err).NotTo(HaveOccurred())

	_, updatedCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).NotTo(HaveOccurred())

	var localModelData map[string]any
	err = json.Unmarshal([]byte(updatedCM.Data[localModelConfigKeyName]), &localModelData)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(localModelData["enabled"]).To(Equal(true))
	g.Expect(localModelData["jobNamespace"]).To(Equal(modelCacheNamespace))
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
	_ = appsv1.AddToScheme(scheme)

	allObjects := objects
	hasTestNS := false
	for _, obj := range objects {
		if ns, ok := obj.(*corev1.Namespace); ok && ns.Name == "test-ns" {
			hasTestNS = true
			break
		}
	}
	if !hasTestNS {
		allObjects = append([]client.Object{&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		}}, objects...)
	}

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

func TestModelCachePostRender_AppendsResourcesAndLabelsNodes(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	r := newReconcilerWithFakeClient(node)

	kserve := testKserveWithModelCache(common.Managed, "500Gi", []string{"worker-1"})
	initial := []unstructured.Unstructured{toUnstructuredDaemonSet(testDaemonSet(""))}

	result, err := modelCacheComponentPostRender(ctx, r, kserve, initial)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(len(result)).To(BeNumerically(">", len(initial)))

	kinds := make(map[string]bool)
	for _, res := range result {
		kinds[res.GetKind()] = true
	}
	g.Expect(kinds).To(HaveKey("PersistentVolume"))
	g.Expect(kinds).To(HaveKey("PersistentVolumeClaim"))
	g.Expect(kinds).To(HaveKey("LocalModelNodeGroup"))

	updatedNode := &corev1.Node{}
	g.Expect(r.Get(ctx, client.ObjectKey{Name: "worker-1"}, updatedNode)).To(Succeed())
	g.Expect(updatedNode.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))

	ns := &corev1.Namespace{}
	g.Expect(r.Get(ctx, client.ObjectKey{Name: modelCacheNamespace}, ns)).To(Succeed())
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

func TestLabelModelCacheNodes_ErrorsWhenNoSelectionCriteria(t *testing.T) {
	g := NewWithT(t)

	labeled := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "worker-1",
		Labels: map[string]string{modelCacheLabelKey: modelCacheLabelValue},
	}}

	r := newReconcilerWithFakeClient(labeled)
	kserve := &platformv1alpha1.Kserve{
		ObjectMeta: metav1.ObjectMeta{Name: platformv1alpha1.KserveInstanceName},
		Spec: platformv1alpha1.KserveSpec{
			ModelCache: &platformv1alpha1.ModelCacheSpec{
				ManagementState: common.Managed,
			},
		},
	}

	err := r.labelModelCacheNodes(context.Background(), kserve)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("no nodeNames or nodeSelector"))

	// Verify the existing label was NOT removed
	node := &corev1.Node{}
	g.Expect(r.Get(context.Background(), client.ObjectKey{Name: "worker-1"}, node)).To(Succeed())
	g.Expect(node.Labels[modelCacheLabelKey]).To(Equal(modelCacheLabelValue))
}

func TestModelCacheComponentPostRender_NilKserve(t *testing.T) {
	g := NewWithT(t)
	r := newReconcilerWithFakeClient()
	resources := []unstructured.Unstructured{
		toUnstructuredDaemonSet(testDaemonSet("")),
	}
	result, err := modelCacheComponentPostRender(context.Background(), r, nil, resources)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(HaveLen(1))
}

func TestCleanupModelCache_DeletesResources(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "worker-1",
		Labels: map[string]string{modelCacheLabelKey: modelCacheLabelValue},
	}}

	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1"})
	r := newReconcilerWithFakeClient(node)

	seedModelCacheObjects(t, r, kserve)

	err := r.cleanupModelCache(ctx)
	g.Expect(err).NotTo(HaveOccurred())

	// Verify node label removed
	updated := &corev1.Node{}
	g.Expect(r.Get(ctx, client.ObjectKey{Name: "worker-1"}, updated)).To(Succeed())
	_, hasLabel := updated.Labels[modelCacheLabelKey]
	g.Expect(hasLabel).To(BeFalse())

	// Verify PV deleted
	pv := &corev1.PersistentVolume{}
	err = r.Get(ctx, client.ObjectKey{Name: modelCachePVName}, pv)
	g.Expect(err).To(HaveOccurred())

	// Verify PVC deleted
	pvc := &corev1.PersistentVolumeClaim{}
	err = r.Get(ctx, client.ObjectKey{Name: modelCachePVCName, Namespace: modelCacheNamespace}, pvc)
	g.Expect(err).To(HaveOccurred())
}

// --- SELinux MCS tests ---

func newOpenShiftReconciler(objects ...client.Object) *KserveModuleReconciler {
	r := newReconcilerWithFakeClient(objects...)
	ct := cluster.ClusterTypeOpenShift
	r.clusterType = &ct
	return r
}

func namespaceWithMCS(name, mcs string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				openshiftSCCMCSAnnotation: mcs,
			},
		},
	}
}

func toUnstructuredDaemonSet(ds *appsv1.DaemonSet) unstructured.Unstructured {
	raw, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(ds)
	u := unstructured.Unstructured{Object: raw}
	u.SetGroupVersionKind(daemonSetGVK)
	return u
}

func testDaemonSet(mcsLevel string) *appsv1.DaemonSet {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      localModelNodeAgentDaemonSetName,
			Namespace: modelCacheNamespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "manager"}},
				},
			},
		},
	}
	if mcsLevel != "" {
		ds.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			SELinuxOptions: &corev1.SELinuxOptions{Level: mcsLevel},
		}
	}
	return ds
}

func TestResolveNamespaceMCSLevel_Valid(t *testing.T) {
	g := NewWithT(t)
	r := newReconcilerWithFakeClient(namespaceWithMCS("test-ns", "s0:c29,c4"))
	level, err := r.resolveNamespaceMCSLevel(context.Background(), "test-ns")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(level).To(Equal("s0:c29,c4"))
}

func TestResolveNamespaceMCSLevel_Missing(t *testing.T) {
	g := NewWithT(t)
	r := newReconcilerWithFakeClient()
	_, err := r.resolveNamespaceMCSLevel(context.Background(), "test-ns")
	g.Expect(err).To(HaveOccurred())
	g.Expect(modelCacheReadinessReason(err)).To(Equal(modelCacheReasonNamespaceMCSMissing))
}

func TestResolveNamespaceMCSLevel_Invalid(t *testing.T) {
	g := NewWithT(t)
	r := newReconcilerWithFakeClient(namespaceWithMCS("test-ns", "s0:c29,c4; rm -rf /"))
	_, err := r.resolveNamespaceMCSLevel(context.Background(), "test-ns")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("invalid MCS level"))
}

func TestPatchLocalModelNodeAgentMCSLevel(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{
		toUnstructuredDaemonSet(testDaemonSet("")),
	}
	result, err := patchLocalModelNodeAgentMCSLevel(resources, "s0:c29,c4")
	g.Expect(err).NotTo(HaveOccurred())
	_, ds, err := getIndexedResource[appsv1.DaemonSet](result, daemonSetGVK, localModelNodeAgentDaemonSetName)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ds.Spec.Template.Spec.SecurityContext.SELinuxOptions.Level).To(Equal("s0:c29,c4"))
}

func TestModelCacheComponentPostRender_PatchesMCS(t *testing.T) {
	g := NewWithT(t)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	r := newOpenShiftReconciler(node, namespaceWithMCS(modelCacheNamespace, "s0:c28,c27"))
	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1"})
	resources := []unstructured.Unstructured{
		toUnstructuredDaemonSet(testDaemonSet("")),
	}
	result, err := modelCacheComponentPostRender(context.Background(), r, kserve, resources)
	g.Expect(err).NotTo(HaveOccurred())
	_, ds, err := getIndexedResource[appsv1.DaemonSet](result, daemonSetGVK, localModelNodeAgentDaemonSetName)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ds.Spec.Template.Spec.SecurityContext.SELinuxOptions.Level).To(Equal("s0:c28,c27"))
}

func TestPatchModelCacheConfigNamespaceEnv(t *testing.T) {
	g := NewWithT(t)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: localmodelControllerDeployment},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "manager"}},
				},
			},
		},
	}
	rawDeploy, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	deployU := unstructured.Unstructured{Object: rawDeploy}
	deployU.SetGroupVersionKind(deploymentGVK)
	resources := []unstructured.Unstructured{
		deployU,
		toUnstructuredDaemonSet(testDaemonSet("")),
	}

	result, err := patchModelCacheConfigNamespaceEnv(resources, "app-ns")
	g.Expect(err).NotTo(HaveOccurred())

	_, patchedDeploy, err := getIndexedResource[appsv1.Deployment](result, deploymentGVK, localmodelControllerDeployment)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(patchedDeploy.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
		Name:  kserveConfigNamespaceEnv,
		Value: "app-ns",
	}))

	_, patchedDS, err := getIndexedResource[appsv1.DaemonSet](result, daemonSetGVK, localModelNodeAgentDaemonSetName)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(patchedDS.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
		Name:  kserveConfigNamespaceEnv,
		Value: "app-ns",
	}))
}

func readyLocalModelControllerDeployment() *appsv1.Deployment {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      localmodelControllerDeployment,
			Namespace: modelCacheNamespace,
		},
		Status: appsv1.DeploymentStatus{
			AvailableReplicas: 1,
		},
	}
	return dep
}

func seedModelCacheObjects(t *testing.T, r *KserveModuleReconciler, kserve *platformv1alpha1.Kserve) {
	t.Helper()
	g := NewWithT(t)
	ctx := context.Background()
	resources, err := buildModelCacheResources(kserve, modelCacheNamespace)
	g.Expect(err).NotTo(HaveOccurred())
	for i := range resources {
		existing := resources[i].DeepCopy()
		if err := r.Get(ctx, client.ObjectKeyFromObject(existing), existing); err == nil {
			continue
		}
		g.Expect(r.Create(ctx, &resources[i])).To(Succeed())
	}
}

func seedModelCacheReadinessObjects(t *testing.T, r *KserveModuleReconciler, dsMCS string) {
	t.Helper()
	g := NewWithT(t)
	ctx := context.Background()
	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1"})
	seedModelCacheObjects(t, r, kserve)
	g.Expect(r.Create(ctx, readyLocalModelControllerDeployment())).To(Succeed())
	if dsMCS != "" {
		g.Expect(r.Create(ctx, testDaemonSet(dsMCS))).To(Succeed())
	}
}

func TestCheckModelCacheReadiness_MCSMatch(t *testing.T) {
	g := NewWithT(t)
	r := newOpenShiftReconciler(namespaceWithMCS(modelCacheNamespace, "s0:c29,c4"))
	seedModelCacheReadinessObjects(t, r, "s0:c29,c4")
	err := r.checkModelCacheReadiness(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
}

func TestCheckModelCacheReadiness_MCSMismatch(t *testing.T) {
	g := NewWithT(t)
	r := newOpenShiftReconciler(namespaceWithMCS(modelCacheNamespace, "s0:c29,c4"))
	seedModelCacheReadinessObjects(t, r, "s0:c240,c768")
	err := r.checkModelCacheReadiness(context.Background())
	g.Expect(err).To(HaveOccurred())
	g.Expect(modelCacheReadinessReason(err)).To(Equal(modelCacheReasonSELinuxMCSMismatch))
}

func TestCheckModelCacheReadiness_DaemonSetMissing(t *testing.T) {
	g := NewWithT(t)
	r := newOpenShiftReconciler(namespaceWithMCS(modelCacheNamespace, "s0:c29,c4"))
	seedModelCacheReadinessObjects(t, r, "")
	err := r.checkModelCacheReadiness(context.Background())
	g.Expect(err).To(HaveOccurred())
	g.Expect(modelCacheReadinessReason(err)).To(Equal(modelCacheReasonResourcesNotReady))
	g.Expect(err.Error()).To(ContainSubstring("DaemonSet kserve-localmodelnode-agent not found"))
}

func TestCheckModelCacheReadiness_SkipsMCSOnKubernetes(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = platformv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	ns := namespaceWithMCS("test-ns", "s0:c29,c4")
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	ct := cluster.ClusterTypeKubernetes
	r := &KserveModuleReconciler{
		Client:                cli,
		Scheme:                scheme,
		applicationsNamespace: "test-ns",
		clusterType:           &ct,
	}
	seedModelCacheReadinessObjects(t, r, "")
	err := r.checkModelCacheReadiness(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
}

func TestModelCacheReadinessReason(t *testing.T) {
	g := NewWithT(t)
	g.Expect(modelCacheReadinessReason(fmt.Errorf("generic"))).To(Equal(modelCacheReasonResourcesNotReady))
	g.Expect(modelCacheReadinessReason(newModelCacheReadinessError(modelCacheReasonSELinuxMCSMismatch, "mismatch"))).
		To(Equal(modelCacheReasonSELinuxMCSMismatch))
}
