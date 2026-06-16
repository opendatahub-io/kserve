package kservemodule

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

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
			Namespace: "test-ns",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{},
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
	r := newOpenShiftReconciler(node, namespaceWithMCS("test-ns", "s0:c28,c27"))

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

func seedModelCacheReadinessObjects(t *testing.T, r *KserveModuleReconciler, dsMCS string) {
	t.Helper()
	g := NewWithT(t)
	ctx := context.Background()

	kserve := testKserveWithModelCache(common.Managed, "100Gi", []string{"worker-1"})
	g.Expect(r.createOrUpdateModelCachePV(ctx, kserve)).To(Succeed())
	g.Expect(r.createOrUpdateModelCachePVC(ctx, kserve)).To(Succeed())
	g.Expect(r.createOrUpdateLocalModelNodeGroup(ctx, kserve)).To(Succeed())

	if dsMCS != "" {
		g.Expect(r.Create(ctx, testDaemonSet(dsMCS))).To(Succeed())
	}
}

func TestCheckModelCacheReadiness_MCSMatch(t *testing.T) {
	g := NewWithT(t)

	r := newOpenShiftReconciler(namespaceWithMCS("test-ns", "s0:c29,c4"))
	seedModelCacheReadinessObjects(t, r, "s0:c29,c4")

	err := r.checkModelCacheReadiness(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
}

func TestCheckModelCacheReadiness_MCSMismatch(t *testing.T) {
	g := NewWithT(t)

	r := newOpenShiftReconciler(namespaceWithMCS("test-ns", "s0:c29,c4"))
	seedModelCacheReadinessObjects(t, r, "s0:c240,c768")

	err := r.checkModelCacheReadiness(context.Background())
	g.Expect(err).To(HaveOccurred())
	g.Expect(modelCacheReadinessReason(err)).To(Equal(modelCacheReasonSELinuxMCSMismatch))
}

func TestCheckModelCacheReadiness_DaemonSetMissing(t *testing.T) {
	g := NewWithT(t)

	r := newOpenShiftReconciler(namespaceWithMCS("test-ns", "s0:c29,c4"))
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
