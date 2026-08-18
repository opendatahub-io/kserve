/*
Copyright 2021 The KServe Authors.

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

package credentials

import (
	"testing"

	"github.com/kserve/kserve/pkg/credentials/s3"

	"github.com/google/go-cmp/cmp"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/kserve/kserve/pkg/constants"
)

func TestS3CredentialBuilderWithODHSecretData(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	existingODHServiceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-default",
			Namespace: "default",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name:      "odh-s3-secret",
				Namespace: "default",
			},
		},
	}
	existingODHS3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-s3-secret",
			Namespace: "default",
			Annotations: map[string]string{
				s3.InferenceServiceS3SecretEndpointAnnotation: "s3.aws.com",
			},
		},
		Data: map[string][]byte{
			"awsAccessKeyID":        {},
			"awsSecretAccessKey":    {},
			constants.ODHS3Endpoint: []byte("odh-s3.aws.com"),
		},
	}

	existingODHServiceAccountWithProtocol := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-default-with-protocol",
			Namespace: "default",
		},
		Secrets: []corev1.ObjectReference{
			{
				Name:      "odh-s3-secret-with-protocol",
				Namespace: "default",
			},
		},
	}
	existingODHS3SecretWithProtocol := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-s3-secret-with-protocol",
			Namespace: "default",
			Annotations: map[string]string{
				s3.InferenceServiceS3SecretEndpointAnnotation: "http://s3.aws.com",
			},
		},
		Data: map[string][]byte{
			"awsAccessKeyID":        {},
			"awsSecretAccessKey":    {},
			constants.ODHS3Endpoint: []byte("http://odh-s3.aws.com"),
			s3.S3VerifySSL:          []byte("0"),
		},
	}
	scenarios := map[string]struct {
		serviceAccount        *corev1.ServiceAccount
		secret                *corev1.Secret
		inputConfiguration    *knservingv1.Configuration
		expectedConfiguration *knservingv1.Configuration
		shouldFail            bool
	}{
		"Build odh s3 secret data envs": {
			serviceAccount: existingODHServiceAccount,
			secret:         existingODHS3Secret,
			inputConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{},
								},
							},
						},
					},
				},
			},
			expectedConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Env: []corev1.EnvVar{
											{
												Name: s3.AWSAccessKeyId,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret",
														},
														Key: "awsAccessKeyID",
													},
												},
											},
											{
												Name: s3.AWSSecretAccessKey,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret",
														},
														Key: "awsSecretAccessKey",
													},
												},
											},
											{
												Name:  s3.S3UseHttps,
												Value: "1",
											},
											{
												Name:  s3.S3Endpoint,
												Value: "odh-s3.aws.com",
											},
											{
												Name:  s3.AWSEndpointUrl,
												Value: "https://odh-s3.aws.com",
											},
											{
												Name:  s3.S3VerifySSL,
												Value: "1",
											},
											{
												Name:  s3.AWSAnonymousCredential,
												Value: "false",
											},
											{
												Name:  s3.AWSRegion,
												Value: "us-east-2",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			shouldFail: false,
		},
		"Build odh s3 secret data envs with protocol": {
			serviceAccount: existingODHServiceAccountWithProtocol,
			secret:         existingODHS3SecretWithProtocol,
			inputConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{},
								},
							},
						},
					},
				},
			},
			expectedConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Env: []corev1.EnvVar{
											{
												Name: s3.AWSAccessKeyId,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret-with-protocol",
														},
														Key: "awsAccessKeyID",
													},
												},
											},
											{
												Name: s3.AWSSecretAccessKey,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret-with-protocol",
														},
														Key: "awsSecretAccessKey",
													},
												},
											},
											{
												Name:  s3.S3UseHttps,
												Value: "0",
											},
											{
												Name:  s3.S3Endpoint,
												Value: "odh-s3.aws.com",
											},
											{
												Name:  s3.AWSEndpointUrl,
												Value: "http://odh-s3.aws.com",
											},
											{
												Name:  s3.S3VerifySSL,
												Value: "0",
											},
											{
												Name:  s3.AWSAnonymousCredential,
												Value: "false",
											},
											{
												Name:  s3.AWSRegion,
												Value: "us-east-2",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			shouldFail: false,
		},
	}

	builder := NewCredentialBuilder(c, clientset, configMap)
	for name, scenario := range scenarios {
		g.Expect(c.Create(t.Context(), scenario.serviceAccount)).NotTo(gomega.HaveOccurred())
		g.Expect(c.Create(t.Context(), scenario.secret)).NotTo(gomega.HaveOccurred())

		err := builder.CreateSecretVolumeAndEnv(
			t.Context(),
			scenario.serviceAccount.Namespace,
			nil,
			scenario.serviceAccount.Name,
			&scenario.inputConfiguration.Spec.Template.Spec.Containers[0],
			&scenario.inputConfiguration.Spec.Template.Spec.Volumes,
		)
		if scenario.shouldFail && err == nil {
			t.Errorf("Test %q failed: returned success but expected error", name)
		}
		// Validate
		if !scenario.shouldFail {
			if err != nil {
				t.Errorf("Test %q failed: returned error: %v", name, err)
			}
			if diff := cmp.Diff(scenario.expectedConfiguration, scenario.inputConfiguration); diff != "" {
				t.Errorf("Test %q unexpected configuration spec (-want +got): %v", name, diff)
			}
		}
		g.Expect(c.Delete(t.Context(), scenario.serviceAccount)).NotTo(gomega.HaveOccurred())
		g.Expect(c.Delete(t.Context(), scenario.secret)).NotTo(gomega.HaveOccurred())
	}
}

func TestS3CredentialBuilderWithODHStorageSecretData(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	existingODHServiceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-default",
			Namespace: "default",
		},
	}
	existingODHS3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-s3-secret",
			Namespace: "default",
			Annotations: map[string]string{
				s3.InferenceServiceS3SecretEndpointAnnotation: "s3.aws.com",
			},
		},
		Data: map[string][]byte{
			"awsAccessKeyID":        {},
			"awsSecretAccessKey":    {},
			constants.ODHS3Endpoint: []byte("odh-s3.aws.com"),
		},
	}

	existingODHServiceAccountWithProtocol := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-default-with-protocol",
			Namespace: "default",
		},
	}
	existingODHS3SecretWithProtocol := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "odh-s3-secret-with-protocol",
			Namespace: "default",
			Annotations: map[string]string{
				s3.InferenceServiceS3SecretEndpointAnnotation: "http://s3.aws.com",
			},
		},
		Data: map[string][]byte{
			"awsAccessKeyID":        {},
			"awsSecretAccessKey":    {},
			constants.ODHS3Endpoint: []byte("http://odh-s3.aws.com"),
			s3.S3VerifySSL:          []byte("0"),
		},
	}
	scenarios := map[string]struct {
		serviceAccount        *corev1.ServiceAccount
		secret                *corev1.Secret
		inputConfiguration    *knservingv1.Configuration
		expectedConfiguration *knservingv1.Configuration
		shouldFail            bool
	}{
		"Build odh s3 secret data envs": {
			serviceAccount: existingODHServiceAccount,
			secret:         existingODHS3Secret,
			inputConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{},
								},
							},
						},
					},
				},
			},
			expectedConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Env: []corev1.EnvVar{
											{
												Name: s3.AWSAccessKeyId,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret",
														},
														Key: "awsAccessKeyID",
													},
												},
											},
											{
												Name: s3.AWSSecretAccessKey,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret",
														},
														Key: "awsSecretAccessKey",
													},
												},
											},
											{
												Name:  s3.S3UseHttps,
												Value: "1",
											},
											{
												Name:  s3.S3Endpoint,
												Value: "odh-s3.aws.com",
											},
											{
												Name:  s3.AWSEndpointUrl,
												Value: "https://odh-s3.aws.com",
											},
											{
												Name:  s3.S3VerifySSL,
												Value: "1",
											},
											{
												Name:  s3.AWSAnonymousCredential,
												Value: "false",
											},
											{
												Name:  s3.AWSRegion,
												Value: "us-east-2",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			shouldFail: false,
		},
		"Build odh s3 secret data envs with protocol": {
			serviceAccount: existingODHServiceAccountWithProtocol,
			secret:         existingODHS3SecretWithProtocol,
			inputConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{},
								},
							},
						},
					},
				},
			},
			expectedConfiguration: &knservingv1.Configuration{
				Spec: knservingv1.ConfigurationSpec{
					Template: knservingv1.RevisionTemplateSpec{
						Spec: knservingv1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Env: []corev1.EnvVar{
											{
												Name: s3.AWSAccessKeyId,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret-with-protocol",
														},
														Key: "awsAccessKeyID",
													},
												},
											},
											{
												Name: s3.AWSSecretAccessKey,
												ValueFrom: &corev1.EnvVarSource{
													SecretKeyRef: &corev1.SecretKeySelector{
														LocalObjectReference: corev1.LocalObjectReference{
															Name: "odh-s3-secret-with-protocol",
														},
														Key: "awsSecretAccessKey",
													},
												},
											},
											{
												Name:  s3.S3UseHttps,
												Value: "0",
											},
											{
												Name:  s3.S3Endpoint,
												Value: "odh-s3.aws.com",
											},
											{
												Name:  s3.AWSEndpointUrl,
												Value: "http://odh-s3.aws.com",
											},
											{
												Name:  s3.S3VerifySSL,
												Value: "0",
											},
											{
												Name:  s3.AWSAnonymousCredential,
												Value: "false",
											},
											{
												Name:  s3.AWSRegion,
												Value: "us-east-2",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			shouldFail: false,
		},
	}

	builder := NewCredentialBuilder(c, clientset, configMap)
	for name, scenario := range scenarios {
		g.Expect(c.Create(t.Context(), scenario.serviceAccount)).NotTo(gomega.HaveOccurred())
		g.Expect(c.Create(t.Context(), scenario.secret)).NotTo(gomega.HaveOccurred())
		annotations := map[string]string{
			"serving.kserve.io/storageSecretName": scenario.secret.Name,
		}
		err := builder.CreateSecretVolumeAndEnv(
			t.Context(),
			scenario.serviceAccount.Namespace,
			annotations,
			scenario.serviceAccount.Name,
			&scenario.inputConfiguration.Spec.Template.Spec.Containers[0],
			&scenario.inputConfiguration.Spec.Template.Spec.Volumes,
		)
		if scenario.shouldFail && err == nil {
			t.Errorf("Test %q failed: returned success but expected error", name)
		}
		// Validate
		if !scenario.shouldFail {
			if err != nil {
				t.Errorf("Test %q failed: returned error: %v", name, err)
			}
			if diff := cmp.Diff(scenario.expectedConfiguration, scenario.inputConfiguration); diff != "" {
				t.Errorf("Test %q unexpected configuration spec (-want +got): %v", name, diff)
			}
		}
		g.Expect(c.Delete(t.Context(), scenario.serviceAccount)).NotTo(gomega.HaveOccurred())
		g.Expect(c.Delete(t.Context(), scenario.secret)).NotTo(gomega.HaveOccurred())
	}
}
