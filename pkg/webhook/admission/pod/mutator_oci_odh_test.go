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

package pod

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/constants"
	kserveTypes "github.com/kserve/kserve/pkg/types"
)

func TestGetServerTypeFromPod(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scenarios := map[string]struct {
		pod          *corev1.Pod
		expectedType string
	}{
		"PodWithMLServerAnnotation": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: constants.ServerTypeMLServer,
					},
				},
			},
			expectedType: constants.ServerTypeMLServer,
		},
		"PodWithTritonAnnotation": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: "triton",
					},
				},
			},
			expectedType: "triton",
		},
		"PodWithoutAnnotation": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			expectedType: "",
		},
		"PodWithNilAnnotations": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expectedType: "",
		},
		"NilPod": {
			pod:          nil,
			expectedType: "",
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			serverType := getServerTypeFromPod(scenario.pod)
			g.Expect(serverType).To(gomega.Equal(scenario.expectedType))
		})
	}
}

func TestGetOciStorageMutator(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	storageInitializer := &StorageInitializerInjector{
		config: &kserveTypes.StorageInitializerConfig{},
	}

	scenarios := map[string]struct {
		pod                *corev1.Pod
		expectedMutatorPtr uintptr
	}{
		"MLServerRuntimeUsesInjectImageVolume": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: constants.ServerTypeMLServer,
					},
				},
			},
			// We'll compare function pointers to verify correct mutator is returned
		},
		"TritonRuntimeUsesInjectModelcar": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: "triton",
					},
				},
			},
		},
		"NoAnnotationUsesInjectModelcar": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			mutator := getOciStorageMutator(scenario.pod, storageInitializer)
			g.Expect(mutator).ToNot(gomega.BeNil())

			// Verify the correct mutator is returned based on server type
			serverType := getServerTypeFromPod(scenario.pod)
			if serverType == constants.ServerTypeMLServer {
				// For MLServer, should return InjectImageVolume
				// We can verify by checking if it's NOT InjectModelcar
				// Since we can't easily compare function pointers in Go without reflection,
				// we'll test behavior instead
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "test:v1",
							constants.ODHKserveRuntimeAnnotation:                       constants.ServerTypeMLServer,
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: constants.InferenceServiceContainerName},
						},
					},
				}
				err := mutator(testPod)
				g.Expect(err).ToNot(gomega.HaveOccurred())

				// InjectImageVolume should NOT add ShareProcessNamespace
				// InjectModelcar WOULD add ShareProcessNamespace
				if serverType == constants.ServerTypeMLServer {
					g.Expect(testPod.Spec.ShareProcessNamespace).To(gomega.BeNil(),
						"InjectImageVolume should not set ShareProcessNamespace")
				}
			} else {
				// For non-MLServer, should return InjectModelcar
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "test:v1",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: constants.InferenceServiceContainerName},
						},
					},
				}
				err := mutator(testPod)
				g.Expect(err).ToNot(gomega.HaveOccurred())

				// InjectModelcar SHOULD add ShareProcessNamespace
				g.Expect(testPod.Spec.ShareProcessNamespace).ToNot(gomega.BeNil(),
					"InjectModelcar should set ShareProcessNamespace")
				if testPod.Spec.ShareProcessNamespace != nil {
					g.Expect(*testPod.Spec.ShareProcessNamespace).To(gomega.BeTrue())
				}
			}
		})
	}
}
