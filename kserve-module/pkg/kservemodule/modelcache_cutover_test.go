package kservemodule

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"
)

func TestEnsureModelCacheNamespace(t *testing.T) {
	r := newReconcilerWithFakeClient()
	if err := r.ensureModelCacheNamespace(context.Background()); err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: modelCacheNamespace}, ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels[securityEnforceLabel] != "privileged" || ns.Annotations[psaElevatedByAnnotation] != psaElevatedByValue {
		t.Fatalf("new namespace lacks managed privileged PSA: %#v", ns.ObjectMeta)
	}
	ns.Labels[securityEnforceLabel] = "restricted"
	if err := r.Update(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureModelCacheNamespace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.updateNamespacePSA(context.Background(), modelCacheNamespace, "privileged"); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKey{Name: modelCacheNamespace}, ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels[securityEnforceLabel] != "privileged" {
		t.Fatalf("existing namespace PSA was not repaired: %#v", ns.Labels)
	}
}

func TestEnsureModelCacheNamespaceRejectsUnmanagedNamespace(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: modelCacheNamespace}}
	r := newReconcilerWithFakeClient(ns)
	if err := r.ensureModelCacheNamespace(context.Background()); err == nil {
		t.Fatal("unmanaged namespace was accepted")
	}
	ns.Labels = map[string]string{securityEnforceLabel: "privileged"}
	if err := r.Update(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKey{Name: modelCacheNamespace}, ns); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureModelCacheNamespace(context.Background()); err == nil {
		t.Fatal("privileged PSA without ownership marker was accepted")
	}
	ns.Annotations = map[string]string{psaElevatedByAnnotation: psaElevatedByValue}
	if err := r.Update(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureModelCacheNamespace(context.Background()); err == nil {
		t.Fatal("PSA elevation annotation without module ownership label was accepted")
	}
	ns.Labels[odhLabels.PlatformPartOf] = "other-component"
	if err := r.Update(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureModelCacheNamespace(context.Background()); err == nil {
		t.Fatal("namespace belonging to another component was accepted")
	}
}

func TestModelCacheNamespaceCanBeReenabled(t *testing.T) {
	ctx := context.Background()
	r := newReconcilerWithFakeClient()
	assertNamespace := func(level, annotation string) {
		t.Helper()
		ns := &corev1.Namespace{}
		if err := r.Get(ctx, client.ObjectKey{Name: modelCacheNamespace}, ns); err != nil {
			t.Fatal(err)
		}
		if ns.Labels[odhLabels.PlatformPartOf] != KserveComponentName {
			t.Fatalf("namespace lost its module ownership label: %#v", ns.Labels)
		}
		if ns.Labels[securityEnforceLabel] != level || ns.Annotations[psaElevatedByAnnotation] != annotation {
			t.Fatalf("expected PSA %q and annotation %q, got %#v", level, annotation, ns.ObjectMeta)
		}
	}
	if err := r.ensureModelCacheNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	assertNamespace("privileged", psaElevatedByValue)

	if err := r.cleanupModelCache(ctx); err != nil {
		t.Fatal(err)
	}
	assertNamespace("baseline", "")

	if err := r.ensureModelCacheNamespace(ctx); err != nil {
		t.Fatalf("re-enabling ModelCache rejected its retained namespace: %v", err)
	}
	if err := r.updateNamespacePSA(ctx, modelCacheNamespace, "privileged"); err != nil {
		t.Fatal(err)
	}
	assertNamespace("privileged", psaElevatedByValue)
}

func TestModelCacheCutoverStopsOldWorkloadsBeforeVolumes(t *testing.T) {
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: localmodelControllerDeployment, Namespace: "test-ns", Labels: labels}}
	daemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: localModelNodeAgentDaemonSetName, Namespace: "test-ns", Labels: labels}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName}}
	r := newReconcilerWithFakeClient(deployment, daemonSet, pvc)
	if err := r.prepareModelCacheCutover(context.Background(), nil); err == nil {
		t.Fatal("expected first reconcile to wait for old Deployment")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old PVC was removed before workloads stopped: %v", err)
	}
	if err := r.prepareModelCacheCutover(context.Background(), nil); err == nil {
		t.Fatal("expected second reconcile to wait for old DaemonSet")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old PVC was removed before agent stopped: %v", err)
	}
}

func TestModelCacheCutoverReleasesOldBoundStaticPV(t *testing.T) {
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels, UID: types.UID("old-claim")},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName, Labels: labels},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: modelCacheHostDir}},
			ClaimRef:                      &corev1.ObjectReference{Namespace: "test-ns", Name: modelCachePVCName, UID: pvc.UID},
		}}
	r := newReconcilerWithFakeClient(pvc, pv)
	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected to wait for old static PVC deletion")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old static PVC still exists: %v", err)
	}
	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected to wait for retained PV deletion")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); !apierrors.IsNotFound(err) {
		t.Fatalf("retained static PV still exists: %v", err)
	}
	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err != nil {
		t.Fatalf("cutover did not complete after old claim and PV were removed: %v", err)
	}
}

func TestModelCacheCutoverDoesNotMutateRenderedNamespaces(t *testing.T) {
	rendered := []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "kserve-modelcache-parameters",
			"namespace": modelCacheNamespace,
		},
	}}}

	if err := newReconcilerWithFakeClient().prepareModelCacheCutover(context.Background(), rendered); err != nil {
		t.Fatalf("ModelCache cutover failed: %v", err)
	}
	if got := rendered[0].GetNamespace(); got != modelCacheNamespace {
		t.Fatalf("cutover changed rendered resource namespace to %q", got)
	}
}

func TestModelCacheCutoverIgnoresUnavailableCertificateAPI(t *testing.T) {
	ctx := context.Background()
	certificateGVK := schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-ns"}}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind() == certificateGVK {
					return &apimeta.NoKindMatchError{
						GroupKind:        certificateGVK.GroupKind(),
						SearchedVersions: []string{certificateGVK.Version},
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	r := &KserveModuleReconciler{
		Client:                cli,
		Scheme:                scheme,
		applicationsNamespace: "test-ns",
	}
	certificate := unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK)
	certificate.SetName("kernelcache-root-ca")
	certificate.SetNamespace(modelCacheNamespace)

	if err := r.prepareModelCacheCutover(ctx, []unstructured.Unstructured{certificate}); err != nil {
		t.Fatalf("unavailable cert-manager API should be treated as absent during cleanup: %v", err)
	}
}

func TestModelCacheCutoverPreservesAmbiguousStaticPV(t *testing.T) {
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName,
		Labels: map[string]string{odhLabels.PlatformPartOf: KserveComponentName}},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/different/storage"}},
			ClaimRef:               &corev1.ObjectReference{Namespace: "test-ns", Name: modelCachePVCName}}}
	r := newReconcilerWithFakeClient(pv)
	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected an ambiguous static PV to block cutover")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("ambiguous static PV was deleted: %v", err)
	}
}

func TestModelCacheCutoverDeletesOnlyLocalModelNodeJobs(t *testing.T) {
	oldJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "iris-worker-1", Namespace: "test-ns",
		Labels:          map[string]string{"model": "iris", "node": "worker-1"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "serving.kserve.io/v1alpha1", Kind: "LocalModelNode", Name: "worker-1", UID: types.UID("node-uid"), Controller: ptr.To(true)}}}}
	sharedPVCJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "shared-import", Namespace: "test-ns",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "serving.kserve.io/v1alpha1", Kind: "LocalModelNamespaceCache", Name: "iris", Controller: ptr.To(true)}}}}
	r := newReconcilerWithFakeClient(oldJob, sharedPVCJob)
	if err := r.removeOldModelCacheJobs(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected to wait for old node Job termination")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(oldJob), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old node Job still exists: %v", err)
	}
	if err := r.removeOldModelCacheJobs(context.Background(), "test-ns"); err != nil {
		t.Fatalf("cutover did not proceed after old Job deletion: %v", err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(sharedPVCJob), &batchv1.Job{}); err != nil {
		t.Fatalf("shared-PVC Job was removed: %v", err)
	}
}

func TestOldModelCacheManifestIncludesKernelCacheCertificates(t *testing.T) {
	for _, kind := range []string{"Certificate", "Issuer"} {
		resource := &unstructured.Unstructured{}
		resource.SetKind(kind)
		if !isOldNamespacedModelCacheManifest(resource) {
			t.Fatalf("%s was not selected for old namespace cleanup", kind)
		}
	}
}

func TestModelCacheCutoverRemovesHealthyLocalModelCacheVolumes(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       "test-ns",
			UID:             types.UID("old-pvc"),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers-download",
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: "test-ns", Name: "iris-workers", UID: pvc.UID},
		},
	}
	r := newReconcilerWithFakeClient(cache, group, pvc, pv)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected old PVC deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old LocalModelCache PVC still exists: %v", err)
	}
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected old PV deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old LocalModelCache PV still exists: %v", err)
	}
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err != nil {
		t.Fatalf("healthy LocalModelCache volume cleanup did not converge: %v", err)
	}
}

func TestModelCacheCutoverRemovesLocalModelNamespaceCacheVolumes(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelNamespaceCache("iris", "user-ns", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-user-ns-download", Namespace: "test-ns"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-user-ns-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-user-ns-download"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: "test-ns", Name: pvc.Name},
		},
	}
	r := newReconcilerWithFakeClient(cache, group, pvc, pv)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected old namespace cache PVC deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old LocalModelNamespaceCache PVC still exists: %v", err)
	}
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected old namespace cache PV deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old LocalModelNamespaceCache PV still exists: %v", err)
	}
}

func TestModelCacheCutoverDeletesActiveOldDownloadJobWithoutRestartingIt(t *testing.T) {
	ctx := context.Background()
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "iris-worker-1",
		Namespace: "test-ns",
		Labels:    map[string]string{"model": "iris", "node": "worker-1"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "serving.kserve.io/v1alpha1", Kind: "LocalModelNode", Name: "worker-1",
			UID: types.UID("node-uid"), Controller: ptr.To(true),
		}},
	}}
	r := newReconcilerWithFakeClient(job)
	if err := r.removeOldModelCacheJobs(ctx, "test-ns"); err == nil {
		t.Fatal("expected old download Job deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old download Job still exists: %v", err)
	}
	if err := r.removeOldModelCacheJobs(ctx, "test-ns"); err != nil {
		t.Fatalf("old download Job cleanup did not converge: %v", err)
	}
}

func TestModelCacheCutoverPreservesUnrelatedDownloadVolume(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	foreignOwner := localModelCacheOwner("other", "other-uid")
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{foreignOwner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: "test-ns", Name: "iris-workers"},
		},
	}
	r := newReconcilerWithFakeClient(cache, group, pv)
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected conflicting ownership to block cleanup")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("conflicting PV was removed: %v", err)
	}
}

func TestModelCacheCutoverFreshInstallConverges(t *testing.T) {
	if err := newReconcilerWithFakeClient().prepareModelCacheCutover(context.Background(), nil); err != nil {
		t.Fatalf("fresh ModelCache install should not require cutover: %v", err)
	}
}

func TestModelCacheCutoverPreservesTargetUnboundDownloadPV(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
		},
	}
	r := newReconcilerWithFakeClient(cache, group, pv)
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err != nil {
		t.Fatalf("unbound download PV should be reusable by target PVC: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("target unbound download PV was removed: %v", err)
	}
}

func TestModelCacheCutoverPreservesOldStaticPVCForWrongUnboundTargetPVC(t *testing.T) {
	ctx := context.Background()
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	oldPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        modelCachePVName,
			Labels:      labels,
			Annotations: map[string]string{modelCacheClaimNamespaceAnnotation: modelCacheNamespace},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: modelCacheHostDir}},
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: modelCacheNamespace, Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "some-other-pv"},
	}
	r := newReconcilerWithFakeClient(oldPVC, pv, targetPVC)

	if err := r.removeOldStaticVolume(ctx, "test-ns"); err == nil {
		t.Fatal("expected an incompatible unbound target PVC to block cutover")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(oldPVC), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old static PVC was removed before validating the target PVC: %v", err)
	}
}

func TestModelCacheCutoverPreservesCompatibleUnboundStaticTargetPVC(t *testing.T) {
	ctx := context.Background()
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	oldPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        modelCachePVName,
			Labels:      labels,
			Annotations: map[string]string{modelCacheClaimNamespaceAnnotation: modelCacheNamespace},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: modelCacheHostDir}},
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: modelCacheNamespace, Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	r := newReconcilerWithFakeClient(oldPVC, pv, targetPVC)

	if err := r.removeOldStaticVolume(ctx, "test-ns"); err == nil {
		t.Fatal("expected old static PVC deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(oldPVC), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old static PVC was not removed after validating the target PVC: %v", err)
	}
	if err := r.removeOldStaticVolume(ctx, "test-ns"); err != nil {
		t.Fatalf("compatible unbound static target PVC did not allow cutover: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("compatible unbound static PV was removed: %v", err)
	}
}

func TestModelCacheCutoverPreservesOldPVCForWrongUnboundDownloadTargetPVC(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	oldPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       "test-ns",
			UID:             types.UID("old-pvc"),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers", Namespace: modelCacheNamespace},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "some-other-pv"},
	}
	r := newReconcilerWithFakeClient(cache, group, oldPVC, pv, targetPVC)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected an incompatible unbound target PVC to block cutover")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(oldPVC), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old download PVC was removed before validating the target PVC: %v", err)
	}
}

func TestModelCacheCutoverPreservesCompatibleUnboundDownloadTargetPVC(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	oldPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       "test-ns",
			UID:             types.UID("old-pvc"),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       modelCacheNamespace,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	r := newReconcilerWithFakeClient(cache, group, oldPVC, pv, targetPVC)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected old download PVC deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(oldPVC), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old download PVC was not removed after validating the target PVC: %v", err)
	}
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err != nil {
		t.Fatalf("compatible unbound download target PVC did not allow cutover: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("compatible unbound download PV was removed: %v", err)
	}
}

func TestModelCacheCutoverPreservesTargetBoundPVWithoutOldPVCAfterNodeGroupChange(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	group.Object["spec"].(map[string]any)["persistentVolumeSpec"].(map[string]any)["hostPath"].(map[string]any)["path"] = "/var/lib/kserve/changed"
	owner := localModelCacheOwner("iris", "cache-uid")
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: modelCacheNamespace, Name: "iris-workers", UID: types.UID("target-pvc")},
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers", Namespace: modelCacheNamespace, UID: types.UID("target-pvc")},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	r := newReconcilerWithFakeClient(cache, group, pv, targetPVC)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err != nil {
		t.Fatalf("target-bound PV should remain valid after the old PVC is gone: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("target-bound PV was removed: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(targetPVC), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("target PVC was removed: %v", err)
	}
}

func TestModelCacheCutoverPreservesUnboundPVWithoutOldPVCAfterNodeGroupChange(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	group.Object["spec"].(map[string]any)["persistentVolumeSpec"].(map[string]any)["hostPath"].(map[string]any)["path"] = "/var/lib/kserve/changed"
	owner := localModelCacheOwner("iris", "cache-uid")
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers", Namespace: modelCacheNamespace, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	r := newReconcilerWithFakeClient(cache, group, pv, targetPVC)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err != nil {
		t.Fatalf("unbound PV should remain valid after the old PVC is gone: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("unbound PV was removed: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(targetPVC), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("target PVC was removed: %v", err)
	}
}

func TestModelCacheCutoverDeletesOldPVCBeforePreservingTargetBoundPV(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       "test-ns",
			UID:             types.UID("old-pvc"),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: modelCacheNamespace, Name: "iris-workers", UID: types.UID("target-pvc")},
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers", Namespace: modelCacheNamespace, UID: types.UID("target-pvc")},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	r := newReconcilerWithFakeClient(cache, group, pvc, pv, targetPVC)
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected old PVC deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old PVC still exists after target PV claim: %v", err)
	}
	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err != nil {
		t.Fatalf("target-bound PV should be preserved after old PVC removal: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pv), &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("target-bound PV was removed: %v", err)
	}
}

func TestModelCacheCutoverPreservesOldPVCForWrongStorageTargetBoundPV(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       "test-ns",
			UID:             types.UID("old-pvc"),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/wrong"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: modelCacheNamespace, Name: "iris-workers", UID: types.UID("target-pvc")},
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers", Namespace: modelCacheNamespace, UID: types.UID("target-pvc")},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	r := newReconcilerWithFakeClient(cache, group, pvc, pv, targetPVC)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected wrong-storage target-bound PV to block cutover")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old PVC was removed for wrong-storage target-bound PV: %v", err)
	}
}

func TestModelCacheCutoverRejectsStaleDynamicTargetPVC(t *testing.T) {
	ctx := context.Background()
	cache := testLocalModelCache("iris", "cache-uid", "workers")
	group := testLocalModelNodeGroup("workers")
	owner := localModelCacheOwner("iris", "cache-uid")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "iris-workers",
			Namespace:       "test-ns",
			UID:             types.UID("old-pvc"),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers-download", OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/iris"}},
			StorageClassName:       "local-storage",
			ClaimRef:               &corev1.ObjectReference{Namespace: modelCacheNamespace, Name: "iris-workers", UID: types.UID("stale-target-pvc")},
		},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "iris-workers", Namespace: modelCacheNamespace, UID: types.UID("current-target-pvc")},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "iris-workers-download"},
	}
	r := newReconcilerWithFakeClient(cache, group, pvc, pv, targetPVC)

	if err := r.removeOldModelCacheVolumes(ctx, "test-ns"); err == nil {
		t.Fatal("expected stale target claim to block cutover")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old PVC was removed before rejecting stale target claim: %v", err)
	}
}

func TestModelCacheCutoverPreservesOldStaticPVCForMalformedPV(t *testing.T) {
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName, Labels: labels},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kserve/wrong"}},
			ClaimRef:                      &corev1.ObjectReference{Namespace: "test-ns", Name: modelCachePVCName},
		},
	}
	r := newReconcilerWithFakeClient(pvc, pv)

	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected malformed static PV to block cutover")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old static PVC was removed for malformed PV: %v", err)
	}
}

func TestModelCacheCutoverDeletesOldStaticPVCWhenPVIsMissing(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelCachePVCName,
			Namespace: "test-ns",
			Labels:    map[string]string{odhLabels.PlatformPartOf: KserveComponentName},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	r := newReconcilerWithFakeClient(pvc)
	ctx := context.Background()

	if err := r.removeOldStaticVolume(ctx, "test-ns"); err == nil {
		t.Fatal("expected old PVC deletion to require a retry")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old static PVC was not removed after its PV was already missing: %v", err)
	}
	if err := r.removeOldStaticVolume(ctx, "test-ns"); err != nil {
		t.Fatalf("partial static install did not converge: %v", err)
	}
}

func TestModelCacheCutoverRejectsStaticTargetAnnotationWithOldClaim(t *testing.T) {
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        modelCachePVName,
			Labels:      labels,
			Annotations: map[string]string{modelCacheClaimNamespaceAnnotation: modelCacheNamespace},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: modelCacheHostDir}},
			ClaimRef:                      &corev1.ObjectReference{Namespace: "test-ns", Name: modelCachePVCName},
		},
	}
	r := newReconcilerWithFakeClient(pvc, pv)

	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected target annotation with old claim to block cutover")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old static PVC was removed before rejecting target annotation: %v", err)
	}
}

func TestModelCacheCutoverRequiresStaticTargetPVC(t *testing.T) {
	labels := map[string]string{odhLabels.PlatformPartOf: KserveComponentName}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: "test-ns", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: modelCachePVName},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName, Labels: labels},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: modelCacheHostDir}},
			ClaimRef:                      &corev1.ObjectReference{Namespace: modelCacheNamespace, Name: modelCachePVCName, UID: types.UID("target-pvc")},
		},
	}
	r := newReconcilerWithFakeClient(pvc, pv)

	if err := r.removeOldStaticVolume(context.Background(), "test-ns"); err == nil {
		t.Fatal("expected missing target PVC to block cutover")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("old static PVC was removed before validating target PVC: %v", err)
	}
}

func TestPersistentVolumeSpecsMatchAccountsForDefaultsAndSemanticFields(t *testing.T) {
	capacity := resource.MustParse("2Gi")
	affinity := &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "kserve/localmodel", Operator: corev1.NodeSelectorOpIn, Values: []string{"worker", "cache"}},
				{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"east", "west"}},
			},
			MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"node-2", "node-1"}}},
		},
		{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "node-role.kubernetes.io/worker", Operator: corev1.NodeSelectorOpExists, Values: []string{}}}, MatchFields: []corev1.NodeSelectorRequirement{}},
	}}}
	expected := corev1.PersistentVolumeSpec{
		Capacity:               corev1.ResourceList{corev1.ResourceStorage: capacity},
		AccessModes:            []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		VolumeMode:             ptr.To(corev1.PersistentVolumeFilesystem),
		StorageClassName:       modelCacheStorageClass,
		PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: modelCacheHostDir}},
		NodeAffinity:           affinity,
		MountOptions:           []string{"nosuid", "nodev"},
	}
	actual := expected
	actual.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2048Mi")}
	actual.VolumeMode = nil
	actual.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	actual.MountOptions = []string{"nodev", "nosuid"}
	actual.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "node-role.kubernetes.io/worker", Operator: corev1.NodeSelectorOpExists}}},
		{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"node-1", "node-2"}}},
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"west", "east"}},
				{Key: "kserve/localmodel", Operator: corev1.NodeSelectorOpIn, Values: []string{"cache", "worker"}},
			}},
	}}}
	if !persistentVolumeSpecsMatch(expected, actual) {
		t.Fatal("equivalent defaulted PV specs did not match")
	}

	actual.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
	if persistentVolumeSpecsMatch(expected, actual) {
		t.Fatal("PV specs with a different reclaim policy matched")
	}
	actual.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	actual.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "other", Operator: corev1.NodeSelectorOpIn, Values: []string{"worker"}}}}}}}
	if persistentVolumeSpecsMatch(expected, actual) {
		t.Fatal("PV specs with different node affinity matched")
	}
}

var localModelCacheGVK = schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1alpha1", Kind: "LocalModelCache"}
var localModelNamespaceCacheGVK = schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1alpha1", Kind: "LocalModelNamespaceCache"}

func testLocalModelCache(name, uid string, groups ...string) *unstructured.Unstructured {
	return testCache(localModelCacheGVK, name, "", uid, groups...)
}

func testLocalModelNamespaceCache(name, namespace, uid string, groups ...string) *unstructured.Unstructured {
	return testCache(localModelNamespaceCacheGVK, name, namespace, uid, groups...)
}

func testCache(gvk schema.GroupVersionKind, name, namespace, uid string, groups ...string) *unstructured.Unstructured {
	groupValues := make([]any, len(groups))
	for i := range groups {
		groupValues[i] = groups[i]
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name": name,
			"uid":  uid,
		},
		"spec": map[string]any{"nodeGroups": groupValues},
	}}
	if namespace != "" {
		u.SetNamespace(namespace)
	}
	u.SetGroupVersionKind(gvk)
	return u
}

func testLocalModelNodeGroup(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": localModelNodeGroupGVK.GroupVersion().String(),
		"kind":       localModelNodeGroupGVK.Kind,
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"persistentVolumeSpec": map[string]any{
				"storageClassName": "local-storage",
				"hostPath":         map[string]any{"path": "/var/lib/kserve/iris"},
			},
		},
	}}
	u.SetGroupVersionKind(localModelNodeGroupGVK)
	return u
}

func localModelCacheOwner(name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "serving.kserve.io/v1alpha1",
		Kind:       "LocalModelCache",
		Name:       name,
		UID:        types.UID(uid),
		Controller: ptr.To(true),
	}
}
