//go:build distro

/*
Copyright 2026 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package inferenceservice

import (
	"context"
	"testing"

	"github.com/onsi/gomega"
	"google.golang.org/protobuf/proto"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestComponentRequiresImageVolumeSCC(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scenarios := map[string]struct {
		storageUris      []v1beta1.StorageUri
		legacyStorageUri *string
		expected         bool
	}{
		"NonLegacyPathWithStorageUris": {
			storageUris:      []v1beta1.StorageUri{{Uri: constants.OciURIPrefix + "image:v1", MountPath: "/models"}},
			legacyStorageUri: nil,
			expected:         false,
		},
		"LegacyOCIStorage": {
			storageUris:      nil,
			legacyStorageUri: proto.String(constants.OciURIPrefix + "myrepo/model:v1"),
			expected:         true,
		},
		"LegacyS3Storage": {
			storageUris:      nil,
			legacyStorageUri: proto.String("s3://bucket/model"),
			expected:         false,
		},
		"LegacyPVCStorage": {
			storageUris:      nil,
			legacyStorageUri: proto.String("pvc://my-pvc/models"),
			expected:         false,
		},
		"NilLegacyStorageUri": {
			storageUris:      nil,
			legacyStorageUri: nil,
			expected:         false,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			result := componentRequiresImageVolumeSCC(scenario.storageUris, scenario.legacyStorageUri)
			g.Expect(result).To(gomega.Equal(scenario.expected))
		})
	}
}

func TestGetServiceAccountsRequiringImageVolumeSCC(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	namespace := "test-namespace"

	scenarios := map[string]struct {
		isvc             *v1beta1.InferenceService
		runtimes         []runtime.Object
		expectedAccounts []string
		expectError      bool
	}{
		"PredictorOnly": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						PodSpec: v1beta1.PodSpec{
							ServiceAccountName: "predictor-sa",
						},
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			expectedAccounts: []string{"predictor-sa"},
			expectError:      false,
		},
		"PredictorWithDefaultServiceAccount": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			expectedAccounts: []string{"default"},
			expectError:      false,
		},
		"PredictorWithWorker": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						PodSpec: v1beta1.PodSpec{
							ServiceAccountName: "predictor-sa",
						},
						WorkerSpec: &v1beta1.WorkerSpec{
							PodSpec: v1beta1.PodSpec{
								ServiceAccountName: "worker-sa",
							},
						},
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			expectedAccounts: []string{"predictor-sa", "worker-sa"},
			expectError:      false,
		},
		"NonMLServerRuntime": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "triton-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "triton-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: "triton",
						},
					},
				},
			},
			expectedAccounts: nil,
			expectError:      false,
		},
		"NoOCIStorage": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String("s3://bucket/model"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			expectedAccounts: nil,
			expectError:      false,
		},
		"RuntimeNotFound": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "nonexistent-runtime",
				},
			},
			runtimes:         []runtime.Object{},
			expectedAccounts: nil,
			expectError:      true,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = scheme.AddToScheme(s)
			_ = v1alpha1.AddToScheme(s)
			_ = v1beta1.SchemeBuilder.AddToScheme(s)

			cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(scenario.runtimes...).Build()

			accounts, err := getServiceAccountsRequiringImageVolumeSCC(context.Background(), cl, scenario.isvc)

			if scenario.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(accounts).To(gomega.ConsistOf(scenario.expectedAccounts))
			}
		})
	}
}

func TestExpectedImageVolumeSCCRoleBinding(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scenarios := map[string]struct {
		isvc             *v1beta1.InferenceService
		serviceAccounts  []string
		expectedName     string
		expectedSubjects int
	}{
		"SingleServiceAccount": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: "default",
				},
			},
			serviceAccounts:  []string{"predictor-sa"},
			expectedName:     "test-isvc-image-volume-scc",
			expectedSubjects: 1,
		},
		"MultipleServiceAccounts": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: "default",
				},
			},
			serviceAccounts:  []string{"predictor-sa", "worker-sa", "transformer-sa"},
			expectedName:     "test-isvc-image-volume-scc",
			expectedSubjects: 3,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			rb := expectedImageVolumeSCCRoleBinding(scenario.isvc, scenario.serviceAccounts)

			g.Expect(rb.Name).To(gomega.Equal(scenario.expectedName))
			g.Expect(rb.Namespace).To(gomega.Equal(scenario.isvc.Namespace))
			g.Expect(rb.Subjects).To(gomega.HaveLen(scenario.expectedSubjects))
			g.Expect(rb.RoleRef.Kind).To(gomega.Equal("ClusterRole"))
			g.Expect(rb.RoleRef.Name).To(gomega.Equal("openshift-ai-inferenceservice-image-volume-scc"))
			g.Expect(rb.Labels[constants.InferenceServiceLabel]).To(gomega.Equal(scenario.isvc.Name))
			g.Expect(rb.OwnerReferences).To(gomega.HaveLen(1))
			g.Expect(rb.OwnerReferences[0].Name).To(gomega.Equal(scenario.isvc.Name))
		})
	}
}

func TestSemanticRoleBindingEquals(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	baseRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "default",
			Labels: map[string]string{
				"key": "value",
			},
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "sa1", Namespace: "default"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "test-role",
		},
	}

	scenarios := map[string]struct {
		desired  *rbacv1.RoleBinding
		existing *rbacv1.RoleBinding
		expected bool
	}{
		"IdenticalRoleBindings": {
			desired:  baseRoleBinding.DeepCopy(),
			existing: baseRoleBinding.DeepCopy(),
			expected: true,
		},
		"DifferentSubjects": {
			desired: baseRoleBinding.DeepCopy(),
			existing: func() *rbacv1.RoleBinding {
				rb := baseRoleBinding.DeepCopy()
				rb.Subjects = []rbacv1.Subject{
					{Kind: "ServiceAccount", Name: "sa2", Namespace: "default"},
				}
				return rb
			}(),
			expected: false,
		},
		"DifferentRoleRef": {
			desired: baseRoleBinding.DeepCopy(),
			existing: func() *rbacv1.RoleBinding {
				rb := baseRoleBinding.DeepCopy()
				rb.RoleRef.Name = "different-role"
				return rb
			}(),
			expected: false,
		},
		"DifferentLabels": {
			desired: baseRoleBinding.DeepCopy(),
			existing: func() *rbacv1.RoleBinding {
				rb := baseRoleBinding.DeepCopy()
				rb.Labels = map[string]string{"different": "label"}
				return rb
			}(),
			expected: false,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			result := semanticRoleBindingEquals(scenario.desired, scenario.existing)
			g.Expect(result).To(gomega.Equal(scenario.expected))
		})
	}
}

func TestDeleteImageVolumeSCCRoleBinding(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-isvc",
			Namespace: "default",
		},
	}

	existingRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-isvc-image-volume-scc",
			Namespace: "default",
		},
	}

	scenarios := map[string]struct {
		existingObjects []client.Object
		expectError     bool
	}{
		"RoleBindingExists": {
			existingObjects: []client.Object{existingRB},
			expectError:     false,
		},
		"RoleBindingDoesNotExist": {
			existingObjects: []client.Object{},
			expectError:     false,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = scheme.AddToScheme(s)
			_ = v1beta1.SchemeBuilder.AddToScheme(s)

			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(scenario.existingObjects...).Build()

			reconciler := &InferenceServiceReconciler{
				Client: cl,
			}

			err := reconciler.deleteImageVolumeSCCRoleBinding(context.Background(), isvc)

			if scenario.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred())

				// Verify RoleBinding was deleted
				rb := &rbacv1.RoleBinding{}
				err = cl.Get(context.Background(), client.ObjectKey{
					Name:      "test-isvc-image-volume-scc",
					Namespace: "default",
				}, rb)
				g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
			}
		})
	}
}

func TestReconcileImageVolumeSCCRoleBinding(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-isvc",
			Namespace: "default",
			UID:       "test-uid",
		},
	}

	scenarios := map[string]struct {
		serviceAccounts []string
		existingRB      *rbacv1.RoleBinding
		expectCreate    bool
		expectUpdate    bool
	}{
		"CreateNewRoleBinding": {
			serviceAccounts: []string{"predictor-sa"},
			existingRB:      nil,
			expectCreate:    true,
			expectUpdate:    false,
		},
		"UpdateExistingRoleBindingWithDifferentSubjects": {
			serviceAccounts: []string{"predictor-sa", "worker-sa"},
			existingRB: &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc-image-volume-scc",
					Namespace: "default",
				},
				Subjects: []rbacv1.Subject{
					{Kind: "ServiceAccount", Name: "old-sa", Namespace: "default"},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "openshift-ai-inferenceservice-image-volume-scc",
				},
			},
			expectCreate: false,
			expectUpdate: true,
		},
		"IdempotentWhenAlreadyCorrect": {
			serviceAccounts: []string{"predictor-sa"},
			existingRB: func() *rbacv1.RoleBinding {
				rb := expectedImageVolumeSCCRoleBinding(isvc, []string{"predictor-sa"})
				return rb
			}(),
			expectCreate: false,
			expectUpdate: false,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = scheme.AddToScheme(s)
			_ = v1beta1.SchemeBuilder.AddToScheme(s)

			var existingObjects []client.Object
			if scenario.existingRB != nil {
				existingObjects = append(existingObjects, scenario.existingRB)
			}

			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(existingObjects...).Build()

			// Create a fake event recorder
			fakeRecorder := record.NewFakeRecorder(10)

			reconciler := &InferenceServiceReconciler{
				Client:   cl,
				Recorder: fakeRecorder,
			}

			err := reconciler.reconcileImageVolumeSCCRoleBinding(context.Background(), isvc, scenario.serviceAccounts)
			g.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify RoleBinding exists with correct subjects
			rb := &rbacv1.RoleBinding{}
			err = cl.Get(context.Background(), client.ObjectKey{
				Name:      "test-isvc-image-volume-scc",
				Namespace: "default",
			}, rb)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(rb.Subjects).To(gomega.HaveLen(len(scenario.serviceAccounts)))

			// Verify subjects match
			for _, sa := range scenario.serviceAccounts {
				found := false
				for _, subject := range rb.Subjects {
					if subject.Name == sa {
						found = true
						break
					}
				}
				g.Expect(found).To(gomega.BeTrue(), "Expected service account %s to be in subjects", sa)
			}
		})
	}
}

func TestReconcileWorkloadPlatformPermissions(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	namespace := "default"

	scenarios := map[string]struct {
		isvc            *v1beta1.InferenceService
		runtimes        []runtime.Object
		existingRB      *rbacv1.RoleBinding
		expectRBCreated bool
		expectRBDeleted bool
		forceStop       bool
		expectError     bool
	}{
		"CreateRoleBindingForMLServerWithOCI": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
					UID:       "test-uid",
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						PodSpec: v1beta1.PodSpec{
							ServiceAccountName: "predictor-sa",
						},
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			existingRB:      nil,
			expectRBCreated: true,
			expectRBDeleted: false,
			forceStop:       false,
			expectError:     false,
		},
		"DeleteRoleBindingWhenForceStop": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
					UID:       "test-uid",
					Annotations: map[string]string{
						constants.StopAnnotationKey: "true",
					},
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			existingRB: &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc-image-volume-scc",
					Namespace: namespace,
				},
			},
			expectRBCreated: false,
			expectRBDeleted: true,
			forceStop:       true,
			expectError:     false,
		},
		"DeleteRoleBindingWhenNoServiceAccounts": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
					UID:       "test-uid",
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String("s3://bucket/model"), // Non-OCI storage
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			existingRB: &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc-image-volume-scc",
					Namespace: namespace,
				},
			},
			expectRBCreated: false,
			expectRBDeleted: true,
			forceStop:       false,
			expectError:     false,
		},
		"NoOpForNonMLServerRuntime": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
					UID:       "test-uid",
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "triton-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "triton-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: "triton",
						},
					},
				},
			},
			existingRB:      nil,
			expectRBCreated: false,
			expectRBDeleted: false,
			forceStop:       false,
			expectError:     false,
		},
		"UpdateRoleBindingWithWorkerSA": {
			isvc: &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc",
					Namespace: namespace,
					UID:       "test-uid",
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						PodSpec: v1beta1.PodSpec{
							ServiceAccountName: "predictor-sa",
						},
						WorkerSpec: &v1beta1.WorkerSpec{
							PodSpec: v1beta1.PodSpec{
								ServiceAccountName: "worker-sa",
							},
						},
						Model: &v1beta1.ModelSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: proto.String(constants.OciURIPrefix + "model:v1"),
							},
						},
					},
				},
				Status: v1beta1.InferenceServiceStatus{
					ServingRuntimeName: "mlserver-runtime",
				},
			},
			runtimes: []runtime.Object{
				&v1alpha1.ServingRuntime{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "mlserver-runtime",
						Namespace: namespace,
						Annotations: map[string]string{
							constants.ServerTypeAnnotationKey: constants.ServerTypeMLServer,
						},
					},
				},
			},
			existingRB: &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-isvc-image-volume-scc",
					Namespace: namespace,
				},
				Subjects: []rbacv1.Subject{
					{Kind: "ServiceAccount", Name: "predictor-sa", Namespace: namespace},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "openshift-ai-inferenceservice-image-volume-scc",
				},
			},
			expectRBCreated: false,
			expectRBDeleted: false,
			forceStop:       false,
			expectError:     false,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = scheme.AddToScheme(s)
			_ = v1alpha1.AddToScheme(s)
			_ = v1beta1.SchemeBuilder.AddToScheme(s)

			var existingObjects []client.Object
			if scenario.existingRB != nil {
				existingObjects = append(existingObjects, scenario.existingRB)
			}
			// Convert runtime.Object to client.Object
			for _, rt := range scenario.runtimes {
				existingObjects = append(existingObjects, rt.(client.Object))
			}

			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(existingObjects...).Build()

			// Create a fake event recorder
			fakeRecorder := record.NewFakeRecorder(10)

			reconciler := &InferenceServiceReconciler{
				Client:   cl,
				Recorder: fakeRecorder,
			}

			err := reconciler.reconcileWorkloadPlatformPermissions(context.Background(), scenario.isvc)

			if scenario.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
				return
			}

			g.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify RoleBinding state
			rb := &rbacv1.RoleBinding{}
			rbName := "test-isvc-image-volume-scc"
			err = cl.Get(context.Background(), client.ObjectKey{
				Name:      rbName,
				Namespace: namespace,
			}, rb)

			switch {
			case scenario.expectRBCreated:
				g.Expect(err).NotTo(gomega.HaveOccurred(), "RoleBinding should have been created")
				g.Expect(rb.Subjects).NotTo(gomega.BeEmpty(), "RoleBinding should have subjects")
			case scenario.expectRBDeleted:
				g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "RoleBinding should have been deleted")
			default:
				// If neither created nor deleted, check if it exists or doesn't based on initial state
				if scenario.existingRB != nil {
					// Should still exist (possibly updated)
					g.Expect(err).NotTo(gomega.HaveOccurred(), "Existing RoleBinding should be preserved")

					// For multi-node scenario, verify both SAs are present
					if scenario.isvc.Spec.Predictor.WorkerSpec != nil {
						subjectNames := []string{}
						for _, s := range rb.Subjects {
							subjectNames = append(subjectNames, s.Name)
						}
						g.Expect(subjectNames).To(gomega.ContainElements("predictor-sa", "worker-sa"),
							"RoleBinding should include both predictor and worker SAs")
					}
				}
			}
		})
	}
}
