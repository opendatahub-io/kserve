//go:build distro

/*
Copyright 2024 The KServe Authors.

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

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestOCPServingCertAnnotationOnDefaultService(t *testing.T) {
	componentMeta := metav1.ObjectMeta{
		Name:      "test-service",
		Namespace: "default",
	}
	componentExt := &v1beta1.ComponentExtensionSpec{}
	podSpec := &corev1.PodSpec{}

	svcs := buildServiceList(componentMeta, componentExt, podSpec, false, nil)
	assert.Len(t, svcs, 1)
	assert.Equal(t, map[string]string{
		constants.OpenshiftServingCertAnnotation: "test-service" + constants.ServingCertSecretSuffix,
	}, svcs[0].Annotations)
}

func TestOCPServingCertAnnotationOnMultiNodeServices(t *testing.T) {
	componentMeta := metav1.ObjectMeta{
		Name:      "default-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			"annotation": "annotation-value",
		},
		Labels: map[string]string{
			constants.RawDeploymentAppLabel:                 "isvc.default-predictor",
			constants.InferenceServicePodLabelKey:           "default-predictor",
			constants.KServiceComponentLabel:                string(v1beta1.PredictorComponent),
			constants.InferenceServiceGenerationPodLabelKey: "1",
		},
	}
	componentExt := &v1beta1.ComponentExtensionSpec{}
	podSpec := &corev1.PodSpec{}

	svcs := buildServiceList(componentMeta, componentExt, podSpec, true, nil)
	// default + head + worker
	assert.Len(t, svcs, 3)

	// default service gets the cert annotation
	assert.Equal(t, "default-predictor"+constants.ServingCertSecretSuffix,
		svcs[0].Annotations[constants.OpenshiftServingCertAnnotation])

	// head service gets the cert annotation
	assert.Equal(t, constants.MultiNodeHead, svcs[1].Labels[constants.MultiNodeRoleLabelKey])
	assert.Equal(t, "default-predictor"+constants.ServingCertSecretSuffix,
		svcs[1].Annotations[constants.OpenshiftServingCertAnnotation])

	// worker service does NOT get the cert annotation
	assert.Equal(t, constants.MultiNodeWorker, svcs[2].Labels[constants.MultiNodeRoleLabelKey])
	assert.Empty(t, svcs[2].Annotations[constants.OpenshiftServingCertAnnotation])
}
