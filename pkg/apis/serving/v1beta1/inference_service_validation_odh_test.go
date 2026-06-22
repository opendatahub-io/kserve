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

package v1beta1

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidatePodSpecSecurity_HostNetwork(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					HostNetwork: true,
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("hostNetwork is not allowed"))
	g.Expect(err.Error()).To(gomega.ContainSubstring("predictor"))
}

func TestValidatePodSpecSecurity_HostPID(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					HostPID: true,
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("hostPID is not allowed"))
}

func TestValidatePodSpecSecurity_HostIPC(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					HostIPC: true,
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("hostIPC is not allowed"))
}

func TestValidatePodSpecSecurity_ServiceAccountName(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					ServiceAccountName: "custom-sa",
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("serviceAccountName is not allowed"))
}

func TestValidatePodSpecSecurity_InitContainers(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "evil-init",
							Image: "attacker/image:latest",
						},
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("initContainers are not allowed"))
}

func TestValidatePodSpecSecurity_ProjectedSATokenVolume(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "sa-token",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "api",
												ExpirationSeconds: proto.Int64(3600),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("serviceAccountToken source"))
	g.Expect(err.Error()).To(gomega.ContainSubstring("sa-token"))
}

func TestValidatePodSpecSecurity_ProjectedVolumeWithoutSAToken(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "config-vol",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "my-config",
												},
											},
										},
									},
								},
							},
						},
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func TestValidatePodSpecSecurity_ValidPodSpec(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func TestValidatePodSpecSecurity_TransformerHostNetwork(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
			Transformer: &TransformerSpec{
				PodSpec: PodSpec{
					HostNetwork: true,
					Containers: []corev1.Container{
						{
							Name:  "kserve-container",
							Image: "transformer:latest",
						},
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("hostNetwork is not allowed in transformer"))
}

func TestValidatePodSpecSecurity_ExplainerServiceAccountName(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
			Explainer: &ExplainerSpec{
				PodSpec: PodSpec{
					ServiceAccountName: "custom-sa",
					Containers: []corev1.Container{
						{
							Name:  "kserve-container",
							Image: "explainer:latest",
						},
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("serviceAccountName is not allowed in explainer"))
}

func TestValidatePodSpecSecurity_WorkerSpecHostPID(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				WorkerSpec: &WorkerSpec{
					PodSpec: PodSpec{
						HostPID: true,
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("hostPID is not allowed in predictor.workerSpec"))
}

func TestValidatePodSpecSecurity_WorkerSpecInitContainers(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				WorkerSpec: &WorkerSpec{
					PodSpec: PodSpec{
						InitContainers: []corev1.Container{
							{Name: "init", Image: "init:latest"},
						},
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("initContainers are not allowed in predictor.workerSpec"))
}

func TestValidatePodSpecSecurity_ErrorIncludesIsvcName(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "my-inference-service", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					HostNetwork: true,
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("my-inference-service"))
}

func TestValidatePodSpecSecurity_MultipleProjectedSourcesWithSAToken(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "mixed-projected",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "my-config",
												},
											},
										},
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Path: "token",
											},
										},
									},
								},
							},
						},
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("serviceAccountToken source"))
}

func TestValidatePodSpecSecurity_NonProjectedVolumesAllowed(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-isvc", Namespace: "default"},
		Spec: InferenceServiceSpec{
			Predictor: PredictorSpec{
				PodSpec: PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "config-vol",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "my-config",
									},
								},
							},
						},
						{
							Name: "secret-vol",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "my-secret",
								},
							},
						},
					},
				},
				Tensorflow: &TFServingSpec{
					PredictorExtensionSpec: PredictorExtensionSpec{
						StorageURI: proto.String("gs://testbucket/testmodel"),
					},
				},
			},
		},
	}

	err := validatePodSpecSecurity(isvc)
	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func TestValidatePodSpecSecurity_AllFieldsTable(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scenarios := map[string]struct {
		isvc      *InferenceService
		expectErr bool
		errSubstr string
	}{
		"ValidMinimalIsvc": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: false,
		},
		"PredictorHostNetwork": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						PodSpec: PodSpec{HostNetwork: true},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "hostNetwork",
		},
		"PredictorHostPID": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						PodSpec: PodSpec{HostPID: true},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "hostPID",
		},
		"PredictorHostIPC": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						PodSpec: PodSpec{HostIPC: true},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "hostIPC",
		},
		"PredictorServiceAccountName": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						PodSpec: PodSpec{ServiceAccountName: "custom"},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "serviceAccountName",
		},
		"PredictorInitContainers": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						PodSpec: PodSpec{
							InitContainers: []corev1.Container{{Name: "init", Image: "img"}},
						},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "initContainers",
		},
		"PredictorProjectedSAToken": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						PodSpec: PodSpec{
							Volumes: []corev1.Volume{
								{
									Name: "tok",
									VolumeSource: corev1.VolumeSource{
										Projected: &corev1.ProjectedVolumeSource{
											Sources: []corev1.VolumeProjection{
												{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
											},
										},
									},
								},
							},
						},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "serviceAccountToken",
		},
		"TransformerInitContainers": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
					Transformer: &TransformerSpec{
						PodSpec: PodSpec{
							InitContainers: []corev1.Container{{Name: "init", Image: "img"}},
							Containers:     []corev1.Container{{Name: "kserve-container", Image: "img"}},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "initContainers are not allowed in transformer",
		},
		"ExplainerHostIPC": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
					Explainer: &ExplainerSpec{
						PodSpec: PodSpec{
							HostIPC:    true,
							Containers: []corev1.Container{{Name: "kserve-container", Image: "img"}},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "hostIPC is not allowed in explainer",
		},
		"WorkerSpecServiceAccountName": {
			isvc: &InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: InferenceServiceSpec{
					Predictor: PredictorSpec{
						WorkerSpec: &WorkerSpec{
							PodSpec: PodSpec{ServiceAccountName: "worker-sa"},
						},
						Tensorflow: &TFServingSpec{
							PredictorExtensionSpec: PredictorExtensionSpec{
								StorageURI: proto.String("gs://bucket/model"),
							},
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "serviceAccountName is not allowed in predictor.workerSpec",
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			err := validatePodSpecSecurity(scenario.isvc)
			if scenario.expectErr {
				g.Expect(err).To(gomega.HaveOccurred(), "expected error for scenario %s", name)
				g.Expect(strings.Contains(err.Error(), scenario.errSubstr)).To(gomega.BeTrue(),
					"expected error to contain %q, got: %s", scenario.errSubstr, err.Error())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred(), "expected no error for scenario %s", name)
			}
		})
	}
}
