package kservemodule

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// ─── REST mapper helpers ──────────────────────────────────────────────────────

// testRESTScope implements apimeta.RESTScope for test REST mapper configuration.
type testRESTScope struct{ name apimeta.RESTScopeName }

func (s testRESTScope) Name() apimeta.RESTScopeName { return s.name }

var (
	testNamespaceScope = testRESTScope{name: apimeta.RESTScopeNameNamespace}
	testClusterScope   = testRESTScope{name: apimeta.RESTScopeNameRoot}
)

// makeUpgradeTestScheme returns a scheme with corev1 and apiextensionsv1 registered.
func makeUpgradeTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = apiextensionsv1.AddToScheme(s)
	return s
}

// makeUpgradeTestRESTMapper returns a REST mapper covering all GVKs used in upgrade tests.
func makeUpgradeTestRESTMapper() apimeta.RESTMapper {
	rm := apimeta.NewDefaultRESTMapper(nil)

	// Typed resources (corev1 and apiextensions)
	rm.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, testClusterScope)
	rm.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Event"}, testNamespaceScope)
	rm.Add(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}, testClusterScope)

	// Unstructured GVKs from upgrade.go
	rm.Add(inferenceServiceGVK, testNamespaceScope)
	rm.Add(servingRuntimeGVK, testNamespaceScope)
	rm.Add(hardwareProfileGVK, testNamespaceScope)
	rm.Add(odhDashboardConfigGVK, testNamespaceScope)

	return rm
}

// ─── Object-builder helpers ───────────────────────────────────────────────────

// makeTestOdhDashboardConfig returns an OdhDashboardConfig with Small and Large modelServerSizes.
// Small: requests cpu=1,memory=4Gi limits cpu=2,memory=8Gi
// Large: requests cpu=6,memory=16Gi limits cpu=10,memory=20Gi
func makeTestOdhDashboardConfig(namespace string) *unstructured.Unstructured {
	cfg := &unstructured.Unstructured{}
	cfg.SetGroupVersionKind(odhDashboardConfigGVK)
	cfg.SetName("odh-dashboard-config")
	cfg.SetNamespace(namespace)

	_ = unstructured.SetNestedSlice(cfg.Object, []any{
		map[string]any{
			"name": "Small",
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "1", "memory": "4Gi"},
				"limits":   map[string]any{"cpu": "2", "memory": "8Gi"},
			},
		},
		map[string]any{
			"name": "Large",
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "6", "memory": "16Gi"},
				"limits":   map[string]any{"cpu": "10", "memory": "20Gi"},
			},
		},
	}, "spec", "modelServerSizes")

	return cfg
}

// makeTestInferenceService returns an unstructured InferenceService.
// runtimeName may be empty (no spec.predictor.model.runtime set).
func makeTestInferenceService(namespace, name, runtimeName string) *unstructured.Unstructured {
	isvc := &unstructured.Unstructured{}
	isvc.SetGroupVersionKind(inferenceServiceGVK)
	isvc.SetName(name)
	isvc.SetNamespace(namespace)

	if runtimeName != "" {
		_ = unstructured.SetNestedField(isvc.Object, runtimeName, "spec", "predictor", "model", "runtime")
	}

	return isvc
}

// makeTestInferenceServiceWithResources returns an ISVC with
// spec.predictor.model.resources set to the given cpu/memory values.
func makeTestInferenceServiceWithResources(namespace, name, reqCPU, reqMem, limCPU, limMem string) *unstructured.Unstructured {
	isvc := &unstructured.Unstructured{}
	isvc.SetGroupVersionKind(inferenceServiceGVK)
	isvc.SetName(name)
	isvc.SetNamespace(namespace)

	_ = unstructured.SetNestedMap(isvc.Object, map[string]any{
		"requests": map[string]any{"cpu": reqCPU, "memory": reqMem},
		"limits":   map[string]any{"cpu": limCPU, "memory": limMem},
	}, "spec", "predictor", "model", "resources")

	return isvc
}

// makeTestServingRuntime returns an unstructured ServingRuntime.
func makeTestServingRuntime(namespace, name string) *unstructured.Unstructured {
	sr := &unstructured.Unstructured{}
	sr.SetGroupVersionKind(servingRuntimeGVK)
	sr.SetName(name)
	sr.SetNamespace(namespace)
	return sr
}

// makeTestHardwareProfile returns an unstructured HardwareProfile.
func makeTestHardwareProfile(namespace, name string) *unstructured.Unstructured {
	hwp := &unstructured.Unstructured{}
	hwp.SetGroupVersionKind(hardwareProfileGVK)
	hwp.SetName(name)
	hwp.SetNamespace(namespace)
	return hwp
}

// makeTestNamespace returns a corev1.Namespace with the given labels.
func makeTestNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// makeCRD returns an Established CustomResourceDefinition object for use in the fake client.
func makeCRD(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			}},
		},
	}
}

// makeISVCFakeClient returns a fake client preloaded with the given objects
// and a scheme with corev1 + apiextensionsv1.
func makeISVCFakeClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(makeUpgradeTestScheme()).
		WithRESTMapper(makeUpgradeTestRESTMapper()).
		WithObjects(objects...).
		Build()
}

// shortCtx returns a context that expires quickly to bound retries in
// cluster.CustomResourceDefinitionExists when the CRD is absent.
func shortCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 50*time.Millisecond)
}

// CRD names derived from GVKs via the formula used in cluster.CustomResourceDefinitionExists.
const (
	hwpCRDName = "hardwareprofiles.infrastructure.opendatahub.io"
	apCRDName  = "acceleratorprofiles.dashboard.opendatahub.io"
)

// ─── Section A — Migrated Tests ───────────────────────────────────────────────

func TestAttachHardwareProfileToInferenceServices(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"

	t.Run("NoISVCs", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		cli := makeISVCFakeClient(odhConfig)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("AlreadyHasHWPAnnotation", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceService(namespace, "isvc-with-hwp", "")
		isvc.SetAnnotations(map[string]string{hwpNameAnnotation: "existing-hwp"})

		cli := makeISVCFakeClient(odhConfig, isvc)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-with-hwp", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, "existing-hwp"))
	})

	t.Run("ServingRuntimeAPAnnotation", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		sr := makeTestServingRuntime(namespace, "test-runtime")
		sr.SetAnnotations(map[string]string{acceleratorNameAnnotation: "nvidia Gpu"})
		isvc := makeTestInferenceService(namespace, "isvc-with-runtime", "test-runtime")
		hwp := makeTestHardwareProfile(namespace, "nvidia-gpu-serving")

		cli := makeISVCFakeClient(odhConfig, sr, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-with-runtime", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, "nvidia-gpu-serving"))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, namespace))
	})

	t.Run("ContainerSizeMatch", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceServiceWithResources(namespace, "isvc-with-resources", "1", "4Gi", "2", "8Gi")
		hwp := makeTestHardwareProfile(namespace, "containersize-small-serving")

		cli := makeISVCFakeClient(odhConfig, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-with-resources", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, "containersize-small-serving"))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, namespace))
	})

	t.Run("NonMatchingResources", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceServiceWithResources(namespace, "isvc-custom", "3", "10Gi", "5", "20Gi")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(odhConfig, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-custom", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, "custom-serving"))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, namespace))
	})

	t.Run("NoResourcesAtAll", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceService(namespace, "isvc-no-resources", "")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(odhConfig, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-no-resources", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, "custom-serving"))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, namespace))
	})

	t.Run("ServerlessAnnotation", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceService(namespace, "isvc-serverless-annotation", "")
		isvc.SetAnnotations(map[string]string{kserveDeploymentModeAnnotation: "Serverless"})

		cli := makeISVCFakeClient(odhConfig, isvc)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-serverless-annotation", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).NotTo(HaveKey(hwpNameAnnotation))
	})

	t.Run("ServerlessStatus", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceService(namespace, "isvc-serverless-status", "")
		isvc.Object["status"] = map[string]any{"deploymentMode": "Serverless"}

		cli := makeISVCFakeClient(odhConfig, isvc)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-serverless-status", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).NotTo(HaveKey(hwpNameAnnotation))
	})

	t.Run("KueueManagedNamespaceNoQueueLabel", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		ns := makeTestNamespace(namespace, map[string]string{kueueManagedLabel: "true"})
		isvc := makeTestInferenceService(namespace, "isvc-kueue-ns-no-label", "")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(ns, odhConfig, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-kueue-ns-no-label", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).NotTo(HaveKey(hwpNameAnnotation))

		var events corev1.EventList
		g.Expect(cli.List(ctx, &events)).To(Succeed())
		g.Expect(events.Items).To(HaveLen(1))
		g.Expect(events.Items[0].Type).To(Equal(corev1.EventTypeWarning))
		g.Expect(events.Items[0].Reason).To(Equal("HardwareProfileMigrationSkipped"))
	})

	t.Run("KueueLegacyManagedNamespaceNoQueueLabel", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		ns := makeTestNamespace(namespace, map[string]string{kueueLegacyManagedLabel: "true"})
		isvc := makeTestInferenceService(namespace, "isvc-legacy-kueue-ns", "")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(ns, odhConfig, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-legacy-kueue-ns", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).NotTo(HaveKey(hwpNameAnnotation))

		var events corev1.EventList
		g.Expect(cli.List(ctx, &events)).To(Succeed())
		g.Expect(events.Items).To(HaveLen(1))
		g.Expect(events.Items[0].Type).To(Equal(corev1.EventTypeWarning))
		g.Expect(events.Items[0].Reason).To(Equal("HardwareProfileMigrationSkipped"))
	})

	t.Run("KueueManagedNamespaceWithQueueLabel", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		ns := makeTestNamespace(namespace, map[string]string{kueueManagedLabel: "true"})
		isvc := makeTestInferenceService(namespace, "isvc-kueue-ns-with-label", "")
		isvc.SetLabels(map[string]string{kueueQueueNameLabel: "my-queue"})
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(ns, odhConfig, isvc, hwp)

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: "isvc-kueue-ns-with-label", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, "custom-serving"))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, namespace))
	})
}

func TestGetOdhDashboardConfig(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"

	t.Run("Found", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		cli := makeISVCFakeClient(odhConfig)

		result, found, err := getOdhDashboardConfig(ctx, cli, namespace)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeTrue())
		g.Expect(result).NotTo(BeNil())
		g.Expect(result.GetName()).To(Equal("odh-dashboard-config"))
		g.Expect(result.GetNamespace()).To(Equal(namespace))
	})

	t.Run("NotFound", func(t *testing.T) {
		g := NewWithT(t)

		cli := makeISVCFakeClient()

		result, found, err := getOdhDashboardConfig(ctx, cli, namespace)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeFalse())
		g.Expect(result).To(BeNil())
	})

	t.Run("NoMatchError", func(t *testing.T) {
		g := NewWithT(t)

		funcs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "OdhDashboardConfig" {
					return &apimeta.NoKindMatchError{
						GroupKind:        schema.GroupKind{Group: "opendatahub.io", Kind: "OdhDashboardConfig"},
						SearchedVersions: []string{"v1alpha"},
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		cli := fake.NewClientBuilder().
			WithScheme(makeUpgradeTestScheme()).
			WithRESTMapper(makeUpgradeTestRESTMapper()).
			WithInterceptorFuncs(funcs).
			Build()

		result, found, err := getOdhDashboardConfig(ctx, cli, namespace)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeFalse())
		g.Expect(result).To(BeNil())
	})

	t.Run("OtherAPIError", func(t *testing.T) {
		g := NewWithT(t)

		funcs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "OdhDashboardConfig" {
					return k8serr.NewInternalError(errors.New("internal server error"))
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		cli := fake.NewClientBuilder().
			WithScheme(makeUpgradeTestScheme()).
			WithRESTMapper(makeUpgradeTestRESTMapper()).
			WithInterceptorFuncs(funcs).
			Build()

		result, found, err := getOdhDashboardConfig(ctx, cli, namespace)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("internal server error"))
		g.Expect(found).To(BeFalse())
		g.Expect(result).To(BeNil())
	})
}

// ─── Section B — New Tests ────────────────────────────────────────────────────

func TestRunUpgradeTasks(t *testing.T) {
	const namespace = "test-namespace"

	t.Run("NeitherCRDExists", func(t *testing.T) {
		g := NewWithT(t)
		cli := makeISVCFakeClient()
		ctx, cancel := shortCtx()
		defer cancel()
		g.Expect(runUpgradeTasks(ctx, cli, namespace)).To(Succeed())
	})

	t.Run("HWPCRDMissingOnly", func(t *testing.T) {
		g := NewWithT(t)
		cli := makeISVCFakeClient(makeCRD(apCRDName))
		ctx, cancel := shortCtx()
		defer cancel()
		g.Expect(runUpgradeTasks(ctx, cli, namespace)).To(Succeed())
	})

	t.Run("AcceleratorCRDMissingOnly", func(t *testing.T) {
		g := NewWithT(t)
		cli := makeISVCFakeClient(makeCRD(hwpCRDName))
		ctx, cancel := shortCtx()
		defer cancel()
		g.Expect(runUpgradeTasks(ctx, cli, namespace)).To(Succeed())
	})

	t.Run("BothCRDsPresentOdhConfigNotFound", func(t *testing.T) {
		g := NewWithT(t)
		cli := makeISVCFakeClient(makeCRD(hwpCRDName), makeCRD(apCRDName))
		g.Expect(runUpgradeTasks(context.Background(), cli, namespace)).To(Succeed())
	})

	t.Run("BothCRDsPresentOdhConfigError", func(t *testing.T) {
		g := NewWithT(t)

		funcs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "OdhDashboardConfig" {
					return k8serr.NewInternalError(errors.New("internal server error"))
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		cli := fake.NewClientBuilder().
			WithScheme(makeUpgradeTestScheme()).
			WithRESTMapper(makeUpgradeTestRESTMapper()).
			WithObjects(makeCRD(hwpCRDName), makeCRD(apCRDName)).
			WithInterceptorFuncs(funcs).
			Build()

		err := runUpgradeTasks(context.Background(), cli, namespace)
		g.Expect(err).To(HaveOccurred())
	})

	t.Run("BothCRDsPresentHappyPath", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceService(namespace, "isvc-one", "")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(makeCRD(hwpCRDName), makeCRD(apCRDName), odhConfig, isvc, hwp)

		g.Expect(runUpgradeTasks(context.Background(), cli, namespace)).To(Succeed())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(context.Background(), client.ObjectKey{Name: "isvc-one", Namespace: namespace}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKey(hwpNameAnnotation))
	})
}

func TestUpgradeRunnableStart(t *testing.T) {
	const namespace = "test-namespace"

	t.Run("StartReturnsNilOnSuccess", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc := makeTestInferenceService(namespace, "isvc-one", "")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		cli := makeISVCFakeClient(makeCRD(hwpCRDName), makeCRD(apCRDName), odhConfig, isvc, hwp)
		r := &upgradeRunnable{client: cli, applicationNS: namespace}

		g.Expect(r.Start(context.Background())).To(Succeed())
	})

	t.Run("StartReturnsNilOnError", func(t *testing.T) {
		g := NewWithT(t)

		funcs := interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "OdhDashboardConfig" {
					return k8serr.NewInternalError(errors.New("internal server error"))
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
		cli := fake.NewClientBuilder().
			WithScheme(makeUpgradeTestScheme()).
			WithRESTMapper(makeUpgradeTestRESTMapper()).
			WithObjects(makeCRD(hwpCRDName), makeCRD(apCRDName)).
			WithInterceptorFuncs(funcs).
			Build()

		r := &upgradeRunnable{client: cli, applicationNS: namespace}
		// Start absorbs all errors so manager startup is never blocked.
		g.Expect(r.Start(context.Background())).To(Succeed())
	})
}

func TestUpgradeRunnableNeedLeaderElection(t *testing.T) {
	t.Run("NeedLeaderElectionIsTrue", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect((&upgradeRunnable{}).NeedLeaderElection()).To(BeTrue())
	})
}

func TestSetHWPAnnotation(t *testing.T) {
	ctx := context.Background()

	const (
		hwpTestName  = "test-hwp"
		apNS         = "ap-ns"
		isvcNS       = "isvc-ns"
		appNS        = "app-ns"
		isvcTestName = "test-isvc"
	)

	t.Run("FoundInAPNamespace", func(t *testing.T) {
		g := NewWithT(t)

		isvc := makeTestInferenceService(isvcNS, isvcTestName, "")
		hwp := makeTestHardwareProfile(apNS, hwpTestName)

		cli := makeISVCFakeClient(isvc, hwp)

		err := setHWPAnnotation(ctx, cli, isvc, hwpTestName, apNS, appNS)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: isvcTestName, Namespace: isvcNS}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, hwpTestName))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, apNS))
	})

	t.Run("FoundInISVCNamespace", func(t *testing.T) {
		g := NewWithT(t)

		isvc := makeTestInferenceService(isvcNS, isvcTestName, "")
		hwp := makeTestHardwareProfile(isvcNS, hwpTestName)

		cli := makeISVCFakeClient(isvc, hwp)

		// apNS has no HWP; ISVC namespace does.
		err := setHWPAnnotation(ctx, cli, isvc, hwpTestName, apNS, appNS)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: isvcTestName, Namespace: isvcNS}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, hwpTestName))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, isvcNS))
	})

	t.Run("FoundInApplicationNamespace", func(t *testing.T) {
		g := NewWithT(t)

		isvc := makeTestInferenceService(isvcNS, isvcTestName, "")
		hwp := makeTestHardwareProfile(appNS, hwpTestName)

		cli := makeISVCFakeClient(isvc, hwp)

		err := setHWPAnnotation(ctx, cli, isvc, hwpTestName, apNS, appNS)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: isvcTestName, Namespace: isvcNS}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, hwpTestName))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, appNS))
	})

	t.Run("NotFoundInAnyNamespace", func(t *testing.T) {
		g := NewWithT(t)

		isvc := makeTestInferenceService(isvcNS, isvcTestName, "")
		cli := makeISVCFakeClient(isvc)

		err := setHWPAnnotation(ctx, cli, isvc, hwpTestName, apNS, appNS)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: isvcTestName, Namespace: isvcNS}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, hwpTestName))
		g.Expect(updated.GetAnnotations()).NotTo(HaveKey(hwpNamespaceAnnotation))

		var events corev1.EventList
		g.Expect(cli.List(ctx, &events)).To(Succeed())
		g.Expect(events.Items).To(HaveLen(1))
		g.Expect(events.Items[0].Type).To(Equal(corev1.EventTypeWarning))
		g.Expect(events.Items[0].Reason).To(Equal("HardwareProfileMigrationSkipped"))
	})

	t.Run("EmptyAPNamespaceSkipped", func(t *testing.T) {
		g := NewWithT(t)

		isvc := makeTestInferenceService(isvcNS, isvcTestName, "")
		hwp := makeTestHardwareProfile(isvcNS, hwpTestName)

		cli := makeISVCFakeClient(isvc, hwp)

		// apNamespace="" means ap-ns is not searched; HWP is found in isvcNS.
		err := setHWPAnnotation(ctx, cli, isvc, hwpTestName, "", appNS)
		g.Expect(err).NotTo(HaveOccurred())

		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(inferenceServiceGVK)
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: isvcTestName, Namespace: isvcNS}, updated)).To(Succeed())
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNameAnnotation, hwpTestName))
		g.Expect(updated.GetAnnotations()).To(HaveKeyWithValue(hwpNamespaceAnnotation, isvcNS))
	})
}

func TestHandleISVCSetHWPAnnotationError(t *testing.T) {
	ctx := context.Background()
	const namespace = "test-namespace"

	cases := []struct {
		name        string
		errStr      string
		wantHandled bool
		wantReason  string
	}{
		{
			name:        "ServerlessDeploymentModeWebhook",
			errStr:      "deploymentMode cannot be changed",
			wantHandled: true,
			wantReason:  "ServerlessMigrationSkipped",
		},
		{
			name:        "ServerlessKeywordWebhook",
			errStr:      "Serverless mode incompatibility",
			wantHandled: true,
			wantReason:  "ServerlessMigrationSkipped",
		},
		{
			name:        "KueueLabelValidationFailed",
			errStr:      "Kueue label validation failed",
			wantHandled: true,
			wantReason:  "HardwareProfileMigrationSkipped",
		},
		{
			name:        "KueueMissingRequiredLabel",
			errStr:      "missing required label kueue something",
			wantHandled: true,
			wantReason:  "HardwareProfileMigrationSkipped",
		},
		{
			name:        "UnrecognisedError",
			errStr:      "something completely different",
			wantHandled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			isvc := makeTestInferenceService(namespace, "test-isvc", "")
			cli := makeISVCFakeClient(isvc)

			handled := handleISVCSetHWPAnnotationError(ctx, cli, isvc, errors.New(tc.errStr))
			g.Expect(handled).To(Equal(tc.wantHandled))

			if tc.wantHandled {
				var events corev1.EventList
				g.Expect(cli.List(ctx, &events)).To(Succeed())
				g.Expect(events.Items).To(HaveLen(1))
				g.Expect(events.Items[0].Type).To(Equal(corev1.EventTypeWarning))
				g.Expect(events.Items[0].Reason).To(Equal(tc.wantReason))
			}
		})
	}
}

func TestGetContainerSizes(t *testing.T) {
	t.Run("Normal", func(t *testing.T) {
		g := NewWithT(t)

		odhConfig := makeTestOdhDashboardConfig("test-namespace")
		sizes, err := getContainerSizes(odhConfig, "modelServerSizes")

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(sizes).To(HaveLen(2))

		g.Expect(sizes[0].Name).To(Equal("Small"))
		g.Expect(sizes[0].Resources.Requests.Cpu).To(Equal("1"))
		g.Expect(sizes[0].Resources.Requests.Memory).To(Equal("4Gi"))
		g.Expect(sizes[0].Resources.Limits.Cpu).To(Equal("2"))
		g.Expect(sizes[0].Resources.Limits.Memory).To(Equal("8Gi"))

		g.Expect(sizes[1].Name).To(Equal("Large"))
		g.Expect(sizes[1].Resources.Requests.Cpu).To(Equal("6"))
		g.Expect(sizes[1].Resources.Requests.Memory).To(Equal("16Gi"))
		g.Expect(sizes[1].Resources.Limits.Cpu).To(Equal("10"))
		g.Expect(sizes[1].Resources.Limits.Memory).To(Equal("20Gi"))
	})

	t.Run("KeyAbsent", func(t *testing.T) {
		g := NewWithT(t)

		cfg := &unstructured.Unstructured{}
		cfg.SetGroupVersionKind(odhDashboardConfigGVK)
		cfg.SetName("odh-dashboard-config")
		cfg.SetNamespace("test-namespace")
		_ = unstructured.SetNestedMap(cfg.Object, map[string]any{}, "spec")

		sizes, err := getContainerSizes(cfg, "modelServerSizes")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(sizes).To(BeEmpty())
	})

	t.Run("MalformedEntry", func(t *testing.T) {
		g := NewWithT(t)

		cfg := &unstructured.Unstructured{}
		cfg.SetGroupVersionKind(odhDashboardConfigGVK)
		cfg.SetName("odh-dashboard-config")
		cfg.SetNamespace("test-namespace")
		_ = unstructured.SetNestedSlice(cfg.Object, []any{
			// Malformed: resources is a string, not a map. Entry is included with empty resource fields.
			map[string]any{"name": "Malformed", "resources": "invalid"},
			map[string]any{
				"name": "Valid",
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "1", "memory": "4Gi"},
					"limits":   map[string]any{"cpu": "2", "memory": "8Gi"},
				},
			},
		}, "spec", "modelServerSizes")

		sizes, err := getContainerSizes(cfg, "modelServerSizes")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(sizes).To(HaveLen(2))
		g.Expect(sizes[0].Name).To(Equal("Malformed"))
		g.Expect(sizes[0].Resources.Requests.Cpu).To(BeEmpty())
		g.Expect(sizes[1].Name).To(Equal("Valid"))
		g.Expect(sizes[1].Resources.Requests.Cpu).To(Equal("1"))
	})
}

func TestFindContainerSizeByResources(t *testing.T) {
	sizes := []containerSize{
		{
			Name: "Small",
			Resources: struct {
				Requests struct{ Cpu, Memory string }
				Limits   struct{ Cpu, Memory string }
			}{
				Requests: struct{ Cpu, Memory string }{Cpu: "1", Memory: "4Gi"},
				Limits:   struct{ Cpu, Memory string }{Cpu: "2", Memory: "8Gi"},
			},
		},
	}

	makeResources := func(reqCPU, reqMem, limCPU, limMem string) map[string]any {
		return map[string]any{
			"requests": map[string]any{"cpu": reqCPU, "memory": reqMem},
			"limits":   map[string]any{"cpu": limCPU, "memory": limMem},
		}
	}

	t.Run("ExactMatch", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(findContainerSizeByResources(sizes, makeResources("1", "4Gi", "2", "8Gi"))).To(Equal("Small"))
	})

	t.Run("PartialMatchCPUOnly", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(findContainerSizeByResources(sizes, makeResources("1", "99Gi", "2", "8Gi"))).To(BeEmpty())
	})

	t.Run("NoMatch", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(findContainerSizeByResources(sizes, makeResources("99", "99Gi", "99", "99Gi"))).To(BeEmpty())
	})

	t.Run("NilResources", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(findContainerSizeByResources(sizes, nil)).To(BeEmpty())
	})

	t.Run("MissingRequestsMap", func(t *testing.T) {
		g := NewWithT(t)
		resources := map[string]any{
			"limits": map[string]any{"cpu": "2", "memory": "8Gi"},
		}
		g.Expect(findContainerSizeByResources(sizes, resources)).To(BeEmpty())
	})

	t.Run("EmptySizeList", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(findContainerSizeByResources([]containerSize{}, makeResources("1", "4Gi", "2", "8Gi"))).To(BeEmpty())
	})
}

func TestIsISVCServerless(t *testing.T) {
	t.Run("AnnotationServerless", func(t *testing.T) {
		g := NewWithT(t)
		isvc := makeTestInferenceService("ns", "isvc", "")
		isvc.SetAnnotations(map[string]string{kserveDeploymentModeAnnotation: "Serverless"})
		g.Expect(isISVCServerless(isvc)).To(BeTrue())
	})

	t.Run("StatusServerless", func(t *testing.T) {
		g := NewWithT(t)
		isvc := makeTestInferenceService("ns", "isvc", "")
		isvc.Object["status"] = map[string]any{"deploymentMode": "Serverless"}
		g.Expect(isISVCServerless(isvc)).To(BeTrue())
	})

	t.Run("AnnotationOtherValue", func(t *testing.T) {
		g := NewWithT(t)
		isvc := makeTestInferenceService("ns", "isvc", "")
		isvc.SetAnnotations(map[string]string{kserveDeploymentModeAnnotation: "RawDeployment"})
		g.Expect(isISVCServerless(isvc)).To(BeFalse())
	})

	t.Run("NeitherSet", func(t *testing.T) {
		g := NewWithT(t)
		isvc := makeTestInferenceService("ns", "isvc", "")
		g.Expect(isISVCServerless(isvc)).To(BeFalse())
	})
}

func TestIsNamespaceManagedByKueue(t *testing.T) {
	ctx := context.Background()

	t.Run("KueueManagedLabel", func(t *testing.T) {
		g := NewWithT(t)
		ns := makeTestNamespace("my-ns", map[string]string{kueueManagedLabel: "true"})
		cli := makeISVCFakeClient(ns)
		managed, err := isNamespaceManagedByKueue(ctx, cli, "my-ns")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(managed).To(BeTrue())
	})

	t.Run("KueueLegacyManagedLabel", func(t *testing.T) {
		g := NewWithT(t)
		ns := makeTestNamespace("my-ns", map[string]string{kueueLegacyManagedLabel: "true"})
		cli := makeISVCFakeClient(ns)
		managed, err := isNamespaceManagedByKueue(ctx, cli, "my-ns")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(managed).To(BeTrue())
	})

	t.Run("NoKueueLabel", func(t *testing.T) {
		g := NewWithT(t)
		ns := makeTestNamespace("my-ns", map[string]string{"unrelated": "label"})
		cli := makeISVCFakeClient(ns)
		managed, err := isNamespaceManagedByKueue(ctx, cli, "my-ns")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(managed).To(BeFalse())
	})

	t.Run("EmptyNamespaceName", func(t *testing.T) {
		g := NewWithT(t)
		cli := makeISVCFakeClient()
		managed, err := isNamespaceManagedByKueue(ctx, cli, "")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(managed).To(BeFalse())
	})

	t.Run("NamespaceNotFound", func(t *testing.T) {
		g := NewWithT(t)
		cli := makeISVCFakeClient()
		_, err := isNamespaceManagedByKueue(ctx, cli, "nonexistent")
		g.Expect(err).To(HaveOccurred())
	})
}

func TestAttachHardwareProfileToInferenceServices_ErrorAggregation(t *testing.T) {
	t.Run("MultipleISVCsOneFailsOthersContinue", func(t *testing.T) {
		g := NewWithT(t)
		ctx := context.Background()
		namespace := "test-namespace"

		odhConfig := makeTestOdhDashboardConfig(namespace)
		isvc1 := makeTestInferenceService(namespace, "isvc-1", "")
		isvc2 := makeTestInferenceService(namespace, "isvc-2", "")
		isvc3 := makeTestInferenceService(namespace, "isvc-3", "")
		hwp := makeTestHardwareProfile(namespace, "custom-serving")

		funcs := interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if obj.GetName() == "isvc-2" {
					return k8serr.NewInternalError(errors.New("update failed for isvc-2"))
				}
				return c.Update(ctx, obj, opts...)
			},
		}
		cli := fake.NewClientBuilder().
			WithScheme(makeUpgradeTestScheme()).
			WithRESTMapper(makeUpgradeTestRESTMapper()).
			WithObjects(odhConfig, isvc1, isvc2, isvc3, hwp).
			WithInterceptorFuncs(funcs).
			Build()

		err := attachHardwareProfileToInferenceServices(ctx, cli, namespace, odhConfig)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("isvc-2"))

		// isvc-1 and isvc-3 must be annotated despite isvc-2 failure.
		for _, name := range []string{"isvc-1", "isvc-3"} {
			updated := &unstructured.Unstructured{}
			updated.SetGroupVersionKind(inferenceServiceGVK)
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, updated)).To(Succeed())
			g.Expect(updated.GetAnnotations()).To(HaveKey(hwpNameAnnotation),
				"expected %s to have HWP annotation", name)
		}
	})
}
